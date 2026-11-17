// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

var testNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

type call struct {
	Method string
	Path   string
	Query  url.Values
	Body   map[string]any
}

// fakeTix answers the handful of tix routes the feature uses from in-memory tasks.
type fakeTix struct {
	mu       sync.Mutex
	tasks    map[string]*tix.Task
	comments map[string][]tix.Comment
	listed   []tix.Task
	calls    []call
}

func (f *fakeTix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := call{Method: r.Method, Path: strings.TrimPrefix(r.URL.Path, "/api/v1"), Query: r.URL.Query()}
	if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
		_ = json.Unmarshal(raw, &c.Body)
	}
	f.calls = append(f.calls, c)
	if r.Header.Get("Authorization") != "Bearer tok" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(c.Path, "/"), "/")
	write := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	notFound := func() {
		w.WriteHeader(http.StatusNotFound)
		write(map[string]any{"error": map[string]string{"code": "not_found", "message": "no such task"}})
	}
	switch {
	case c.Method == http.MethodGet && c.Path == "/tasks":
		write(tix.Page[tix.Task]{Items: f.listed})
	case c.Method == http.MethodPost && c.Path == "/tasks":
		t := &tix.Task{Ref: "INFRA-9", ProjectID: "p1", Status: "todo", Title: c.Body["title"].(string)}
		if a, ok := c.Body["assignee_actor_id"].(string); ok {
			t.AssigneeActorID = a
		}
		f.tasks["infra-9"] = t
		write(t)
	case len(parts) >= 2 && parts[0] == "tasks":
		t, ok := f.tasks[strings.ToLower(parts[1])]
		if !ok {
			notFound()
			return
		}
		switch {
		case len(parts) == 2 && c.Method == http.MethodGet:
			write(t)
		case len(parts) == 2 && c.Method == http.MethodPatch:
			if a, ok := c.Body["assignee_actor_id"].(string); ok {
				t.AssigneeActorID = a
			}
			write(t)
		case parts[2] == "transition":
			t.Status = c.Body["to"].(string)
			write(t)
		case parts[2] == "comments" && c.Method == http.MethodGet:
			write(tix.Page[tix.Comment]{Items: f.comments[t.Ref]})
		case parts[2] == "comments":
			write(tix.Comment{ID: "c1", Body: c.Body["body"].(string)})
		}
	default:
		notFound()
	}
}

func (f *fakeTix) last(method, path string) *call {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		if f.calls[i].Method == method && f.calls[i].Path == path {
			return &f.calls[i]
		}
	}
	return nil
}

func newTestEnv(t *testing.T) (*env, *fakeTix) {
	t.Helper()
	ft := &fakeTix{tasks: map[string]*tix.Task{
		"infra-1": {Ref: "INFRA-1", ProjectID: "p1", Status: "todo", Title: "Rotate keys", Priority: 2, Tags: []string{"ops"}, AssigneeActorID: "A_ME"},
		"infra-2": {Ref: "INFRA-2", ProjectID: "p1", Status: "review", Title: "Ship it"},
		"infra-3": {Ref: "INFRA-3", ProjectID: "p1", Status: "done", Title: "Old"},
	}, comments: map[string][]tix.Comment{}}
	srv := httptest.NewServer(ft)
	t.Cleanup(srv.Close)
	wf := testWorkflow()
	e := &env{
		tc:      tix.New(srv.URL, "tok"),
		actorID: "A_ME",
		projects: func() ([]tix.Project, error) {
			return []tix.Project{{ID: "p1", Key: "infra", Name: "Infra", WorkflowID: "wf1"}}, nil
		},
		workflows: func() ([]tix.Workflow, error) { return []tix.Workflow{*wf}, nil },
		mention: func(id string) string {
			if id == "" {
				return "nobody"
			}
			return "@" + strings.ToLower(strings.TrimPrefix(id, "A_"))
		},
		taskURL: func(t *tix.Task) string { return "https://tix.test/tasks/" + t.Ref },
		mmActor: func(name string) (string, bool, error) {
			switch name {
			case "bob":
				return "A_BOB", true, nil
			case "carol":
				return "", true, errUnlinkedUser
			}
			return "", false, nil
		},
		now: func() time.Time { return testNow },
	}
	return e, ft
}

func TestMove(t *testing.T) {
	for _, tc := range []struct {
		name     string
		ref      string
		in       intent
		target   string
		comment  string
		wantTo   string // transition sent, empty for none
		contains string
	}{
		{"start", "infra-1", toStart, "", "", "doing", "To do → **In progress**"},
		{"done needs a comment", "infra-2", toDone, "", "", "", "needs a comment: `/tix done INFRA-2 <comment>`"},
		{"done with comment", "infra-2", toDone, "", "lgtm", "done", "Your comment was added"},
		{"done too far", "infra-1", toDone, "", "", "", "shortest way is To do → In progress → In review → Done: start with `/tix move INFRA-1 doing`"},
		{"close", "infra-1", toClose, "", "", "wontdo", "**Won't do**"},
		{"close finished", "infra-3", toClose, "", "", "", "already **Done**"},
		{"reopen", "infra-3", toReopen, "", "", "todo", "Done → **To do**"},
		{"move needs comment", "infra-2", toState, "done", "", "", "`/tix move INFRA-2 done <comment>`"},
		{"move back", "infra-2", toState, "in progress", "", "doing", "**In progress**"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ft := newTestEnv(t)
			msg, err := e.move(t.Context(), tc.ref, tc.in, tc.target, tc.comment)
			require.NoError(t, err)
			assert.Contains(t, msg, tc.contains)
			tr := ft.last(http.MethodPost, "/tasks/"+strings.ToUpper(tc.ref)+"/transition")
			if tc.wantTo == "" {
				assert.Nil(t, tr, "no transition expected")
				return
			}
			require.NotNil(t, tr)
			assert.Equal(t, tc.wantTo, tr.Body["to"])
			if tc.comment != "" {
				assert.Equal(t, tc.comment, tr.Body["comment"])
			} else {
				assert.NotContains(t, tr.Body, "comment")
			}
		})
	}
}

func TestMoveNotFound(t *testing.T) {
	e, _ := newTestEnv(t)
	_, err := e.move(t.Context(), "nope-1", toDone, "", "")
	require.Error(t, err)
	assert.True(t, tix.IsStatus(err, http.StatusNotFound))
}

func TestCreate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		na        newArgs
		wantTitle string
		wantAssig any
		err       string
	}{
		{"parse is on", newArgs{Project: "infra", Title: "fix !high #auth"}, "fix !high #auth", nil, ""},
		{"assign me", newArgs{Title: "fix", Assign: "me"}, "fix", "A_ME", ""},
		{"mention in title maps to actor", newArgs{Title: "fix login @bob due:fri"}, "fix login due:fri", "A_BOB", ""},
		{"unknown mention stays for tix", newArgs{Title: "fix @raj"}, "fix @raj", nil, ""},
		{"unlinked mention", newArgs{Title: "fix @carol"}, "", nil, "has not connected tix"},
		{"assign tix handle", newArgs{Title: "fix", Assign: "raj"}, "fix", "raj", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ft := newTestEnv(t)
			task, err := e.create(t.Context(), &tc.na)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "INFRA-9", task.Ref)
			c := ft.last(http.MethodPost, "/tasks")
			require.NotNil(t, c)
			assert.Equal(t, tc.wantTitle, c.Body["title"])
			assert.Equal(t, true, c.Body["parse"])
			assert.Equal(t, tc.wantAssig, c.Body["assignee_actor_id"])
		})
	}
	e, ft := newTestEnv(t)
	_, err := e.create(t.Context(), &newArgs{Title: "keep #1 @bob", Raw: true, Priority: 4})
	require.NoError(t, err)
	c := ft.last(http.MethodPost, "/tasks")
	assert.Equal(t, "keep #1 @bob", c.Body["title"])
	assert.NotContains(t, c.Body, "parse")
	assert.InDelta(t, 4, c.Body["priority"], 0)
}

func TestCard(t *testing.T) {
	e, _ := newTestEnv(t)
	due := testNow.Add(-24 * time.Hour)
	task := &tix.Task{Ref: "INFRA-1", Title: "Rotate\nkeys", Status: "doing", Priority: 2, DueAt: &due, Tags: []string{"ops", "sec"}, AssigneeActorID: "A_BOB"}
	got := e.card(task, testWorkflow(), "tasks.new.created")
	assert.Equal(t, "Created [INFRA-1](https://tix.test/tasks/INFRA-1) **Rotate keys**\n"+
		"**Status:** In progress · **Assignee:** @bob · **Priority:** high · **Due:** 16.11.2026 (overdue) · **Tags:** `ops`, `sec`", got)
}

func TestShow(t *testing.T) {
	e, ft := newTestEnv(t)
	ft.tasks["infra-1"].Body = "Line one\nLine two"
	for i, body := range []string{"first", "second", "third", "fourth"} {
		ft.comments["INFRA-1"] = append(ft.comments["INFRA-1"], tix.Comment{AuthorActorID: "A_BOB", Body: body, CreatedAt: testNow.Add(time.Duration(i-5) * time.Hour)})
	}
	got, err := e.show(t.Context(), "infra-1")
	require.NoError(t, err)
	assert.Contains(t, got, "#### [INFRA-1](https://tix.test/tasks/INFRA-1) **Rotate keys**")
	assert.Contains(t, got, "**Status:** To do · **Assignee:** @me · **Priority:** high")
	assert.Contains(t, got, "> Line one\n> Line two")
	assert.Contains(t, got, "**Comments** (last 3 of 4)")
	assert.NotContains(t, got, "first")
	assert.Contains(t, got, "- @bob, 2 h ago: fourth")
}

func TestList(t *testing.T) {
	e, ft := newTestEnv(t)
	done := testNow
	ft.listed = []tix.Task{
		{Ref: "INFRA-1", ProjectID: "p1", Status: "todo", Title: "Rotate keys", Priority: 2},
		{Ref: "INFRA-4", ProjectID: "p1", Status: "doing", Title: "Patch", AssigneeActorID: "A_BOB"},
		{Ref: "INFRA-5", ProjectID: "p1", Status: "review", Title: "Review me"},
		{Ref: "INFRA-3", ProjectID: "p1", Status: "done", Title: "Old", CompletedAt: &done},
	}
	q, err := parseList([]string{"infra"}, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	got, err := e.list(t.Context(), q)
	require.NoError(t, err)
	c := ft.last(http.MethodGet, "/tasks")
	require.NotNil(t, c)
	assert.Equal(t, []string{"done", "wontdo"}, c.Query["not_status"])
	assert.Equal(t, "infra", c.Query.Get("project"))
	assert.Equal(t, "#### Open tasks in INFRA: 3\n"+
		"**In progress** (1)\n- [INFRA-4](https://tix.test/tasks/INFRA-4) Patch · @bob\n"+
		"**In review** (1)\n- [INFRA-5](https://tix.test/tasks/INFRA-5) Review me\n"+
		"**To do** (1)\n- [INFRA-1](https://tix.test/tasks/INFRA-1) Rotate keys · !high", got)

	ft.listed = nil
	q, err = parseList(nil, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	got, err = e.list(t.Context(), q)
	require.NoError(t, err)
	assert.Equal(t, "You have no open tasks assigned to you in tix.", got)
}

func TestAssignAndComment(t *testing.T) {
	e, ft := newTestEnv(t)
	got, err := e.assign(t.Context(), "infra-2", "@bob")
	require.NoError(t, err)
	assert.Equal(t, "Assigned [INFRA-2](https://tix.test/tasks/INFRA-2) **Ship it** to @bob.", got)
	got, err = e.assign(t.Context(), "infra-2", "none")
	require.NoError(t, err)
	assert.Equal(t, "Unassigned [INFRA-2](https://tix.test/tasks/INFRA-2) **Ship it**.", got)
	assert.Equal(t, "", ft.last(http.MethodPatch, "/tasks/infra-2").Body["assignee_actor_id"])
	_, err = e.assign(t.Context(), "infra-2", "@carol")
	assert.ErrorIs(t, err, errUnlinkedUser)

	got, err = e.comment(t.Context(), "infra-2", "looks *good*")
	require.NoError(t, err)
	assert.Contains(t, got, "Commented on [INFRA-2]")
	assert.Equal(t, "looks *good*", ft.last(http.MethodPost, "/tasks/infra-2/comments").Body["body"])
	_, err = e.comment(t.Context(), "infra-2", "  ")
	assert.Error(t, err)
}

func TestAutocompleteRoutes(t *testing.T) {
	e, ft := newTestEnv(t)
	ft.listed = []tix.Task{{Ref: "INFRA-1", ProjectID: "p1", Status: "todo", Title: "Rotate keys"}}
	f := &Feature{envFor: func(string) (*env, error) { return e, nil }}

	get := func(h http.HandlerFunc, target, userID string) (int, []model.AutocompleteListItem) {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		if userID != "" {
			r.Header.Set("Mattermost-User-ID", userID)
		}
		w := httptest.NewRecorder()
		h(w, r)
		var items []model.AutocompleteListItem
		if w.Code == http.StatusOK {
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
		}
		return w.Code, items
	}

	for _, h := range []http.HandlerFunc{f.autocompleteRefs, f.autocompleteProjects, f.autocompleteStates} {
		code, _ := get(h, "/x", "")
		assert.Equal(t, http.StatusUnauthorized, code)
	}

	code, items := get(f.autocompleteRefs, "/x", "u1")
	assert.Equal(t, http.StatusOK, code)
	assert.Equal(t, []model.AutocompleteListItem{{Item: "INFRA-1", Hint: "Rotate keys", HelpText: "To do"}}, items)
	assert.Equal(t, "me", ft.last(http.MethodGet, "/tasks").Query.Get("creator"))

	_, items = get(f.autocompleteProjects, "/x?list=1", "u1")
	require.Len(t, items, 3)
	assert.Equal(t, "mine", items[0].Item)
	assert.Equal(t, "infra", items[2].Item)

	_, items = get(f.autocompleteStates, "/x?parsed="+url.QueryEscape("/tix move infra-2 "), "u1")
	assert.Equal(t, []model.AutocompleteListItem{
		{Item: "done", HelpText: "Done (needs a comment)"},
		{Item: "doing", HelpText: "In progress"},
	}, items)

	_, items = get(f.autocompleteStates, "/x?parsed="+url.QueryEscape("/tix move "), "u1")
	assert.Empty(t, items)

	notLinked := &Feature{envFor: func(string) (*env, error) { return nil, assert.AnError }}
	code, items = get(notLinked.autocompleteRefs, "/x", "u1")
	assert.Equal(t, http.StatusOK, code)
	assert.Empty(t, items)
}
