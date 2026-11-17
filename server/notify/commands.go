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
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// privacyNote goes with every new subscription: whoever subscribes publishes that activity here.
const privacyNote = "Everyone who can read this channel will see the titles and comment text of those tasks, " +
	"including people who cannot open the project in tix. You chose to publish it here; `/tix unsubscribe` stops it."

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

// canManageChannel is the one check behind subscribe and unsubscribe: the caller may change the channel's settings.
func (f *Feature) canManageChannel(userID, channelID string) (bool, string) {
	ch, appErr := f.a.API.GetChannel(channelID)
	if appErr != nil {
		return false, "Could not read this channel."
	}
	var perm *model.Permission
	switch ch.Type {
	case model.ChannelTypeOpen:
		perm = model.PermissionManagePublicChannelProperties
	case model.ChannelTypePrivate:
		perm = model.PermissionManagePrivateChannelProperties
	default:
		return false, "tix feeds go into public or private channels, not direct or group messages."
	}
	if !f.a.API.HasPermissionToChannel(userID, channelID, perm) {
		return false, "Only people who can manage this channel's settings can change its tix feeds."
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
				return nil, fmt.Errorf("unknown event `%s`; use %s", part, strings.Join(allKinds, ", "))
			}
			if !slices.Contains(out, k) {
				out = append(out, k)
			}
		}
	}
	slices.SortFunc(out, func(a, b string) int { return slices.Index(allKinds, a) - slices.Index(allKinds, b) })
	return out, nil
}

func (f *Feature) subscribe(c *command.Ctx, args []string) *model.CommandResponse {
	if len(args) == 0 {
		return command.Ephemeral("Usage: `/tix subscribe <project|all> [" + strings.Join(allKinds, " ") + "]`. Default: " + strings.Join(defaultKinds, ", ") + ".")
	}
	userID, channelID := c.Args.UserId, c.Args.ChannelId
	rec, done := f.audit(c, "tixSubscribe")
	defer done()
	if ok, msg := f.canManageChannel(userID, channelID); !ok {
		return command.Ephemeral(msg)
	}
	ks, err := parseKinds(args[1:])
	if err != nil {
		return command.Ephemeral(err.Error())
	}
	project, label := "", "every project"
	if strings.EqualFold(args[0], "all") {
		if !f.a.API.HasPermissionTo(userID, model.PermissionManageSystem) {
			return command.Ephemeral("Only system admins can subscribe a channel to every project. Name one project you can see.")
		}
	} else {
		p, perr := f.findProject(userID, args[0])
		if perr != nil {
			return command.Ephemeral(app.ErrText(perr))
		}
		project, label = p.Key, fmt.Sprintf("**%s** (%s)", p.Key, p.Name)
	}
	err = f.a.Store.SaveSubscriptions(func(cur []store.Subscription) []store.Subscription {
		out := slices.DeleteFunc(cur, func(s store.Subscription) bool {
			return s.ChannelID == channelID && strings.EqualFold(s.Project, project)
		})
		return append(out, store.Subscription{ChannelID: channelID, Project: project, Events: ks, CreatedBy: userID, CreatedAt: time.Now()})
	})
	if err != nil {
		f.a.API.LogError("tix: saving a subscription", "err", err.Error())
		return command.Ephemeral("Could not save the subscription.")
	}
	rec.AddMeta("project", project)
	rec.Success()
	msg := fmt.Sprintf("This channel now gets tix activity of %s: %s.\n%s", label, strings.Join(ks, ", "), privacyNote)
	if f.a.Config().WebhookSecret == "" {
		msg += "\nNothing arrives until a system admin runs `/tix webhook setup`."
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
	return nil, fmt.Errorf("no project `%s` among the projects you can see in tix", key)
}

func (f *Feature) unsubscribe(c *command.Ctx, args []string) *model.CommandResponse {
	if len(args) == 0 {
		return command.Ephemeral("Usage: `/tix unsubscribe <project|all>`")
	}
	userID, channelID := c.Args.UserId, c.Args.ChannelId
	rec, done := f.audit(c, "tixUnsubscribe")
	defer done()
	if ok, msg := f.canManageChannel(userID, channelID); !ok {
		return command.Ephemeral(msg)
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
		return command.Ephemeral("Could not save the change.")
	}
	rec.AddMeta("project", project)
	rec.Success()
	if removed == 0 {
		return command.Ephemeral("This channel had no such tix feed. `/tix subscriptions` lists them.")
	}
	return command.Ephemeral("Stopped that tix feed in this channel.")
}

func (f *Feature) subscriptions(c *command.Ctx, args []string) *model.CommandResponse {
	all := len(args) > 0 && strings.EqualFold(args[0], "all")
	if all && !f.a.API.HasPermissionTo(c.Args.UserId, model.PermissionManageSystem) {
		return command.Ephemeral("Only system admins can list the feeds of every channel.")
	}
	subs, err := f.a.Store.Subscriptions()
	if err != nil {
		return command.Ephemeral("Could not read the subscriptions.")
	}
	var b strings.Builder
	n := 0
	for _, s := range subs {
		if !all && s.ChannelID != c.Args.ChannelId {
			continue
		}
		if n == 0 {
			if all {
				b.WriteString("| Channel | Project | Events | By |\n| --- | --- | --- | --- |\n")
			} else {
				b.WriteString("| Project | Events | By |\n| --- | --- | --- |\n")
			}
		}
		n++
		project := s.Project
		if project == "" {
			project = "all"
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
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", ch, project, strings.Join(s.Events, ", "), by)
		} else {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", project, strings.Join(s.Events, ", "), by)
		}
	}
	if n == 0 {
		if all {
			return command.Ephemeral("No channel has a tix feed.")
		}
		return command.Ephemeral("This channel has no tix feeds. `/tix subscribe <project>` adds one.")
	}
	return command.Ephemeral(b.String())
}

func (f *Feature) notifications(c *command.Ctx, args []string) *model.CommandResponse {
	userID := c.Args.UserId
	if len(args) == 0 {
		state := "on"
		if f.dmsOff(userID) {
			state = "off"
		}
		return command.Ephemeral("Your personal tix DMs are **" + state + "**. `/tix notifications on|off` changes that.")
	}
	var p prefs
	switch strings.ToLower(args[0]) {
	case "on":
	case "off":
		p.DMsOff = true
	default:
		return command.Ephemeral("Usage: `/tix notifications on|off`")
	}
	var v any = p
	if !p.DMsOff {
		v = nil
	}
	if _, err := f.a.Store.Set("notify", "prefs_"+userID, v); err != nil {
		f.a.API.LogError("tix: saving notification prefs", "err", err.Error())
		return command.Ephemeral("Could not save that.")
	}
	if p.DMsOff {
		return command.Ephemeral("Personal tix DMs are off. Channel feeds are not affected.")
	}
	return command.Ephemeral("Personal tix DMs are on: you hear about tasks assigned to you, comments on tasks you are assigned or created, your tasks being closed, and mentions.")
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
	cfg := f.a.Config()
	if cfg.AdminToken == "" {
		return command.Ephemeral("Set the admin token (with `webhook:admin`) in System Console > Plugins > tix first.")
	}
	admin, err := f.a.NewTix(cfg.AdminToken)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	switch sub {
	case "setup":
		return f.webhookSetup(c, admin)
	case "status":
		return f.webhookStatus(admin)
	case "remove":
		return f.webhookRemove(c, admin)
	}
	return command.Ephemeral("Usage: `/tix webhook setup|status|remove`")
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
	url := f.a.PluginURL("/public/webhook")
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
	rec, done := f.audit(c, "tixWebhookSetup")
	defer done()
	secret := f.a.Config().WebhookSecret
	if secret == "" {
		return command.Ephemeral("Generate the webhook secret in System Console > Plugins > tix first.")
	}
	existing, err := f.findEndpoint(admin)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	in := tix.WebhookInput{URL: f.a.PluginURL("/public/webhook"), Secret: secret, EventTypes: webhookEventTypes, Active: true}
	if existing != nil {
		in.ID = existing.ID
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	hook, err := admin.PutWebhook(ctx, in)
	if err != nil {
		return command.Ephemeral("tix refused the webhook: " + app.ErrText(err))
	}
	if _, err := f.a.Store.Set("notify", "endpoint", endpointRecord{ID: hook.ID, URL: hook.URL}); err != nil {
		f.a.API.LogWarn("tix: remembering the webhook id", "err", err.Error())
	}
	rec.Success()
	verb := "Registered"
	if existing != nil {
		verb = "Updated"
	}
	return command.Ephemeral(fmt.Sprintf("%s the tix webhook `%s` → %s for %s.", verb, hook.ID, hook.URL, strings.Join(hook.EventTypes, ", ")))
}

func (f *Feature) webhookStatus(admin *tix.Client) *model.CommandResponse {
	hook, err := f.findEndpoint(admin)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	if hook == nil {
		return command.Ephemeral("No tix webhook points at this plugin. `/tix webhook setup` registers one.")
	}
	var b strings.Builder
	state := "active"
	if !hook.Active {
		state = "inactive"
	}
	fmt.Fprintf(&b, "tix webhook `%s` (%s) → %s\nEvents: %s\n", hook.ID, state, hook.URL, strings.Join(hook.EventTypes, ", "))
	if f.a.Config().WebhookSecret == "" {
		b.WriteString("**The webhook secret is not set, so every delivery is rejected.**\n")
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	ds, err := admin.ListWebhookDeliveries(ctx, hook.ID, 10)
	if err != nil {
		b.WriteString("Could not read the delivery log: " + app.ErrText(err))
		return command.Ephemeral(b.String())
	}
	if len(ds) == 0 {
		b.WriteString("No deliveries yet.")
		return command.Ephemeral(b.String())
	}
	b.WriteString("\n| Delivery | Event | Status | Attempts | HTTP | Queued |\n| --- | --- | --- | --- | --- | --- |\n")
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
	rec, done := f.audit(c, "tixWebhookRemove")
	defer done()
	hook, err := f.findEndpoint(admin)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	if hook == nil {
		return command.Ephemeral("No tix webhook points at this plugin.")
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	if err := admin.DeleteWebhook(ctx, hook.ID); err != nil && !tix.IsStatus(err, 404) {
		return command.Ephemeral(app.ErrText(err))
	}
	_, _ = f.a.Store.Set("notify", "endpoint", nil)
	rec.Success()
	return command.Ephemeral(fmt.Sprintf("Removed the tix webhook `%s`. Channel feeds and DMs stop until `/tix webhook setup`.", hook.ID))
}
