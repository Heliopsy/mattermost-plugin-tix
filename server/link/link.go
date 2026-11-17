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
		{Name: "connect", Hint: "[token]", Help: "Connect your Mattermost account to tix", Run: f.connect},
		{Name: "disconnect", Help: "Disconnect from tix and revoke the token the plugin holds", Run: f.disconnect},
		{Name: "me", Aliases: []string{"whoami"}, Help: "Show the tix account you are connected as", Run: f.me},
	}
}

// Routes mounts the connect dialog's submit handler.
func (f *Feature) Routes(h *api.Handler) {
	h.User.HandleFunc("/dialog/connect", f.dialogSubmit).Methods(http.MethodPost)
}

func (f *Feature) connect(c *command.Ctx, args []string) *model.CommandResponse {
	userID := c.Args.UserId
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		msg, err := f.linkWithToken(userID, strings.TrimSpace(args[0]))
		if err != nil {
			return command.Ephemeral(app.ErrText(err))
		}
		return command.Ephemeral(msg)
	}
	if f.a.Config().AdminToken != "" {
		msg, err := f.linkByEmail(userID)
		if err == nil {
			return command.Ephemeral(msg)
		}
		f.a.API.LogInfo("tix: automatic connect failed, offering the token dialog", "user_id", userID, "err", err.Error())
		if !errors.Is(err, errNoMatch) {
			return command.Ephemeral(app.ErrText(err))
		}
	}
	return f.openDialog(c)
}

func (f *Feature) openDialog(c *command.Ctx) *model.CommandResponse {
	tokensURL := f.a.TixURL() + "/connect?tab=api"
	dlg := model.OpenDialogRequest{
		TriggerId: c.Args.TriggerId,
		URL:       f.a.PluginURL("/api/v1/dialog/connect"),
		Dialog: model.Dialog{
			Title:            "Connect to tix",
			IntroductionText: fmt.Sprintf("Make a token in tix ([%s](%s)) with the task, project, workflow and comment scopes, then paste it here. The plugin stores it encrypted and acts as you with it.", tokensURL, tokensURL),
			SubmitLabel:      "Connect",
			Elements: []model.DialogElement{{
				DisplayName: "tix token", Name: "token", Type: "text", SubType: "password",
				Placeholder: "tix_...",
			}},
		},
	}
	if err := f.a.API.OpenInteractiveDialog(dlg); err != nil {
		return command.Ephemeral("Run `/tix connect <token>` with a token made in tix: " + tokensURL)
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
	msg, err := f.linkWithToken(req.UserId, strings.TrimSpace(tok))
	if err != nil {
		api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{Errors: map[string]string{"token": app.ErrText(err)}})
		return
	}
	f.a.Ephemeral(req.UserId, req.ChannelId, "", msg)
	api.WriteJSON(w, r, http.StatusOK, model.SubmitDialogResponse{})
}

// linkWithToken checks a pasted token and stores it.
func (f *Feature) linkWithToken(userID, token string) (string, error) {
	if token == "" {
		return "", errors.New("paste a token")
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
	l := &store.Link{UserID: userID, ActorID: me.ID, Handle: me.Handle, Email: me.Email, TenantKey: me.TenantKey, TokenID: me.TokenID, LinkedAt: time.Now()}
	if err := f.a.Store.SaveLink(l, token); err != nil {
		return "", err
	}
	f.a.InvalidateUser(userID)
	return fmt.Sprintf("Connected to tix as **%s** in tenant **%s**.", me.Name(), me.TenantName), nil
}

var errNoMatch = errors.New("no tix account matches your email")

// ErrNoMatch is returned by ConnectByEmail when no tix person has the user's verified email.
var ErrNoMatch = errNoMatch

// ErrNoAdminToken is returned by ConnectByEmail when no admin token is configured to issue tokens with.
var ErrNoAdminToken = errors.New("connecting by email needs the admin token; run `/tix connect` with a token made in tix")

// ConnectByEmail runs the email-matched connect of `/tix connect` for the webapp and returns its message.
func (f *Feature) ConnectByEmail(userID string) (string, error) {
	if f.a.Config().AdminToken == "" {
		return "", ErrNoAdminToken
	}
	return f.linkByEmail(userID)
}

// linkByEmail finds the tix person with the user's verified email and issues them a token.
func (f *Feature) linkByEmail(userID string) (string, error) {
	u, appErr := f.a.API.GetUser(userID)
	if appErr != nil {
		return "", appErr
	}
	if !u.EmailVerified || u.Email == "" {
		return "", fmt.Errorf("%w: your Mattermost email is not verified", errNoMatch)
	}
	admin, err := f.a.NewTix(f.a.Config().AdminToken)
	if err != nil {
		return "", err
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	actors, err := admin.ListActors(ctx)
	if err != nil {
		return "", fmt.Errorf("listing tix accounts with the admin token: %w", err)
	}
	var match *tix.Actor
	for i := range actors {
		if actors[i].Kind == "user" && strings.EqualFold(actors[i].Email, u.Email) {
			match = &actors[i]
			break
		}
	}
	if match == nil {
		return "", errNoMatch
	}
	scopes := tix.MemberScopes
	if match.Role == "viewer" {
		scopes = tix.ViewerScopes
	}
	if old, gerr := f.a.Store.GetLink(userID); gerr == nil && old.Minted {
		f.revoke(old)
	}
	name := fmt.Sprintf("mattermost-%s-%d", u.Username, time.Now().Unix())
	issued, err := admin.CreateToken(ctx, name, match.ID, scopes)
	if err != nil {
		return "", fmt.Errorf("issuing your tix token: %w", err)
	}
	l := &store.Link{UserID: userID, ActorID: match.ID, Handle: match.Handle, Email: match.Email, TenantKey: match.TenantKey, TokenID: issued.ID, Minted: true, LinkedAt: time.Now()}
	if err := f.a.Store.SaveLink(l, issued.Token); err != nil {
		return "", err
	}
	f.a.InvalidateUser(userID)
	return fmt.Sprintf("Connected to tix as **%s** (matched by your email). tix records what you do here as you; the token is named `%s` and `/tix disconnect` revokes it.", match.Name(), name), nil
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
	l, err := f.a.Store.GetLink(c.Args.UserId)
	if errors.Is(err, store.ErrNotFound) {
		return command.Ephemeral("You are not connected to tix.")
	} else if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	if l.Minted {
		f.revoke(l)
	}
	if err := f.a.Store.DeleteLink(c.Args.UserId); err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	f.a.InvalidateUser(c.Args.UserId)
	msg := "Disconnected from tix. The plugin no longer holds a token for you."
	if !l.Minted {
		msg += " The token you pasted still works in tix; revoke it there if you no longer need it."
	}
	return command.Ephemeral(msg)
}

func (f *Feature) me(c *command.Ctx, _ []string) *model.CommandResponse {
	tc, l, err := f.a.TixFor(c.Args.UserId)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	me, err := tc.WhoAmI(ctx)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	scopes := slices.Clone(me.Scopes)
	slices.Sort(scopes)
	return command.Ephemeral(fmt.Sprintf("Connected to **%s** as **%s** (`%s`) in tenant **%s** since %s.\nScopes: `%s`",
		f.a.TixURL(), me.Name(), me.Handle, me.TenantName, l.LinkedAt.Format("02.01.2006"), strings.Join(scopes, "`, `")))
}
