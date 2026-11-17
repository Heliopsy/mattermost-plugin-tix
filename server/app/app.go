// SPDX-License-Identifier: AGPL-3.0-only

// Package app holds what every feature shares: the Mattermost API, the store,
// the bot, the configuration and a tix client per linked user.
package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// PluginID is the plugin's manifest id.
const PluginID = "cc.heliopsy.tix"

// Config is the plugin's System Console configuration.
type Config struct {
	// TixURL is the root of the tix server, e.g. https://tix.example.com.
	TixURL string
	// AdminToken is an optional tix token with token:admin (and actor reads) used to issue each
	// person a token of their own on /tix connect, matched by verified email, and to register
	// the plugin's webhook. Without it people paste a token they made in tix.
	AdminToken string
	// EncryptionKey encrypts stored personal tokens (generated setting).
	EncryptionKey string
	// WebhookSecret signs the webhooks tix sends to the plugin (generated setting).
	WebhookSecret string
	// UnfurlRefs turns task references in messages into task cards.
	UnfurlRefs bool
	// CardStyle is "reply" (the bot answers in the thread, the default) or "inline" (the card is
	// attached to the person's own post).
	CardStyle string
	// CardQuietMinutes: a task carded in a channel gets no new card there for this long (0 = default 10).
	CardQuietMinutes int
	// CreateEmoji is the reaction that turns a message into a task, without colons.
	CreateEmoji string
}

// ErrNotLinked is returned when the acting user has not run /tix connect.
var ErrNotLinked = errors.New("your Mattermost account is not connected to tix yet: run `/tix connect`")

// App is shared by every feature package.
type App struct {
	API    plugin.API
	Client *pluginapi.Client
	Store  *store.Store
	BotID  string
	Config func() *Config

	mu       sync.Mutex
	projects map[string]cached[[]tix.Project]
	flows    map[string]cached[[]tix.Workflow]
	actors   map[string]cached[*tix.Actor]
}

type cached[T any] struct {
	v  T
	at time.Time
}

const cacheTTL = 2 * time.Minute

// New returns the shared app.
func New(api plugin.API, client *pluginapi.Client, st *store.Store, botID string, cfg func() *Config) *App {
	return &App{
		API: api, Client: client, Store: st, BotID: botID, Config: cfg,
		projects: map[string]cached[[]tix.Project]{},
		flows:    map[string]cached[[]tix.Workflow]{},
		actors:   map[string]cached[*tix.Actor]{},
	}
}

// Ctx returns a context for one tix call.
func Ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 15*time.Second)
}

// TixURL is the configured server root without a trailing slash.
func (a *App) TixURL() string {
	return strings.TrimRight(strings.TrimSpace(a.Config().TixURL), "/")
}

// NewTix returns a client for token against the configured server.
func (a *App) NewTix(token string) (*tix.Client, error) {
	if a.TixURL() == "" {
		return nil, errors.New("the tix server URL is not configured; an admin sets it in System Console > Plugins > tix")
	}
	return tix.New(a.TixURL(), token), nil
}

// TixFor returns a client acting as the Mattermost user, or ErrNotLinked.
func (a *App) TixFor(userID string) (*tix.Client, *store.Link, error) {
	l, err := a.Store.GetLink(userID)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, ErrNotLinked
	} else if err != nil {
		return nil, nil, err
	}
	tok, err := a.Store.Token(l)
	if err != nil {
		return nil, nil, err
	}
	c, err := a.NewTix(tok)
	if c != nil {
		c.Me = l.ActorID
	}
	return c, l, err
}

// Projects lists the projects visible to the user, cached briefly.
func (a *App) Projects(userID string) ([]tix.Project, error) {
	a.mu.Lock()
	if c, ok := a.projects[userID]; ok && time.Since(c.at) < cacheTTL {
		a.mu.Unlock()
		return c.v, nil
	}
	a.mu.Unlock()
	c, _, err := a.TixFor(userID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := Ctx()
	defer cancel()
	ps, err := c.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.projects[userID] = cached[[]tix.Project]{ps, time.Now()}
	a.mu.Unlock()
	return ps, nil
}

// Workflows lists the tenant's workflows, cached briefly.
func (a *App) Workflows(userID string) ([]tix.Workflow, error) {
	a.mu.Lock()
	if c, ok := a.flows[userID]; ok && time.Since(c.at) < cacheTTL {
		a.mu.Unlock()
		return c.v, nil
	}
	a.mu.Unlock()
	c, _, err := a.TixFor(userID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := Ctx()
	defer cancel()
	ws, err := c.ListWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.flows[userID] = cached[[]tix.Workflow]{ws, time.Now()}
	a.mu.Unlock()
	return ws, nil
}

// WorkflowFor returns the workflow of the project a task belongs to.
func (a *App) WorkflowFor(userID, projectID string) (*tix.Workflow, *tix.Project, error) {
	ps, err := a.Projects(userID)
	if err != nil {
		return nil, nil, err
	}
	var proj *tix.Project
	for i := range ps {
		if ps[i].ID == projectID || strings.EqualFold(ps[i].Key, projectID) {
			proj = &ps[i]
			break
		}
	}
	if proj == nil {
		return nil, nil, fmt.Errorf("project %s not found", projectID)
	}
	ws, err := a.Workflows(userID)
	if err != nil {
		return nil, nil, err
	}
	for i := range ws {
		if ws[i].ID == proj.WorkflowID {
			return &ws[i], proj, nil
		}
	}
	return nil, proj, fmt.Errorf("workflow of project %s not found", proj.Key)
}

// Actor returns a tix actor by id as the user sees it, cached briefly.
func (a *App) Actor(userID, actorID string) (*tix.Actor, error) {
	if actorID == "" {
		return nil, nil
	}
	a.mu.Lock()
	if c, ok := a.actors[actorID]; ok && time.Since(c.at) < 10*time.Minute {
		a.mu.Unlock()
		return c.v, nil
	}
	a.mu.Unlock()
	c, _, err := a.TixFor(userID)
	if err != nil {
		return nil, err
	}
	ctx, cancel := Ctx()
	defer cancel()
	act, err := c.GetActor(ctx, actorID)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.actors[actorID] = cached[*tix.Actor]{act, time.Now()}
	a.mu.Unlock()
	return act, nil
}

// InvalidateUser drops cached data of one user (after connect or disconnect).
func (a *App) InvalidateUser(userID string) {
	a.mu.Lock()
	delete(a.projects, userID)
	delete(a.flows, userID)
	a.mu.Unlock()
}

// MentionFor renders a tix actor as an @mention when they are linked, else their name.
func (a *App) MentionFor(viewerID, actorID string) string {
	if actorID == "" {
		return "nobody"
	}
	if uid, err := a.Store.UserForActor(actorID); err == nil {
		if u, err := a.API.GetUser(uid); err == nil {
			return "@" + u.Username
		}
	}
	if act, err := a.Actor(viewerID, actorID); err == nil && act != nil {
		return act.Name()
	}
	return actorID
}

// TaskURL links to a task in the tix web interface.
func (a *App) TaskURL(t *tix.Task) string {
	if t.WebURL != "" {
		return t.WebURL
	}
	return a.TixURL() + "/tasks/" + t.Ref
}

// Ephemeral shows a bot message only to one user.
func (a *App) Ephemeral(userID, channelID, rootID, msg string) {
	a.API.SendEphemeralPost(userID, &model.Post{UserId: a.BotID, ChannelId: channelID, RootId: rootID, Message: msg})
}

// DM sends a direct message from the bot to a user.
func (a *App) DM(userID string, post *model.Post) error {
	ch, appErr := a.API.GetDirectChannel(userID, a.BotID)
	if appErr != nil {
		return appErr
	}
	post.UserId = a.BotID
	post.ChannelId = ch.Id
	_, appErr = a.API.CreatePost(post)
	if appErr != nil {
		return appErr
	}
	return nil
}

// PluginURL is the absolute URL of a plugin route, for interactive message actions.
func (a *App) PluginURL(path string) string {
	site := ""
	if cfg := a.API.GetConfig(); cfg != nil && cfg.ServiceSettings.SiteURL != nil {
		site = strings.TrimRight(*cfg.ServiceSettings.SiteURL, "/")
	}
	return site + "/plugins/" + PluginID + path
}

// ErrText turns an error into a short line for a user.
func ErrText(err error) string {
	var te *tix.Error
	if errors.As(err, &te) {
		switch te.Status {
		case 401:
			return "tix rejected your token (expired or revoked?). Reconnect with `/tix connect`."
		case 403:
			return "tix says you are not allowed to do that: " + te.Message
		case 404:
			return "Not found in tix."
		}
		return "tix: " + te.Message
	}
	return err.Error()
}
