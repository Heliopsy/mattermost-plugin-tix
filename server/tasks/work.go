// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

const (
	// maxList is the most tasks /tix list fetches; maxShown the most it prints.
	maxList  = 100
	maxShown = 50
	// recentComments is how many comments /tix show prints.
	recentComments = 3
)

// create makes a task from a parsed /tix new.
func (e *env) create(ctx context.Context, na *newArgs) (*tix.Task, error) {
	in := tix.CreateTask{Project: na.Project, Title: na.Title, Priority: na.Priority, Parse: !na.Raw}
	if na.Assign != "" {
		id, err := e.actorFor(na.Assign)
		if err != nil {
			return nil, err
		}
		in.Assignee = id
	} else if !na.Raw {
		// An @mention in the title is a Mattermost user; hand tix their actor, not the username.
		words := strings.Fields(in.Title)
		for i, w := range words {
			name, ok := strings.CutPrefix(w, "@")
			if !ok || name == "" {
				continue
			}
			id, found, err := e.mmActor(name)
			if err != nil {
				return nil, i18n.Wrap(errUnlinkedUser, "tasks.err.unlinked_mention", "name", name)
			}
			if found {
				in.Assignee = id
				in.Title = strings.Join(append(words[:i:i], words[i+1:]...), " ")
			}
			break
		}
	}
	return e.tc.CreateTask(ctx, in)
}

// card renders a task as a few lines in the user's language: the linked title, after the
// message headID (with args and {task}) when one is given, and its facts.
func (e *env) card(t *tix.Task, wf *tix.Workflow, headID string, args ...any) string {
	return e.cardIn(e.l(), t, wf, headID, args...)
}

// cardIn is card in language l, for a post the whole channel reads.
func (e *env) cardIn(l *i18n.Localizer, t *tix.Task, wf *tix.Workflow, headID string, args ...any) string {
	var b strings.Builder
	head := fmt.Sprintf("[%s](%s) **%s**", t.Ref, e.taskURL(t), oneLine(t.Title))
	if headID != "" {
		head = l.T(headID, append(args, "task", head)...)
	}
	b.WriteString(head + "\n")
	b.WriteString(strings.Join(e.facts(l, t, wf), " · "))
	return b.String()
}

// facts are the labelled attributes of a task for a card, in language l.
func (e *env) facts(l *i18n.Localizer, t *tix.Task, wf *tix.Workflow) []string {
	fact := func(labelID, value string) string { return l.T("tasks.fact", "label", l.T(labelID), "value", value) }
	out := []string{fact(i18n.M("tasks.fact.status"), label(wf, t.Status))}
	out = append(out, fact(i18n.M("tasks.fact.assignee"), e.mention(t.AssigneeActorID)))
	if p := priorityText(l, t.Priority); p != "" {
		out = append(out, fact(i18n.M("tasks.fact.priority"), p))
	}
	if d, ok := render.DueOf(t.DueAt); ok {
		now := e.now()
		due := render.New(e.format.OrDefaults(), l, now.Location(), now).DueDay(d)
		if t.CompletedAt == nil && d.Overdue(now, now.Location()) {
			due = l.T("tasks.fact.due_overdue", "due", due)
		}
		out = append(out, fact(i18n.M("tasks.fact.due"), due))
	}
	if len(t.Tags) > 0 {
		out = append(out, fact(i18n.M("tasks.fact.tags"), "`"+strings.Join(t.Tags, "`, `")+"`"))
	}
	if t.Blocked {
		out = append(out, l.T("tasks.fact.waiting"))
	}
	return out
}

// priorityText is a priority in words in language l (lowercase, as in the !high syntax), or "".
func priorityText(l *i18n.Localizer, p int) string {
	switch p {
	case 1:
		return l.T("tasks.priority.highest")
	case 2:
		return l.T("tasks.priority.high")
	case 3:
		return l.T("tasks.priority.normal")
	case 4:
		return l.T("tasks.priority.low")
	case 5:
		return l.T("tasks.priority.lowest")
	}
	return ""
}

// show renders one task with its latest comments.
func (e *env) show(ctx context.Context, ref string) (string, error) {
	t, err := e.tc.GetTask(ctx, ref)
	if err != nil {
		return "", err
	}
	wf, _, _ := e.workflowFor(t.ProjectID)
	l := e.l()
	var b strings.Builder
	b.WriteString("#### " + e.card(t, wf, ""))
	if body := excerpt(t.Body, 600); body != "" {
		b.WriteString("\n\n" + quote(body))
	}
	cs, err := e.tc.ListComments(ctx, t.Ref, nil)
	if err == nil && len(cs.Items) > 0 {
		items := cs.Items
		if len(items) > recentComments {
			b.WriteString("\n\n" + l.N("tasks.show.comments_last", len(items), "shown", recentComments))
			items = items[len(items)-recentComments:]
		} else {
			b.WriteString("\n\n" + l.T("tasks.show.comments"))
		}
		for _, c := range items {
			b.WriteString("\n- " + l.T("tasks.show.comment", "who", e.mention(c.AuthorActorID), "when", ago(l, e.now().Sub(c.CreatedAt)), "text", oneLine(excerpt(c.Body, 300))))
		}
	}
	return b.String(), nil
}

// list renders the tasks a /tix list selects, grouped by status.
func (e *env) list(ctx context.Context, q *listQuery) (string, error) {
	p := url.Values{}
	for k, v := range q.Params {
		p[k] = append([]string(nil), v...)
	}
	if p.Get("limit") == "" {
		p.Set("limit", strconv.Itoa(maxList))
	}
	ws, wfErr := e.workflows()
	if q.Open && wfErr == nil {
		for _, k := range terminalKeys(ws) {
			p.Add("not_status", k)
		}
	}
	page, err := e.tc.ListTasks(ctx, p)
	if err != nil {
		return "", err
	}
	type group struct {
		label string
		rank  int
		tasks []tix.Task
	}
	groups := map[string]*group{}
	total := 0
	for _, t := range page.Items {
		if (q.Open && t.CompletedAt != nil) || !q.keepDue(&t, e.now()) {
			continue
		}
		total++
		wf, _, _ := e.workflowFor(t.ProjectID)
		lbl, rank := t.Status, categoryRank("")
		if wf != nil {
			if s, ok := wf.StateByKey(t.Status); ok {
				lbl, rank = labelOf(s), categoryRank(category(wf, s))
			}
		}
		key := strings.ToLower(lbl)
		g, ok := groups[key]
		if !ok {
			g = &group{label: lbl, rank: rank}
			groups[key] = g
		}
		g.tasks = append(g.tasks, t)
	}
	l := e.l()
	if total == 0 {
		if q.Mine && len(q.Params) == 1 && q.Open {
			return l.T("tasks.list.none_mine"), nil
		}
		return l.T("tasks.list.none"), nil
	}
	ordered := make([]*group, 0, len(groups))
	for _, g := range groups {
		ordered = append(ordered, g)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].rank != ordered[j].rank {
			return ordered[i].rank < ordered[j].rank
		}
		return ordered[i].label < ordered[j].label
	})
	var b strings.Builder
	more := ""
	if page.NextCursor != "" {
		more = "+"
	}
	fmt.Fprintf(&b, "#### %s: %d%s", q.Title(l), total, more)
	shown := 0
	for _, g := range ordered {
		if shown >= maxShown {
			break
		}
		fmt.Fprintf(&b, "\n**%s** (%d)", g.label, len(g.tasks))
		for _, t := range g.tasks {
			if shown >= maxShown {
				break
			}
			b.WriteString("\n- " + e.line(&t, !q.Mine))
			shown++
		}
	}
	if shown < total || page.NextCursor != "" {
		b.WriteString("\n\n" + l.N("tasks.list.more", shown))
	}
	return b.String(), nil
}

// line renders a task in a listing.
func (e *env) line(t *tix.Task, withAssignee bool) string {
	parts := []string{fmt.Sprintf("[%s](%s) %s", t.Ref, e.taskURL(t), oneLine(t.Title))}
	if t.Priority == 1 || t.Priority == 2 {
		parts = append(parts, "!"+priorityName(t.Priority))
	}
	l := e.l()
	if d, ok := render.DueOf(t.DueAt); ok {
		now := e.now()
		day := e.r().DueDay(d)
		due := l.T("tasks.line.due", "due", day)
		if t.CompletedAt == nil && d.Overdue(now, now.Location()) {
			due = l.T("tasks.line.overdue", "due", day)
		}
		parts = append(parts, due)
	}
	if t.Blocked {
		parts = append(parts, l.T("tasks.line.waiting"))
	}
	if withAssignee && t.AssigneeActorID != "" {
		parts = append(parts, e.mention(t.AssigneeActorID))
	}
	return strings.Join(parts, " · ")
}

// terminalKeys are the state keys that are terminal in every workflow that declares them, so
// leaving them out of a listing never hides an open task of another workflow.
func terminalKeys(ws []tix.Workflow) []string {
	terminal, open := map[string]bool{}, map[string]bool{}
	for _, w := range ws {
		for _, s := range w.Definition.States {
			if s.Terminal {
				terminal[s.Key] = true
			} else {
				open[s.Key] = true
			}
		}
	}
	var out []string
	for k := range terminal {
		if !open[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// move carries out start, done, close, reopen or move <state>.
func (e *env) move(ctx context.Context, ref string, in intent, target, comment string) (string, error) {
	t, err := e.tc.GetTask(ctx, ref)
	if err != nil {
		return "", err
	}
	wf, _, err := e.workflowFor(t.ProjectID)
	if err != nil {
		return "", err
	}
	g, err := goalFor(wf, in, target)
	if err != nil {
		return "", err
	}
	r := resolve(wf, t.Status, g)
	l := e.l()
	link := fmt.Sprintf("[%s](%s)", t.Ref, e.taskURL(t))
	cur := label(wf, t.Status)
	switch {
	case r.Already:
		return l.T("tasks.move.already", "task", link, "state", cur), nil
	case r.Transition == nil:
		msg := l.T("tasks.move.no_direct", "workflow", wf.Name, "from", cur, "goal", g.describe(l))
		if len(r.Reachable) > 0 {
			msg += " " + l.T("tasks.move.reachable", "states", stateList(l, wf, r.Reachable))
		} else {
			msg += " " + l.T("tasks.move.nothing_reachable")
		}
		if len(r.Route) > 1 {
			names := make([]string, len(r.Route))
			for i, s := range r.Route {
				names[i] = labelOf(s)
			}
			msg += " " + l.T("tasks.move.route", "route", cur+" → "+strings.Join(names, " → "),
				"command", fmt.Sprintf("/tix move %s %s", t.Ref, r.Route[0].Key))
		}
		return msg, nil
	case r.Transition.RequiresComment && strings.TrimSpace(comment) == "":
		return l.T("tasks.move.needs_comment", "task", link, "state", label(wf, r.Transition.To),
			"command", usageWithComment(in, t.Ref, r.Transition.To)), nil
	}
	moved, err := e.tc.Transition(ctx, t.Ref, r.Transition.To, comment)
	if err != nil {
		return "", err
	}
	msg := l.T("tasks.move.moved", "task", link, "title", oneLine(moved.Title), "from", cur, "to", label(wf, moved.Status))
	if comment != "" {
		msg += " " + l.T("tasks.move.comment_added")
	}
	return msg, nil
}

func usageWithComment(in intent, ref, to string) string {
	if in == toState {
		return fmt.Sprintf("/tix move %s %s <comment>", ref, to)
	}
	return fmt.Sprintf("/tix %s %s <comment>", in, ref)
}

// comment adds a comment to a task.
func (e *env) comment(ctx context.Context, ref, text string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", i18n.NewError("tasks.err.comment_text", "ref", ref)
	}
	if _, err := e.tc.AddComment(ctx, ref, text); err != nil {
		return "", err
	}
	t, err := e.tc.GetTask(ctx, ref)
	if err != nil {
		return e.l().T("tasks.comment.done", "task", ref), nil
	}
	return e.l().T("tasks.comment.done", "task", fmt.Sprintf("[%s](%s) **%s**", t.Ref, e.taskURL(t), oneLine(t.Title))), nil
}

// assign sets or clears a task's assignee.
func (e *env) assign(ctx context.Context, ref, who string) (string, error) {
	id, err := e.actorFor(who)
	if err != nil {
		return "", err
	}
	t, err := e.tc.UpdateTask(ctx, ref, map[string]any{"assignee_actor_id": id})
	if err != nil {
		return "", err
	}
	link := fmt.Sprintf("[%s](%s) **%s**", t.Ref, e.taskURL(t), oneLine(t.Title))
	if t.AssigneeActorID == "" {
		return e.l().T("tasks.assign.unassigned", "task", link), nil
	}
	return e.l().T("tasks.assign.assigned", "task", link, "assignee", e.mention(t.AssigneeActorID)), nil
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// excerpt shortens text to about n bytes on a rune boundary.
func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8Start(s[cut]) {
		cut--
	}
	return strings.TrimSpace(s[:cut]) + "…"
}

func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

func quote(s string) string {
	return "> " + strings.ReplaceAll(s, "\n", "\n> ")
}

// ago renders a duration in the past, roughly, in language l.
func ago(l *i18n.Localizer, d time.Duration) string {
	switch {
	case d < time.Minute:
		return l.T("tasks.ago.now")
	case d < time.Hour:
		return l.N("tasks.ago.minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return l.N("tasks.ago.hours", int(d.Hours()))
	default:
		return l.N("tasks.ago.days", int(d.Hours()/24))
	}
}
