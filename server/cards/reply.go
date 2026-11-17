// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/heliopsy/mattermost-plugin-tix/server/store"
)

// quiet is how long a task carded in a channel gets no new card there.
func (f *Feature) quiet() time.Duration {
	if m := f.a.Config().CardQuietMinutes; m > 0 {
		return time.Duration(m) * time.Minute
	}
	return defaultQuiet
}

// fresh drops the refs that already have a card in the thread, or got one in the channel within the
// quiet period, so a busy conversation is not filled with the same card.
func (f *Feature) fresh(refs []string, channelID, rootID string) []string {
	now := f.now()
	var out []string
	for _, ref := range refs {
		cards, err := f.a.Store.Cards(ref)
		if err != nil {
			continue
		}
		dup := slices.ContainsFunc(cards, func(c store.CardRef) bool {
			return c.ChannelID == channelID && (c.RootID == rootID || c.PostID == rootID || now.Sub(c.CreatedAt) < f.quiet())
		})
		if !dup {
			out = append(out, ref)
		}
	}
	return out
}

// replyCards answers a person's message with a bot reply carrying its task cards, drawn with the
// person's own token. The message itself is not changed.
func (f *Feature) replyCards(post *model.Post) {
	refs := f.refsIn(post)
	if len(refs) == 0 {
		return
	}
	root := threadOf(post)
	refs = f.fresh(refs, post.ChannelId, root)
	if len(refs) == 0 {
		return
	}
	atts, shown, _ := f.render(post.UserId, refs)
	if len(atts) == 0 {
		return
	}
	card := &model.Post{UserId: f.a.BotID, ChannelId: post.ChannelId, RootId: root}
	card.AddProp(model.PostPropsAttachments, atts)
	card.AddProp(propRefs, shown)
	card.AddProp(propSource, post.Id)
	created, appErr := f.a.API.CreatePost(card)
	if appErr != nil {
		f.a.API.LogWarn("tix: posting a task card failed", "post_id", post.Id, "err", appErr.Error())
		return
	}
	f.record(created)
	if err := f.appendIDs("src_"+post.Id, created.Id); err != nil {
		f.a.API.LogWarn("tix: recording a task card's source failed", "post_id", post.Id, "err", err.Error())
	}
	if err := f.appendIDs("thread_"+root, shown...); err != nil {
		f.a.API.LogWarn("tix: recording a card thread failed", "root_id", root, "err", err.Error())
	}
}

// appendIDs adds values to a small list record of this feature, atomically.
func (f *Feature) appendIDs(key string, values ...string) error {
	for range 5 {
		var raw []byte
		if err := f.a.Store.Get(feature, key, &raw); err != nil {
			return err
		}
		var cur []string
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &cur)
		}
		next := slices.Clone(cur)
		for _, v := range values {
			if !slices.Contains(next, v) {
				next = append(next, v)
			}
		}
		if len(next) > 50 {
			next = next[len(next)-50:]
		}
		ok, err := f.a.Store.Set(feature, key, next, pluginapi.SetAtomic(raw))
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return errors.New("too much contention")
}

// threadRefs lists the tasks that have a card in the thread rooted at rootID.
func (f *Feature) threadRefs(root *model.Post) []string {
	var refs []string
	_ = f.a.Store.Get(feature, "thread_"+root.Id, &refs)
	// An inline card on the root: tix_refs on a person's post is only ever set by the plugin.
	for _, r := range refsOf(root) {
		if !slices.Contains(refs, r) {
			refs = append(refs, r)
		}
	}
	out := refs[:0]
	for _, r := range refs {
		if validRef.MatchString(r) {
			out = append(out, r)
		}
	}
	return out
}
