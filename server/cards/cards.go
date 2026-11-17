// SPDX-License-Identifier: AGPL-3.0-only

// Package cards turns task references in messages into live task cards with action buttons.
//
// A card is a message attachment on the post that mentions the task. It is drawn with the
// token of the person who wrote the post, so a card never shows a channel a task its author
// cannot see. The buttons act with the token of the person who clicks them.
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
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// propRefs lists, in order, the task references whose cards a post carries.
const propRefs = "tix_refs"

const (
	actionPath = "/api/v1/actions/card"
	dialogPath = "/api/v1/dialogs/card"
)

// hookTimeout bounds the tix calls made while a message is being posted.
const hookTimeout = 5 * time.Second

// Feature is the cards feature.
type Feature struct{ a *app.App }

// New returns the feature.
func New(a *app.App) *Feature { return &Feature{a: a} }

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub { return nil }

// Routes mounts the card buttons and the comment dialog.
func (f *Feature) Routes(h *api.Handler) {
	h.User.HandleFunc("/actions/card", f.action).Methods(http.MethodPost)
	h.User.HandleFunc("/dialogs/card", f.dialogSubmit).Methods(http.MethodPost)
}

// maybeRef is a cheap test that a message could hold a reference at all.
var maybeRef = regexp.MustCompile(`[A-Za-z0-9]-[0-9]`)

// MessageWillBePosted attaches cards for the task references in a new message.
func (f *Feature) MessageWillBePosted(_ *plugin.Context, post *model.Post) (*model.Post, string) {
	f.sanitize(post)
	if !f.a.Config().UnfurlRefs || post.Type != "" || post.UserId == f.a.BotID || !maybeRef.MatchString(post.Message) {
		return post, ""
	}
	projects, err := f.a.Projects(post.UserId)
	if err != nil {
		return post, ""
	}
	keys := make([]string, 0, len(projects))
	for _, p := range projects {
		keys = append(keys, p.Key)
	}
	refs := extractRefs(post.Message, keys, maxCards)
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

// MessageHasBeenPosted records which posts show which tasks.
func (f *Feature) MessageHasBeenPosted(_ *plugin.Context, post *model.Post) {
	for _, ref := range refsOf(post) {
		if err := f.a.Store.AddCard(ref, store.CardRef{PostID: post.Id, ChannelID: post.ChannelId, CreatedAt: time.Now()}); err != nil {
			f.a.API.LogWarn("tix: recording a task card failed", "post_id", post.Id, "err", err.Error())
		}
	}
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
		updated := f.redraw(post)
		if updated == nil {
			continue
		}
		if _, appErr := f.a.API.UpdatePost(updated); appErr != nil {
			f.a.API.LogWarn("tix: redrawing a task card failed", "post_id", post.Id, "err", appErr.Error())
		}
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
	atts, shown, err := f.render(post.UserId, refs)
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
	return buildCard(cardInput{
		Task:      t,
		Workflow:  wf,
		Project:   proj,
		URL:       f.a.TaskURL(&t.Task),
		Assignee:  f.a.MentionFor(viewer, t.AssigneeActorID),
		ActionURL: f.a.PluginURL(actionPath),
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

// errText is the line a person sees for a failed action.
func errText(err error) string {
	if errors.Is(err, app.ErrNotLinked) {
		return "Connect your Mattermost account to tix first: run `/tix connect`."
	}
	return app.ErrText(err)
}
