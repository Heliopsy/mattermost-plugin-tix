// SPDX-License-Identifier: AGPL-3.0-only

// Package tasks is /tix for working tasks: new, show, list, start, done, move, comment, assign, plus the autocomplete lists.
package tasks

import (
	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
)

// Feature is the tasks feature.
type Feature struct{ a *app.App }

// New returns the feature.
func New(a *app.App) *Feature { return &Feature{a: a} }

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub { return nil }

// Routes mounts the feature's HTTP routes.
func (f *Feature) Routes(h *api.Handler) {}
