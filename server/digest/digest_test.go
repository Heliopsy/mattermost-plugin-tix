// SPDX-License-Identifier: AGPL-3.0-only

package digest

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	renderpkg "github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func TestParseClock(t *testing.T) {
	for in, want := range map[string]string{"9": "09:00", "08:30": "08:30", "7:05": "07:05", "23:59": "23:59"} {
		got, err := parseClock(in)
		require.NoError(t, err, in)
		assert.Equal(t, want, got, in)
	}
	for _, in := range []string{"24:00", "12:60", "noon", "1:2:3", "-1:00", "10:000"} {
		_, err := parseClock(in)
		assert.Error(t, err, in)
	}
}

func TestNextSlot(t *testing.T) {
	sofia := mustLoc(t, "Europe/Sofia")
	ny := mustLoc(t, "America/New_York")
	for _, tc := range []struct {
		name string
		now  time.Time
		loc  *time.Location
		at   string
		want time.Time
	}{
		{"later today", time.Date(2026, 11, 17, 6, 0, 0, 0, time.UTC), sofia, "09:00", time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)},
		{"exactly now is tomorrow", time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC), sofia, "09:00", time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC)},
		{"local date ahead of UTC", time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC), sofia, "08:00", time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)},
		{"local date behind UTC", time.Date(2026, 11, 18, 2, 0, 0, 0, time.UTC), ny, "22:00", time.Date(2026, 11, 18, 3, 0, 0, 0, time.UTC)},
		{"spring forward keeps the wall clock", time.Date(2026, 3, 28, 12, 0, 0, 0, time.UTC), sofia, "09:00", time.Date(2026, 3, 29, 6, 0, 0, 0, time.UTC)},
		{"day before spring forward", time.Date(2026, 3, 27, 12, 0, 0, 0, time.UTC), sofia, "09:00", time.Date(2026, 3, 28, 7, 0, 0, 0, time.UTC)},
		{"time skipped by spring forward", time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC), ny, "02:30", time.Date(2026, 3, 8, 7, 0, 0, 0, time.UTC)},
		{"time repeated by fall back takes the first", time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC), ny, "01:30", time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nextSlot(tc.now, tc.loc, tc.at)
			assert.True(t, tc.want.Equal(got), "got %s want %s", got.UTC(), tc.want)
		})
	}
}

func TestNextCheck(t *testing.T) {
	sofia := mustLoc(t, "Europe/Sofia")
	morning := time.Date(2026, 11, 18, 7, 0, 0, 0, time.UTC) // 09:00 Sofia
	for _, tc := range []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{"an hour on", time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC), time.Date(2026, 11, 17, 11, 0, 0, 0, time.UTC)},
		{"evening waits for the morning", time.Date(2026, 11, 17, 18, 30, 0, 0, time.UTC), time.Date(2026, 11, 18, 6, 0, 0, 0, time.UTC)},
		{"never after the morning run", time.Date(2026, 11, 18, 6, 30, 0, 0, time.UTC), morning},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := nextCheck(tc.now, sofia, morning)
			assert.True(t, tc.want.Equal(got), "got %s want %s", got.UTC(), tc.want)
		})
	}
}

func TestDigestDay(t *testing.T) {
	sat := time.Date(2026, 11, 21, 9, 0, 0, 0, time.UTC)
	mon := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	assert.False(t, digestDay(sat, false))
	assert.True(t, digestDay(sat, true))
	assert.True(t, digestDay(mon, false))
}

func TestClassify(t *testing.T) {
	sofia := mustLoc(t, "Europe/Sofia")
	now := time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC) // 09:00 Sofia
	items := []item{
		{t: tix.Task{ID: "a", Ref: "w-1", DueAt: new(now.Add(-time.Hour))}, label: "To do", category: "todo"},
		{t: tix.Task{ID: "b", Ref: "w-2", DueAt: new(time.Date(2026, 11, 17, 21, 30, 0, 0, time.UTC))}, label: "To do"}, // 23:30 Sofia
		{t: tix.Task{ID: "c", Ref: "w-3", DueAt: new(time.Date(2026, 11, 17, 22, 30, 0, 0, time.UTC))}, label: "To do"}, // 00:30 tomorrow
		{t: tix.Task{ID: "d", Ref: "w-4"}, label: "Doing", category: "in_progress"},
		{t: tix.Task{ID: "e", Ref: "w-5", Blocked: true}, label: "To do", category: "todo"},
		{t: tix.Task{ID: "f", Ref: "w-6"}, label: "Blocked", category: "blocked"},
		{t: tix.Task{ID: "g", Ref: "w-7"}, label: "Doing", category: "in_progress"},
	}
	refs := func(l []item) []string {
		var out []string
		for _, it := range l {
			out = append(out, it.t.Ref)
		}
		return out
	}

	s := classify(items, now, sofia, nil)
	assert.Equal(t, []string{"w-1"}, refs(s.Overdue))
	assert.Equal(t, []string{"w-2"}, refs(s.DueToday))
	assert.Empty(t, s.New, "nothing is new before the first digest")
	assert.Equal(t, []string{"w-5", "w-6"}, refs(s.Blocked))
	assert.Equal(t, []string{"w-4", "w-7"}, refs(s.InProgress))
	assert.Equal(t, 7, s.Open)
	assert.Equal(t, map[string]int{"To do": 4, "Doing": 2, "Blocked": 1}, s.Counts)

	s = classify(items, now, sofia, []string{"a", "b", "c", "d", "e", "f"})
	assert.Equal(t, []string{"w-7"}, refs(s.New))
	assert.Equal(t, []string{"w-4"}, refs(s.InProgress))

	assert.True(t, classify(nil, now, sofia, nil).empty(renderpkg.AllDigestSections))

	// A bare due date (midnight UTC) is the whole day: due today, not overdue, in any zone.
	la := mustLoc(t, "America/Los_Angeles")
	bare := []item{
		{t: tix.Task{ID: "h", Ref: "w-8", DueAt: new(time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC))}, label: "To do"},
		{t: tix.Task{ID: "i", Ref: "w-9", DueAt: new(time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC))}, label: "To do"},
	}
	s = classify(bare, now, sofia, nil)
	assert.Equal(t, []string{"w-8"}, refs(s.DueToday))
	assert.Equal(t, []string{"w-9"}, refs(s.Overdue))
	// 23:00 on the 16th in Los Angeles: the 17th has not begun there, the 16th is today.
	s = classify(bare, now, la, nil)
	assert.Equal(t, []string{"w-9"}, refs(s.DueToday))
	assert.Empty(t, s.Overdue)
}

// fakeKV is the plugin KV store in memory, expiring records on the test clock.
type fakeKV struct {
	mu   sync.Mutex
	data map[string][]byte
	exp  map[string]time.Time
	now  func() time.Time
}

func (k *fakeKV) get(key string) []byte {
	if e, ok := k.exp[key]; ok && !k.now().Before(e) {
		delete(k.data, key)
		delete(k.exp, key)
	}
	return k.data[key]
}

// testAPI answers KV calls from fakeKV and everything else from the plugintest mock.
type testAPI struct {
	*plugintest.API
	kv *fakeKV
}

func (a *testAPI) KVGet(key string) ([]byte, *model.AppError) {
	a.kv.mu.Lock()
	defer a.kv.mu.Unlock()
	return a.kv.get(key), nil
}

func (a *testAPI) KVSetWithOptions(key string, value []byte, o model.PluginKVSetOptions) (bool, *model.AppError) {
	a.kv.mu.Lock()
	defer a.kv.mu.Unlock()
	cur := a.kv.get(key)
	if o.Atomic && !bytes.Equal(cur, o.OldValue) {
		return false, nil
	}
	if value == nil {
		delete(a.kv.data, key)
		delete(a.kv.exp, key)
		return true, nil
	}
	a.kv.data[key] = value
	delete(a.kv.exp, key)
	if o.ExpireInSeconds > 0 {
		a.kv.exp[key] = a.kv.now().Add(time.Duration(o.ExpireInSeconds) * time.Second)
	}
	return true, nil
}

func (a *testAPI) KVList(page, count int) ([]string, *model.AppError) {
	a.kv.mu.Lock()
	defer a.kv.mu.Unlock()
	var keys []string
	for k := range a.kv.data {
		keys = append(keys, k)
	}
	if page*count >= len(keys) {
		return nil, nil
	}
	return keys[page*count : min(len(keys), (page+1)*count)], nil
}

func (a *testAPI) LogDebug(string, ...any)        {}
func (a *testAPI) LogInfo(string, ...any)         {}
func (a *testAPI) LogWarn(string, ...any)         {}
func (a *testAPI) LogError(string, ...any)        {}
func (a *testAPI) LogAuditRec(*model.AuditRecord) {}

const (
	userID  = "uuuuuuuuuuuuuuuuuuuuuuuuuu"
	actorID = "01ACTOR"
	botID   = "bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// fakeTix serves the bits of the tix API the digest reads.
type fakeTix struct {
	mu    sync.Mutex
	tasks []tix.Task
	fail  int // answer this status to /tasks when not 0
	calls int
	query []string
}

func (ft *fakeTix) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	switch r.URL.Path {
	case "/api/v1/projects":
		_, _ = w.Write([]byte(`{"items":[{"id":"p1","key":"w","workflow_id":"wf1"}]}`))
	case "/api/v1/workflows":
		_, _ = w.Write([]byte(`{"items":[{"id":"wf1","definition":{"initial":"todo","states":[
			{"key":"todo","label":"To do","category":"todo"},{"key":"doing","label":"Doing","category":"in_progress"},
			{"key":"blocked","label":"Blocked","category":"blocked"},{"key":"done","label":"Done","category":"done","terminal":true}]}}]}`))
	case "/api/v1/tasks":
		ft.calls++
		ft.query = append(ft.query, r.URL.RawQuery)
		if ft.fail != 0 {
			w.WriteHeader(ft.fail)
			_, _ = w.Write([]byte(`{"error":{"code":"x","message":"boom"}}`))
			return
		}
		q := r.URL.Query()
		var out []tix.Task
		for _, t := range ft.tasks {
			if t.AssigneeActorID != q.Get("assignee") || (t.Status == "done" && q.Get("not_status") == "done") {
				continue
			}
			if db := q.Get("due_before"); db != "" {
				d, _ := time.Parse(time.RFC3339, db)
				if t.DueAt == nil || !t.DueAt.Before(d) {
					continue
				}
			}
			out = append(out, t)
		}
		_ = json.NewEncoder(w).Encode(tix.Page[tix.Task]{Items: out})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (ft *fakeTix) set(tasks ...tix.Task) {
	ft.mu.Lock()
	ft.tasks = tasks
	ft.mu.Unlock()
}

type env struct {
	t     *testing.T
	api   *testAPI
	kv    *fakeKV
	app   *app.App
	tix   *fakeTix
	clock time.Time
	posts []string
	// locale is the person's Mattermost language ("" for the server default).
	locale string
}

func newEnv(t *testing.T, tz string, start time.Time) *env {
	e := &env{t: t, clock: start, tix: &fakeTix{}}
	srv := httptest.NewServer(e.tix)
	t.Cleanup(srv.Close)
	e.kv = &fakeKV{data: map[string][]byte{}, exp: map[string]time.Time{}, now: func() time.Time { return e.clock }}
	e.api = &testAPI{API: &plugintest.API{}, kv: e.kv}
	e.api.On("GetUser", userID).Return(func(string) (*model.User, *model.AppError) {
		return &model.User{Id: userID, Locale: e.locale, Timezone: model.StringMap{"useAutomaticTimezone": "true", "automaticTimezone": tz}}, nil
	})
	e.api.On("GetDirectChannel", userID, botID).Return(&model.Channel{Id: "dm"}, nil)
	e.api.On("GetConfig").Return(&model.Config{}).Maybe()
	e.api.On("CreatePost", mock.AnythingOfType("*model.Post")).Run(func(args mock.Arguments) {
		p := args.Get(0).(*model.Post)
		assert.Equal(t, botID, p.UserId)
		assert.Equal(t, "dm", p.ChannelId)
		e.posts = append(e.posts, p.Message)
	}).Return(&model.Post{}, nil)
	client := pluginapi.NewClient(e.api, nil)
	st := store.New(&client.KV, func() string { return "test-key" })
	require.NoError(t, st.SaveLink(&store.Link{UserID: userID, ActorID: actorID}, "tok"))
	e.app = app.New(e.api, client, st, botID, func() *app.Config { return &app.Config{TixURL: srv.URL} })
	return e
}

// feature is a fresh instance on the shared store, as after a plugin restart.
func (e *env) feature() *Feature {
	f := New(e.app)
	f.now = func() time.Time { return e.clock }
	return f
}

func (e *env) at(t time.Time) *env { e.clock = t; return e }

func (e *env) cmd(f *Feature, line string) string {
	fields := command.SplitArgs(line)
	c := &command.Ctx{App: e.app, Args: &model.CommandArgs{UserId: userID, Command: line}}
	var r *model.CommandResponse
	switch fields[1] {
	case "digest":
		r = f.digestCmd(c, fields[2:])
	case "reminders":
		r = f.remindersCmd(c, fields[2:])
	}
	return r.Text
}

func (e *env) take() []string {
	p := e.posts
	e.posts = nil
	return p
}

func task(id, status string, due *time.Time, prio int) tix.Task {
	return tix.Task{ID: id, Ref: "w-" + id, Title: "Task " + id, Status: status, ProjectID: "p1", AssigneeActorID: actorID, DueAt: due, Priority: prio}
}

// Tuesday 17 November 2026; Sofia is UTC+2.
var tue = time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)

func sofiaAt(day time.Time, h, m int) time.Time {
	return day.Add(time.Duration(h-2)*time.Hour + time.Duration(m)*time.Minute)
}

func TestDigestSchedule(t *testing.T) {
	e := newEnv(t, "Europe/Sofia", sofiaAt(tue, 7, 0))
	e.tix.set(
		task("1", "doing", nil, 1),
		task("2", "todo", new(sofiaAt(tue, 17, 0)), 3),
		task("3", "done", new(sofiaAt(tue, 1, 0)), 3),
	)
	f := e.feature()
	assert.Contains(t, e.cmd(f, "/tix digest on 08:30"), "**on**")

	f.Tick()
	e.at(sofiaAt(tue, 8, 29)).feature().Tick()
	assert.Empty(t, e.take(), "not before 08:30 Sofia")

	e.at(sofiaAt(tue, 8, 30)).feature().Tick()
	posts := e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "Due today (1)")
	assert.Contains(t, posts[0], "In progress (1)")
	assert.Contains(t, posts[0], "`P1` [w-1 Task 1](")
	assert.NotContains(t, posts[0], "w-3", "closed tasks are left out")
	assert.Contains(t, e.tix.query[0], "not_status=done")

	for _, at := range []time.Time{sofiaAt(tue, 8, 31), sofiaAt(tue, 12, 0)} {
		e.at(at).feature().Tick() // a restart: a new instance on the same store
	}
	assert.Empty(t, e.take(), "one digest a day")

	e.cmd(f, "/tix digest on 13:00")
	e.at(sofiaAt(tue, 13, 0)).feature().Tick()
	assert.Empty(t, e.take(), "moving the time does not send a second digest the same day")

	wed := tue.Add(24 * time.Hour)
	e.tix.set(task("1", "doing", nil, 1), task("4", "todo", nil, 2))
	e.at(sofiaAt(wed, 13, 0)).feature().Tick()
	posts = e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "Newly assigned (1)")
	assert.Contains(t, posts[0], "w-4")

	sat := tue.Add(4 * 24 * time.Hour)
	e.at(sofiaAt(sat, 13, 0)).feature().Tick()
	assert.Empty(t, e.take(), "no digest on Saturday by default")
	e.cmd(f, "/tix digest days daily")
	sun := sat.Add(24 * time.Hour)
	e.at(sofiaAt(sun, 13, 0)).feature().Tick()
	assert.Len(t, e.take(), 1, "daily includes Sunday")

	e.cmd(f, "/tix digest off")
	e.at(sofiaAt(sun.Add(24*time.Hour), 13, 0)).feature().Tick()
	assert.Empty(t, e.take())
	ids, err := f.index()
	require.NoError(t, err)
	assert.Empty(t, ids, "off for everything leaves the index")
}

func TestDigestAcrossDST(t *testing.T) {
	// Europe/Sofia moves to summer time on Sunday 29 March 2026.
	fri := time.Date(2026, 3, 27, 0, 0, 0, 0, time.UTC)
	e := newEnv(t, "Europe/Sofia", fri.Add(5*time.Hour)) // 07:00 local
	e.tix.set(task("1", "doing", nil, 3))
	f := e.feature()
	e.cmd(f, "/tix digest on 09:00")
	e.cmd(f, "/tix digest days daily")
	for day, utcHour := range []int{7, 7, 6, 6} { // Fri, Sat on UTC+2; Sun, Mon on UTC+3
		d := fri.Add(time.Duration(day) * 24 * time.Hour)
		e.at(d.Add(time.Duration(utcHour)*time.Hour - time.Minute)).feature().Tick()
		assert.Empty(t, e.take(), "day %d: a minute early", day)
		e.at(d.Add(time.Duration(utcHour) * time.Hour)).feature().Tick()
		assert.Len(t, e.take(), 1, "day %d: at 09:00 local", day)
	}
}

func TestQuietEmptyDigest(t *testing.T) {
	for _, tc := range []struct {
		name  string
		quiet bool
		want  int
	}{{"all clear", false, 1}, {"quiet skips", true, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "UTC", tue.Add(6*time.Hour))
			e.tix.set(task("1", "todo", nil, 3))
			f := e.feature()
			e.cmd(f, "/tix digest on 09:00")
			if tc.quiet {
				e.cmd(f, "/tix digest quiet on")
			}
			e.at(tue.Add(9 * time.Hour)).feature().Tick()
			posts := e.take()
			require.Len(t, posts, tc.want)
			if tc.want == 1 {
				assert.Contains(t, posts[0], "All clear")
				assert.Contains(t, posts[0], "1 To do")
			}
		})
	}
}

func TestReminders(t *testing.T) {
	e := newEnv(t, "UTC", tue.Add(6*time.Hour))
	e.tix.set(
		task("1", "todo", new(tue.Add(15*time.Hour)), 3),     // due today 15:00
		task("2", "todo", new(tue.Add(-20*time.Hour)), 2),    // overdue since yesterday
		task("3", "todo", new(tue.Add(-30*24*time.Hour)), 3), // long overdue: no reminder
		task("4", "todo", new(tue.Add(48*time.Hour)), 3),     // due later
	)
	f := e.feature()
	assert.Contains(t, e.cmd(f, "/tix reminders on"), "Due reminders are **on**")

	e.at(tue.Add(9 * time.Hour)).feature().Tick()
	posts := e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "**Due today**\n- [w-1")
	assert.Contains(t, posts[0], "**Now overdue**\n- `P2` [w-2")
	assert.NotContains(t, posts[0], "w-3")
	assert.NotContains(t, posts[0], "w-4")

	for h := 10; h <= 14; h++ {
		e.at(tue.Add(time.Duration(h) * time.Hour)).feature().Tick()
	}
	assert.Empty(t, e.take(), "nothing new until w-1 passes its due time")

	e.at(tue.Add(15*time.Hour + time.Minute)).feature().Tick()
	posts = e.take()
	require.Len(t, posts, 1, "the first check after 15:00 sees w-1 overdue")
	assert.Contains(t, posts[0], "**Now overdue**\n- [w-1")
	assert.NotContains(t, posts[0], "w-2", "w-2 was reminded already")

	for h := 16; h < 24+9; h++ {
		e.at(tue.Add(time.Duration(h) * time.Hour)).feature().Tick()
	}
	assert.Empty(t, e.take(), "never the same reminder twice, and no checks at night")

	// A new due date is a new reminder.
	wed := tue.Add(24 * time.Hour)
	e.tix.set(task("1", "todo", new(wed.Add(18*time.Hour)), 3))
	e.at(wed.Add(9 * time.Hour)).feature().Tick()
	posts = e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "**Due today**\n- [w-1")
}

func TestRemindersFoldIntoDigest(t *testing.T) {
	e := newEnv(t, "UTC", tue.Add(6*time.Hour))
	e.tix.set(task("1", "todo", new(tue.Add(15*time.Hour)), 3), task("2", "todo", new(tue.Add(-2*time.Hour)), 3))
	f := e.feature()
	e.cmd(f, "/tix digest on 09:00")
	e.cmd(f, "/tix reminders on")
	e.at(tue.Add(9 * time.Hour)).feature().Tick()
	posts := e.take()
	require.Len(t, posts, 1, "one DM: the digest carries the reminders")
	assert.Contains(t, posts[0], "Overdue (1)")
	assert.Contains(t, posts[0], "Due today (1)")
	assert.NotContains(t, posts[0], "Now overdue")

	e.at(tue.Add(10 * time.Hour)).feature().Tick()
	assert.Empty(t, e.take(), "w-2 was in the digest")
	e.at(tue.Add(16 * time.Hour)).feature().Tick()
	posts = e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "**Now overdue**\n- [w-1")
}

func TestMissedAndFailedRuns(t *testing.T) {
	t.Run("a run missed by more than the grace is skipped", func(t *testing.T) {
		e := newEnv(t, "UTC", tue.Add(6*time.Hour))
		e.tix.set(task("1", "doing", nil, 3))
		f := e.feature()
		e.cmd(f, "/tix digest on 09:00")
		e.at(tue.Add(13 * time.Hour)).feature().Tick()
		assert.Empty(t, e.take())
		r, _, err := f.load(userID)
		require.NoError(t, err)
		assert.True(t, r.NextRun.Equal(tue.Add(33*time.Hour)), "next run is tomorrow 09:00, got %s", r.NextRun)
	})
	t.Run("a late tick within the grace still sends", func(t *testing.T) {
		e := newEnv(t, "UTC", tue.Add(6*time.Hour))
		e.tix.set(task("1", "doing", nil, 3))
		f := e.feature()
		e.cmd(f, "/tix digest on 09:00")
		e.at(tue.Add(11 * time.Hour)).feature().Tick()
		assert.Len(t, e.take(), 1)
	})
	t.Run("a tix failure is retried", func(t *testing.T) {
		e := newEnv(t, "UTC", tue.Add(6*time.Hour))
		e.tix.set(task("1", "doing", nil, 3))
		f := e.feature()
		e.cmd(f, "/tix digest on 09:00")
		e.tix.fail = http.StatusBadGateway
		e.at(tue.Add(9 * time.Hour)).feature().Tick()
		e.at(tue.Add(9*time.Hour + time.Minute)).feature().Tick()
		assert.Empty(t, e.take())
		assert.Equal(t, 1, e.tix.calls, "no retry before the back-off")
		e.tix.fail = 0
		e.at(tue.Add(9*time.Hour + 5*time.Minute)).feature().Tick()
		assert.Len(t, e.take(), 1)
	})
	t.Run("a rejected token skips the day", func(t *testing.T) {
		e := newEnv(t, "UTC", tue.Add(6*time.Hour))
		f := e.feature()
		e.cmd(f, "/tix digest on 09:00")
		e.tix.fail = http.StatusUnauthorized
		e.at(tue.Add(9 * time.Hour)).feature().Tick()
		for m := 1; m < 60; m += 5 {
			e.at(tue.Add(9*time.Hour + time.Duration(m)*time.Minute)).feature().Tick()
		}
		assert.Equal(t, 1, e.tix.calls)
		assert.Empty(t, e.take())
	})
	t.Run("an unlinked person is dropped", func(t *testing.T) {
		e := newEnv(t, "UTC", tue.Add(6*time.Hour))
		f := e.feature()
		e.cmd(f, "/tix digest on 09:00")
		require.NoError(t, e.app.Store.DeleteLink(userID))
		e.at(tue.Add(9 * time.Hour)).feature().Tick()
		ids, err := f.index()
		require.NoError(t, err)
		assert.Empty(t, ids)
		assert.Empty(t, e.take())
	})
}

func TestCommands(t *testing.T) {
	e := newEnv(t, "Europe/Sofia", sofiaAt(tue, 7, 0))
	f := e.feature()
	assert.Contains(t, e.cmd(f, "/tix digest"), "digest is **off**")
	assert.Contains(t, e.cmd(f, "/tix digest on 25:00"), "not a time of day")
	out := e.cmd(f, "/tix digest on 7:45")
	assert.Contains(t, out, "Monday to Friday at 07:45 (Europe/Sofia)")
	assert.Contains(t, out, "Next run: 17.11.2026 07:45")
	assert.Contains(t, e.cmd(f, "/tix digest bogus"), "Usage")
	assert.Contains(t, e.cmd(f, "/tix reminders maybe"), "Usage")

	e.tix.set(task("1", "doing", nil, 3))
	assert.Contains(t, e.cmd(f, "/tix digest now"), "Sent")
	posts := e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "In progress (1)")

	require.NoError(t, e.app.Store.DeleteLink(userID))
	assert.True(t, strings.Contains(e.cmd(f, "/tix reminders on"), "/tix connect"), "linking is required")
}

func TestCommandsInBulgarian(t *testing.T) {
	e := newEnv(t, "Europe/Sofia", sofiaAt(tue, 7, 0))
	e.locale = "bg"
	f := e.feature()
	assert.Equal(t, "Обобщението ти от tix е **изключено**. `/tix digest on [HH:MM]` го включва.\n"+
		"Напомнянията са **изключени**. `/tix reminders on` ги включва.\n"+
		"Те са отделно от `/tix notifications`, което е за лични съобщения при промени по задачите ти.", e.cmd(f, "/tix digest"))
	assert.Equal(t, "`25:00` не е час от деня; използвай HH:MM, напр. 08:30", e.cmd(f, "/tix digest on 25:00"))
	out := e.cmd(f, "/tix digest on 7:45")
	assert.Contains(t, out, "Обобщението ти от tix е **включено**: от понеделник до петък в 07:45 (Europe/Sofia); празното казва „Всичко е наред“.")
	assert.Contains(t, out, "Следващо: 17.11.2026 07:45.")
	out = e.cmd(f, "/tix reminders on")
	assert.Contains(t, out, "Напомнянията са **включени**: за задачите със срок днес сутрин в 07:45, за просрочените между 08:00 и 21:00.")
	assert.Equal(t, "Употреба: `/tix reminders on|off|status`", e.cmd(f, "/tix reminders maybe"))
	e.tix.set(task("1", "doing", nil, 3))
	assert.Equal(t, "Изпратих обобщението ти в личните съобщения с @tix.", e.cmd(f, "/tix digest now"))
	posts := e.take()
	require.Len(t, posts, 1)
	assert.Contains(t, posts[0], "**В процес (1)**", "the digest itself is in the person's language")
}

// TestDigestSections: the DigestSections setting picks and orders the sections; a task in a
// section left out moves to the next listed one it fits.
func TestDigestSections(t *testing.T) {
	now := time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)
	yesterday := time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC)
	items := []item{
		{t: tix.Task{ID: "a", Ref: "w-1", Title: "Late and doing", DueAt: &yesterday}, label: "Doing", category: "in_progress"},
		{t: tix.Task{ID: "b", Ref: "w-2", Title: "Doing"}, label: "Doing", category: "in_progress"},
		{t: tix.Task{ID: "c", Ref: "w-3", Title: "Stuck"}, label: "Stuck", category: "blocked"},
	}
	s := classify(items, now, time.UTC, nil)
	f := &Feature{a: app.New(nil, nil, nil, botID, func() *app.Config { return &app.Config{TixURL: "https://tix.test"} })}
	render := func(sections []string, lang string) string {
		o := renderpkg.Defaults()
		o.DigestSections = sections
		return f.renderDigest(s, renderpkg.New(o, i18n.For(lang), time.UTC, now))
	}
	headings := func(text string) []string {
		var out []string
		for line := range strings.SplitSeq(text, "\n") {
			if strings.HasPrefix(line, "**") || strings.HasPrefix(line, "Open and") {
				out = append(out, line)
			}
		}
		return out
	}
	for _, tc := range []struct {
		name     string
		sections []string
		want     []string
	}{
		{"default", renderpkg.AllDigestSections, []string{"**Overdue (1)**", "**Blocked (1)**", "**In progress (1)**", "Open and assigned to you: 3 (2 Doing, 1 Stuck)"}},
		{"reordered, no counts", []string{renderpkg.SectionInProgress, renderpkg.SectionOverdue}, []string{"**In progress (2)**"}},
		{
			"overdue left out: the late task shows where it also fits",
			[]string{renderpkg.SectionBlocked, renderpkg.SectionInProgress, renderpkg.SectionCounts},
			[]string{"**Blocked (1)**", "**In progress (2)**", "Open and assigned to you: 3 (2 Doing, 1 Stuck)"},
		},
		{"only counts", []string{renderpkg.SectionCounts}, []string{"Open and assigned to you: 3 (2 Doing, 1 Stuck)"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, headings(render(tc.sections, "en")))
		})
	}
	assert.Contains(t, render([]string{renderpkg.SectionCounts}, "en"), "All clear", "nothing listed reads all clear")
	assert.True(t, s.empty([]string{renderpkg.SectionNew}))

	bg := render(renderpkg.AllDigestSections, "bg")
	assert.Contains(t, bg, "#### Твоето tix обобщение за вторник, 17.11.2026")
	assert.Contains(t, bg, "**Просрочени (1)**")
	assert.Contains(t, bg, "срок 16.11.2026")
	assert.Contains(t, bg, "Отворени и възложени на теб: 3")
}
