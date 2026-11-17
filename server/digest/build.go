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
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
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

// summary is a digest's content. Overdue to InProgress put each task in the first section that
// fits, in the default order; reminders read Overdue and DueToday. fits records every section a
// task fits, so the digest can follow the DigestSections setting.
type summary struct {
	Overdue, DueToday, New, Blocked, InProgress []item
	Counts                                      map[string]int
	Open                                        int
	ids                                         []string
	all                                         []fitted
}

// fitted is a task and the sections it fits.
type fitted struct {
	it   item
	fits map[string]bool
}

// view lists, for each section in order (counts aside), the tasks to show there: each task in the
// first listed section it fits, so leaving a section out moves its tasks to the next one they fit.
func (s *summary) view(order []string) [][]item {
	shown := map[string]bool{}
	out := make([][]item, len(order))
	for i, sec := range order {
		if sec == render.SectionCounts {
			continue
		}
		for _, f := range s.all {
			if f.fits[sec] && !shown[f.it.t.ID] {
				shown[f.it.t.ID] = true
				out[i] = append(out[i], f.it)
			}
		}
		sortSection(sec, out[i])
	}
	return out
}

// empty says whether the digest has no task to list in the given sections.
func (s *summary) empty(order []string) bool {
	for _, l := range s.view(order) {
		if len(l) > 0 {
			return false
		}
	}
	return true
}

func sortSection(sec string, l []item) {
	if sec == render.SectionOverdue || sec == render.SectionDueToday {
		sort.SliceStable(l, func(i, j int) bool { return l[i].t.DueAt.Before(*l[j].t.DueAt) })
		return
	}
	sort.SliceStable(l, func(i, j int) bool { return prio(l[i].t.Priority) < prio(l[j].t.Priority) })
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
		fits := map[string]bool{
			render.SectionOverdue:    dated && due.Overdue(now, loc),
			render.SectionDueToday:   dated && due.DueToday(now, loc),
			render.SectionNew:        seen != nil && !slices.Contains(seen, it.t.ID),
			render.SectionBlocked:    it.category == "blocked" || it.category == "waiting" || it.t.Blocked,
			render.SectionInProgress: it.category == "in_progress",
		}
		s.all = append(s.all, fitted{it: it, fits: fits})
	}
	v := s.view(render.AllDigestSections)
	s.Overdue, s.DueToday, s.New, s.Blocked, s.InProgress = v[0], v[1], v[2], v[3], v[4]
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

func (f *Feature) line(it item, r *render.R, withDue bool) string {
	t := it.t
	l := fmt.Sprintf("- %s[%s %s](%s)", marker(t.Priority), t.Ref, escapeLink(t.Title), f.a.TaskURL(&t))
	if due, ok := render.DueOf(t.DueAt); withDue && ok {
		l += " · " + r.L.T("digest.line.due", "due", r.Due(due))
	}
	return l
}

// sectionTitles are the digest's section headings.
var sectionTitles = map[string]string{
	render.SectionOverdue:    i18n.M("digest.section.overdue"),
	render.SectionDueToday:   i18n.M("digest.section.due_today"),
	render.SectionNew:        i18n.M("digest.section.new"),
	render.SectionBlocked:    i18n.M("digest.section.blocked"),
	render.SectionInProgress: i18n.M("digest.section.in_progress"),
}

func (f *Feature) section(b *strings.Builder, r *render.R, sec string, items []item) {
	if len(items) == 0 {
		return
	}
	fmt.Fprintf(b, "**%s**\n", r.L.T("digest.section.title", "title", r.L.T(sectionTitles[sec]), "count", len(items)))
	withDue := sec == render.SectionOverdue || sec == render.SectionDueToday
	for i, it := range items {
		if i == maxLines {
			b.WriteString("- " + r.L.N("digest.more", len(items)-maxLines) + "\n")
			break
		}
		b.WriteString(f.line(it, r, withDue) + "\n")
	}
}

// renderDigest is the digest DM in r's language, time zone and date format, with the sections
// of the DigestSections setting in its order. An empty summary reads "All clear".
func (f *Feature) renderDigest(s *summary, r *render.R) string {
	var b strings.Builder
	order := r.O.DigestSections
	today := render.Today(r.Now, r.Loc)
	b.WriteString("#### " + r.L.T("digest.header", "weekday", r.Weekday(r.Now.In(r.Loc).Weekday()), "date", r.AbsDay(today)) + "\n")
	if s.empty(order) {
		b.WriteString(r.L.T("digest.all_clear") + "\n")
	} else {
		for i, items := range s.view(order) {
			f.section(&b, r, order[i], items)
		}
	}
	if s.Open > 0 && slices.Contains(order, render.SectionCounts) {
		labels := make([]string, 0, len(s.Counts))
		for l := range s.Counts {
			labels = append(labels, l)
		}
		sort.Strings(labels)
		parts := make([]string, 0, len(labels))
		for _, l := range labels {
			parts = append(parts, fmt.Sprintf("%d %s", s.Counts[l], l))
		}
		b.WriteString("\n" + r.L.T("digest.counts", "count", s.Open, "states", strings.Join(parts, ", ")))
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderReminders is the reminder DM.
func (f *Feature) renderReminders(dueToday, overdue []item, r *render.R) string {
	var b strings.Builder
	if len(dueToday) > 0 {
		b.WriteString("**" + r.L.T("digest.reminders.due_today") + "**\n")
		for _, it := range dueToday {
			b.WriteString(f.line(it, r, true) + "\n")
		}
	}
	if len(overdue) > 0 {
		b.WriteString("**" + r.L.T("digest.reminders.overdue") + "**\n")
		for _, it := range overdue {
			b.WriteString(f.line(it, r, true) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
