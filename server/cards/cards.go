// SPDX-License-Identifier: AGPL-3.0-only

// Package cards turns task references in messages into live task cards with action buttons.
//
// A card is a message attachment on a bot reply to the post that mentions the task (or, in the
// inline style, on that post itself). It is drawn with the token of the person who wrote the
// post, so a card never shows a channel a task its author cannot see. The buttons act with the
// token of the person who clicks them. In channels with thread sync on, replies in a card's
// thread become tix comments and tix comments are posted into the thread.
package cards

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// propRefs lists, in order, the task references whose cards a post carries.
const propRefs = "tix_refs"

// propSource, on a bot card reply, is the id of the person's post it answers.
const propSource = "tix_source_post"

// feature namespaces the records this package keeps with store.Get and store.Set.
const feature = "cards"

// Card styles (the CardStyle setting).
const (
	styleReply  = "reply"
	styleInline = "inline"
)

// defaultQuiet is how long a task carded in a channel gets no new card there.
const defaultQuiet = 10 * time.Minute

const (
	actionPath = "/api/v1/actions/card"
	dialogPath = "/api/v1/dialogs/card"
)

// hookTimeout bounds the tix calls made while a message is being posted.
const hookTimeout = 5 * time.Second

// Feature is the cards feature.
type Feature struct {
	a   *app.App
	now func() time.Time
}

// New returns the feature.
func New(a *app.App) *Feature { return &Feature{a: a, now: time.Now} }

// Routes mounts the card buttons and the comment dialog.
func (f *Feature) Routes(h *api.Handler) {
	h.User.HandleFunc("/actions/card", f.action).Methods(http.MethodPost)
	h.User.HandleFunc("/dialogs/card", f.dialogSubmit).Methods(http.MethodPost)
}

// maybeRef is a cheap test that a message could hold a reference at all.
var maybeRef = regexp.MustCompile(`[A-Za-z0-9]-[0-9]`)

// inline reports whether cards go on the person's own post rather than in a bot reply.
func (f *Feature) inline() bool { return f.a.Config().CardStyle == styleInline }

// MessageWillBePosted strips cards a message brought itself and, in the inline style, attaches cards
// for its task references. In the reply style the message is left as written.
func (f *Feature) MessageWillBePosted(_ *plugin.Context, post *model.Post) (*model.Post, string) {
	if post.UserId == f.a.BotID {
		return post, ""
	}
	f.sanitize(post)
	if !f.inline() {
		return post, ""
	}
	refs := f.refsIn(post)
	if len(refs) == 0 {
		return post, ""
	}
	atts, shown, _ := f.render(post.UserId, refs)
	if len(atts) == 0 {
		return post, ""
	}
	post.AddProp(model.PostPropsAttachments, append(post.Attachments(), atts...))
	post.AddProp(propRefs, shown)
	return post, ""
}

// refsIn lists the task references of a person's message that could get cards, as its author sees projects.
func (f *Feature) refsIn(post *model.Post) []string {
	if !f.a.Config().UnfurlRefs || post.Type != "" || post.UserId == f.a.BotID || !maybeRef.MatchString(post.Message) {
		return nil
	}
	projects, err := f.a.Projects(post.UserId)
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(projects))
	for _, p := range projects {
		keys = append(keys, p.Key)
	}
	return extractRefs(post.Message, keys, maxCards)
}

// MessageHasBeenPosted answers a message with its task cards (reply style), records which posts show
// which tasks, and mirrors a reply in a synced card thread to tix.
func (f *Feature) MessageHasBeenPosted(_ *plugin.Context, post *model.Post) {
	if post.UserId == f.a.BotID {
		// The bot's own card replies; replyCards records them too, AddCard is idempotent.
		f.record(post)
		return
	}
	if f.inline() {
		f.record(post)
	} else {
		f.replyCards(post)
	}
	f.mirrorReply(post)
}

// MessageHasBeenUpdated cards a reference a person added by editing their message. A task that
// already has a card in the thread does not get another. Edits are never mirrored to tix.
func (f *Feature) MessageHasBeenUpdated(_ *plugin.Context, newPost, oldPost *model.Post) {
	if newPost == nil || newPost.UserId == f.a.BotID || f.inline() || newPost.DeleteAt != 0 {
		return
	}
	if oldPost != nil && oldPost.Message == newPost.Message {
		return
	}
	f.replyCards(newPost)
}

// MessageHasBeenDeleted removes the bot cards that answered a deleted message.
func (f *Feature) MessageHasBeenDeleted(_ *plugin.Context, post *model.Post) {
	if post == nil || post.UserId == f.a.BotID || !model.IsValidId(post.Id) {
		return
	}
	var ids []string
	if err := f.a.Store.Get(feature, "src_"+post.Id, &ids); err != nil || len(ids) == 0 {
		return
	}
	for _, id := range ids {
		card, appErr := f.a.API.GetPost(id)
		if appErr != nil || card.UserId != f.a.BotID {
			continue
		}
		f.dropCard(card)
	}
	_, _ = f.a.Store.Set(feature, "src_"+post.Id, nil)
}

// record remembers the cards a post shows.
func (f *Feature) record(post *model.Post) {
	refs := refsOf(post)
	if len(refs) == 0 {
		return
	}
	c := store.CardRef{PostID: post.Id, ChannelID: post.ChannelId, CreatedAt: time.Now(), RootID: threadOf(post)}
	if post.UserId == f.a.BotID {
		c.SourceID, _ = post.GetProp(propSource).(string)
	}
	for _, ref := range refs {
		if err := f.a.Store.AddCard(ref, c); err != nil {
			f.a.API.LogWarn("tix: recording a task card failed", "post_id", post.Id, "err", err.Error())
		}
	}
}

// threadOf is the root of the thread a post is in.
func threadOf(post *model.Post) string {
	if post.RootId != "" {
		return post.RootId
	}
	return post.Id
}

// Refresh redraws every card showing the task. notify calls it when tix reports a change.
func (f *Feature) Refresh(ref string) { f.refresh(ref, "") }

// refresh redraws the cards of ref except the one on post skip.
func (f *Feature) refresh(ref, skip string) {
	ref = strings.ToLower(ref)
	if !validRef.MatchString(ref) {
		return
	}
	cards, err := f.a.Store.Cards(ref)
	if err != nil {
		f.a.API.LogWarn("tix: reading task cards failed", "err", err.Error())
		return
	}
	for _, c := range cards {
		if c.PostID == skip {
			continue
		}
		post, appErr := f.a.API.GetPost(c.PostID)
		if appErr != nil || post.DeleteAt != 0 || !slices.Contains(refsOf(post), ref) {
			if appErr == nil || appErr.StatusCode == http.StatusNotFound {
				_ = f.a.Store.RemoveCard(ref, c.PostID)
			}
			continue
		}
		if _, gone := f.author(post); gone {
			f.dropCard(post)
			continue
		}
		updated := f.redraw(post)
		if updated == nil {
			continue
		}
		if post.UserId == f.a.BotID && len(refsOf(updated)) == 0 {
			// The author lost sight of every task on the reply: nothing is left to show.
			f.dropCard(post)
			continue
		}
		if _, appErr := f.a.API.UpdatePost(updated); appErr != nil {
			f.a.API.LogWarn("tix: redrawing a task card failed", "post_id", post.Id, "err", appErr.Error())
		}
	}
}

// author is the person whose access a card post is drawn with: the poster of an inline card, the
// poster of the message a bot card answers. gone is true when that message has been deleted.
func (f *Feature) author(post *model.Post) (userID string, gone bool) {
	if post.UserId != f.a.BotID {
		return post.UserId, false
	}
	src, _ := post.GetProp(propSource).(string)
	if !model.IsValidId(src) {
		return "", true
	}
	sp, appErr := f.a.API.GetPost(src)
	if appErr != nil {
		return "", appErr.StatusCode == http.StatusNotFound
	}
	if sp.DeleteAt != 0 {
		return "", true
	}
	return sp.UserId, false
}

// dropCard deletes a bot card post and forgets it.
func (f *Feature) dropCard(post *model.Post) {
	if post.UserId != f.a.BotID {
		return
	}
	if appErr := f.a.API.DeletePost(post.Id); appErr != nil && appErr.StatusCode != http.StatusNotFound {
		f.a.API.LogWarn("tix: deleting a task card failed", "post_id", post.Id, "err", appErr.Error())
		return
	}
	for _, ref := range refsOf(post) {
		_ = f.a.Store.RemoveCard(ref, post.Id)
	}
}

// refreshAsync redraws the other cards of ref in the background.
func (f *Feature) refreshAsync(ref, skip string) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				f.a.API.LogError("tix: panic while redrawing task cards", "panic", fmt.Sprint(r))
			}
		}()
		f.refresh(ref, skip)
	}()
}

// redraw returns post with its cards drawn again as its author sees the tasks now, or nil when it
// cannot be redrawn (author not connected, tix unreachable). A task the author can no longer see
// loses its card.
func (f *Feature) redraw(post *model.Post) *model.Post {
	refs := refsOf(post)
	if len(refs) == 0 {
		return nil
	}
	author, gone := f.author(post)
	if gone || author == "" {
		return nil
	}
	atts, shown, err := f.render(author, refs)
	if err != nil {
		return nil
	}
	out := post.Clone()
	keep := make([]*model.MessageAttachment, 0, len(atts))
	for _, a := range post.Attachments() {
		if !f.ours(a) {
			keep = append(keep, a)
		}
	}
	out.AddProp(model.PostPropsAttachments, append(keep, atts...))
	if len(shown) == 0 {
		out.DelProp(propRefs)
	} else {
		out.AddProp(propRefs, shown)
	}
	return out
}

// render draws the cards of refs as userID sees them. A task they cannot see is left out; any
// other failure is returned with the cards that could be drawn.
func (f *Feature) render(userID string, refs []string) ([]*model.MessageAttachment, []string, error) {
	tc, _, err := f.a.TixFor(userID)
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()
	var (
		atts  []*model.MessageAttachment
		shown []string
		first error
	)
	for _, ref := range refs {
		t, err := getTask(ctx, tc, ref)
		if err != nil {
			if !tix.IsStatus(err, http.StatusNotFound) && !tix.IsStatus(err, http.StatusForbidden) && first == nil {
				first = err
			}
			continue
		}
		atts = append(atts, f.card(userID, t, len(atts)))
		shown = append(shown, strings.ToLower(ref))
	}
	return atts, shown, first
}

// card draws one task as viewer sees it.
func (f *Feature) card(viewer string, t *task, index int) *model.MessageAttachment {
	wf, proj, err := f.a.WorkflowFor(viewer, t.ProjectID)
	if err != nil {
		wf = nil
	}
	// A card is a channel post: it speaks the server's language, dated in its author's zone.
	r := f.a.Renderer(f.a.ServerLocale(), f.a.Location(viewer))
	return buildCard(cardInput{
		Task:      t,
		Workflow:  wf,
		Project:   proj,
		URL:       f.a.TaskURL(&t.Task),
		Assignee:  f.a.MentionFor(r.L, viewer, t.AssigneeActorID),
		ActionURL: f.a.PluginURL(actionPath),
		R:         r,
		Index:     index,
	})
}

// ours reports whether an attachment carries buttons aimed at this plugin.
func (f *Feature) ours(a *model.MessageAttachment) bool {
	if a == nil {
		return false
	}
	marker := "/plugins/" + app.PluginID + "/"
	for _, act := range a.Actions {
		if act == nil {
			continue
		}
		if strings.HasPrefix(act.Id, "tixc") || (act.Integration != nil && strings.Contains(act.Integration.URL, marker)) {
			return true
		}
	}
	return false
}

// sanitize removes cards and card props a message brought itself: only the plugin draws cards.
func (f *Feature) sanitize(post *model.Post) {
	post.DelProp(propRefs)
	post.DelProp(propSource)
	if post.GetProp(model.PostPropsAttachments) == nil {
		return
	}
	atts := post.Attachments()
	keep := make([]*model.MessageAttachment, 0, len(atts))
	for _, a := range atts {
		if !f.ours(a) {
			keep = append(keep, a)
		}
	}
	if len(keep) != len(atts) {
		post.AddProp(model.PostPropsAttachments, keep)
	}
}

// refsOf reads the references a post's cards show.
func refsOf(post *model.Post) []string {
	var raw []string
	switch v := post.GetProp(propRefs).(type) {
	case []string:
		raw = v
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				raw = append(raw, s)
			}
		}
	}
	var out []string
	for _, r := range raw {
		r = strings.ToLower(r)
		if validRef.MatchString(r) && !slices.Contains(out, r) {
			out = append(out, r)
		}
		if len(out) == maxCards {
			break
		}
	}
	return out
}

// errText is the line a person sees for a failed action, in l's language.
func errText(l *i18n.Localizer, err error) string {
	if errors.Is(err, app.ErrNotLinked) {
		return l.T("cards.err.not_linked")
	}
	return app.ErrText(l, err)
}
