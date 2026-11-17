// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

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
		return nil, errors.New("say what to look for: `/tix search <text> [project] [filters]`")
	}
	q.Params.Set("q", text)
	q.Params.Set("limit", strconv.Itoa(maxSearch))
	q.Title = fmt.Sprintf("Tasks matching “%s”", excerpt(text, 80))
	if project != "" {
		q.Title += " in " + strings.ToUpper(project)
	}
	if q.Open {
		q.Title += " (open)"
	}
	return q, nil
}

// parseDue reads /tix due [filter terms...]: the caller's open tasks due by the end of today
// (UTC), overdue ones included, soonest first.
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
	u := now.UTC()
	tomorrow := time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
	q.Params.Set("due_before", tomorrow.Format(time.RFC3339))
	q.Params.Set("sort", "due_at")
	q.Params.Set("direction", "asc")
	q.Params.Set("limit", strconv.Itoa(maxSearch))
	q.Title = "My tasks due today or overdue"
	return q, nil
}

// flat lists the tasks a query selects, in tix's order, each with its status and assignee.
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
		if q.Open && t.CompletedAt != nil {
			continue
		}
		rows = append(rows, t)
		if len(rows) == maxSearch {
			break
		}
	}
	if len(rows) == 0 {
		return empty, nil
	}
	var b strings.Builder
	more := ""
	if page.NextCursor != "" {
		more = "+"
	}
	fmt.Fprintf(&b, "#### %s: %d%s", q.Title, len(rows), more)
	for i := range rows {
		t := &rows[i]
		wf, _, _ := e.workflowFor(t.ProjectID)
		b.WriteString("\n- " + e.line(t, false) + " · " + label(wf, t.Status) + " · " + e.mention(t.AssigneeActorID))
	}
	if more != "" {
		fmt.Fprintf(&b, "\n\nShowing the first %d. Narrow it with a project or a filter.", len(rows))
	}
	return b.String(), nil
}
