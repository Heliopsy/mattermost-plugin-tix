// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// maxSearch is how many tasks /tix search and /tix due show.
const maxSearch = 15

// parseSearch reads /tix search <text...> [project] [filter terms...]. Bare words are the free
// text; a bare word after the first that names a project picks that project; open leaves out
// finished tasks, which a search otherwise includes.
func parseSearch(args []string, isProject func(string) bool, resolve func(string) (string, error), now time.Time) (*listQuery, error) {
	q := &listQuery{Params: url.Values{}}
	var free []string
	project := ""
	for i, a := range args {
		low := strings.ToLower(a)
		switch {
		case low == "open":
			q.Open = true
			continue
		case i > 0 && project == "" && !strings.ContainsAny(a, ":~@-") && isProject(a):
			project = a
			q.Params.Set("project", a)
			continue
		}
		if err := addTerm(q, a, resolve, now, &free); err != nil {
			return nil, err
		}
	}
	text := strings.TrimSpace(strings.Join(free, " "))
	if text == "" {
		return nil, i18n.NewError("tasks.err.search_text")
	}
	q.Params.Set("q", text)
	q.Params.Set("limit", strconv.Itoa(maxSearch))
	q.titleID, q.titleArgs = i18n.M("tasks.search.title"), []any{"text", excerpt(text, 80)}
	if project != "" {
		q.titleID = i18n.M("tasks.search.title_project")
		q.titleArgs = append(q.titleArgs, "project", strings.ToUpper(project))
	}
	if q.Open {
		q.desc = append(q.desc, i18n.M("tasks.search.desc_open"))
	}
	return q, nil
}

// parseDue reads /tix due [filter terms...]: the caller's open tasks due by the end of today in
// the caller's time zone (now's location), overdue ones included, soonest first.
func parseDue(args []string, isProject func(string) bool, resolve func(string) (string, error), now time.Time) (*listQuery, error) {
	q := &listQuery{Params: url.Values{}, Open: true, Mine: true}
	var free []string
	for i, a := range args {
		if i == 0 && !strings.ContainsAny(a, ":~@-") && isProject(a) {
			q.Params.Set("project", a)
			continue
		}
		if err := addTerm(q, a, resolve, now, &free); err != nil {
			return nil, err
		}
	}
	if len(free) > 0 {
		q.Params.Set("q", strings.Join(free, " "))
	}
	if q.Params.Get("assignee") == "" {
		q.Params.Set("assignee", "me")
	}
	q.Params.Set("due_before", render.DueTodayBound(now, now.Location()).UTC().Format(time.RFC3339))
	q.Due = dueByToday
	q.Params.Set("sort", "due_at")
	q.Params.Set("direction", "asc")
	q.Params.Set("limit", strconv.Itoa(maxSearch))
	q.titleID = i18n.M("tasks.due.title")
	return q, nil
}

// flat lists the tasks a query selects, in tix's order, each with its status and assignee.
// empty is the message id of the reply when nothing matches.
func (e *env) flat(cx context.Context, q *listQuery, empty string) (string, error) {
	p := url.Values{}
	for k, v := range q.Params {
		p[k] = append([]string(nil), v...)
	}
	if q.Open {
		if ws, err := e.workflows(); err == nil {
			for _, k := range terminalKeys(ws) {
				p.Add("not_status", k)
			}
		}
	}
	page, err := e.tc.ListTasks(cx, p)
	if err != nil {
		return "", err
	}
	var rows []tix.Task
	for _, t := range page.Items {
		if (q.Open && t.CompletedAt != nil) || !q.keepDue(&t, e.now()) {
			continue
		}
		rows = append(rows, t)
		if len(rows) == maxSearch {
			break
		}
	}
	l := e.l()
	if len(rows) == 0 {
		return l.T(empty), nil
	}
	var b strings.Builder
	more := ""
	if page.NextCursor != "" {
		more = "+"
	}
	fmt.Fprintf(&b, "#### %s: %d%s", q.Title(l), len(rows), more)
	for i := range rows {
		t := &rows[i]
		wf, _, _ := e.workflowFor(t.ProjectID)
		b.WriteString("\n- " + e.line(t, false) + " · " + label(wf, t.Status) + " · " + e.mention(t.AssigneeActorID))
	}
	if more != "" {
		b.WriteString("\n\n" + l.N("tasks.flat.more", len(rows)))
	}
	return b.String(), nil
}
