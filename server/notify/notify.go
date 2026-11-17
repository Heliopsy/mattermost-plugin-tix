// SPDX-License-Identifier: AGPL-3.0-only

// Package notify receives tix webhooks: it posts project activity into subscribed channels, DMs people about their tasks and redraws task cards.
package notify

import (
	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
)

// Refresher redraws task cards (the cards feature).
type Refresher interface{ Refresh(ref string) }

// Feature is the notify feature.
type Feature struct {
	a     *app.App
	cards Refresher
}

// New returns the feature.
func New(a *app.App, cards Refresher) *Feature { return &Feature{a: a, cards: cards} }

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub { return nil }

// Routes mounts the feature's HTTP routes.
func (f *Feature) Routes(h *api.Handler) {}
