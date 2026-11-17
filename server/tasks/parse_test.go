// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePriority(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
		err  bool
	}{
		{"highest", 1, false},
		{"High", 2, false},
		{"!high", 2, false},
		{"normal", 3, false},
		{"medium", 3, false},
		{"low", 4, false},
		{"lowest", 5, false},
		{"1", 1, false},
		{"p5", 5, false},
		{"6", 0, true},
		{"urgent", 0, true},
		{"", 0, true},
	} {
		got, err := parsePriority(tc.in)
		if tc.err {
			assert.Error(t, err, tc.in)
			continue
		}
		require.NoError(t, err, tc.in)
		assert.Equal(t, tc.want, got, tc.in)
	}
}

func TestRestAfter(t *testing.T) {
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"/tix comment infra-1 looks  good\nthanks", 3, "looks  good\nthanks"},
		{`/tix comment infra-1 "quoted whole"`, 3, "quoted whole"},
		{`/tix comment infra-1 say "hi" there`, 3, `say "hi" there`},
		{`/tix move infra-1 "in review" because`, 4, "because"},
		{"/tix done infra-1", 3, ""},
		{"/tix done", 3, ""},
	} {
		assert.Equal(t, tc.want, restAfter(tc.in, tc.n), tc.in)
	}
}

func TestNormRef(t *testing.T) {
	for in, want := range map[string]string{
		"infra-42":                             "infra-42",
		"#INFRA-42":                            "INFRA-42",
		"https://tix.example.com/tasks/web-7":  "web-7",
		"https://tix.example.com/tasks/web-7/": "web-7",
	} {
		assert.Equal(t, want, normRef(in), in)
	}
}

func isProj(w string) bool { return strings.EqualFold(w, "infra") || strings.EqualFold(w, "web") }

func TestParseNew(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want newArgs
		err  string
	}{
		{"project and title", []string{"infra", "rotate", "keys"}, newArgs{Project: "infra", Title: "rotate keys"}, ""},
		{"title only", []string{"rotate", "keys"}, newArgs{Title: "rotate keys"}, ""},
		{"lone project word is a title", []string{"infra"}, newArgs{Title: "infra"}, ""},
		{"quoted title", []string{"web", "fix the  login"}, newArgs{Project: "web", Title: "fix the  login"}, ""},
		{"flags", []string{"infra", "rotate", "--assign", "@bob", "-p", "high", "--post"}, newArgs{Project: "infra", Title: "rotate", Assign: "@bob", Priority: 2, Post: true}, ""},
		{"flag with equals", []string{"rotate", "--priority=1", "--assign=me"}, newArgs{Title: "rotate", Priority: 1, Assign: "me"}, ""},
		{"explicit project", []string{"--project", "ops", "rotate"}, newArgs{Project: "ops", Title: "rotate"}, ""},
		{"quick-add tokens stay in the title", []string{"infra", "fix", "!high", "#auth", "due:fri"}, newArgs{Project: "infra", Title: "fix !high #auth due:fri"}, ""},
		{"raw", []string{"fix", "#1", "--raw"}, newArgs{Title: "fix #1", Raw: true}, ""},
		{"no title", []string{"--post"}, newArgs{}, "title"},
		{"missing value", []string{"fix", "--assign"}, newArgs{}, "needs a value"},
		{"bad priority", []string{"fix", "-p", "urgent"}, newArgs{}, "unknown priority"},
		{"unknown flag", []string{"fix", "--nope"}, newArgs{}, "unknown option"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseNew(tc.args, isProj)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, *got)
		})
	}
}

func TestParseList(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	resolve := func(who string) (string, error) {
		switch who {
		case "@me", "me":
			return "me", nil
		case "@bob":
			return "A_BOB", nil
		}
		return "", errors.New("@" + who + " has not connected tix")
	}
	for _, tc := range []struct {
		name  string
		args  []string
		want  url.Values
		open  bool
		mine  bool
		title string
		err   string
	}{
		{"default is mine", nil, url.Values{"assignee": {"me"}}, true, true, "My open tasks", ""},
		{"mine", []string{"mine"}, url.Values{"assignee": {"me"}}, true, true, "My open tasks", ""},
		{"project", []string{"infra"}, url.Values{"project": {"infra"}}, true, false, "Open tasks in INFRA", ""},
		{"project mine", []string{"infra", "mine"}, url.Values{"project": {"infra"}, "assignee": {"me"}}, true, true, "My open tasks in INFRA", ""},
		{"all", []string{"all", "mine"}, url.Values{"assignee": {"me"}}, false, true, "My tasks (including finished)", ""},
		{"status turns off the open filter", []string{"status:done", "tag:ops"}, url.Values{"status": {"done"}, "tag": {"ops"}}, false, false, "Tasks (filtered)", ""},
		{"negations", []string{"-tag:ops", "-project:web", "-status:blocked", "-@bob"}, url.Values{"not_tag": {"ops"}, "not_project": {"web"}, "not_status": {"blocked"}, "not_assignee": {"A_BOB"}}, true, false, "Open tasks (filtered)", ""},
		{"mention and priority", []string{"@bob", "priority:high"}, url.Values{"assignee": {"A_BOB"}, "priority": {"2"}}, true, false, "Open tasks (filtered)", ""},
		{"assignee me", []string{"a:@me"}, url.Values{"assignee": {"me"}}, true, true, "My open tasks (filtered)", ""},
		{"text terms", []string{"title~api", "-body~legacy", "text~gateway", "deploy", "rollback", "-spam"}, url.Values{"text": {"title~api", "-body~legacy", "any~gateway", "-any~spam"}, "q": {"deploy rollback"}}, true, false, "Open tasks (filtered)", ""},
		{"is terms", []string{"is:claimed", "-is:blocked", "is:root"}, url.Values{"claimed": {"true"}, "blocked": {"false"}, "root_only": {"true"}}, true, false, "Open tasks (filtered)", ""},
		{"due overdue", []string{"due:overdue"}, url.Values{"due_before": {"2026-11-17T10:00:00Z"}}, true, false, "Open tasks (filtered)", ""},
		{"due today", []string{"due:today"}, url.Values{"due_after": {"2026-11-17T00:00:00Z"}, "due_before": {"2026-11-18T00:00:00Z"}}, true, false, "Open tasks (filtered)", ""},
		{"due before date", []string{"due-before:2026-12-01"}, url.Values{"due_before": {"2026-12-01T00:00:00Z"}}, true, false, "Open tasks (filtered)", ""},
		{"sort and limit", []string{"sort:due_at", "limit:500"}, url.Values{"sort": {"due_at"}, "limit": {"100"}}, true, false, "Open tasks (filtered)", ""},
		{"unknown term", []string{"color:red"}, nil, false, false, "", "unknown filter term"},
		{"weak on status", []string{"status~do"}, nil, false, false, "", "only with title"},
		{"negated due", []string{"-due:week"}, nil, false, false, "", "cannot be negated"},
		{"unlinked mention", []string{"@carol"}, nil, false, false, "", "has not connected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseList(tc.args, isProj, resolve, now)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Params)
			assert.Equal(t, tc.open, got.Open, "open")
			assert.Equal(t, tc.mine, got.Mine, "mine")
			assert.Equal(t, tc.title, got.Title)
		})
	}
}
