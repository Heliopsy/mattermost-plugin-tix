// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin/plugintest"
	"github.com/mattermost/mattermost/server/public/pluginapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// quick is a tasks feature on a mocked Mattermost: alice is a channel admin of ch, bob a
// member, carol a member without a tix link, and dave cannot read ch.
type quick struct {
	f                       *Feature
	ft                      *fakeTix
	ch, other               string
	alice, bob, carol, dave string
	emoji                   string

	mu         sync.Mutex
	kv         map[string][]byte
	posts      map[string]*model.Post
	created    []*model.Post
	ephemerals map[string][]string
}

func newQuick(t *testing.T) *quick {
	t.Helper()
	q := &quick{
		ch: model.NewId(), other: model.NewId(),
		alice: model.NewId(), bob: model.NewId(), carol: model.NewId(), dave: model.NewId(),
		kv: map[string][]byte{}, posts: map[string]*model.Post{}, ephemerals: map[string][]string{},
	}
	papi := &plugintest.API{}
	papi.On("KVGet", mock.Anything).Return(func(key string) ([]byte, *model.AppError) {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.kv[key], nil
	}).Maybe()
	papi.On("KVSetWithOptions", mock.Anything, mock.Anything, mock.Anything).Return(func(key string, v []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if o.Atomic && !bytes.Equal(o.OldValue, q.kv[key]) {
			return false, nil
		}
		if v == nil {
			delete(q.kv, key)
		} else {
			q.kv[key] = v
		}
		return true, nil
	}).Maybe()
	papi.On("LogAuditRec", mock.Anything).Maybe()
	site := "https://mm.test"
	papi.On("GetConfig").Return(&model.Config{ServiceSettings: model.ServiceSettings{SiteURL: &site}}).Maybe()
	names := map[string]string{q.alice: "alice", q.bob: "bob", q.carol: "carol", q.dave: "dave"}
	papi.On("GetUser", mock.Anything).Return(func(id string) (*model.User, *model.AppError) {
		if n, ok := names[id]; ok {
			return &model.User{Id: id, Username: n}, nil
		}
		return nil, model.NewAppError("GetUser", "not_found", nil, "", http.StatusNotFound)
	}).Maybe()
	papi.On("HasPermissionToChannel", mock.Anything, mock.Anything, mock.Anything).Return(func(uid, ch string, p *model.Permission) bool {
		if ch != q.ch && ch != q.other {
			return false
		}
		switch p.Id {
		case model.PermissionManageChannelRoles.Id:
			return uid == q.alice
		case model.PermissionReadChannelContent.Id:
			return uid != q.dave
		}
		return false
	}).Maybe()
	papi.On("GetPost", mock.Anything).Return(func(id string) (*model.Post, *model.AppError) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if p, ok := q.posts[id]; ok {
			return p.Clone(), nil
		}
		return nil, model.NewAppError("GetPost", "not_found", nil, "", http.StatusNotFound)
	}).Maybe()
	papi.On("CreatePost", mock.Anything).Return(func(p *model.Post) (*model.Post, *model.AppError) {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.created = append(q.created, p)
		return p, nil
	}).Maybe()
	papi.On("SendEphemeralPost", mock.Anything, mock.Anything).Return(func(uid string, p *model.Post) *model.Post {
		q.mu.Lock()
		defer q.mu.Unlock()
		q.ephemerals[uid] = append(q.ephemerals[uid], p.Message)
		return p
	}).Maybe()

	client := pluginapi.NewClient(papi, nil)
	st := store.New(&client.KV, func() string { return "test-key" })
	a := app.New(papi, client, st, "botid", func() *app.Config { return &app.Config{TixURL: "https://tix.test", CreateEmoji: q.emoji} })
	e, ft := newTestEnv(t)
	q.ft = ft
	q.f = New(a)
	q.f.envFor = func(uid string) (*env, error) {
		if uid == q.carol || uid == q.dave {
			return nil, app.ErrNotLinked
		}
		return e, nil
	}
	return q
}

func (q *quick) lastEphemeral(uid string) string {
	q.mu.Lock()
	defer q.mu.Unlock()
	if m := q.ephemerals[uid]; len(m) > 0 {
		return m[len(m)-1]
	}
	return ""
}

func (q *quick) creates() int {
	q.ft.mu.Lock()
	defer q.ft.mu.Unlock()
	n := 0
	for _, c := range q.ft.calls {
		if c.Method == http.MethodPost && c.Path == "/tasks" {
			n++
		}
	}
	return n
}

func (q *quick) cmd(uid, line string) string {
	fields := command.SplitArgs(line)
	c := &command.Ctx{Args: &model.CommandArgs{UserId: uid, ChannelId: q.ch, Command: line}}
	var r *model.CommandResponse
	switch fields[1] {
	case "channel":
		r = q.f.cmdChannel(c, fields[2:])
	case "new":
		r = q.f.cmdNew(c, fields[2:])
	}
	return r.Text
}

func TestChannelProject(t *testing.T) {
	q := newQuick(t)
	assert.Contains(t, q.cmd(q.bob, "/tix channel"), "no default tix project")

	assert.Contains(t, q.cmd(q.bob, "/tix channel project infra"), "only channel admins")
	assert.Empty(t, q.f.ChannelProject(q.alice, q.ch), "a refused change stores nothing")

	assert.Contains(t, q.cmd(q.alice, "/tix channel project nope"), "cannot see a tix project `nope`")
	assert.Contains(t, q.cmd(q.alice, "/tix channel project ../x"), "not a project key")
	assert.Contains(t, q.cmd(q.alice, "/tix channel project INFRA"), "go to **INFRA**")

	assert.Equal(t, "infra", q.f.ChannelProject(q.bob, q.ch))
	assert.Empty(t, q.f.ChannelProject(q.dave, q.ch), "only people who can read the channel see it")
	assert.Empty(t, q.f.ChannelProject(q.bob, q.other))
	assert.Empty(t, q.f.ChannelProject(q.bob, "bad"))
	assert.Contains(t, q.cmd(q.bob, "/tix channel"), "**INFRA**")

	assert.Contains(t, q.cmd(q.alice, "/tix channel project none"), "no longer has")
	assert.Empty(t, q.f.ChannelProject(q.bob, q.ch))
}

func TestNewUsesChannelDefault(t *testing.T) {
	q := newQuick(t)
	q.cmd(q.bob, "/tix new fix it")
	assert.NotContains(t, q.ft.last(http.MethodPost, "/tasks").Body, "project_ref", "no default, tix decides")

	q.cmd(q.alice, "/tix channel project infra")
	q.cmd(q.bob, "/tix new fix it")
	assert.Equal(t, "infra", q.ft.last(http.MethodPost, "/tasks").Body["project_ref"])
	assert.Equal(t, "fix it", q.ft.last(http.MethodPost, "/tasks").Body["title"])

	q.cmd(q.bob, "/tix new --project ops fix it")
	assert.Equal(t, "ops", q.ft.last(http.MethodPost, "/tasks").Body["project_ref"], "a named project wins")
}

func TestReactionCreatesTask(t *testing.T) {
	q := newQuick(t)
	post := &model.Post{Id: model.NewId(), UserId: q.carol, ChannelId: q.ch, Message: "\n  ## Login breaks on Safari  \nsteps: open, click"}
	q.posts[post.Id] = post
	react := func(uid, emoji string) {
		q.f.ReactionHasBeenAdded(nil, &model.Reaction{UserId: uid, PostId: post.Id, EmojiName: emoji})
	}

	react(q.bob, "ticket")
	assert.Contains(t, q.lastEphemeral(q.bob), "`/tix channel project <key>`")
	assert.Contains(t, q.lastEphemeral(q.bob), "**Create tix task**")
	assert.Zero(t, q.creates())

	q.cmd(q.alice, "/tix channel project infra")

	react(q.bob, "thumbsup")
	react(q.dave, "ticket")
	assert.Empty(t, q.lastEphemeral(q.dave), "people who cannot read the channel get nothing")
	react("botid", "ticket")
	assert.Zero(t, q.creates())

	react(q.carol, "ticket")
	assert.Contains(t, q.lastEphemeral(q.carol), "`/tix connect`")
	assert.Zero(t, q.creates(), "an unlinked reactor creates nothing")

	react(q.bob, "ticket")
	require.Equal(t, 1, q.creates())
	c := q.ft.last(http.MethodPost, "/tasks")
	assert.Equal(t, "infra", c.Body["project_ref"])
	assert.Equal(t, "Login breaks on Safari", c.Body["title"])
	assert.NotContains(t, c.Body, "parse", "a message is not a quick-add line")
	body := c.Body["body"].(string)
	assert.Contains(t, body, "steps: open, click")
	assert.Contains(t, body, "[a message by @carol](https://mm.test/_redirect/pl/"+post.Id+")")
	require.Len(t, q.created, 1)
	reply := q.created[0]
	assert.Equal(t, "botid", reply.UserId)
	assert.Equal(t, post.Id, reply.RootId)
	assert.Equal(t, "@bob created INFRA-9", reply.Message)
	assert.Equal(t, "INFRA-9", reply.GetProp(PropCreatedRef))

	// A second reaction, by anyone, links the task instead of making another.
	react(q.alice, "ticket")
	react(q.carol, "ticket")
	assert.Equal(t, 1, q.creates())
	assert.Contains(t, q.lastEphemeral(q.alice), "already tracked as [INFRA-9](https://tix.test/tasks/INFRA-9)")
	assert.Contains(t, q.lastEphemeral(q.carol), "already tracked as [INFRA-9]")
	assert.Len(t, q.created, 1)
}

func TestReactionClaim(t *testing.T) {
	q := newQuick(t)
	q.cmd(q.alice, "/tix channel project infra")
	post := &model.Post{Id: model.NewId(), UserId: q.carol, ChannelId: q.ch, RootId: model.NewId(), Message: "x"}
	q.posts[post.Id] = post

	// A fresh claim by a reaction still in flight blocks a second task.
	_, err := q.f.a.Store.Set(storeFeature, reactKey(post.Id, "ticket"), reactRecord{Pending: true, At: time.Now().UTC()})
	require.NoError(t, err)
	q.f.ReactionHasBeenAdded(nil, &model.Reaction{UserId: q.bob, PostId: post.Id, EmojiName: "ticket"})
	assert.Contains(t, q.lastEphemeral(q.bob), "being created")
	assert.Zero(t, q.creates())

	// A stale claim (the creator crashed) is taken over.
	_, err = q.f.a.Store.Set(storeFeature, reactKey(post.Id, "ticket"), reactRecord{Pending: true, At: time.Now().Add(-time.Hour).UTC()})
	require.NoError(t, err)
	q.f.ReactionHasBeenAdded(nil, &model.Reaction{UserId: q.bob, PostId: post.Id, EmojiName: "ticket"})
	assert.Equal(t, 1, q.creates())
	require.Len(t, q.created, 1)
	assert.Equal(t, post.RootId, q.created[0].RootId, "the reply goes to the thread's root")
}

func TestReactionCustomEmoji(t *testing.T) {
	q := newQuick(t)
	q.emoji = ":Memo:"
	q.cmd(q.alice, "/tix channel project infra")
	post := &model.Post{Id: model.NewId(), UserId: q.carol, ChannelId: q.ch, Message: "write it down"}
	q.posts[post.Id] = post
	q.f.ReactionHasBeenAdded(nil, &model.Reaction{UserId: q.bob, PostId: post.Id, EmojiName: "ticket"})
	assert.Zero(t, q.creates())
	q.f.ReactionHasBeenAdded(nil, &model.Reaction{UserId: q.bob, PostId: post.Id, EmojiName: "memo"})
	assert.Equal(t, 1, q.creates())

	q.emoji = "bad emoji!"
	assert.Equal(t, defaultCreateEmoji, q.f.createEmoji())
}

func TestReactTitle(t *testing.T) {
	long := strings.Repeat("ä", 200)
	assert.Equal(t, "Message from @x", reactTitle(enL, " \n\n", "@x"))
	assert.Equal(t, "Deploy now", reactTitle(enL, "> Deploy   now\nmore", "@x"))
	got := reactTitle(enL, long, "@x")
	assert.Len(t, []rune(got), maxReactTitle)
	assert.True(t, strings.HasSuffix(got, "…"))
}

func TestAutocompleteHintsChannelDefault(t *testing.T) {
	q := newQuick(t)
	q.cmd(q.alice, "/tix channel project infra")
	get := func(target string) []model.AutocompleteListItem {
		r := httptest.NewRequest(http.MethodGet, target, nil)
		r.Header.Set("Mattermost-User-ID", q.bob)
		w := httptest.NewRecorder()
		q.f.autocompleteProjects(w, r)
		var items []model.AutocompleteListItem
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &items))
		return items
	}
	items := get("/x?channel_id=" + q.ch)
	require.NotEmpty(t, items)
	assert.Equal(t, "infra", items[0].Item)
	assert.Contains(t, items[0].HelpText, "this channel's default")

	items = get("/x?channel=1&channel_id=" + q.other)
	assert.Equal(t, "Infra", items[0].HelpText)
	assert.Equal(t, "none", items[len(items)-1].Item)
}

func TestParseSearch(t *testing.T) {
	e, _ := newTestEnv(t)
	for _, tc := range []struct {
		name string
		args []string
		want url.Values
		open bool
		err  string
	}{
		{"text", []string{"login", "bug"}, url.Values{"q": {"login bug"}, "limit": {"15"}}, false, ""},
		{"project after text", []string{"login", "infra"}, url.Values{"q": {"login"}, "project": {"infra"}, "limit": {"15"}}, false, ""},
		{"first word stays text", []string{"infra", "keys"}, url.Values{"q": {"infra keys"}, "limit": {"15"}}, false, ""},
		{"filters and open", []string{"deploy", "tag:ops", "@bob", "open"}, url.Values{"q": {"deploy"}, "tag": {"ops"}, "assignee": {"A_BOB"}, "limit": {"15"}}, true, ""},
		{"no text", []string{"tag:ops"}, nil, false, "say what to look for"},
		{"bad term", []string{"x", "zzz:1"}, nil, false, "unknown filter term"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, err := parseSearch(tc.args, e.isProject, e.filterActor, testNow)
			if tc.err != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, q.Params)
			assert.Equal(t, tc.open, q.Open)
		})
	}
}

func TestSearchAndDue(t *testing.T) {
	e, ft := newTestEnv(t)
	e.tc.Me = "A_ME"
	done := testNow
	ft.listed = []tix.Task{
		{Ref: "INFRA-1", ProjectID: "p1", Status: "todo", Title: "Rotate keys", AssigneeActorID: "A_BOB"},
		{Ref: "INFRA-3", ProjectID: "p1", Status: "done", Title: "Old keys", CompletedAt: &done},
	}
	q, err := parseSearch([]string{"keys"}, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	got, err := e.flat(t.Context(), q, "none")
	require.NoError(t, err)
	assert.Empty(t, ft.last(http.MethodGet, "/tasks").Query["not_status"], "search includes finished tasks")
	assert.Equal(t, "#### Tasks matching “keys”: 2\n"+
		"- [INFRA-1](https://tix.test/tasks/INFRA-1) Rotate keys · To do · @bob\n"+
		"- [INFRA-3](https://tix.test/tasks/INFRA-3) Old keys · Done · nobody", got)

	q, err = parseSearch([]string{"keys", "open"}, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	got, err = e.flat(t.Context(), q, "none")
	require.NoError(t, err)
	assert.Equal(t, []string{"done", "wontdo"}, ft.last(http.MethodGet, "/tasks").Query["not_status"])
	assert.NotContains(t, got, "INFRA-3")

	q, err = parseDue(nil, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	assert.Equal(t, "me", q.Params.Get("assignee"))
	assert.Equal(t, "2026-11-17T23:59:59Z", q.Params.Get("due_before"))
	assert.Empty(t, q.Params.Get("due_after"), "overdue tasks are included")
	assert.Equal(t, "due_at", q.Params.Get("sort"))
	ft.listed = nil
	got, err = e.flat(t.Context(), q, "Nothing due.")
	require.NoError(t, err)
	assert.Equal(t, "Nothing due.", got)
	assert.Equal(t, "A_ME", ft.last(http.MethodGet, "/tasks").Query.Get("assignee"), "me goes to tix as the actor id")

	q, err = parseDue([]string{"infra"}, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	assert.Equal(t, "infra", q.Params.Get("project"))
}

// TestDueInTimeZone: a bare due date (midnight UTC) is a whole day in the caller's zone; tix's
// inclusive bound lets tomorrow's bare date through, and the calendar filter drops it.
func TestDueInTimeZone(t *testing.T) {
	sofia, err := time.LoadLocation("Europe/Sofia")
	require.NoError(t, err)
	e, ft := newTestEnv(t)
	now := time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC).In(sofia) // 09:00 in Sofia
	e.now = func() time.Time { return now }
	day := func(d int) *time.Time { v := time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC); return &v }
	ft.listed = []tix.Task{
		{Ref: "INFRA-1", ProjectID: "p1", Status: "todo", Title: "Due today", DueAt: day(17)},
		{Ref: "INFRA-2", ProjectID: "p1", Status: "todo", Title: "Due yesterday", DueAt: day(16)},
		{Ref: "INFRA-3", ProjectID: "p1", Status: "todo", Title: "Due tomorrow", DueAt: day(18)},
	}
	q, err := parseDue(nil, e.isProject, e.filterActor, now)
	require.NoError(t, err)
	assert.Equal(t, "2026-11-17T21:59:59Z", q.Params.Get("due_before"), "the end of the Sofia day")
	got, err := e.flat(t.Context(), q, "none")
	require.NoError(t, err)
	assert.Contains(t, got, "INFRA-1")
	assert.Contains(t, got, "INFRA-2")
	assert.NotContains(t, got, "INFRA-3")
	assert.Contains(t, got, "due 17.11.", "due today is not overdue")
	assert.NotContains(t, got, "**overdue** 17.11.")
	assert.Contains(t, got, "**overdue** 16.11.")

	q, err = parseList([]string{"due:overdue"}, e.isProject, e.filterActor, now)
	require.NoError(t, err)
	got, err = e.list(t.Context(), q)
	require.NoError(t, err)
	assert.Contains(t, got, "INFRA-2")
	assert.NotContains(t, got, "INFRA-1", "a bare date due today is not overdue")

	q, err = parseList([]string{"due:today"}, e.isProject, e.filterActor, now)
	require.NoError(t, err)
	got, err = e.list(t.Context(), q)
	require.NoError(t, err)
	assert.Contains(t, got, "INFRA-1")
	assert.NotContains(t, got, "INFRA-2")
	assert.NotContains(t, got, "INFRA-3")

	created := ft.listed[0]
	assert.Contains(t, e.card(&created, nil, "tasks.new.created"), "**Due:** 17.11.2026", "the reported bug")
	assert.NotContains(t, e.card(&created, nil, "tasks.new.created"), "overdue")

	// The date format setting reaches task commands.
	for format, want := range map[string]string{render.DateISO: "**Due:** 2026-11-17", render.DateRelative: "**Due:** today", render.DateEU: "**Due:** 17.11.2026"} {
		e.format = render.Defaults()
		e.format.DateFormat = format
		assert.Contains(t, e.card(&created, nil, ""), want, format)
	}
	e.format.DateFormat = render.DateRelative
	yesterday := ft.listed[1]
	assert.Contains(t, e.line(&yesterday, false), "**overdue** yesterday")
}

// TestBulgarian renders a few replies for a person whose Mattermost speaks Bulgarian.
func TestBulgarian(t *testing.T) {
	bg := i18n.For("bg")
	e, ft := newTestEnv(t)
	e.lang = bg
	e.mention = func(id string) string {
		if id == "" {
			return bg.T("app.nobody")
		}
		return "@" + strings.ToLower(strings.TrimPrefix(id, "A_"))
	}
	ft.comments["INFRA-1"] = []tix.Comment{{AuthorActorID: "A_BOB", Body: "готово", CreatedAt: testNow.Add(-3 * 24 * time.Hour)}}
	got, err := e.show(t.Context(), "infra-1")
	require.NoError(t, err)
	assert.Contains(t, got, "**Статус:** To do", "state labels stay as tix sends them")
	assert.Contains(t, got, "**Възложена на:** @me")
	assert.Contains(t, got, "**Приоритет:** висок")
	assert.Contains(t, got, "**Коментари**\n- @bob, преди 3 дни: готово")

	q, err := parseList([]string{"infra", "all"}, e.isProject, e.filterActor, testNow)
	require.NoError(t, err)
	assert.Equal(t, "Задачи в INFRA (вкл. приключените)", q.Title(bg))
	assert.Equal(t, "Tasks in INFRA (including finished)", q.Title(enL))

	_, err = needRef(nil, "/tix show <ref>")
	assert.Equal(t, "употреба: `/tix show <ref>`", app.ErrText(bg, err))
	_, err = parseNew([]string{"--priority", "urgent", "x"}, e.isProject)
	assert.Equal(t, "непознат приоритет `urgent`: използвай highest, high, normal, low, lowest или от 1 до 5", app.ErrText(bg, err))

	assert.Equal(t, "току-що", ago(bg, time.Second))
	assert.Equal(t, "преди 1 ч", ago(bg, time.Hour))
	assert.Equal(t, "преди 2 дни", ago(bg, 49*time.Hour))
	assert.Equal(t, "2 days ago", ago(enL, 49*time.Hour))
}
