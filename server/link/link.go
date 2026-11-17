// SPDX-License-Identifier: AGPL-3.0-only

// Package link connects a Mattermost user to their tix account: /tix connect,
// disconnect and me. With an admin token configured, connect issues the person
// a token of their own in tix, matched by verified email, so nothing is pasted.
// Without one, or as an alternative, a person pastes a token they made in tix.
package link

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// Feature is the account-linking feature.
type Feature struct{ a *app.App }

// New returns the feature.
func New(a *app.App) *Feature { return &Feature{a: a} }

// Commands are /tix connect, disconnect and me.
func (f *Feature) Commands() []command.Sub {
	return []command.Sub{
		{Name: "connect", Hint: "[token]", HelpID: "link.cmd.connect", Run: f.connect},
		{Name: "disconnect", HelpID: "link.cmd.disconnect", Run: f.disconnect},
		{Name: "me", Aliases: []string{"whoami"}, HelpID: "link.cmd.me", Run: f.me},
	}
}

// Routes mounts the connect dialog's submit handler.
func (f *Feature) Routes(h *api.Handler) {
	h.User.HandleFunc("/dialog/connect", f.dialogSubmit).Methods(http.MethodPost)
}

func (f *Feature) connect(c *command.Ctx, args []string) *model.CommandResponse {
	userID, l := c.Args.UserId, c.L()
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		msg, err := f.linkWithToken(l, userID, strings.TrimSpace(args[0]))
		if err != nil {
			return command.Ephemeral(app.ErrText(l, err))
		}
		return command.Ephemeral(msg)
	}
	if f.a.Config().AdminToken != "" {
		msg, err := f.linkByEmail(l, userID)
		if err == nil {
			return command.Ephemeral(msg)
		}
		f.a.API.LogInfo("tix: automatic connect failed, offering the token dialog", "user_id", userID, "err", err.Error())
		if !errors.Is(err, errNoMatch) {
			return command.Ephemeral(app.ErrText(l, err))
		}
	}
	return f.openDialog(c)
}

func (f *Feature) openDialog(c *command.Ctx) *model.CommandResponse {
	tokensURL := f.a.TixURL() + "/connect?tab=api"
	l := c.L()
	dlg := model.OpenDialogRequest{
		TriggerId: c.Args.TriggerId,
		URL:       f.a.PluginURL("/api/v1/dialog/connect"),
		Dialog: model.Dialog{
			Title:            l.T("link.dialog.title"),
			IntroductionText: l.T("link.dialog.intro", "url", tokensURL),
			SubmitLabel:      l.T("link.dialog.submit"),
			Elements: []model.DialogElement{{
				DisplayName: l.T("link.dialog.token"), Name: "token", Type: "text", SubType: "password",
				Placeholder: "tix_...",
			}},
		},
	}
	if err := f.a.API.OpenInteractiveDialog(dlg); err != nil {
		return command.Ephemeral(l.T("link.connect.by_command", "url", tokensURL))
	}
	return &model.CommandResponse{}
}

func (f *Feature) dialogSubmit(w http.ResponseWriter, r *http.Request) {
	var req model.SubmitDialogRequest
	if err := decode(r, &req); err != nil || req.UserId != api.UserID(r) {
		api.WriteError(w, r, http.StatusBadRequest, "bad dialog submission", err)
		return
	}
	tok, _ := req.Submission["token"].(string)
	l := f.a.Locale(req.UserId)
	msg, err := f.linkWithToken(l, req.UserId, strings.TrimSpace(tok))
	if err != nil {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{Errors: map[string]string{"token": app.ErrText(l, err)}})
		return
	}
	f.a.Ephemeral(req.UserId, req.ChannelId, "", msg)
	api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{})
}

// linkWithToken checks a pasted token and stores it; the answer is in l's language.
func (f *Feature) linkWithToken(l *i18n.Localizer, userID, token string) (string, error) {
	if token == "" {
		return "", i18n.NewError("link.err.paste_token")
	}
	c, err := f.a.NewTix(token)
	if err != nil {
		return "", err
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	me, err := c.WhoAmI(ctx)
	if err != nil {
		return "", err
	}
	// whoami answers for the token, so its handle is the token's name. Mentions match people by
	// their own handle, which the actor record has.
	person := me
	if act, aerr := c.GetActor(ctx, me.ID); aerr == nil && act != nil && act.Handle != "" {
		person = act
	}
	ln := &store.Link{UserID: userID, ActorID: me.ID, Handle: person.Handle, Email: me.Email, TenantKey: me.TenantKey, TokenID: me.TokenID, LinkedAt: time.Now()}
	if err := f.a.Store.SaveLink(ln, token); err != nil {
		return "", err
	}
	f.a.InvalidateUser(userID)
	return l.T("link.connected_token", "name", person.Name(), "tenant", me.TenantName), nil
}

var errNoMatch = i18n.NewError("link.err.no_match")

// ErrNoMatch is returned by ConnectByEmail when no tix person has the user's verified email.
var ErrNoMatch = errNoMatch

// ErrNoAdminToken is returned by ConnectByEmail when no admin token is configured to issue tokens with.
var ErrNoAdminToken = i18n.NewError("link.err.no_admin_token")

// ConnectByEmail runs the email-matched connect of `/tix connect` for the webapp and returns its
// message, in the user's language.
func (f *Feature) ConnectByEmail(userID string) (string, error) {
	if f.a.Config().AdminToken == "" {
		return "", ErrNoAdminToken
	}
	return f.linkByEmail(f.a.Locale(userID), userID)
}

// linkByEmail finds the tix person with the user's verified email and issues them a token.
func (f *Feature) linkByEmail(l *i18n.Localizer, userID string) (string, error) {
	u, appErr := f.a.API.GetUser(userID)
	if appErr != nil {
		return "", appErr
	}
	if !u.EmailVerified || u.Email == "" {
		return "", i18n.Wrap(errNoMatch, "link.err.email_unverified")
	}
	admin, err := f.a.NewTix(f.a.Config().AdminToken)
	if err != nil {
		return "", err
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	users, err := admin.ListUsers(ctx)
	if err != nil {
		return "", i18n.Wrap(err, "link.err.list_users")
	}
	var match *tix.User
	for i := range users {
		if !users[i].Removed && users[i].DisabledAt == "" && strings.EqualFold(users[i].Email, u.Email) {
			match = &users[i]
			break
		}
	}
	if match == nil {
		return "", errNoMatch
	}
	members, err := admin.ListMembers(ctx)
	if err != nil {
		return "", i18n.Wrap(err, "link.err.list_members")
	}
	role := ""
	for _, m := range members {
		if m.ActorID == match.ID {
			role = m.Role
		}
	}
	scopes := tix.MemberScopes
	switch role {
	case "member", "admin":
	case "viewer":
		scopes = tix.ViewerScopes
	default:
		return "", i18n.Wrap(errNoMatch, "link.err.not_member")
	}
	actor, err := admin.GetActor(ctx, match.ID)
	if err != nil {
		return "", i18n.Wrap(err, "link.err.read_actor")
	}
	if old, gerr := f.a.Store.GetLink(userID); gerr == nil && old.Minted {
		f.revoke(old)
	}
	name := fmt.Sprintf("mattermost-%s-%d", u.Username, time.Now().Unix())
	issued, err := admin.CreateToken(ctx, name, match.ID, scopes)
	if err != nil {
		return "", i18n.Wrap(err, "link.err.issue_token")
	}
	ln := &store.Link{UserID: userID, ActorID: match.ID, Handle: actor.Handle, Email: match.Email, TenantKey: actor.TenantKey, TokenID: issued.ID, Minted: true, LinkedAt: time.Now()}
	if err := f.a.Store.SaveLink(ln, issued.Token); err != nil {
		return "", err
	}
	f.a.InvalidateUser(userID)
	return l.T("link.connected_email", "name", actor.Name(), "token", name), nil
}

// revoke deletes the token a link holds, as the linked person, falling back to the admin token.
func (f *Feature) revoke(l *store.Link) {
	if l.TokenID == "" {
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	if tok, err := f.a.Store.Token(l); err == nil {
		if c, err := f.a.NewTix(tok); err == nil && c.RevokeToken(ctx, l.TokenID) == nil {
			return
		}
	}
	if f.a.Config().AdminToken != "" {
		if c, err := f.a.NewTix(f.a.Config().AdminToken); err == nil {
			_ = c.RevokeToken(ctx, l.TokenID)
		}
	}
}

func (f *Feature) disconnect(c *command.Ctx, _ []string) *model.CommandResponse {
	lang := c.L()
	l, err := f.a.Store.GetLink(c.Args.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return command.Ephemeral(lang.T("link.disconnect.not_connected"))
	} else if err != nil {
		return command.Ephemeral(app.ErrText(lang, err))
	}
	if l.Minted {
		f.revoke(l)
	}
	if err := f.a.Store.DeleteLink(c.Args.UserId); err != nil {
		return command.Ephemeral(app.ErrText(lang, err))
	}
	f.a.InvalidateUser(c.Args.UserId)
	msg := lang.T("link.disconnect.done")
	if !l.Minted {
		msg += " " + lang.T("link.disconnect.pasted_token")
	}
	return command.Ephemeral(msg)
}

func (f *Feature) me(c *command.Ctx, _ []string) *model.CommandResponse {
	lang := c.L()
	tc, l, err := f.a.TixFor(c.Args.UserId)
	if err != nil {
		return command.Ephemeral(app.ErrText(lang, err))
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	me, err := tc.WhoAmI(ctx)
	if err != nil {
		return command.Ephemeral(app.ErrText(lang, err))
	}
	person, handle := me.Name(), ""
	if act, aerr := f.a.Actor(c.Args.UserId, l.ActorID); aerr == nil && act != nil {
		person, handle = act.Name(), " (`"+act.Handle+"`)"
	}
	scopes := slices.Clone(me.Scopes)
	slices.Sort(scopes)
	tenant := ""
	if me.TenantName != "" {
		tenant = lang.T("link.me.tenant", "tenant", me.TenantName)
	}
	return command.Ephemeral(lang.T("link.me", "url", f.a.TixURL(), "name", person, "handle", handle, "tenant", tenant,
		"since", f.a.RendererFor(c.Args.UserId).Date(l.LinkedAt), "token", me.Handle, "scopes", strings.Join(scopes, "`, `")))
}
