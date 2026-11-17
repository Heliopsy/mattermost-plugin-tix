// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// audit starts an audit record for a command; the caller sets Success and the record is logged on return.
func (f *Feature) audit(c *command.Ctx, event string) (*model.AuditRecord, func()) {
	pc := c.Plugin
	if pc == nil {
		pc = &plugin.Context{}
	}
	rec := plugin.MakeAuditRecordWithContext(event, model.AuditStatusFail, pc, c.Args.UserId, "/tix")
	rec.AddMeta("channel_id", c.Args.ChannelId)
	return rec, func() { f.a.API.LogAuditRec(rec) }
}

// canManageChannel is the one check behind subscribe and unsubscribe: the caller may change the
// channel's settings. The second result is the message id of the refusal.
func (f *Feature) canManageChannel(userID, channelID string) (bool, string) {
	ch, appErr := f.a.API.GetChannel(channelID)
	if appErr != nil {
		return false, i18n.M("notify.channel.read_failed")
	}
	var perm *model.Permission
	switch ch.Type {
	case model.ChannelTypeOpen:
		perm = model.PermissionManagePublicChannelProperties
	case model.ChannelTypePrivate:
		perm = model.PermissionManagePrivateChannelProperties
	default:
		return false, i18n.M("notify.channel.wrong_type")
	}
	if !f.a.API.HasPermissionToChannel(userID, channelID, perm) {
		return false, i18n.M("notify.channel.not_allowed")
	}
	return true, ""
}

// parseKinds reads the event kinds of /tix subscribe; none means the default set.
func parseKinds(args []string) ([]string, error) {
	if len(args) == 0 {
		return slices.Clone(defaultKinds), nil
	}
	var out []string
	for _, a := range args {
		for _, part := range strings.FieldsFunc(strings.ToLower(a), func(r rune) bool { return r == ',' }) {
			if part == "all" {
				return slices.Clone(allKinds), nil
			}
			k, ok := kindAliases[part]
			if !ok {
				return nil, i18n.NewError("notify.subscribe.unknown_event", "event", part, "events", strings.Join(allKinds, ", "))
			}
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(allKinds, a) - slices.Index(allKinds, b) })
	return out, nil
}

// parseFeedOptions takes --detail, --comments and --people (as --name value or --name=value) out
// of the arguments of /tix subscribe and returns the rest.
func parseFeedOptions(args []string) (render.Feed, []string, error) {
	var opts render.Feed
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			rest = append(rest, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.ToLower(a), "=")
		if !hasVal {
			if i+1 >= len(args) {
				return opts, nil, i18n.NewError("notify.subscribe.option_needs_value", "option", name)
			}
			i++
			val = strings.ToLower(args[i])
		}
		var dst *string
		var allowed []string
		switch name {
		case "--detail":
			dst, allowed = &opts.Detail, []string{render.DetailCompact, render.DetailDetailed, render.DetailCard}
		case "--comments":
			dst, allowed = &opts.Comments, []string{render.CommentFull, render.CommentExcerpt, render.CommentNone}
		case "--people":
			dst, allowed = &opts.People, []string{render.PeopleNames, render.PeopleMentions}
		default:
			return opts, nil, i18n.NewError("notify.subscribe.unknown_option", "option", name)
		}
		if !slices.Contains(allowed, val) {
			return opts, nil, i18n.NewError("notify.subscribe.bad_value", "option", name, "value", val, "allowed", strings.Join(allowed, ", "))
		}
		*dst = val
	}
	return opts, rest, nil
}

// feedFormat describes a feed's overrides, or "" when it has none.
func feedFormat(s store.Subscription) string {
	var parts []string
	for _, kv := range [][2]string{{"detail", s.Detail}, {"comments", s.Comments}, {"people", s.People}} {
		if kv[1] != "" {
			parts = append(parts, "--"+kv[0]+" "+kv[1])
		}
	}
	return strings.Join(parts, " ")
}

func (f *Feature) subscribe(c *command.Ctx, args []string) *model.CommandResponse {
	if len(args) == 0 {
		return command.Ephemeral(c.L().T("notify.subscribe.usage", "events", strings.Join(allKinds, " "), "defaults", strings.Join(defaultKinds, ", ")))
	}
	userID, channelID := c.Args.UserId, c.Args.ChannelId
	rec, done := f.audit(c, "tixSubscribe")
	defer done()
	l := c.L()
	if ok, msg := f.canManageChannel(userID, channelID); !ok {
		return command.Ephemeral(l.T(msg))
	}
	opts, kindArgs, err := parseFeedOptions(args[1:])
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	ks, err := parseKinds(kindArgs)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	project, label := "", l.T("notify.subscribe.every_project")
	if strings.EqualFold(args[0], "all") {
		if !f.a.API.HasPermissionTo(userID, model.PermissionManageSystem) {
			return command.Ephemeral(l.T("notify.subscribe.all_admins_only"))
		}
	} else {
		p, perr := f.findProject(userID, args[0])
		if perr != nil {
			return command.Ephemeral(app.ErrText(l, perr))
		}
		project, label = p.Key, fmt.Sprintf("**%s** (%s)", p.Key, p.Name)
	}
	err = f.a.Store.SaveSubscriptions(func(cur []store.Subscription) []store.Subscription {
		out := slices.DeleteFunc(cur, func(s store.Subscription) bool {
			return s.ChannelID == channelID && strings.EqualFold(s.Project, project)
		})
		return append(out, store.Subscription{
			ChannelID: channelID, Project: project, Events: ks, CreatedBy: userID, CreatedAt: time.Now(),
			Detail: opts.Detail, Comments: opts.Comments, People: opts.People,
		})
	})
	if err != nil {
		f.a.API.LogError("tix: saving a subscription", "err", err.Error())
		return command.Ephemeral(l.T("notify.subscribe.save_failed"))
	}
	rec.AddMeta("project", project)
	rec.Success()
	msg := l.T("notify.subscribe.done", "project", label, "events", strings.Join(ks, ", ")) + "\n" + l.T("notify.subscribe.privacy")
	if format := feedFormat(store.Subscription{Detail: opts.Detail, Comments: opts.Comments, People: opts.People}); format != "" {
		msg += "\n" + l.T("notify.subscribe.format", "format", format)
	}
	if f.a.Config().WebhookSecret == "" {
		msg += "\n" + l.T("notify.subscribe.no_webhook")
	}
	return command.Ephemeral(msg)
}

// findProject resolves a project key the caller can see with their own token.
func (f *Feature) findProject(userID, key string) (*tix.Project, error) {
	ps, err := f.a.Projects(userID)
	if err != nil {
		return nil, err
	}
	for i := range ps {
		if strings.EqualFold(ps[i].Key, key) {
			return &ps[i], nil
		}
	}
	return nil, i18n.NewError("notify.subscribe.no_project", "project", key)
}

func (f *Feature) unsubscribe(c *command.Ctx, args []string) *model.CommandResponse {
	if len(args) == 0 {
		return command.Ephemeral(c.L().T("notify.unsubscribe.usage"))
	}
	userID, channelID := c.Args.UserId, c.Args.ChannelId
	rec, done := f.audit(c, "tixUnsubscribe")
	defer done()
	if ok, msg := f.canManageChannel(userID, channelID); !ok {
		return command.Ephemeral(c.L().T(msg))
	}
	project := args[0]
	if strings.EqualFold(project, "all") {
		project = ""
	}
	removed := 0
	err := f.a.Store.SaveSubscriptions(func(cur []store.Subscription) []store.Subscription {
		removed = 0
		return slices.DeleteFunc(cur, func(s store.Subscription) bool {
			hit := s.ChannelID == channelID && strings.EqualFold(s.Project, project)
			if hit {
				removed++
			}
			return hit
		})
	})
	if err != nil {
		f.a.API.LogError("tix: saving subscriptions", "err", err.Error())
		return command.Ephemeral(c.L().T("notify.unsubscribe.save_failed"))
	}
	rec.AddMeta("project", project)
	rec.Success()
	if removed == 0 {
		return command.Ephemeral(c.L().T("notify.unsubscribe.none"))
	}
	return command.Ephemeral(c.L().T("notify.unsubscribe.done"))
}

func (f *Feature) subscriptions(c *command.Ctx, args []string) *model.CommandResponse {
	l := c.L()
	all := len(args) > 0 && strings.EqualFold(args[0], "all")
	if all && !f.a.API.HasPermissionTo(c.Args.UserId, model.PermissionManageSystem) {
		return command.Ephemeral(l.T("notify.subscriptions.all_admins_only"))
	}
	subs, err := f.a.Store.Subscriptions()
	if err != nil {
		return command.Ephemeral(l.T("notify.subscriptions.read_failed"))
	}
	var b strings.Builder
	n := 0
	for _, s := range subs {
		if !all && s.ChannelID != c.Args.ChannelId {
			continue
		}
		if n == 0 {
			if all {
				fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n| --- | --- | --- | --- | --- |\n", l.T("notify.subscriptions.channel"),
					l.T("notify.subscriptions.project"), l.T("notify.subscriptions.events"), l.T("notify.subscriptions.format"), l.T("notify.subscriptions.by"))
			} else {
				fmt.Fprintf(&b, "| %s | %s | %s | %s |\n| --- | --- | --- | --- |\n", l.T("notify.subscriptions.project"),
					l.T("notify.subscriptions.events"), l.T("notify.subscriptions.format"), l.T("notify.subscriptions.by"))
			}
		}
		n++
		project := s.Project
		if project == "" {
			project = "all"
		}
		format := l.T("notify.subscriptions.default_format")
		if ff := feedFormat(s); ff != "" {
			format = "`" + ff + "`"
		}
		by := s.CreatedBy
		if u, appErr := f.a.API.GetUser(s.CreatedBy); appErr == nil {
			by = "@" + u.Username
		}
		if all {
			ch := s.ChannelID
			if channel, appErr := f.a.API.GetChannel(s.ChannelID); appErr == nil {
				ch = "~" + channel.Name
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %s |\n", ch, project, strings.Join(s.Events, ", "), format, by)
		} else {
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", project, strings.Join(s.Events, ", "), format, by)
		}
	}
	if n == 0 {
		if all {
			return command.Ephemeral(l.T("notify.subscriptions.none_anywhere"))
		}
		return command.Ephemeral(l.T("notify.subscriptions.none"))
	}
	return command.Ephemeral(b.String())
}

func (f *Feature) notifications(c *command.Ctx, args []string) *model.CommandResponse {
	userID, l := c.Args.UserId, c.L()
	if len(args) == 0 {
		if f.dmsOff(userID) {
			return command.Ephemeral(l.T("notify.notifications.state_off"))
		}
		return command.Ephemeral(l.T("notify.notifications.state_on"))
	}
	var p prefs
	switch strings.ToLower(args[0]) {
	case "on":
	case "off":
		p.DMsOff = true
	default:
		return command.Ephemeral(l.T("notify.notifications.usage"))
	}
	var v any = p
	if !p.DMsOff {
		v = nil
	}
	if _, err := f.a.Store.Set("notify", "prefs_"+userID, v); err != nil {
		f.a.API.LogError("tix: saving notification prefs", "err", err.Error())
		return command.Ephemeral(l.T("notify.notifications.save_failed"))
	}
	if p.DMsOff {
		return command.Ephemeral(l.T("notify.notifications.off"))
	}
	return command.Ephemeral(l.T("notify.notifications.on"))
}

// endpointRecord remembers which tix webhook is the plugin's.
type endpointRecord struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (f *Feature) webhookCmd(c *command.Ctx, args []string) *model.CommandResponse {
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	l := c.L()
	cfg := f.a.Config()
	if cfg.AdminToken == "" {
		return command.Ephemeral(l.T("notify.webhook.no_admin_token"))
	}
	admin, err := f.a.NewTix(cfg.AdminToken)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	switch sub {
	case "setup":
		return f.webhookSetup(c, admin)
	case "status":
		return f.webhookStatus(l, admin)
	case "remove":
		return f.webhookRemove(c, admin)
	}
	return command.Ephemeral(l.T("notify.webhook.usage"))
}

// findEndpoint returns the plugin's endpoint in tix: the remembered id, else one with our URL.
func (f *Feature) findEndpoint(admin *tix.Client) (*tix.Webhook, error) {
	ctx, cancel := app.Ctx()
	defer cancel()
	hooks, err := admin.ListWebhooks(ctx)
	if err != nil {
		return nil, err
	}
	var rec endpointRecord
	_ = f.a.Store.Get("notify", "endpoint", &rec)
	url := f.a.PluginURL("/hooks/webhook")
	for i := range hooks {
		if rec.ID != "" && hooks[i].ID == rec.ID {
			return &hooks[i], nil
		}
	}
	for i := range hooks {
		if hooks[i].URL == url {
			return &hooks[i], nil
		}
	}
	return nil, nil
}

func (f *Feature) webhookSetup(c *command.Ctx, admin *tix.Client) *model.CommandResponse {
	l := c.L()
	rec, done := f.audit(c, "tixWebhookSetup")
	defer done()
	secret := f.a.Config().WebhookSecret
	if secret == "" {
		return command.Ephemeral(l.T("notify.webhook.no_secret"))
	}
	existing, err := f.findEndpoint(admin)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	in := tix.WebhookInput{URL: f.a.PluginURL("/hooks/webhook"), Secret: secret, EventTypes: webhookEventTypes, Active: true}
	if existing != nil {
		in.ID = existing.ID
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	hook, err := admin.PutWebhook(ctx, in)
	if err != nil {
		return command.Ephemeral(l.T("notify.webhook.refused", "error", app.ErrText(l, err)))
	}
	if _, err := f.a.Store.Set("notify", "endpoint", endpointRecord{ID: hook.ID, URL: hook.URL}); err != nil {
		f.a.API.LogWarn("tix: remembering the webhook id", "err", err.Error())
	}
	rec.Success()
	id := i18n.M("notify.webhook.registered")
	if existing != nil {
		id = i18n.M("notify.webhook.updated")
	}
	return command.Ephemeral(l.T(id, "id", hook.ID, "url", hook.URL, "events", strings.Join(hook.EventTypes, ", ")))
}

func (f *Feature) webhookStatus(l *i18n.Localizer, admin *tix.Client) *model.CommandResponse {
	hook, err := f.findEndpoint(admin)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	if hook == nil {
		return command.Ephemeral(l.T("notify.webhook.none"))
	}
	var b strings.Builder
	state := l.T("notify.webhook.active")
	if !hook.Active {
		state = l.T("notify.webhook.inactive")
	}
	b.WriteString(l.T("notify.webhook.status", "id", hook.ID, "state", state, "url", hook.URL, "events", strings.Join(hook.EventTypes, ", ")) + "\n")
	if f.a.Config().WebhookSecret == "" {
		b.WriteString("**" + l.T("notify.webhook.secret_unset") + "**\n")
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	ds, err := admin.ListWebhookDeliveries(ctx, hook.ID, 10)
	if err != nil {
		b.WriteString(l.T("notify.webhook.log_failed", "error", app.ErrText(l, err)))
		return command.Ephemeral(b.String())
	}
	if len(ds) == 0 {
		b.WriteString(l.T("notify.webhook.no_deliveries"))
		return command.Ephemeral(b.String())
	}
	fmt.Fprintf(&b, "\n| %s | %s | %s | %s | HTTP | %s |\n| --- | --- | --- | --- | --- | --- |\n", l.T("notify.webhook.col_delivery"),
		l.T("notify.webhook.col_event"), l.T("notify.webhook.col_status"), l.T("notify.webhook.col_attempts"), l.T("notify.webhook.col_queued"))
	for _, d := range ds {
		status := d.Status
		if d.LastError != "" {
			status += ": " + d.LastError
		}
		code := ""
		if d.LastStatusCode != 0 {
			code = fmt.Sprint(d.LastStatusCode)
		}
		fmt.Fprintf(&b, "| `%s` | %d | %s | %d | %s | %s |\n", d.ID, d.EventSeq, status, d.Attempts, code, d.CreatedAt.UTC().Format("2006-01-02 15:04"))
	}
	return command.Ephemeral(b.String())
}

func (f *Feature) webhookRemove(c *command.Ctx, admin *tix.Client) *model.CommandResponse {
	l := c.L()
	rec, done := f.audit(c, "tixWebhookRemove")
	defer done()
	hook, err := f.findEndpoint(admin)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	if hook == nil {
		return command.Ephemeral(l.T("notify.webhook.none_short"))
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	if err := admin.DeleteWebhook(ctx, hook.ID); err != nil && !tix.IsStatus(err, 404) {
		return command.Ephemeral(app.ErrText(l, err))
	}
	_, _ = f.a.Store.Set("notify", "endpoint", nil)
	rec.Success()
	return command.Ephemeral(l.T("notify.webhook.removed", "id", hook.ID))
}
