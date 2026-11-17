// SPDX-License-Identifier: AGPL-3.0-only

package webapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/link"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
)

// fakeTix answers the tix API calls the webapi makes and records the last write.
type fakeTix struct {
	mu       sync.Mutex
	lastAuth string
	lastQ    url.Values
	lastBody map[string]any
	// fail, when set, answers every /tasks call with this status and tix error envelope.
	fail int
}

const workflowJSON = `{"items":[{"id":"wf1","key":"basic","name":"Basic","definition":{"initial":"todo",
"states":[{"key":"todo","label":"To do","category":"todo"},{"key":"doing","label":"Doing","category":"in_progress"},
{"key":"done","label":"Done","category":"done","terminal":true}],
"transitions":[{"from":"todo","to":"doing"},{"from":"doing","to":"done","requires_comment":true},{"from":"doing","to":"todo"}]}}]}`

func taskJSON(ref, status, assignee string) string {
	return fmt.Sprintf(`{"id":"t-%s","ref":%q,"title":"Fix login","body":"It breaks","status":%q,"priority":2,
"project_id":"p1","assignee_actor_id":%q,"tags":["auth"],"created_at":"2026-11-01T10:00:00Z","updated_at":"2026-11-01T10:00:00Z","version":3}`,
		ref, ref, status, assignee)
}

func (f *fakeTix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.lastAuth = r.Header.Get("Authorization")
	if strings.HasPrefix(r.URL.Path, "/api/v1/tasks") {
		f.lastQ = r.URL.Query()
		if r.Method != http.MethodGet {
			raw, _ := io.ReadAll(r.Body)
			f.lastBody = map[string]any{}
			_ = json.Unmarshal(raw, &f.lastBody)
		}
	}
	fail := f.fail
	f.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	if fail != 0 && strings.HasPrefix(r.URL.Path, "/api/v1/tasks") {
		w.WriteHeader(fail)
		_, _ = io.WriteString(w, `{"error":{"code":"invalid","message":"title is required"}}`)
		return
	}
	switch {
	case r.URL.Path == "/api/v1/projects":
		_, _ = io.WriteString(w, `{"items":[{"id":"p1","key":"infra","name":"Infra","workflow_id":"wf1"},{"id":"p2","key":"old","name":"Old","workflow_id":"wf1","archived_at":"2026-01-01T00:00:00Z"}]}`)
	case r.URL.Path == "/api/v1/workflows":
		_, _ = io.WriteString(w, workflowJSON)
	case r.URL.Path == "/api/v1/actors/act-dave":
		_, _ = io.WriteString(w, `{"id":"act-dave","handle":"dave","display_name":"Dave Agent","kind":"agent"}`)
	case r.URL.Path == "/api/v1/tasks" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"items":[`+taskJSON("infra-1", "doing", "act-bob")+`,`+taskJSON("infra-2", "todo", "act-dave")+`],"next_cursor":"c2"}`)
	case r.URL.Path == "/api/v1/tasks" && r.Method == http.MethodPost:
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, taskJSON("infra-3", "todo", fmt.Sprint(f.lastBody["assignee_actor_id"])))
	case r.URL.Path == "/api/v1/tasks/infra-1/comments" && r.Method == http.MethodGet:
		_, _ = io.WriteString(w, `{"items":[{"id":"c1","task_id":"t-infra-1","author_actor_id":"act-alice","body":"on it","created_at":"2026-11-01T11:00:00Z"}]}`)
	case r.URL.Path == "/api/v1/tasks/infra-1/comments":
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"c2","task_id":"t-infra-1","author_actor_id":"act-alice","body":"done soon","created_at":"2026-11-01T12:00:00Z"}`)
	case r.URL.Path == "/api/v1/tasks/infra-1/transition":
		_, _ = io.WriteString(w, taskJSON("infra-1", fmt.Sprint(f.lastBody["to"]), "act-bob"))
	case r.URL.Path == "/api/v1/tasks/infra-1":
		_, _ = io.WriteString(w, taskJSON("infra-1", "doing", "act-bob"))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"code":"not_found","message":"not found"}}`)
	}
}

type env struct {
	h                *api.Handler
	tix              *fakeTix
	alice, bob, carl string
	connectErr       error
}

// memKV backs the plugin KV store with a map.
func memKV(papi *plugintest.API) {
	var mu sync.Mutex
	kv := map[string][]byte{}
	papi.On("KVGet", mock.Anything).Return(func(key string) ([]byte, *model.AppError) {
		mu.Lock()
		defer mu.Unlock()
		return kv[key], nil
	}).Maybe()
	papi.On("KVSetWithOptions", mock.Anything, mock.Anything, mock.Anything).Return(func(key string, v []byte, _ model.PluginKVSetOptions) (bool, *model.AppError) {
		mu.Lock()
		defer mu.Unlock()
		if v == nil {
			delete(kv, key)
		} else {
			kv[key] = v
		}
		return true, nil
	}).Maybe()
}

// quietLogs accepts the log calls of the request logger, whatever their arity.
func quietLogs(papi *plugintest.API) {
	for _, m := range []string{"LogDebug", "LogInfo", "LogWarn", "LogError"} {
		for n := 1; n <= 21; n++ {
			args := make([]any, n)
			for i := range args {
				args[i] = mock.Anything
			}
			papi.On(m, args...).Maybe()
		}
	}
}

func setup(t *testing.T) *env {
	t.Helper()
	ft := &fakeTix{}
	srv := httptest.NewServer(ft)
	t.Cleanup(srv.Close)

	e := &env{tix: ft, alice: model.NewId(), bob: model.NewId(), carl: model.NewId()}
	papi := &plugintest.API{}
	memKV(papi)
	quietLogs(papi)
	papi.On("LogAuditRec", mock.Anything).Maybe()
	for id, name := range map[string]string{e.alice: "alice", e.bob: "bob", e.carl: "carl"} {
		papi.On("GetUser", id).Return(&model.User{Id: id, Username: name, FirstName: strings.ToUpper(name[:1]) + name[1:]}, nil).Maybe()
	}

	client := pluginapi.NewClient(papi, nil)
	st := store.New(&client.KV, func() string { return "test-key" })
	a := app.New(papi, client, st, "bot", func() *app.Config {
		return &app.Config{TixURL: srv.URL, EncryptionKey: "test-key", AdminToken: "admin"}
	})
	require.NoError(t, st.SaveLink(&store.Link{UserID: e.alice, ActorID: "act-alice", Handle: "alice", LinkedAt: time.Now()}, "tok-alice"))
	require.NoError(t, st.SaveLink(&store.Link{UserID: e.bob, ActorID: "act-bob", Handle: "bob", LinkedAt: time.Now()}, "tok-bob"))

	e.h = api.NewHandler(papi)
	New(a, func(string) (string, error) {
		if e.connectErr != nil {
			return "", e.connectErr
		}
		return "Connected", nil
	}).Routes(e.h)
	return e
}

func (e *env) do(t *testing.T, userID, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(t, err)
		rd = bytes.NewReader(b)
	}
	r := httptest.NewRequest(method, "/api/v1"+path, rd)
	if userID != "" {
		r.Header.Set("Mattermost-User-ID", userID)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(&plugin.Context{}, w, r)
	var out map[string]any
	if strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{") {
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
	}
	return w.Code, out
}

func TestRequiresMattermostUser(t *testing.T) {
	e := setup(t)
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/me"},
		{http.MethodPost, "/connect"},
		{http.MethodGet, "/projects"},
		{http.MethodGet, "/workflows"},
		{http.MethodGet, "/tasks"},
		{http.MethodPost, "/tasks"},
		{http.MethodGet, "/tasks/infra-1"},
		{http.MethodPatch, "/tasks/infra-1"},
		{http.MethodPost, "/tasks/infra-1/transition"},
		{http.MethodPost, "/tasks/infra-1/comments"},
	} {
		code, _ := e.do(t, "", tc.method, tc.path, map[string]string{})
		assert.Equal(t, http.StatusUnauthorized, code, "%s %s", tc.method, tc.path)
	}
}

func TestNotLinked(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, e.carl, http.MethodGet, "/me", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, false, body["linked"])
	assert.Equal(t, true, body["can_connect"])

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/tasks", nil},
		{http.MethodGet, "/projects", nil},
		{http.MethodGet, "/tasks/infra-1", nil},
		{http.MethodPost, "/tasks", map[string]any{"title": "x"}},
		{http.MethodPost, "/tasks/infra-1/comments", map[string]any{"body": "x"}},
	} {
		code, body := e.do(t, e.carl, tc.method, tc.path, tc.body)
		assert.Equal(t, http.StatusConflict, code, tc.path)
		assert.Equal(t, "not_linked", body["code"], tc.path)
	}
}

func TestMeLinked(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, e.alice, http.MethodGet, "/me", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, true, body["linked"])
	actor := body["actor"].(map[string]any)
	assert.Equal(t, "act-alice", actor["actor_id"])
	assert.Equal(t, "alice", actor["username"])
}

func TestConnect(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, e.carl, http.MethodPost, "/connect", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "Connected", body["message"])

	e.connectErr = fmt.Errorf("%w: unverified", link.ErrNoMatch)
	code, body = e.do(t, e.carl, http.MethodPost, "/connect", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "no_match", body["code"])

	e.connectErr = link.ErrNoAdminToken
	code, body = e.do(t, e.carl, http.MethodPost, "/connect", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "connect_unavailable", body["code"])
}

func TestListMineOpen(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, e.alice, http.MethodGet, "/tasks?mine=true&open=true&q=login", nil)
	require.Equal(t, http.StatusOK, code)

	assert.Equal(t, "Bearer tok-alice", e.tix.lastAuth)
	assert.Equal(t, "me", e.tix.lastQ.Get("assignee"))
	assert.Equal(t, []string{"done"}, e.tix.lastQ["not_status"])
	assert.Equal(t, "login", e.tix.lastQ.Get("q"))

	assert.Equal(t, "c2", body["next_cursor"])
	items := body["items"].([]any)
	require.Len(t, items, 2)
	first := items[0].(map[string]any)
	assert.Equal(t, "infra-1", first["ref"])
	assert.Equal(t, "infra", first["project_key"])
	assert.EqualValues(t, 2, first["priority"])
	assert.Equal(t, "in_progress", first["state"].(map[string]any)["category"])
	assert.Equal(t, "bob", first["assignee"].(map[string]any)["username"])
	// An unlinked actor is named from tix.
	assert.Equal(t, "Dave Agent", items[1].(map[string]any)["assignee"].(map[string]any)["name"])
}

func TestListProjectFilter(t *testing.T) {
	e := setup(t)
	code, _ := e.do(t, e.alice, http.MethodGet, "/tasks?project=INFRA&status=todo", nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, "infra", e.tix.lastQ.Get("project"))
	assert.Equal(t, []string{"todo"}, e.tix.lastQ["status"])

	code, _ = e.do(t, e.alice, http.MethodGet, "/tasks?project=nope", nil)
	assert.Equal(t, http.StatusNotFound, code)
}

func TestProjectsSkipArchived(t *testing.T) {
	e := setup(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	r.Header.Set("Mattermost-User-ID", e.alice)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(&plugin.Context{}, w, r)
	require.Equal(t, http.StatusOK, w.Code)
	var ps []ProjectView
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &ps))
	require.Len(t, ps, 1)
	assert.Equal(t, "infra", ps[0].Key)
}

func TestTaskDetail(t *testing.T) {
	e := setup(t)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/tasks/infra-1", nil)
	r.Header.Set("Mattermost-User-ID", e.alice)
	w := httptest.NewRecorder()
	e.h.ServeHTTP(&plugin.Context{}, w, r)
	require.Equal(t, http.StatusOK, w.Code)
	var d TaskDetail
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &d))
	assert.Equal(t, "Doing", d.Task.State.Label)
	require.Len(t, d.Comments, 1)
	assert.Equal(t, "alice", d.Comments[0].Author.Username)
	assert.Equal(t, []Move{
		{To: "done", Label: "Done", Category: "done", RequiresComment: true},
		{To: "todo", Label: "To do", Category: "todo"},
	}, d.Transitions)

	code, _ := e.do(t, e.alice, http.MethodGet, "/tasks/bad%20ref", nil)
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestCreateMapsAssignee(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, e.alice, http.MethodPost, "/tasks", map[string]any{
		"project": "infra", "title": " Fix login ", "body": "From a post", "priority": 2, "assignee_user_id": e.bob,
	})
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "infra-3", body["ref"])
	assert.Equal(t, map[string]any{
		"project_ref": "infra", "title": "Fix login", "body": "From a post", "priority": float64(2), "assignee_actor_id": "act-bob",
	}, e.tix.lastBody)

	code, body = e.do(t, e.alice, http.MethodPost, "/tasks", map[string]any{"title": "x", "assignee_user_id": e.carl})
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "assignee_not_linked", body["code"])

	for _, bad := range []map[string]any{
		{"title": ""},
		{"title": "x", "priority": 9},
		{"title": "x", "assignee_user_id": "not-an-id"},
		{"title": "x", "project": "a b"},
	} {
		code, _ = e.do(t, e.alice, http.MethodPost, "/tasks", bad)
		assert.Equal(t, http.StatusBadRequest, code, bad)
	}
}

func TestUpdate(t *testing.T) {
	e := setup(t)
	code, _ := e.do(t, e.alice, http.MethodPatch, "/tasks/infra-1", map[string]any{"assignee_user_id": "", "priority": 1})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"assignee_actor_id": "", "priority": float64(1)}, e.tix.lastBody)

	code, _ = e.do(t, e.alice, http.MethodPatch, "/tasks/infra-1", map[string]any{"assignee_user_id": e.alice})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"assignee_actor_id": "act-alice"}, e.tix.lastBody)

	code, _ = e.do(t, e.alice, http.MethodPatch, "/tasks/infra-1", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestTransitionAndComment(t *testing.T) {
	e := setup(t)
	code, body := e.do(t, e.alice, http.MethodPost, "/tasks/infra-1/transition", map[string]any{"to": "done", "comment": "shipped"})
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, map[string]any{"to": "done", "comment": "shipped"}, e.tix.lastBody)
	assert.Equal(t, true, body["state"].(map[string]any)["terminal"])

	code, _ = e.do(t, e.alice, http.MethodPost, "/tasks/infra-1/transition", map[string]any{})
	assert.Equal(t, http.StatusBadRequest, code)

	code, body = e.do(t, e.alice, http.MethodPost, "/tasks/infra-1/comments", map[string]any{"body": "done soon"})
	require.Equal(t, http.StatusCreated, code)
	assert.Equal(t, "done soon", body["body"])

	code, _ = e.do(t, e.alice, http.MethodPost, "/tasks/infra-1/comments", map[string]any{"body": "  "})
	assert.Equal(t, http.StatusBadRequest, code)
}

func TestTixErrors(t *testing.T) {
	e := setup(t)
	e.tix.fail = http.StatusUnprocessableEntity
	code, body := e.do(t, e.alice, http.MethodPost, "/tasks", map[string]any{"title": "x"})
	assert.Equal(t, http.StatusUnprocessableEntity, code)
	assert.Equal(t, "tix: title is required", body["error"])

	e.tix.fail = http.StatusUnauthorized
	code, body = e.do(t, e.alice, http.MethodGet, "/tasks/infra-1", nil)
	assert.Equal(t, http.StatusConflict, code)
	assert.Equal(t, "token_rejected", body["code"])

	e.tix.fail = http.StatusInternalServerError
	code, body = e.do(t, e.alice, http.MethodGet, "/tasks/infra-1", nil)
	assert.Equal(t, http.StatusBadGateway, code)
	assert.NotContains(t, body["error"], "title is required")
}
