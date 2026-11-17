// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

func TestExtractRefs(t *testing.T) {
	keys := []string{"infra", "INFRA2", "web"}
	for _, tc := range []struct {
		name, msg string
		want      []string
	}{
		{"plain", "please look at infra-42 today", []string{"infra-42"}},
		{"case-insensitive", "INFRA-7 and Web-3", []string{"infra-7", "web-3"}},
		{"longest key wins", "infra2-5", []string{"infra2-5"}},
		{"punctuation", "(infra-1), infra-2. infra-3!", []string{"infra-1", "infra-2", "infra-3"}},
		{"duplicates once", "infra-1 infra-1 INFRA-1", []string{"infra-1"}},
		{"cap", "infra-1 infra-2 infra-3 infra-4", []string{"infra-1", "infra-2", "infra-3"}},
		{"unknown project", "ops-12 and foo-1", nil},
		{"inline code", "run `tix show infra-9` now", nil},
		{"fenced code", "```\ninfra-9\n```\nbut infra-10", []string{"infra-10"}},
		{"unterminated fence", "```\ninfra-9", nil},
		{"url", "https://tix.example.com/tasks/infra-9 and www.x.com/infra-8", nil},
		{"markdown link target", "[the task](https://x/infra-9)", nil},
		{"glued to a word", "xinfra-1 infra-1x infra-1-2 a/infra-3 #infra-4 @infra-5 infra-6.txt", nil},
		{"sentence end", "done with infra-6.", []string{"infra-6"}},
		{"no number", "infra- infra-x", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractRefs(tc.msg, keys, maxCards))
		})
	}
	assert.Nil(t, extractRefs("infra-1", nil, maxCards))
}

// testWorkflow: todo -> doing|cancelled, doing -> review (needs a comment)|done, review -> done.
func testWorkflow() tix.Workflow {
	var w tix.Workflow
	w.ID, w.Key, w.Name = "w1", "basic", "Basic"
	w.Definition.Initial = "todo"
	w.Definition.States = []tix.State{
		{Key: "todo", Label: "To do"},
		{Key: "doing", Label: "In progress", Category: "in_progress"},
		{Key: "review", Label: "Review", Category: "waiting"},
		{Key: "stuck", Label: "Stuck", Category: "blocked"},
		{Key: "done", Label: "Done", Terminal: true},
		{Key: "cancelled", Label: "Cancelled", Category: "cancelled", Terminal: true},
	}
	w.Definition.Transitions = []tix.Transition{
		{From: "todo", To: "cancelled"},
		{From: "todo", To: "doing"},
		{From: "doing", To: "review", RequiresComment: true},
		{From: "doing", To: "done"},
		{From: "doing", To: "stuck"},
		{From: "doing", To: "todo"},
		{From: "review", To: "done"},
	}
	return w
}

func TestBuildCard(t *testing.T) {
	wf := testWorkflow()
	due := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		status, color, label string
		buttons              []string
	}{
		{"todo", "#8A8F98", "To do", []string{"In progress", "Cancelled", "Assign to me", "Comment"}},
		{"doing", "#1E88E5", "In progress", []string{"Done", "Review", "Stuck", "Assign to me", "Comment"}},
		{"review", "#F5A623", "Review", []string{"Done", "Assign to me", "Comment"}},
		{"stuck", "#F5A623", "Stuck", []string{"Assign to me", "Comment"}},
		{"done", "#2E7D32", "Done", []string{"Comment"}},
		{"cancelled", "#37474F", "Cancelled", []string{"Comment"}},
	} {
		t.Run(tc.status, func(t *testing.T) {
			tk := &task{Task: tix.Task{Ref: "infra-42", Title: "Fix login", Status: tc.status, Tags: []string{"auth", "web"}, DueAt: &due}, Priority: 2}
			c := buildCard(cardInput{
				Task: tk, Workflow: &wf, Project: &tix.Project{Name: "Infrastructure"},
				URL: "https://tix.example.com/tasks/infra-42", Assignee: "@alice", ActionURL: "https://mm/plugins/x/api/v1/actions/card", Index: 1,
			})
			assert.Equal(t, tc.color, c.Color)
			assert.Equal(t, "infra-42 · Fix login", c.Title)
			assert.Equal(t, "https://tix.example.com/tasks/infra-42", c.TitleLink)
			assert.Equal(t, "tix · Infrastructure", c.Footer)
			fields := map[string]any{}
			for _, f := range c.Fields {
				fields[f.Title] = f.Value
			}
			assert.Equal(t, map[string]any{"Status": tc.label, "Assignee": "@alice", "Priority": "High", "Due": "2026-11-20", "Tags": "auth, web"}, fields)
			var names []string
			for i, a := range c.Actions {
				names = append(names, a.Name)
				assert.Equal(t, "infra-42", a.Integration.Context["ref"])
				assert.Equal(t, "tixc1a"+string(rune('0'+i)), a.Id)
				assert.Equal(t, model.PostActionTypeButton, a.Type)
				require.NoError(t, a.IsValid())
			}
			assert.Equal(t, tc.buttons, names)
		})
	}
}

// --- a fake tix and a fake Mattermost ---

type fakeTix struct {
	mu          sync.Mutex
	tasks       map[string]*task
	visible     map[string]func(ref string) bool // token -> which tasks it sees
	actors      map[string]string                // token -> actor id
	transitions []map[string]any
	patches     []map[string]any
	comments    []string
	commenters  []string // token of each comment
}

func newFakeTix() *fakeTix {
	return &fakeTix{
		tasks: map[string]*task{
			"infra-1": {Task: tix.Task{ID: "t1", Ref: "infra-1", Title: "Fix login", Status: "todo", ProjectID: "p1", AssigneeActorID: "a1"}, Priority: 3},
			"infra-2": {Task: tix.Task{ID: "t2", Ref: "infra-2", Title: "Secret plan", Status: "doing", ProjectID: "p1"}, Priority: 1},
		},
		visible: map[string]func(string) bool{
			"tok-alice": func(string) bool { return true },
			"tok-bob":   func(ref string) bool { return ref == "infra-1" },
		},
		actors: map[string]string{"tok-alice": "a1", "tok-bob": "a2"},
	}
}

func (f *fakeTix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	see, ok := f.visible[tok]
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	writeJSON := func(v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1")
	switch {
	case path == "/projects":
		writeJSON(map[string]any{"items": []tix.Project{{ID: "p1", Key: "infra", Name: "Infrastructure", WorkflowID: "w1"}}})
	case path == "/workflows":
		writeJSON(map[string]any{"items": []tix.Workflow{testWorkflow()}})
	case strings.HasPrefix(path, "/actors/"):
		id := strings.TrimPrefix(path, "/actors/")
		writeJSON(tix.Actor{ID: id, DisplayName: "Actor " + id})
	case strings.HasPrefix(path, "/tasks/"):
		rest := strings.TrimPrefix(path, "/tasks/")
		ref, sub, _ := strings.Cut(rest, "/")
		t, exists := f.tasks[ref]
		if !exists || !see(ref) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"error":{"code":"not_found","message":"task not found"}}`))
			return
		}
		var in map[string]any
		_ = json.NewDecoder(r.Body).Decode(&in)
		switch {
		case r.Method == http.MethodGet && sub == "":
		case r.Method == http.MethodPatch && sub == "":
			f.patches = append(f.patches, in)
			t.AssigneeActorID, _ = in["assignee_actor_id"].(string)
		case r.Method == http.MethodPost && sub == "transition":
			f.transitions = append(f.transitions, in)
			t.Status, _ = in["to"].(string)
		case r.Method == http.MethodPost && sub == "comments":
			body, _ := in["body"].(string)
			f.comments = append(f.comments, body)
			f.commenters = append(f.commenters, tok)
			writeJSON(tix.Comment{ID: fmt.Sprintf("c%d", len(f.comments)), Body: body})
			return
		default:
			w.WriteHeader(http.StatusNotFound)
			return
		}
		// tix sends priority as a number; encode a map so the test catches a client that reads it as a string.
		writeJSON(map[string]any{"id": t.ID, "ref": t.Ref, "title": t.Title, "status": t.Status, "project_id": t.ProjectID, "assignee_actor_id": t.AssigneeActorID, "priority": t.Priority})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type env struct {
	t      *testing.T
	api    *plugintest.API
	app    *app.App
	f      *Feature
	tix    *fakeTix
	kv     map[string][]byte
	kvMu   sync.Mutex
	pmu    sync.Mutex
	posts  map[string]*model.Post
	alice  string // linked, sees everything
	bob    string // linked, sees infra-1 only
	carol  string // not linked
	unfurl bool
	style  string
	quiet  int
	// created are the posts the bot made, in order.
	created []*model.Post
	deleted []string
}

func newEnv(t *testing.T) *env {
	e := &env{
		t: t, api: &plugintest.API{}, tix: newFakeTix(), kv: map[string][]byte{}, posts: map[string]*model.Post{},
		alice: model.NewId(), bob: model.NewId(), carol: model.NewId(), unfurl: true, style: "inline",
	}
	srv := httptest.NewServer(e.tix)
	t.Cleanup(srv.Close)

	for _, lvl := range []string{"LogDebug", "LogInfo", "LogWarn", "LogError"} {
		for n := 1; n <= 15; n++ {
			args := make([]any, n)
			for i := range args {
				args[i] = mock.Anything
			}
			e.api.On(lvl, args...).Maybe()
		}
	}
	e.api.On("KVGet", mock.Anything).Return(func(key string) ([]byte, *model.AppError) {
		e.kvMu.Lock()
		defer e.kvMu.Unlock()
		return e.kv[key], nil
	}).Maybe()
	e.api.On("KVSetWithOptions", mock.Anything, mock.Anything, mock.Anything).Return(func(key string, v []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
		e.kvMu.Lock()
		defer e.kvMu.Unlock()
		if o.Atomic && !bytes.Equal(o.OldValue, e.kv[key]) {
			return false, nil
		}
		if v == nil {
			delete(e.kv, key)
		} else {
			e.kv[key] = v
		}
		return true, nil
	}).Maybe()
	e.api.On("KVDelete", mock.Anything).Return(func(key string) *model.AppError {
		e.kvMu.Lock()
		defer e.kvMu.Unlock()
		delete(e.kv, key)
		return nil
	}).Maybe()
	site := "https://mm.example.com"
	e.api.On("GetConfig").Return(&model.Config{ServiceSettings: model.ServiceSettings{SiteURL: &site}}).Maybe()
	e.api.On("GetUser", mock.Anything).Return(func(id string) (*model.User, *model.AppError) {
		if id == e.alice {
			return &model.User{Id: id, Username: "alice"}, nil
		}
		return nil, model.NewAppError("GetUser", "not_found", nil, "", http.StatusNotFound)
	}).Maybe()
	e.api.On("GetPost", mock.Anything).Return(func(id string) (*model.Post, *model.AppError) {
		e.pmu.Lock()
		defer e.pmu.Unlock()
		if p, ok := e.posts[id]; ok {
			return p.Clone(), nil
		}
		return nil, model.NewAppError("GetPost", "not_found", nil, "", http.StatusNotFound)
	}).Maybe()
	e.api.On("UpdatePost", mock.Anything).Return(func(p *model.Post) (*model.Post, *model.AppError) {
		e.pmu.Lock()
		defer e.pmu.Unlock()
		e.posts[p.Id] = p.Clone()
		return p, nil
	}).Maybe()
	e.api.On("LogAuditRec", mock.Anything).Maybe()
	e.api.On("CreatePost", mock.Anything).Return(func(p *model.Post) (*model.Post, *model.AppError) {
		p = e.save(p)
		e.pmu.Lock()
		e.created = append(e.created, p.Clone())
		e.pmu.Unlock()
		return p, nil
	}).Maybe()
	e.api.On("DeletePost", mock.Anything).Return(func(id string) *model.AppError {
		e.pmu.Lock()
		defer e.pmu.Unlock()
		if p, ok := e.posts[id]; ok {
			p.DeleteAt = 1
		}
		e.deleted = append(e.deleted, id)
		return nil
	}).Maybe()

	client := pluginapi.NewClient(e.api, nil)
	st := store.New(&client.KV, func() string { return "test-key" })
	e.app = app.New(e.api, client, st, "botid", func() *app.Config {
		return &app.Config{TixURL: srv.URL, UnfurlRefs: e.unfurl, CardStyle: e.style, CardQuietMinutes: e.quiet}
	})
	require.NoError(t, st.SaveLink(&store.Link{UserID: e.alice, ActorID: "a1"}, "tok-alice"))
	require.NoError(t, st.SaveLink(&store.Link{UserID: e.bob, ActorID: "a2"}, "tok-bob"))
	e.f = New(e.app)
	return e
}

// post runs a message through both hooks and stores it.
func (e *env) post(userID, msg string, props model.StringInterface) *model.Post {
	return e.postIn("", userID, msg, props)
}

// postIn posts a message in the thread rooted at root ("" for a new thread).
func (e *env) postIn(root, userID, msg string, props model.StringInterface) *model.Post {
	p := &model.Post{Id: model.NewId(), UserId: userID, ChannelId: "ch1", RootId: root, Message: msg}
	p.SetProps(props)
	p, rejected := e.f.MessageWillBePosted(&plugin.Context{}, p)
	require.Empty(e.t, rejected)
	saved := e.save(p)
	e.f.MessageHasBeenPosted(&plugin.Context{}, saved)
	return e.stored(saved.Id)
}

// save stores a post as the database would, props through JSON.
func (e *env) save(p *model.Post) *model.Post {
	if p.Id == "" {
		p.Id = model.NewId()
	}
	raw, err := json.Marshal(p)
	require.NoError(e.t, err)
	var saved model.Post
	require.NoError(e.t, json.Unmarshal(raw, &saved))
	e.pmu.Lock()
	e.posts[saved.Id] = &saved
	e.pmu.Unlock()
	return saved.Clone()
}

func (e *env) serve(userID, path string, body any) *httptest.ResponseRecorder {
	h := api.NewHandler(e.api)
	e.f.Routes(h)
	raw, err := json.Marshal(body)
	require.NoError(e.t, err)
	r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	if userID != "" {
		r.Header.Set("Mattermost-User-ID", userID)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(&plugin.Context{}, w, r)
	return w
}

func (e *env) click(userID string, post *model.Post, ctx map[string]any) model.PostActionIntegrationResponse {
	w := e.serve(userID, "/api/v1/actions/card", model.PostActionIntegrationRequest{UserId: userID, PostId: post.Id, ChannelId: post.ChannelId, TriggerId: "trigger", Context: ctx})
	require.Equal(e.t, http.StatusOK, w.Code, w.Body.String())
	var resp model.PostActionIntegrationResponse
	require.NoError(e.t, json.Unmarshal(w.Body.Bytes(), &resp))
	return resp
}

// stored returns the post as it is stored now.
func (e *env) stored(id string) *model.Post {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	if p, ok := e.posts[id]; ok {
		return p.Clone()
	}
	return &model.Post{}
}

func cardTitles(p *model.Post) []string {
	var out []string
	for _, a := range p.Attachments() {
		out = append(out, a.Title)
	}
	return out
}

func TestUnfurl(t *testing.T) {
	e := newEnv(t)

	p := e.post(e.alice, "infra-1 and INFRA-2, not `infra-3`, not ops-4", nil)
	assert.Equal(t, []string{"infra-1 · Fix login", "infra-2 · Secret plan"}, cardTitles(p))
	assert.Equal(t, []string{"infra-1", "infra-2"}, refsOf(p))
	cards, err := e.app.Store.Cards("infra-2")
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, p.Id, cards[0].PostID)
	a := p.Attachments()[0]
	assert.Equal(t, "@alice", a.Fields[1].Value, "assignee a1 is alice, linked")
	assert.Equal(t, "https://mm.example.com/plugins/"+app.PluginID+"/api/v1/actions/card", a.Actions[0].Integration.URL)

	// Bob cannot see infra-2: his message gets no card for it, whoever else could see it.
	p = e.post(e.bob, "infra-1 infra-2", nil)
	assert.Equal(t, []string{"infra-1 · Fix login"}, cardTitles(p))
	assert.Equal(t, []string{"infra-1"}, refsOf(p))

	// Carol is not connected: nothing.
	p = e.post(e.carol, "infra-1", nil)
	assert.Empty(t, p.Attachments())
	assert.Nil(t, p.GetProp(propRefs))

	// Unfurling off: nothing.
	e.unfurl = false
	p = e.post(e.alice, "infra-1", nil)
	assert.Empty(t, p.Attachments())
}

func TestUnfurlStripsForgedCards(t *testing.T) {
	e := newEnv(t)
	forged := []*model.MessageAttachment{
		{Text: "harmless"},
		{Title: "fake", Actions: []*model.PostAction{{Name: "Like", Integration: &model.PostActionIntegration{URL: "/plugins/" + app.PluginID + "/api/v1/actions/card", Context: map[string]any{"ref": "infra-2", "action": "transition", "to": "done"}}}}},
	}
	p := e.post(e.carol, "hello", model.StringInterface{"attachments": forged, propRefs: []string{"infra-2"}})
	require.Len(t, p.Attachments(), 1)
	assert.Equal(t, "harmless", p.Attachments()[0].Text)
	assert.Nil(t, p.GetProp(propRefs))
}

func TestActionDenied(t *testing.T) {
	e := newEnv(t)
	p := e.post(e.alice, "infra-1", nil)
	ctx := map[string]any{"ref": "infra-1", "action": "transition", "to": "doing"}

	// No session.
	w := e.serve("", "/api/v1/actions/card", model.PostActionIntegrationRequest{PostId: p.Id, Context: ctx})
	assert.Equal(t, http.StatusUnauthorized, w.Code)

	// A body claiming another user.
	w = e.serve(e.carol, "/api/v1/actions/card", model.PostActionIntegrationRequest{UserId: e.alice, PostId: p.Id, Context: ctx})
	assert.Equal(t, http.StatusBadRequest, w.Code)

	// Not connected to tix.
	e.api.On("HasPermissionToChannel", e.carol, "ch1", model.PermissionReadChannelContent).Return(true)
	resp := e.click(e.carol, p, ctx)
	assert.Contains(t, resp.EphemeralText, "/tix connect")
	assert.Nil(t, resp.Update)

	// Cannot read the channel.
	e.api.On("HasPermissionToChannel", e.bob, "ch1", model.PermissionReadChannelContent).Return(false)
	resp = e.click(e.bob, p, ctx)
	assert.Contains(t, resp.EphemeralText, "cannot see")

	// A ref the card does not show.
	e.api.On("HasPermissionToChannel", e.alice, "ch1", model.PermissionReadChannelContent).Return(true)
	resp = e.click(e.alice, p, map[string]any{"ref": "infra-2", "action": "transition", "to": "done"})
	assert.Contains(t, resp.EphemeralText, "out of date")

	assert.Empty(t, e.tix.transitions)
}

func TestActionTransitionAndAssign(t *testing.T) {
	e := newEnv(t)
	e.api.On("HasPermissionToChannel", mock.Anything, "ch1", model.PermissionReadChannelContent).Return(true)
	p := e.post(e.bob, "infra-1", nil)
	other := e.post(e.alice, "also infra-1", nil)

	// Alice moves the task on Bob's card, with her own token.
	resp := e.click(e.alice, p, map[string]any{"ref": "infra-1", "action": "transition", "to": "doing"})
	assert.Empty(t, resp.EphemeralText)
	require.Len(t, e.tix.transitions, 1)
	assert.Equal(t, map[string]any{"to": "doing"}, e.tix.transitions[0])
	require.NotNil(t, resp.Update)
	assert.Equal(t, "In progress", resp.Update.Attachments()[0].Fields[0].Value)
	assert.Equal(t, p.Message, resp.Update.Message)

	// The other card follows.
	require.Eventually(t, func() bool {
		q := e.stored(other.Id)
		return len(q.Attachments()) == 1 && q.Attachments()[0].Fields[0].Value == "In progress"
	}, 2*time.Second, 20*time.Millisecond)

	// Moves the card no longer offers are refused before reaching tix.
	resp = e.click(e.alice, p, map[string]any{"ref": "infra-1", "action": "transition", "to": "cancelled"})
	assert.Contains(t, resp.EphemeralText, "cannot move")
	assert.Len(t, e.tix.transitions, 1)

	// Bob takes it.
	resp = e.click(e.bob, p, map[string]any{"ref": "infra-1", "action": "assign"})
	assert.Empty(t, resp.EphemeralText)
	require.Len(t, e.tix.patches, 1)
	assert.Equal(t, map[string]any{"assignee_actor_id": "a2"}, e.tix.patches[0])
	resp = e.click(e.bob, p, map[string]any{"ref": "infra-1", "action": "assign"})
	assert.Contains(t, resp.EphemeralText, "already assigned to you")
}

func TestActionRequiresComment(t *testing.T) {
	e := newEnv(t)
	e.api.On("HasPermissionToChannel", e.alice, "ch1", model.PermissionReadChannelContent).Return(true)
	e.api.On("SendEphemeralPost", mock.Anything, mock.Anything).Return(&model.Post{}).Maybe()
	p := e.post(e.alice, "infra-2", nil) // infra-2 is in progress; review needs a comment

	var opened model.OpenDialogRequest
	e.api.On("OpenInteractiveDialog", mock.Anything).Run(func(args mock.Arguments) {
		opened = args.Get(0).(model.OpenDialogRequest)
	}).Return(nil)

	resp := e.click(e.alice, p, map[string]any{"ref": "infra-2", "action": "transition", "to": "review"})
	assert.Empty(t, resp.EphemeralText)
	assert.Empty(t, e.tix.transitions, "the move waits for the comment")
	assert.Equal(t, "trigger", opened.TriggerId)
	assert.Equal(t, "https://mm.example.com/plugins/"+app.PluginID+"/api/v1/dialogs/card", opened.URL)
	assert.Equal(t, "Move infra-2 to Review", opened.Dialog.Title)
	require.NoError(t, opened.Dialog.IsValid())

	submit := func(user, comment string) model.SubmitDialogResponse {
		w := e.serve(user, "/api/v1/dialogs/card", model.SubmitDialogRequest{UserId: user, ChannelId: "ch1", State: opened.Dialog.State, Submission: map[string]any{"comment": comment}})
		require.Equal(t, http.StatusOK, w.Code)
		var out model.SubmitDialogResponse
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	assert.NotEmpty(t, submit(e.alice, "  ").Errors["comment"])
	assert.Empty(t, e.tix.transitions)

	// A forged state for a post Bob cannot read is refused.
	e.api.On("HasPermissionToChannel", e.bob, "ch1", model.PermissionReadChannelContent).Return(false)
	assert.NotEmpty(t, submit(e.bob, "ready").Error)

	out := submit(e.alice, "ready for review")
	assert.Empty(t, out.Error)
	assert.Empty(t, out.Errors)
	require.Len(t, e.tix.transitions, 1)
	assert.Equal(t, map[string]any{"to": "review", "comment": "ready for review"}, e.tix.transitions[0])
	assert.Equal(t, "Review", e.stored(p.Id).Attachments()[0].Fields[0].Value)

	// Comment button: dialog, then a plain comment.
	resp = e.click(e.alice, p, map[string]any{"ref": "infra-2", "action": "comment"})
	assert.Empty(t, resp.EphemeralText)
	assert.Equal(t, "Comment on infra-2", opened.Dialog.Title)
	assert.Empty(t, submit(e.alice, "looks good").Error)
	assert.Equal(t, []string{"looks good"}, e.tix.comments)
}

func TestRefresh(t *testing.T) {
	e := newEnv(t)
	pa := e.post(e.alice, "infra-1 infra-2", nil)
	pb := e.post(e.bob, "infra-1", nil)

	e.tix.mu.Lock()
	e.tix.tasks["infra-1"].Status = "done"
	// Alice loses sight of infra-2: its card goes away rather than going stale.
	e.tix.visible["tok-alice"] = func(ref string) bool { return ref == "infra-1" }
	e.tix.mu.Unlock()

	e.f.Refresh("INFRA-1")
	assert.Equal(t, "Done", e.stored(pa.Id).Attachments()[0].Fields[0].Value)
	assert.Equal(t, []string{"infra-1 · Fix login"}, cardTitles(e.stored(pa.Id)))
	assert.Equal(t, []string{"infra-1"}, refsOf(e.stored(pa.Id)))
	assert.Equal(t, "Done", e.stored(pb.Id).Attachments()[0].Fields[0].Value)

	// A deleted post is forgotten.
	e.pmu.Lock()
	delete(e.posts, pb.Id)
	e.pmu.Unlock()
	e.f.Refresh("infra-1")
	cards, err := e.app.Store.Cards("infra-1")
	require.NoError(t, err)
	require.Len(t, cards, 1)
	assert.Equal(t, pa.Id, cards[0].PostID)
}
