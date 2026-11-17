// SPDX-License-Identifier: AGPL-3.0-only

package digest

import (
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

const (
	maxPages   = 2  // at most 1000 open tasks per person
	maxLines   = 10 // per digest section
	maxSeen    = 1000
	recentDays = 7 // an overdue reminder goes out only within this many days of the due date
)

// item is one open task of the person with what its workflow says about its status.
type item struct {
	t        tix.Task
	label    string // status label
	category string
}

// fetch lists the person's open assigned tasks, optionally only those due before dueBefore.
func (f *Feature) fetch(userID string, tc *tix.Client, dueBefore time.Time) ([]item, error) {
	ps, err := f.a.Projects(userID)
	if err != nil {
		return nil, err
	}
	ws, err := f.a.Workflows(userID)
	if err != nil {
		return nil, err
	}
	flows := map[string]*tix.Workflow{}
	for i := range ws {
		flows[ws[i].ID] = &ws[i]
	}
	byProject := map[string]*tix.Workflow{}
	for _, p := range ps {
		byProject[p.ID] = flows[p.WorkflowID]
	}
	var terminal []string
	for _, w := range ws {
		for _, s := range w.Definition.States {
			if isClosed(s) && !slices.Contains(terminal, s.Key) {
				terminal = append(terminal, s.Key)
			}
		}
	}
	sort.Strings(terminal)

	q := url.Values{"assignee": {"me"}, "limit": {"500"}}
	for _, k := range terminal {
		q.Add("not_status", k)
	}
	if !dueBefore.IsZero() {
		q.Set("due_before", dueBefore.UTC().Format(time.RFC3339))
	}
	var out []item
	for range maxPages {
		ctx, cancel := app.Ctx()
		page, err := tc.ListTasks(ctx, q)
		cancel()
		if err != nil {
			return nil, err
		}
		for _, t := range page.Items {
			it := item{t: t, label: t.Status}
			if w := byProject[t.ProjectID]; w != nil {
				if s, ok := w.StateByKey(t.Status); ok {
					if isClosed(s) {
						continue
					}
					it.category = s.Category
					if s.Label != "" {
						it.label = s.Label
					}
				}
			}
			out = append(out, it)
		}
		if page.NextCursor == "" {
			break
		}
		q.Set("cursor", page.NextCursor)
	}
	return out, nil
}

func isClosed(s tix.State) bool {
	return s.Terminal || s.Category == "done" || s.Category == "cancelled"
}

// summary is a digest's content, each task in the first section that fits.
type summary struct {
	Overdue, DueToday, New, Blocked, InProgress []item
	Counts                                      map[string]int
	Open                                        int
	ids                                         []string
}

func (s *summary) empty() bool {
	return len(s.Overdue)+len(s.DueToday)+len(s.New)+len(s.Blocked)+len(s.InProgress) == 0
}

// classify sorts open tasks into the digest's sections. seen is nil before the first digest,
// when nothing counts as newly assigned.
func classify(items []item, now time.Time, loc *time.Location, seen []string) *summary {
	s := &summary{Counts: map[string]int{}}
	for _, it := range items {
		s.Open++
		s.Counts[it.label]++
		s.ids = append(s.ids, it.t.ID)
		due, dated := render.DueOf(it.t.DueAt)
		switch {
		case dated && due.Overdue(now, loc):
			s.Overdue = append(s.Overdue, it)
		case dated && due.DueToday(now, loc):
			s.DueToday = append(s.DueToday, it)
		case seen != nil && !slices.Contains(seen, it.t.ID):
			s.New = append(s.New, it)
		case it.category == "blocked" || it.category == "waiting" || it.t.Blocked:
			s.Blocked = append(s.Blocked, it)
		case it.category == "in_progress":
			s.InProgress = append(s.InProgress, it)
		}
	}
	for _, l := range [][]item{s.Overdue, s.DueToday} {
		sort.SliceStable(l, func(i, j int) bool { return l[i].t.DueAt.Before(*l[j].t.DueAt) })
	}
	for _, l := range [][]item{s.New, s.Blocked, s.InProgress} {
		sort.SliceStable(l, func(i, j int) bool { return prio(l[i].t.Priority) < prio(l[j].t.Priority) })
	}
	if len(s.ids) > maxSeen {
		s.ids = s.ids[:maxSeen]
	}
	return s
}

// prio orders unset priorities with normal ones.
func prio(p int) int {
	if p < 1 || p > 5 {
		return 3
	}
	return p
}

// marker flags the two highest priorities; normal and lower carry none, to keep lines short.
func marker(p int) string {
	switch p {
	case 1:
		return "`P1` "
	case 2:
		return "`P2` "
	}
	return ""
}

func escapeLink(s string) string {
	return strings.NewReplacer("[", "\\[", "]", "\\]").Replace(s)
}

func (f *Feature) line(it item, loc *time.Location, withDue bool) string {
	t := it.t
	l := fmt.Sprintf("- %s[%s %s](%s)", marker(t.Priority), t.Ref, escapeLink(t.Title), f.a.TaskURL(&t))
	if due, ok := render.DueOf(t.DueAt); withDue && ok {
		if due.DateOnly {
			l += " · due " + due.At.Format("Mon 2 Jan")
		} else {
			l += " · due " + due.At.In(loc).Format("Mon 2 Jan 15:04")
		}
	}
	return l
}

func (f *Feature) section(b *strings.Builder, title string, items []item, loc *time.Location, withDue bool) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "**%s (%d)**\n", title, len(items))
	for i, it := range items {
		if i == maxLines {
			fmt.Fprintf(b, "- …and %d more\n", len(items)-maxLines)
			break
		}
		b.WriteString(f.line(it, loc, withDue) + "\n")
	}
}

// renderDigest is the digest DM. An empty summary reads "All clear".
func (f *Feature) renderDigest(s *summary, now time.Time, loc *time.Location) string {
	var b strings.Builder
	fmt.Fprintf(&b, "#### Your tix digest for %s\n", now.In(loc).Format("Monday 2 January"))
	if s.empty() {
		b.WriteString("All clear: nothing overdue, due today, new, blocked or in progress.\n")
	} else {
		f.section(&b, "Overdue", s.Overdue, loc, true)
		f.section(&b, "Due today", s.DueToday, loc, true)
		f.section(&b, "Newly assigned", s.New, loc, false)
		f.section(&b, "Blocked", s.Blocked, loc, false)
		f.section(&b, "In progress", s.InProgress, loc, false)
	}
	if s.Open > 0 {
		labels := make([]string, 0, len(s.Counts))
		for l := range s.Counts {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		parts := make([]string, 0, len(labels))
		for _, l := range labels {
			parts = append(parts, fmt.Sprintf("%d %s", s.Counts[l], l))
		}
		fmt.Fprintf(&b, "\nOpen and assigned to you: %d (%s)", s.Open, strings.Join(parts, ", "))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderReminders is the reminder DM.
func (f *Feature) renderReminders(dueToday, overdue []item, loc *time.Location) string {
	var b strings.Builder
	if len(dueToday) > 0 {
		b.WriteString("**Due today**\n")
		for _, it := range dueToday {
			b.WriteString(f.line(it, loc, true) + "\n")
		}
	}
	if len(overdue) > 0 {
		b.WriteString("**Now overdue**\n")
		for _, it := range overdue {
			b.WriteString(f.line(it, loc, true) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
