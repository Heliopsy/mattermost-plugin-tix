// SPDX-License-Identifier: AGPL-3.0-only

// Package cards turns task references in messages into live task cards with action buttons, and offers creating a task from a message.
package cards

import (
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
)

// Feature is the cards feature.
type Feature struct{ a *app.App }

// New returns the feature.
func New(a *app.App) *Feature { return &Feature{a: a} }

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub { return nil }

// Routes mounts the feature's HTTP routes.
func (f *Feature) Routes(h *api.Handler) {}

// Refresh redraws every card showing the task. notify calls it when tix reports a change.
func (f *Feature) Refresh(ref string) {}

// MessageWillBePosted attaches cards for the task references in a new message.
func (f *Feature) MessageWillBePosted(_ *plugin.Context, post *model.Post) (*model.Post, string) {
	return post, ""
}

// MessageHasBeenPosted records which posts show which tasks.
func (f *Feature) MessageHasBeenPosted(_ *plugin.Context, _ *model.Post) {}
