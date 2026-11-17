// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

const (
	// defaultCreateEmoji is the reaction that creates a task when the setting is empty.
	defaultCreateEmoji = "ticket"
	// maxReactTitle caps a title taken from a message, in runes.
	maxReactTitle = 120
	// maxReactBody caps the message text copied into the task body, in bytes.
	maxReactBody = 8000
	// pendingTTL is how long a claim on a message blocks another attempt while its task is created.
	pendingTTL = 2 * time.Minute
	// PropCreatedRef marks the bot's "created" reply with the task it announces.
	PropCreatedRef = "tix_created_ref"
)

// reactRecord remembers the task made from a message with the create emoji.
type reactRecord struct {
	Ref     string    `json:"ref,omitempty"`
	URL     string    `json:"url,omitempty"`
	Pending bool      `json:"pending,omitempty"`
	At      time.Time `json:"at"`
}

// emojiChars are the characters of an emoji name.
const emojiChars = "abcdefghijklmnopqrstuvwxyz0123456789_-+"

func reactKey(postID, emoji string) string { return "react_" + postID + "_" + emoji }

// createEmoji is the configured emoji name, without colons.
func (f *Feature) createEmoji() string {
	e := strings.ToLower(strings.Trim(strings.TrimSpace(f.a.Config().CreateEmoji), ":"))
	if e == "" || len(e) > 64 || strings.ContainsFunc(e, func(r rune) bool {
		return !strings.ContainsRune(emojiChars, r)
	}) {
		return defaultCreateEmoji
	}
	return e
}

func (f *Feature) emojiText() string { return ":" + f.createEmoji() + ":" }

// ReactionHasBeenAdded turns a message into a task when someone reacts to it with the create
// emoji: as the reacting person, in the channel's default project, once per message.
func (f *Feature) ReactionHasBeenAdded(_ *plugin.Context, r *model.Reaction) {
	if r == nil || r.UserId == f.a.BotID || !model.IsValidId(r.UserId) || !model.IsValidId(r.PostId) {
		return
	}
	emoji := f.createEmoji()
	if !strings.EqualFold(r.EmojiName, emoji) {
		return
	}
	post, appErr := f.a.API.GetPost(r.PostId)
	if appErr != nil || post == nil || post.IsSystemMessage() || post.DeleteAt != 0 {
		return
	}
	if !f.a.API.HasPermissionToChannel(r.UserId, post.ChannelId, model.PermissionReadChannelContent) {
		return
	}
	root := post.RootId
	if root == "" {
		root = post.Id
	}
	tell := func(msg string) { f.a.Ephemeral(r.UserId, post.ChannelId, root, msg) }
	if msg := f.createFromPost(r.UserId, emoji, post, root); msg != "" {
		tell(msg)
	}
}

// createFromPost does the work of the reaction and returns what to tell the reacting person,
// or "" when the bot's thread reply says it all.
func (f *Feature) createFromPost(userID, emoji string, post *model.Post, root string) string {
	key := reactKey(post.Id, emoji)
	var raw []byte
	if err := f.a.Store.Get(storeFeature, key, &raw); err != nil {
		return "Could not check whether this message already has a task. Try again."
	}
	var existing reactRecord
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &existing)
	}
	switch {
	case existing.Ref != "":
		return fmt.Sprintf("This message is already tracked as [%s](%s).", existing.Ref, existing.URL)
	case existing.Pending && time.Since(existing.At) < pendingTTL:
		return "A task is being created from this message already."
	}

	e, err := f.envFor(userID)
	if errors.Is(err, app.ErrNotLinked) {
		return fmt.Sprintf("Connect your tix account with `/tix connect` to create tasks with %s. Your reaction stays as it is.", f.emojiText())
	} else if err != nil {
		return app.ErrText(app.EN, err)
	}
	project := f.ChannelProject(userID, post.ChannelId)
	if project == "" {
		return fmt.Sprintf("This channel has no default tix project, so %s cannot create a task here. A channel admin can set one with `/tix channel project <key>`, or use **Create tix task** from the message's menu.", f.emojiText())
	}

	// Claim the message so two reactions at once make one task.
	var claimOld any
	if len(raw) > 0 {
		claimOld = raw
	}
	claim := reactRecord{Pending: true, At: time.Now().UTC()}
	ok, err := f.a.Store.Set(storeFeature, key, claim, pluginapi.SetAtomic(claimOld))
	if err != nil {
		return "Could not create the task. Try again."
	}
	if !ok {
		var now reactRecord
		if f.a.Store.Get(storeFeature, key, &now) == nil && now.Ref != "" {
			return fmt.Sprintf("This message is already tracked as [%s](%s).", now.Ref, now.URL)
		}
		return "A task is being created from this message already."
	}

	author := "someone"
	if u, appErr := f.a.API.GetUser(post.UserId); appErr == nil && u != nil {
		author = "@" + u.Username
	}
	link := f.siteURL() + "/_redirect/pl/" + post.Id

	cx, cancel := ctx()
	defer cancel()
	t, err := e.tc.CreateTask(cx, tix.CreateTask{
		Project: project,
		Title:   reactTitle(post.Message, author),
		Body:    reactBody(post.Message, author, link),
	})
	if err != nil {
		_, _ = f.a.Store.Set(storeFeature, key, nil)
		return "Could not create the task: " + app.ErrText(app.EN, err)
	}
	url := e.taskURL(t)
	_, _ = f.a.Store.Set(storeFeature, key, reactRecord{Ref: t.Ref, URL: url, At: time.Now().UTC()})

	who := "Someone"
	if u, appErr := f.a.API.GetUser(userID); appErr == nil && u != nil {
		who = "@" + u.Username
	}
	reply := &model.Post{UserId: f.a.BotID, ChannelId: post.ChannelId, RootId: root, Message: who + " created " + t.Ref}
	reply.AddProp(PropCreatedRef, t.Ref)
	if _, appErr := f.a.API.CreatePost(reply); appErr != nil {
		return fmt.Sprintf("Created [%s](%s) (posting it to the thread failed).", t.Ref, url)
	}
	return ""
}

// siteURL is the Mattermost site root without a trailing slash.
func (f *Feature) siteURL() string {
	if cfg := f.a.API.GetConfig(); cfg != nil && cfg.ServiceSettings.SiteURL != nil {
		return strings.TrimRight(*cfg.ServiceSettings.SiteURL, "/")
	}
	return ""
}

// reactTitle is the first non-empty line of a message, trimmed and capped.
func reactTitle(msg, author string) string {
	for line := range strings.SplitSeq(msg, "\n") {
		line = oneLine(strings.TrimLeft(strings.TrimSpace(line), "#>*- "))
		if line == "" {
			continue
		}
		if r := []rune(line); len(r) > maxReactTitle {
			return strings.TrimSpace(string(r[:maxReactTitle-1])) + "…"
		}
		return line
	}
	return "Message from " + author
}

// reactBody is the message followed by a link back to it.
func reactBody(msg, author, permalink string) string {
	msg = excerpt(msg, maxReactBody)
	from := fmt.Sprintf("From [a message by %s](%s) in Mattermost.", author, permalink)
	if msg == "" {
		return from
	}
	return msg + "\n\n" + from
}
