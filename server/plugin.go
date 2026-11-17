// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/cards"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/digest"
	"github.com/heliopsy/mattermost-plugin-tix/server/link"
	"github.com/heliopsy/mattermost-plugin-tix/server/notify"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tasks"
	"github.com/heliopsy/mattermost-plugin-tix/server/webapi"
)

// Plugin implements the interface expected by the Mattermost server.
type Plugin struct {
	plugin.MattermostPlugin

	configurationLock sync.RWMutex
	configuration     *configuration

	// botID is set once the bot exists; configuration changes before that leave the bot alone.
	botID atomic.Pointer[string]
	// botMu serializes applying the bot's name and icon; iconApplied is the custom icon URL this
	// process set (EnsureBot puts the default back on every activation).
	botMu       sync.Mutex
	iconApplied string

	app      *app.App
	http     *api.Handler
	commands *command.Registry
	cards    *cards.Feature
	tasks    *tasks.Feature
	digest   *digest.Feature
}

// feature is what every feature package offers.
type feature interface {
	Commands() []command.Sub
	Routes(h *api.Handler)
}

// OnActivate wires the features together.
func (p *Plugin) OnActivate() error {
	client := pluginapi.NewClient(p.API, p.Driver)

	botID, err := client.Bot.EnsureBot(&model.Bot{
		Username:    "tix",
		DisplayName: botDisplayName(p.getConfiguration().BotDisplayName),
		Description: "Tasks from tix.",
	}, pluginapi.ProfileImagePath(defaultBotIcon))
	if err != nil {
		return errors.Wrap(err, "failed to ensure the tix bot")
	}
	p.botID.Store(&botID)

	st := store.New(&client.KV, func() string { return p.getConfiguration().EncryptionKey })
	p.app = app.New(p.API, client, st, botID, func() *app.Config {
		c := p.getConfiguration()
		return &app.Config{
			TixURL: c.TixURL, AdminToken: c.AdminToken, EncryptionKey: c.EncryptionKey, WebhookSecret: c.WebhookSecret,
			UnfurlRefs: c.UnfurlRefs, CardStyle: c.CardStyle, CardQuietMinutes: quietMinutes(c.CardQuietMinutes),
			CreateEmoji: c.CreateEmoji, Format: c.format, BotDisplayName: c.BotDisplayName, BotIconURL: c.BotIconURL,
		}
	})

	p.http = api.NewHandler(p.API)
	p.commands = command.NewRegistry(p.app)
	p.cards = cards.New(p.app)
	p.digest = digest.New(p.app)
	linker := link.New(p.app)
	p.tasks = tasks.New(p.app)
	web := webapi.New(p.app, linker.ConnectByEmail)
	web.ChannelProject = p.tasks.ChannelProject
	for _, f := range []feature{linker, p.tasks, p.cards, notify.New(p.app, p.cards), web, p.digest} {
		p.commands.Add(f.Commands()...)
		f.Routes(p.http)
	}
	if err := p.commands.Register(client); err != nil {
		return err
	}
	p.applyBotSettings()
	return p.digest.Start(p.API)
}

// OnDeactivate stops the digest job on this node.
func (p *Plugin) OnDeactivate() error {
	if p.digest == nil {
		return nil
	}
	return p.digest.Stop()
}

// ServeHTTP is invoked for requests to /plugins/{plugin_id}.
func (p *Plugin) ServeHTTP(c *plugin.Context, w http.ResponseWriter, r *http.Request) {
	p.http.ServeHTTP(c, w, r)
}

// ExecuteCommand runs /tix.
func (p *Plugin) ExecuteCommand(c *plugin.Context, args *model.CommandArgs) (*model.CommandResponse, *model.AppError) {
	return p.commands.Execute(c, args), nil
}

// MessageWillBePosted lets the cards feature attach task cards to new messages.
func (p *Plugin) MessageWillBePosted(c *plugin.Context, post *model.Post) (*model.Post, string) {
	return p.cards.MessageWillBePosted(c, post)
}

// MessageHasBeenPosted lets the cards feature answer with task cards and mirror synced replies.
func (p *Plugin) MessageHasBeenPosted(c *plugin.Context, post *model.Post) {
	p.cards.MessageHasBeenPosted(c, post)
}

// ReactionHasBeenAdded lets the tasks feature create a task from a message reacted to with the create emoji.
func (p *Plugin) ReactionHasBeenAdded(c *plugin.Context, reaction *model.Reaction) {
	p.tasks.ReactionHasBeenAdded(c, reaction)
}

// MessageHasBeenUpdated lets the cards feature card a reference added by an edit.
func (p *Plugin) MessageHasBeenUpdated(c *plugin.Context, newPost, oldPost *model.Post) {
	p.cards.MessageHasBeenUpdated(c, newPost, oldPost)
}

// MessageHasBeenDeleted lets the cards feature remove the card that answered a deleted post.
func (p *Plugin) MessageHasBeenDeleted(c *plugin.Context, post *model.Post) {
	p.cards.MessageHasBeenDeleted(c, post)
}

// quietMinutes reads the CardQuietMinutes setting; anything unreadable means the default.
func quietMinutes(n json.Number) int {
	v, err := strconv.Atoi(strings.TrimSpace(n.String()))
	if err != nil || v < 0 {
		return 0
	}
	return v
}
