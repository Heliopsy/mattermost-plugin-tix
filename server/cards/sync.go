// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
)

// propCommentID, on a bot post in a synced thread, is the tix comment it shows.
const propCommentID = "tix_comment_id"

// PropFromPlugin marks a post the plugin's webapp made on a person's behalf (the create dialog's
// "Post link" message). Thread sync skips such posts. It is set by the client, so it is only ever
// used to skip work, never to grant anything.
const PropFromPlugin = "from_tix_plugin"

// maxThreadComment caps the text of a tix comment posted into a thread.
const maxThreadComment = 4000

// syncRecord is a channel's thread sync switch.
type syncRecord struct {
	On    bool      `json:"on"`
	By    string    `json:"by"`
	SetAt time.Time `json:"set_at"`
}

// Commands returns /tix sync.
func (f *Feature) Commands() []command.Sub {
	return []command.Sub{{
		Name: "sync", Hint: "on|off",
		HelpID: "cards.cmd.sync",
		Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
			n.AddCommand(model.NewAutocompleteData("on", "", l.T("cards.cmd.sync_on")))
			n.AddCommand(model.NewAutocompleteData("off", "", l.T("cards.cmd.sync_off")))
		},
		Run: f.syncCmd,
	}}
}

// synced reports whether thread sync is on in a channel.
func (f *Feature) synced(channelID string) bool {
	var r syncRecord
	if err := f.a.Store.Get(feature, "sync_"+channelID, &r); err != nil {
		return false
	}
	return r.On
}

// canSync is the one check behind /tix sync: only channel admins (and team or system admins, who
// hold the same permission) of a public or private channel may switch it.
func (f *Feature) canSync(userID, channelID string) (bool, string) {
	ch, appErr := f.a.API.GetChannel(channelID)
	if appErr != nil {
		return false, "Could not read this channel."
	}
	if ch.Type != model.ChannelTypeOpen && ch.Type != model.ChannelTypePrivate {
		return false, "Thread sync works in public and private channels, not direct or group messages."
	}
	if !f.a.API.HasPermissionToChannel(userID, channelID, model.PermissionManageChannelRoles) {
		return false, "Only channel admins can turn thread sync on or off."
	}
	return true, ""
}

func (f *Feature) syncCmd(c *command.Ctx, args []string) *model.CommandResponse {
	userID, channelID := c.Args.UserId, c.Args.ChannelId
	if len(args) == 0 {
		state := "off"
		if f.synced(channelID) {
			state = "on"
		}
		return command.Ephemeral("Thread sync is " + state + " in this channel. Channel admins change it with `/tix sync on|off`.")
	}
	want := strings.ToLower(args[0])
	if len(args) != 1 || (want != "on" && want != "off") {
		return command.Ephemeral("Usage: `/tix sync on|off`")
	}
	pc := c.Plugin
	if pc == nil {
		pc = &plugin.Context{}
	}
	rec := plugin.MakeAuditRecordWithContext("tixCardSync", model.AuditStatusFail, pc, userID, "/tix")
	defer f.a.API.LogAuditRec(rec)
	if model.IsValidId(channelID) {
		rec.AddMeta("channel_id", channelID)
	}
	rec.AddMeta("on", want == "on")
	if ok, msg := f.canSync(userID, channelID); !ok {
		return command.Ephemeral(msg)
	}
	on := want == "on"
	var err error
	if on {
		_, err = f.a.Store.Set(feature, "sync_"+channelID, syncRecord{On: true, By: userID, SetAt: f.now()})
	} else {
		_, err = f.a.Store.Set(feature, "sync_"+channelID, nil)
	}
	if err != nil {
		return command.Ephemeral("Could not save the setting.")
	}
	rec.Success()
	text := "turned tix thread sync off in this channel."
	if on {
		text = "turned tix thread sync on in this channel: replies in a task card's thread are added to the task in tix " +
			"as comments by whoever wrote them, and tix comments on the task are posted into the thread. " +
			"Everyone who can read this channel sees those comments."
	}
	who := "Someone"
	if u, appErr := f.a.API.GetUser(userID); appErr == nil {
		who = "@" + u.Username
	}
	if _, appErr := f.a.API.CreatePost(&model.Post{UserId: f.a.BotID, ChannelId: channelID, Message: who + " " + text}); appErr != nil {
		return command.Ephemeral("Thread sync is " + want + ".")
	}
	return &model.CommandResponse{}
}

// mirrorReply adds a person's reply in a synced card thread to the task as a tix comment, with
// their own token. Bot, system and webhook posts and posts the plugin made for a person are never
// mirrored, and a post at most once.
func (f *Feature) mirrorReply(post *model.Post) {
	if post.RootId == "" || post.Type != "" || post.UserId == f.a.BotID || post.GetProp(model.PostPropsFromWebhook) == "true" ||
		post.GetProp("from_bot") == "true" || post.GetProp("from_plugin") == "true" || post.GetProp(PropFromPlugin) != nil {
		return
	}
	body := strings.TrimSpace(post.Message)
	if body == "" || !f.synced(post.ChannelId) {
		return
	}
	root, appErr := f.a.API.GetPost(post.RootId)
	if appErr != nil || root.DeleteAt != 0 {
		return
	}
	targets := f.targetsOf(post, f.threadRefs(root))
	if len(targets) == 0 {
		return
	}
	ok, err := f.a.Store.Set(feature, "mirrored_"+post.Id, 1, pluginapi.SetAtomic(nil), pluginapi.SetExpiry(24*time.Hour))
	if err != nil || !ok {
		return
	}
	tc, link, err := f.a.TixFor(post.UserId)
	if errors.Is(err, app.ErrNotLinked) {
		if first, _ := f.a.Store.Set(feature, "synchint_"+post.UserId, 1, pluginapi.SetAtomic(nil)); first {
			f.a.Ephemeral(post.UserId, post.ChannelId, post.RootId,
				"Replies in this thread are copied to the task in tix, but your account is not connected, so yours was not. Run `/tix connect`.")
		}
		return
	} else if err != nil {
		return
	}
	if len(body) > maxCommentLen {
		body = truncate(body, maxCommentLen)
	}
	m := store.Mirror{ChannelID: post.ChannelId, RootID: post.RootId, PostID: post.Id}
	ctx, cancel := app.Ctx()
	defer cancel()
	for _, ref := range targets {
		// Recorded before the call too: tix may deliver the webhook before it answers.
		_ = f.a.Store.MarkMirrored(ref, link.ActorID, body, "", m)
		cm, err := tc.AddComment(ctx, ref, body)
		if err != nil {
			f.a.API.LogDebug("tix: mirroring a thread reply failed", "post_id", post.Id, "err", err.Error())
			continue
		}
		_ = f.a.Store.MarkMirrored(ref, link.ActorID, body, cm.ID, m)
	}
}

// targetsOf picks the tasks a reply is about: those of the thread's cards it names, or the only one.
func (f *Feature) targetsOf(post *model.Post, refs []string) []string {
	if len(refs) == 0 {
		return nil
	}
	keys := make([]string, 0, len(refs))
	for _, r := range refs {
		keys = append(keys, r[:strings.LastIndex(r, "-")])
	}
	var out []string
	for _, r := range extractRefs(post.Message, keys, maxCards) {
		for _, t := range refs {
			if r == t {
				out = append(out, r)
			}
		}
	}
	if len(out) == 0 && len(refs) == 1 {
		out = refs
	}
	return out
}

// CommentAdded posts a new tix comment into every synced thread with a card of the task, except
// the thread it was mirrored from and channels in skipChannels (notify already posted it there).
// notify calls it for comment.added events.
func (f *Feature) CommentAdded(ref, commentID, actorID, author, body string, skipChannels []string) {
	ref = strings.ToLower(ref)
	body = strings.TrimSpace(body)
	if !validRef.MatchString(ref) || body == "" {
		return
	}
	from := f.a.Store.MirroredComment(ref, actorID, body, commentID)
	cards, err := f.a.Store.Cards(ref)
	if err != nil {
		return
	}
	done := map[string]bool{}
	synced := map[string]bool{}
	for _, c := range cards {
		if c.ChannelID == "" || slices.Contains(skipChannels, c.ChannelID) {
			continue
		}
		on, seen := synced[c.ChannelID]
		if !seen {
			on = f.synced(c.ChannelID)
			synced[c.ChannelID] = on
		}
		if !on {
			continue
		}
		root := c.RootID
		if root == "" {
			p, appErr := f.a.API.GetPost(c.PostID)
			if appErr != nil || p.DeleteAt != 0 {
				continue
			}
			root = threadOf(p)
		}
		if done[root] || (from != nil && from.RootID == root) {
			continue
		}
		done[root] = true
		if commentID != "" && len(commentID) <= 64 {
			if ok, _ := f.a.Store.Set(feature, "posted_"+commentID+"_"+root, 1, pluginapi.SetAtomic(nil), pluginapi.SetExpiry(72*time.Hour)); !ok {
				continue
			}
		}
		p := &model.Post{UserId: f.a.BotID, ChannelId: c.ChannelID, RootId: root, Message: threadComment(author, ref, body)}
		p.AddProp(propCommentID, commentID)
		if _, appErr := f.a.API.CreatePost(p); appErr != nil {
			f.a.API.LogWarn("tix: posting a comment into a card thread failed", "root_id", root, "err", appErr.Error())
		}
	}
}

// threadComment is the bot post for a tix comment in a card thread.
func threadComment(author, ref, body string) string {
	if author == "" {
		author = "someone"
	}
	body = truncate(body, maxThreadComment)
	return fmt.Sprintf("**%s** commented on %s in tix:\n> %s", author, ref, strings.ReplaceAll(body, "\n", "\n> "))
}

// truncate cuts s to at most n bytes on a rune boundary, marking the cut.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}
