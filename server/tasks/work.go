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
				return nil, fmt.Errorf("@%s %w; they can run `/tix connect`, or use `--assign <tix handle>`", name, errUnlinkedUser)
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

// card renders a task as a few lines: the linked title and its facts.
func (e *env) card(t *tix.Task, wf *tix.Workflow, verb string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s[%s](%s) **%s**\n", verb, t.Ref, e.taskURL(t), oneLine(t.Title))
	b.WriteString(strings.Join(e.facts(t, wf), " · "))
	return b.String()
}

// facts are the labelled attributes of a task for a card.
func (e *env) facts(t *tix.Task, wf *tix.Workflow) []string {
	out := []string{"**Status:** " + label(wf, t.Status)}
	out = append(out, "**Assignee:** "+e.mention(t.AssigneeActorID))
	if p := priorityName(t.Priority); p != "" {
		out = append(out, "**Priority:** "+p)
	}
	if t.DueAt != nil {
		due := t.DueAt.UTC().Format("Mon 02.01.2006")
		if t.CompletedAt == nil && t.DueAt.Before(e.now()) {
			due += " (overdue)"
		}
		out = append(out, "**Due:** "+due)
	}
	if len(t.Tags) > 0 {
		out = append(out, "**Tags:** `"+strings.Join(t.Tags, "`, `")+"`")
	}
	if t.Blocked {
		out = append(out, "waiting on another task")
	}
	return out
}

// show renders one task with its latest comments.
func (e *env) show(ctx context.Context, ref string) (string, error) {
	t, err := e.tc.GetTask(ctx, ref)
	if err != nil {
		return "", err
	}
	wf, _, _ := e.workflowFor(t.ProjectID)
	var b strings.Builder
	b.WriteString("#### " + e.card(t, wf, ""))
	if body := excerpt(t.Body, 600); body != "" {
		b.WriteString("\n\n" + quote(body))
	}
	cs, err := e.tc.ListComments(ctx, t.Ref, nil)
	if err == nil && len(cs.Items) > 0 {
		items := cs.Items
		if len(items) > recentComments {
			fmt.Fprintf(&b, "\n\n**Comments** (last %d of %d)", recentComments, len(items))
			items = items[len(items)-recentComments:]
		} else {
			b.WriteString("\n\n**Comments**")
		}
		for _, c := range items {
			fmt.Fprintf(&b, "\n- %s, %s: %s", e.mention(c.AuthorActorID), ago(e.now().Sub(c.CreatedAt)), oneLine(excerpt(c.Body, 300)))
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
		if q.Open && t.CompletedAt != nil {
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
	if total == 0 {
		if q.Mine && len(q.Params) == 1 && q.Open {
			return "You have no open tasks assigned to you in tix.", nil
		}
		return "No tasks match.", nil
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
	fmt.Fprintf(&b, "#### %s: %d%s", q.Title, total, more)
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
		b.WriteString("\n\nShowing the first " + strconv.Itoa(shown) + ". Narrow it with a filter, e.g. `/tix list mine status:todo` or `/tix list infra tag:ops`.")
	}
	return b.String(), nil
}

// line renders a task in a listing.
func (e *env) line(t *tix.Task, withAssignee bool) string {
	parts := []string{fmt.Sprintf("[%s](%s) %s", t.Ref, e.taskURL(t), oneLine(t.Title))}
	if t.Priority == 1 || t.Priority == 2 {
		parts = append(parts, "!"+priorityName(t.Priority))
	}
	if t.DueAt != nil {
		due := "due " + t.DueAt.UTC().Format("02.01.")
		if t.CompletedAt == nil && t.DueAt.Before(e.now()) {
			due = "**overdue** " + t.DueAt.UTC().Format("02.01.")
		}
		parts = append(parts, due)
	}
	if t.Blocked {
		parts = append(parts, "waiting")
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
	link := fmt.Sprintf("[%s](%s)", t.Ref, e.taskURL(t))
	cur := label(wf, t.Status)
	switch {
	case r.Already:
		return fmt.Sprintf("%s is already **%s**.", link, cur), nil
	case r.Transition == nil:
		msg := fmt.Sprintf("The %s workflow has no direct move from **%s** to %s.", wf.Name, cur, g.what)
		if len(r.Reachable) > 0 {
			msg += " From here it can go to " + stateList(wf, r.Reachable) + "."
		} else {
			msg += " Nothing is reachable from here."
		}
		if len(r.Route) > 1 {
			names := make([]string, len(r.Route))
			for i, s := range r.Route {
				names[i] = labelOf(s)
			}
			msg += fmt.Sprintf(" The shortest way is %s → %s: start with `/tix move %s %s`.", cur, strings.Join(names, " → "), t.Ref, r.Route[0].Key)
		}
		return msg, nil
	case r.Transition.RequiresComment && strings.TrimSpace(comment) == "":
		return fmt.Sprintf("Moving %s to **%s** needs a comment: `%s`.", link, label(wf, r.Transition.To), usageWithComment(in, t.Ref, r.Transition.To)), nil
	}
	moved, err := e.tc.Transition(ctx, t.Ref, r.Transition.To, comment)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("Moved %s **%s**: %s → **%s**.", link, oneLine(moved.Title), cur, label(wf, moved.Status))
	if comment != "" {
		msg += " Your comment was added."
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
		return "", fmt.Errorf("write the comment after the reference: `/tix comment %s <text>`", ref)
	}
	if _, err := e.tc.AddComment(ctx, ref, text); err != nil {
		return "", err
	}
	t, err := e.tc.GetTask(ctx, ref)
	if err != nil {
		return fmt.Sprintf("Commented on %s.", ref), nil
	}
	return fmt.Sprintf("Commented on [%s](%s) **%s**.", t.Ref, e.taskURL(t), oneLine(t.Title)), nil
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
		return "Unassigned " + link + ".", nil
	}
	return fmt.Sprintf("Assigned %s to %s.", link, e.mention(t.AssigneeActorID)), nil
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

// ago renders a duration in the past, roughly.
func ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d min ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d h ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days ago", int(d.Hours()/24))
	}
}
