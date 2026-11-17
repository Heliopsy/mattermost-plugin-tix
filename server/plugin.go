// SPDX-License-Identifier: AGPL-3.0-only

package main

import (
	"net/http"
	"sync"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/pkg/errors"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/cards"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/link"
	"github.com/heliopsy/mattermost-plugin-tix/server/notify"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tasks"
)

// Plugin implements the interface expected by the Mattermost server.
type Plugin struct {
	plugin.MattermostPlugin

	configurationLock sync.RWMutex
	configuration     *configuration

	app      *app.App
	http     *api.Handler
	commands *command.Registry
	cards    *cards.Feature
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
		DisplayName: "tix",
		Description: "Tasks from tix.",
	}, pluginapi.ProfileImagePath("assets/icon.png"))
	if err != nil {
		return errors.Wrap(err, "failed to ensure the tix bot")
	}

	st := store.New(&client.KV, func() string { return p.getConfiguration().EncryptionKey })
	p.app = app.New(p.API, client, st, botID, func() *app.Config {
		c := p.getConfiguration()
		return &app.Config{TixURL: c.TixURL, AdminToken: c.AdminToken, EncryptionKey: c.EncryptionKey, WebhookSecret: c.WebhookSecret, UnfurlRefs: c.UnfurlRefs}
	})

	p.http = api.NewHandler(p.API)
	p.commands = command.NewRegistry(p.app)
	p.cards = cards.New(p.app)
	for _, f := range []feature{link.New(p.app), tasks.New(p.app), p.cards, notify.New(p.app, p.cards)} {
		p.commands.Add(f.Commands()...)
		f.Routes(p.http)
	}
	return p.commands.Register(client)
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

// MessageHasBeenPosted lets the cards feature remember which posts show which tasks.
func (p *Plugin) MessageHasBeenPosted(c *plugin.Context, post *model.Post) {
	p.cards.MessageHasBeenPosted(c, post)
}
