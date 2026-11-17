// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// task is a tix task as the API sends it. tix.Task declares priority as a string, but the API
// (Task.priority in openapi.yaml) sends an integer from 1 (highest) to 5 (lowest), so it is read here.
type task struct {
	tix.Task
	Priority int `json:"priority"`
}

func getTask(ctx context.Context, c *tix.Client, ref string) (*task, error) {
	var t task
	return &t, c.Do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(ref), nil, nil, &t)
}

func transitionTask(ctx context.Context, c *tix.Client, ref, to, comment string) (*task, error) {
	in := map[string]any{"to": to}
	if comment != "" {
		in["comment"] = comment
	}
	var t task
	return &t, c.Do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(ref)+"/transition", nil, in, &t)
}

func assignTask(ctx context.Context, c *tix.Client, ref, actorID string) (*task, error) {
	var t task
	return &t, c.Do(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(ref), nil, map[string]any{"assignee_actor_id": actorID}, &t)
}

// category is the state's category, derived as tix does when the workflow leaves it empty.
func category(w *tix.Workflow, status string) string {
	if w == nil {
		return "todo"
	}
	s, ok := w.StateByKey(status)
	switch {
	case !ok:
		return "todo"
	case s.Category != "":
		return s.Category
	case s.Terminal:
		return "done"
	case s.Key == w.Definition.Initial:
		return "todo"
	default:
		return "in_progress"
	}
}

func stateLabel(w *tix.Workflow, key string) string {
	if w != nil {
		if s, ok := w.StateByKey(key); ok && s.Label != "" {
			return s.Label
		}
	}
	return key
}

func terminal(w *tix.Workflow, status string) bool {
	c := category(w, status)
	return c == "done" || c == "cancelled"
}

// Order in which next states become buttons: move work forward first.
var categoryRank = map[string]int{"in_progress": 0, "done": 1, "waiting": 2, "blocked": 3, "todo": 4, "cancelled": 5}

// nextTransitions are at most limit moves out of the task's state, the sensible ones first.
func nextTransitions(w *tix.Workflow, status string, limit int) []tix.Transition {
	if w == nil || limit <= 0 {
		return nil
	}
	ts := w.NextStates(status)
	sort.SliceStable(ts, func(i, j int) bool {
		return categoryRank[category(w, ts[i].To)] < categoryRank[category(w, ts[j].To)]
	})
	seen := map[string]bool{}
	var out []tix.Transition
	for _, t := range ts {
		if seen[t.To] || t.To == status {
			continue
		}
		seen[t.To] = true
		out = append(out, t)
		if len(out) == limit {
			break
		}
	}
	return out
}

// Action names carried in a button's context.
const (
	actTransition = "transition"
	actAssign     = "assign"
	actComment    = "comment"
)

// cardInput is everything one card shows.
type cardInput struct {
	Task      *task
	Workflow  *tix.Workflow
	Project   *tix.Project
	URL       string
	Assignee  string
	ActionURL string
	// R renders labels and dates: the card's language (the channel's), the time zone of the
	// person it is drawn for and the card settings.
	R *render.R
	// Index is the card's position in its post; it keeps action ids unique within the post.
	Index int
}

// buildCard renders a task as a message attachment with action buttons. The fields and buttons
// follow the card settings.
func buildCard(in cardInput) *model.MessageAttachment {
	t, r := in.Task, in.R
	if r == nil {
		r = render.New(render.Defaults(), nil, nil, time.Now())
	}
	label := stateLabel(in.Workflow, t.Status)
	var fields []*model.MessageAttachmentField
	add := func(title, value string) {
		fields = append(fields, &model.MessageAttachmentField{Title: title, Value: value, Short: true})
	}
	footer := "tix"
	project := ""
	for _, f := range r.O.CardFields {
		switch f {
		case render.FieldStatus:
			status := label
			if t.Blocked && !terminal(in.Workflow, t.Status) {
				status = r.L.T("cards.card.status_blocked", "status", label)
			}
			add(r.L.T("cards.card.status"), status)
		case render.FieldAssignee:
			add(r.L.T("cards.card.assignee"), in.Assignee)
		case render.FieldPriority:
			if p := r.Priority(t.Priority); p != "" {
				add(r.L.T("cards.card.priority"), p)
			}
		case render.FieldDue:
			if d, ok := render.DueOf(t.DueAt); ok {
				add(r.L.T("cards.card.due"), r.DueDay(d))
			}
		case render.FieldTags:
			if len(t.Tags) > 0 {
				add(r.L.T("cards.card.tags"), strings.Join(t.Tags, ", "))
			}
		case render.FieldProject:
			if in.Project != nil && in.Project.Name != "" {
				project = in.Project.Name
			}
		}
	}
	if project != "" {
		footer = "tix · " + project
	}
	color := render.CategoryColor(category(in.Workflow, t.Status))
	return &model.MessageAttachment{
		Fallback:  fmt.Sprintf("%s: %s (%s)", t.Ref, t.Title, label),
		Color:     color,
		Title:     t.Ref + " · " + t.Title,
		TitleLink: in.URL,
		Fields:    fields,
		Footer:    footer,
		Actions:   buildActions(in, r),
	}
}

func buildActions(in cardInput, r *render.R) []*model.PostAction {
	t := in.Task
	var out []*model.PostAction
	add := func(name, style string, ctx map[string]any) {
		ctx["ref"] = t.Ref
		out = append(out, &model.PostAction{
			Id:          fmt.Sprintf("tixc%da%d", in.Index, len(out)),
			Type:        model.PostActionTypeButton,
			Name:        name,
			Style:       style,
			Integration: &model.PostActionIntegration{URL: in.ActionURL, Context: ctx},
		})
	}
	for _, tr := range nextTransitions(in.Workflow, t.Status, r.O.CardButtons) {
		style := "default"
		switch category(in.Workflow, tr.To) {
		case "in_progress":
			style = "primary"
		case "done":
			style = "success"
		}
		add(stateLabel(in.Workflow, tr.To), style, map[string]any{"action": actTransition, "to": tr.To})
	}
	if r.O.AssignButton && !terminal(in.Workflow, t.Status) {
		add(r.L.T("cards.card.assign_me"), "default", map[string]any{"action": actAssign})
	}
	if r.O.CommentButton {
		add(r.L.T("cards.card.comment"), "default", map[string]any{"action": actComment})
	}
	return out
}
