// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// priorityNames are tix's priority words, 1 the highest.
var priorityNames = []string{"", "highest", "high", "normal", "low", "lowest"}

// parsePriority reads a priority as a word (high), a number (2) or p2.
func parsePriority(s string) (int, error) {
	s = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "!"))
	if s == "medium" {
		return 3, nil
	}
	for i, n := range priorityNames {
		if i > 0 && s == n {
			return i, nil
		}
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(s, "p")); err == nil && n >= 1 && n <= 5 {
		return n, nil
	}
	return 0, fmt.Errorf("unknown priority `%s`: use highest, high, normal, low, lowest or 1 to 5", s)
}

// priorityName renders a priority, empty when unset.
func priorityName(p int) string {
	if p >= 1 && p < len(priorityNames) {
		return priorityNames[p]
	}
	return ""
}

// restAfter returns the raw text after the first n arguments of a command line, split the way
// command.SplitArgs splits it, so a comment keeps its quotes, spacing and line breaks.
func restAfter(s string, n int) string {
	i := 0
	for range n {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			return ""
		}
		inQuote := false
		for i < len(s) && (inQuote || !isSpace(s[i])) {
			if s[i] == '"' {
				inQuote = !inQuote
			}
			i++
		}
	}
	return unquote(strings.TrimSpace(s[i:]))
}

func isSpace(b byte) bool { return b == ' ' || b == '\t' || b == '\n' || b == '\r' }

// unquote drops one pair of double quotes around the whole text.
func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' && !strings.Contains(s[1:len(s)-1], `"`) {
		return s[1 : len(s)-1]
	}
	return s
}

// normRef accepts infra-42, #infra-42, INFRA-42 or a link to the task in tix.
func normRef(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			s = parts[len(parts)-1]
		}
	}
	return strings.TrimPrefix(s, "#")
}

// newArgs is a parsed /tix new.
type newArgs struct {
	Project  string
	Title    string
	Assign   string // as typed: @user, me, none or a tix handle
	Priority int
	Post     bool
	Raw      bool
}

// parseNew reads /tix new [project] <title...> [--assign X] [--priority X] [--post] [--raw].
// isProject tells whether a word is a project key; the first word is taken as the project only
// when a title follows it.
func parseNew(args []string, isProject func(string) bool) (*newArgs, error) {
	out := &newArgs{}
	var words []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, val, hasVal := strings.Cut(a, "=")
		next := func() (string, error) {
			if hasVal {
				return val, nil
			}
			if i+1 >= len(args) {
				return "", fmt.Errorf("`%s` needs a value", name)
			}
			i++
			return args[i], nil
		}
		switch strings.ToLower(name) {
		case "--assign", "-a", "--assignee":
			v, err := next()
			if err != nil {
				return nil, err
			}
			out.Assign = v
		case "--priority", "-p":
			v, err := next()
			if err != nil {
				return nil, err
			}
			if out.Priority, err = parsePriority(v); err != nil {
				return nil, err
			}
		case "--project":
			v, err := next()
			if err != nil {
				return nil, err
			}
			out.Project = v
		case "--post":
			out.Post = true
		case "--raw":
			out.Raw = true
		default:
			if strings.HasPrefix(a, "--") {
				return nil, fmt.Errorf("unknown option `%s`", a)
			}
			words = append(words, a)
		}
	}
	if out.Project == "" && len(words) > 1 && isProject(words[0]) {
		out.Project, words = words[0], words[1:]
	}
	out.Title = strings.TrimSpace(strings.Join(words, " "))
	if out.Title == "" {
		return nil, errors.New("give the task a title: `/tix new [project] <title>`")
	}
	return out, nil
}

// listQuery is a parsed /tix list.
type listQuery struct {
	Params url.Values
	// Open keeps only tasks that are not in a terminal state.
	Open bool
	// Mine is set when the listing is the caller's own tasks.
	Mine bool
	// Title describes the listing for the reply.
	Title string
}

// parseList reads /tix list [mine|all|<project>] [filter terms...]. A filter term is the tix
// filter language (docs/filtering.md) carried as GET /tasks query parameters. resolve turns an
// @mention into something tix's assignee filter takes.
func parseList(args []string, isProject func(string) bool, resolve func(string) (string, error), now time.Time) (*listQuery, error) {
	q := &listQuery{Params: url.Values{}, Open: true}
	var desc []string
	project := ""
	var free []string
	filtered := false
	for i, a := range args {
		low := strings.ToLower(a)
		switch {
		case low == "mine" || low == "me":
			q.Params.Add("assignee", "me")
			q.Mine = true
			continue
		case low == "all":
			q.Open = false
			desc = append(desc, "including finished")
			continue
		case i == 0 && !strings.ContainsAny(a, ":~@-") && isProject(a):
			project = a
			q.Params.Add("project", a)
			continue
		}
		if err := addTerm(q, a, resolve, now, &free); err != nil {
			return nil, err
		}
		filtered = true
	}
	if len(free) > 0 {
		q.Params.Set("q", strings.Join(free, " "))
	}
	if len(args) == 0 {
		q.Params.Add("assignee", "me")
		q.Mine = true
	}
	switch {
	case q.Mine && project != "":
		q.Title = "My open tasks in " + strings.ToUpper(project)
	case q.Mine:
		q.Title = "My open tasks"
	case project != "":
		q.Title = "Open tasks in " + strings.ToUpper(project)
	default:
		q.Title = "Open tasks"
	}
	if !q.Open {
		q.Title = strings.Replace(q.Title, "open tasks", "tasks", 1)
		q.Title = strings.Replace(q.Title, "Open tasks", "Tasks", 1)
	}
	if filtered {
		desc = append(desc, "filtered")
	}
	if len(desc) > 0 {
		q.Title += " (" + strings.Join(desc, ", ") + ")"
	}
	return q, nil
}

// addTerm turns one filter term into query parameters.
func addTerm(q *listQuery, term string, resolve func(string) (string, error), now time.Time, free *[]string) error {
	p := q.Params
	neg := strings.HasPrefix(term, "-") && len(term) > 1
	body := term
	if neg {
		body = term[1:]
	}
	param := func(name string) string {
		if neg {
			return "not_" + name
		}
		return name
	}
	if strings.HasPrefix(body, "@") && len(body) > 1 {
		who, err := resolve(body)
		if err != nil {
			return err
		}
		p.Add(param("assignee"), who)
		if who == "me" && !neg {
			q.Mine = true
		}
		return nil
	}
	sep := strings.IndexAny(body, ":~")
	if sep <= 0 {
		if neg {
			p.Add("text", "-any~"+body)
		} else {
			*free = append(*free, body)
		}
		return nil
	}
	key, op, val := strings.ToLower(body[:sep]), body[sep], body[sep+1:]
	if val == "" {
		return fmt.Errorf("filter term `%s` has no value", term)
	}
	weak := op == '~'
	if weak && key != "title" && key != "body" && key != "text" {
		return fmt.Errorf("`~` works only with title, body and text, not `%s`", term)
	}
	noNeg := func() error {
		if neg {
			return fmt.Errorf("`%s` cannot be negated", term)
		}
		return nil
	}
	switch key {
	case "project", "p":
		p.Add(param("project"), val)
	case "status", "s":
		p.Add(param("status"), val)
		if !neg {
			q.Open = false
		}
	case "tag":
		p.Add(param("tag"), strings.TrimPrefix(val, "#"))
	case "assignee", "a", "creator", "claimed-by":
		who := val
		if strings.HasPrefix(val, "@") {
			var err error
			if who, err = resolve(val); err != nil {
				return err
			}
		}
		name := map[string]string{"assignee": "assignee", "a": "assignee", "creator": "creator", "claimed-by": "claimed_by"}[key]
		p.Add(param(name), who)
		if name == "assignee" && who == "me" && !neg {
			q.Mine = true
		}
	case "priority":
		n, err := parsePriority(val)
		if err != nil {
			return err
		}
		p.Add(param("priority"), strconv.Itoa(n))
	case "title", "body":
		p.Add("text", term)
	case "text", "q":
		switch {
		case weak || neg:
			prefix := ""
			if neg {
				prefix = "-"
			}
			p.Add("text", prefix+"any~"+val)
		default:
			*free = append(*free, val)
		}
	case "is":
		return addIs(p, strings.ToLower(val), neg, term)
	case "due":
		if err := noNeg(); err != nil {
			return err
		}
		return addDue(p, strings.ToLower(val), now)
	case "due-before", "due-after":
		if err := noNeg(); err != nil {
			return err
		}
		t, err := parseDate(val)
		if err != nil {
			return err
		}
		p.Set(strings.ReplaceAll(key, "-", "_"), t.Format(time.RFC3339))
	case "sort":
		if err := noNeg(); err != nil {
			return err
		}
		field, dir, _ := strings.Cut(val, ",")
		p.Set("sort", field)
		if dir != "" {
			p.Set("direction", dir)
		}
	case "limit":
		if err := noNeg(); err != nil {
			return err
		}
		n, err := strconv.Atoi(val)
		if err != nil || n < 1 {
			return fmt.Errorf("`%s`: limit takes a positive number", term)
		}
		p.Set("limit", strconv.Itoa(min(n, maxList)))
	default:
		return fmt.Errorf("unknown filter term `%s`; see the tix filter language (project:, status:, tag:, assignee:, priority:, title~, due:, is:)", term)
	}
	return nil
}

func addIs(p url.Values, val string, neg bool, term string) error {
	flag := map[string]struct {
		param string
		value bool
	}{
		"claimed":    {"claimed", true},
		"unclaimed":  {"claimed", false},
		"blocked":    {"blocked", true},
		"waiting-on": {"blocked", true},
		"unblocked":  {"blocked", false},
		"starred":    {"starred", true},
		"unstarred":  {"starred", false},
		"recurring":  {"recurring", true},
		"snoozed":    {"snoozed", true},
		"awake":      {"snoozed", false},
		"trashed":    {"trashed", true},
	}
	if val == "root" {
		if neg {
			return fmt.Errorf("`%s` cannot be negated", term)
		}
		p.Set("root_only", "true")
		return nil
	}
	f, ok := flag[val]
	if !ok {
		return fmt.Errorf("unknown filter term `%s`", term)
	}
	p.Set(f.param, strconv.FormatBool(f.value != neg))
	return nil
}

func addDue(p url.Values, val string, now time.Time) error {
	now = now.UTC()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	switch {
	case val == "overdue" || val == "late":
		p.Set("due_before", now.Format(time.RFC3339))
	case val == "today":
		p.Set("due_after", day.Format(time.RFC3339))
		p.Set("due_before", day.AddDate(0, 0, 1).Format(time.RFC3339))
	case val == "week":
		p.Set("due_after", now.Format(time.RFC3339))
		p.Set("due_before", now.AddDate(0, 0, 7).Format(time.RFC3339))
	case val == "month":
		p.Set("due_after", now.Format(time.RFC3339))
		p.Set("due_before", now.AddDate(0, 1, 0).Format(time.RFC3339))
	case strings.HasPrefix(val, "<") || strings.HasPrefix(val, ">"):
		t, err := parseDate(val[1:])
		if err != nil {
			return err
		}
		if val[0] == '<' {
			p.Set("due_before", t.Format(time.RFC3339))
		} else {
			p.Set("due_after", t.Format(time.RFC3339))
		}
	default:
		return fmt.Errorf("unknown due window `%s`: use overdue, today, week, month, <DATE or >DATE", val)
	}
	return nil
}

// parseDate reads YYYY-MM-DD, YYYY-MM-DD HH:MM or RFC 3339, in UTC.
func parseDate(s string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("`%s` is not a date: use YYYY-MM-DD", s)
}
