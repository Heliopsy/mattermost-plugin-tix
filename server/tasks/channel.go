// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

// storeFeature namespaces the feature's records in the store (x_tasks_...).
const storeFeature = "tasks"

// maxProjectKey bounds a project key typed into /tix channel project.
const maxProjectKey = 64

// channelDefaults is what a channel sets for tasks made in it.
type channelDefaults struct {
	Project string    `json:"project"`
	SetBy   string    `json:"set_by"`
	SetAt   time.Time `json:"set_at"`
}

func channelKey(channelID string) string { return "chproj_" + channelID }

// channelDefaults reads a channel's record; a missing record is the zero value.
func (f *Feature) channelDefaults(channelID string) (channelDefaults, error) {
	var d channelDefaults
	if !model.IsValidId(channelID) {
		return d, nil
	}
	if err := f.a.Store.Get(storeFeature, channelKey(channelID), &d); err != nil {
		return channelDefaults{}, err
	}
	return d, nil
}

// ChannelProject is the default project of a channel, for a user who can read that channel,
// and "" otherwise. Every entry point (slash commands, autocomplete, the webapp API) asks
// through here, so the permission check lives in one place.
func (f *Feature) ChannelProject(userID, channelID string) string {
	if !model.IsValidId(userID) || !model.IsValidId(channelID) ||
		!f.a.API.HasPermissionToChannel(userID, channelID, model.PermissionReadChannelContent) {
		return ""
	}
	d, err := f.channelDefaults(channelID)
	if err != nil {
		return ""
	}
	return d.Project
}

// canManageChannel tells whether a user may change a channel's tix settings: channel admins
// (and the team and system admins who hold the same permission).
func (f *Feature) canManageChannel(userID, channelID string) bool {
	return f.a.API.HasPermissionToChannel(userID, channelID, model.PermissionManageChannelRoles)
}

// projectKeyChars are the characters of a project key, lower-cased.
const projectKeyChars = "abcdefghijklmnopqrstuvwxyz0123456789_-"

// validProjectKey accepts what tix project keys are made of.
func validProjectKey(k string) bool {
	if k == "" || len(k) > maxProjectKey {
		return false
	}
	return !strings.ContainsFunc(strings.ToLower(k), func(c rune) bool {
		return !strings.ContainsRune(projectKeyChars, c)
	})
}

// cmdChannel is /tix channel [project <key>|none].
func (f *Feature) cmdChannel(c *command.Ctx, args []string) *model.CommandResponse {
	if len(args) == 0 || (len(args) == 1 && strings.EqualFold(args[0], "project")) {
		return command.Ephemeral(f.showChannel(c.L(), c.Args.UserId, c.Args.ChannelId))
	}
	if !strings.EqualFold(args[0], "project") || len(args) != 2 {
		return command.Ephemeral(c.L().T("tasks.channel.usage"))
	}
	msg, err := f.setChannelProject(c.L(), c.Plugin, c.Args.UserId, c.Args.ChannelId, args[1])
	if err != nil {
		return command.Ephemeral(app.ErrText(c.L(), err))
	}
	return command.Ephemeral(msg)
}

func (f *Feature) showChannel(l *i18n.Localizer, userID, channelID string) string {
	p := f.ChannelProject(userID, channelID)
	if p == "" {
		return l.T("tasks.channel.none")
	}
	return l.T("tasks.channel.show", "project", strings.ToUpper(p), "emoji", f.emojiText())
}

// errNotChannelAdmin is the answer to someone who may not change the channel's settings.
var errNotChannelAdmin = i18n.NewError("tasks.err.not_channel_admin")

// setChannelProject sets or clears (none) a channel's default project as userID and says so in l.
func (f *Feature) setChannelProject(l *i18n.Localizer, pc *plugin.Context, userID, channelID, key string) (string, error) {
	if pc == nil {
		pc = &plugin.Context{}
	}
	rec := plugin.MakeAuditRecordWithContext("tixSetChannelProject", model.AuditStatusFail, pc, userID, "/tix channel project")
	defer f.a.API.LogAuditRec(rec)
	if !model.IsValidId(channelID) {
		return "", i18n.NewError("tasks.err.not_in_channel")
	}
	model.AddEventParameterToAuditRec(rec, "channel_id", channelID)
	if !f.canManageChannel(userID, channelID) {
		return "", errNotChannelAdmin
	}
	key = strings.TrimSpace(key)
	unset := strings.EqualFold(key, "none") || key == "-"
	if !unset {
		if !validProjectKey(key) {
			return "", i18n.NewError("tasks.err.not_project_key", "key", excerpt(key, maxProjectKey))
		}
		e, err := f.envFor(userID)
		if err != nil {
			return "", err
		}
		if !e.isProject(key) {
			return "", i18n.NewError("tasks.err.project_hidden", "key", key)
		}
		key = strings.ToLower(key)
		model.AddEventParameterToAuditRec(rec, "project", key)
	}
	var v any
	if !unset {
		v = channelDefaults{Project: key, SetBy: userID, SetAt: time.Now().UTC()}
	}
	if _, err := f.a.Store.Set(storeFeature, channelKey(channelID), v); err != nil {
		return "", i18n.NewError("tasks.err.save_channel")
	}
	rec.Success()
	if unset {
		return l.T("tasks.channel.cleared"), nil
	}
	return l.T("tasks.channel.set", "project", strings.ToUpper(key)), nil
}
