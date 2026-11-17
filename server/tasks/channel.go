// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/command"
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
		return command.Ephemeral(f.showChannel(c.Args.UserId, c.Args.ChannelId))
	}
	if !strings.EqualFold(args[0], "project") || len(args) != 2 {
		return command.Ephemeral("usage: `/tix channel` or `/tix channel project <key>|none`")
	}
	msg, err := f.setChannelProject(c.Plugin, c.Args.UserId, c.Args.ChannelId, args[1])
	if err != nil {
		return command.Ephemeral(err.Error())
	}
	return command.Ephemeral(msg)
}

func (f *Feature) showChannel(userID, channelID string) string {
	p := f.ChannelProject(userID, channelID)
	if p == "" {
		return "This channel has no default tix project. A channel admin can set one with `/tix channel project <key>`."
	}
	return fmt.Sprintf("Default tix project for this channel: **%s**. `/tix new <title>` and the %s reaction create tasks there.", strings.ToUpper(p), f.emojiText())
}

// errNotChannelAdmin is the answer to someone who may not change the channel's settings.
var errNotChannelAdmin = errors.New("only channel admins can change this channel's tix settings")

// setChannelProject sets or clears (none) a channel's default project as userID.
func (f *Feature) setChannelProject(pc *plugin.Context, userID, channelID, key string) (string, error) {
	if pc == nil {
		pc = &plugin.Context{}
	}
	rec := plugin.MakeAuditRecordWithContext("tixSetChannelProject", model.AuditStatusFail, pc, userID, "/tix channel project")
	defer f.a.API.LogAuditRec(rec)
	if !model.IsValidId(channelID) {
		return "", errors.New("run this in a channel")
	}
	model.AddEventParameterToAuditRec(rec, "channel_id", channelID)
	if !f.canManageChannel(userID, channelID) {
		return "", errNotChannelAdmin
	}
	key = strings.TrimSpace(key)
	unset := strings.EqualFold(key, "none") || key == "-"
	if !unset {
		if !validProjectKey(key) {
			return "", fmt.Errorf("`%s` is not a project key", excerpt(key, maxProjectKey))
		}
		e, err := f.envFor(userID)
		if err != nil {
			return "", err
		}
		if !e.isProject(key) {
			return "", fmt.Errorf("you cannot see a tix project `%s`", key)
		}
		key = strings.ToLower(key)
		model.AddEventParameterToAuditRec(rec, "project", key)
	}
	var v any
	if !unset {
		v = channelDefaults{Project: key, SetBy: userID, SetAt: time.Now().UTC()}
	}
	if _, err := f.a.Store.Set(storeFeature, channelKey(channelID), v); err != nil {
		return "", errors.New("could not save the channel setting, try again")
	}
	rec.Success()
	if unset {
		return "This channel no longer has a default tix project.", nil
	}
	return fmt.Sprintf("New tasks from this channel go to **%s** by default.", strings.ToUpper(key)), nil
}
