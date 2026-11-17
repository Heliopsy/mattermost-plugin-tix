// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
)

// Deliveries captured from tix 0.56 serve (`tix webhook` to an httptest receiver).
const (
	payloadCreated      = `{"seq":16,"id":"01M7MBT4GM151SA6PZ9QA3ZNTN","tenant_id":"01M7MBR9AQWQ43NH4FYFENX95Q","type":"task.created","project_id":"01M7MBR9ATS3EPKVNDNVWJR8AR","subject_type":"task","subject_id":"01M7MBT4GMXQ53EAKP7KY5DD13","actor_id":"01M7MBR9AX5ZP13SENM97C9541","payload":{"actor_handle":"local","ref":"work-2","status":"todo","title":"Fix [backups]"},"via":{"kind":"api","token_id":"01M7MBRR7CHYDXZVZVTC6TC9GJ","token_name":"local"},"occurred_at":"2026-11-17T09:01:07.475701091Z"}`
	payloadAssigned     = `{"seq":18,"id":"01M7MBT4GR2RQ6HV221QCX9KSH","tenant_id":"01M7MBR9AQWQ43NH4FYFENX95Q","type":"task.updated","project_id":"01M7MBR9ATS3EPKVNDNVWJR8AR","subject_type":"task","subject_id":"01M7MBT4GMXQ53EAKP7KY5DD13","actor_id":"01M7MBR9AX5ZP13SENM97C9541","payload":{"actor_handle":"local","fields":["assignee_actor_id"],"ref":"work-2","version":2},"via":{"kind":"api","token_id":"01M7MBRR7CHYDXZVZVTC6TC9GJ","token_name":"local"},"occurred_at":"2026-11-17T09:01:07.478494147Z"}`
	payloadTransitioned = `{"seq":20,"id":"01M7MBT4GWFV11AJ4WTSVQ5ZM8","tenant_id":"01M7MBR9AQWQ43NH4FYFENX95Q","type":"task.transitioned","project_id":"01M7MBR9ATS3EPKVNDNVWJR8AR","subject_type":"task","subject_id":"01M7MBT4GMXQ53EAKP7KY5DD13","actor_id":"01M7MBR9AX5ZP13SENM97C9541","payload":{"actor_handle":"local","from":"todo","ref":"work-2","to":"doing"},"via":{"kind":"api","token_id":"01M7MBRR7CHYDXZVZVTC6TC9GJ","token_name":"local"},"occurred_at":"2026-11-17T09:01:07.483449184Z"}`
	payloadCommented    = `{"seq":22,"id":"01M7MBT4GY44ZJX35024KPQG5Z","tenant_id":"01M7MBR9AQWQ43NH4FYFENX95Q","type":"comment.added","project_id":"01M7MBR9ATS3EPKVNDNVWJR8AR","subject_type":"comment","subject_id":"01M7MBT4GXXAMEDZ9TPAJNFZBW","actor_id":"01M7MBR9AX5ZP13SENM97C9541","payload":{"actor_handle":"local","ref":"work-2","task_id":"01M7MBT4GMXQ53EAKP7KY5DD13"},"via":{"kind":"api","token_id":"01M7MBRR7CHYDXZVZVTC6TC9GJ","token_name":"local"},"occurred_at":"2026-11-17T09:01:07.48544256Z"}`
	payloadDeleted      = `{"seq":24,"id":"01M7MBT4H1J5HREM5ZJP2SQQAC","tenant_id":"01M7MBR9AQWQ43NH4FYFENX95Q","type":"task.deleted","project_id":"01M7MBR9ATS3EPKVNDNVWJR8AR","subject_type":"task","subject_id":"01M7MBT4GMXQ53EAKP7KY5DD13","actor_id":"01M7MBR9AX5ZP13SENM97C9541","payload":{"actor_handle":"local","cascade":false,"hard":false,"ref":"work-2"},"via":{"kind":"api","token_id":"01M7MBRR7CHYDXZVZVTC6TC9GJ","token_name":"local"},"occurred_at":"2026-11-17T09:01:07.488945761Z"}`
	// One real delivery with tix's own headers, to check the scheme end to end.
	capturedBody      = `{"seq":35,"id":"01M7MBWKFWAA09BMEGV54AA3HR","tenant_id":"01M7MBR9AQWQ43NH4FYFENX95Q","type":"task.created","project_id":"01M7MBR9ATS3EPKVNDNVWJR8AR","subject_type":"task","subject_id":"01M7MBWKFV5FAHETDY8X863XG9","actor_id":"01M7MBR9AX5ZP13SENM97C9541","payload":{"actor_handle":"local","ref":"work-3","status":"todo","title":"Fix [backups]"},"via":{"kind":"api","token_id":"01M7MBRR7CHYDXZVZVTC6TC9GJ","token_name":"local"},"occurred_at":"2026-11-17T09:02:28.346961023Z"}`
	capturedTimestamp = "2026-11-17T09:02:28.7120721Z"
	capturedSignature = "sha256=bf86ac033e44af8702089b2724ac2ae1f3c001946e42fdbac02696cfd04a6567"
	capturedSecret    = "s3cret-for-live-check"
)

const (
	actorLocal = "01M7MBR9AX5ZP13SENM97C9541"
	actorBob   = "01M7MBRVWANKBH0G9PEB4C4QKT"
)

func mustEvent(t *testing.T, raw string) *Event {
	t.Helper()
	ev, err := parseEvent([]byte(raw))
	require.NoError(t, err)
	return ev
}

func TestVerify(t *testing.T) {
	body := []byte(capturedBody)
	ts := capturedTimestamp
	at, _ := time.Parse(time.RFC3339Nano, ts)
	good := capturedSignature

	for _, tc := range []struct {
		name    string
		secret  string
		ts      string
		sig     string
		body    []byte
		now     time.Time
		wantErr error
	}{
		{"valid", capturedSecret, ts, good, body, at.Add(2 * time.Second), nil},
		{"wrong secret", "other", ts, good, body, at, errSignature},
		{"tampered body", capturedSecret, ts, good, bytes.Replace(body, []byte("work-3"), []byte("work-4"), 1), at, errSignature},
		{"timestamp swapped", capturedSecret, "2026-11-17T09:01:08Z", good, body, at, errSignature},
		{"stale", capturedSecret, ts, good, body, at.Add(maxSkew + time.Second), errStale},
		{"from the future", capturedSecret, ts, good, body, at.Add(-maxSkew - time.Second), errStale},
		{"no secret configured", "", ts, sign("", ts, body), body, at, errNoSecret},
		{"missing signature", capturedSecret, ts, "", body, at, errSignature},
		{"bare hex without prefix", capturedSecret, ts, strings.TrimPrefix(good, signaturePrefix), body, at, errSignature},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, verify(tc.secret, tc.ts, tc.sig, tc.body, tc.now), tc.wantErr)
		})
	}
}

// TestSignMatchesTix recomputes the signature tix sent with a real delivery.
func TestSignMatchesTix(t *testing.T) {
	assert.Equal(t, capturedSignature, sign(capturedSecret, capturedTimestamp, []byte(capturedBody)))
}

func TestParseEvent(t *testing.T) {
	ev := mustEvent(t, payloadTransitioned)
	assert.Equal(t, "task.transitioned", ev.Type)
	assert.Equal(t, "01M7MBT4GWFV11AJ4WTSVQ5ZM8", ev.ID)
	assert.Equal(t, actorLocal, ev.ActorID)
	assert.Equal(t, "work-2", ev.Ref())
	assert.Equal(t, "todo", ev.Str("from"))
	assert.Equal(t, "doing", ev.Str("to"))

	ev = mustEvent(t, payloadAssigned)
	assert.Equal(t, []string{"assignee_actor_id"}, ev.Strings("fields"))

	ev = mustEvent(t, payloadCommented)
	assert.Equal(t, "01M7MBT4GXXAMEDZ9TPAJNFZBW", ev.SubjectID)
	assert.False(t, ev.Bool("edited"))

	_, err := parseEvent([]byte(`{"seq":1}`))
	assert.Error(t, err)
	_, err = parseEvent([]byte(`not json`))
	assert.Error(t, err)
}

func TestKinds(t *testing.T) {
	doing := state{Key: "doing", Category: "in_progress"}
	done := state{Key: "done", Label: "Done", Category: "done"}
	cancelled := state{Key: "cancelled", Category: "cancelled"}
	for _, tc := range []struct {
		name string
		raw  string
		in   info
		want []string
	}{
		{"created", payloadCreated, info{}, []string{kindCreated}},
		{"assigned", payloadAssigned, info{}, []string{kindAssigned}},
		{"moved", payloadTransitioned, info{To: doing}, []string{kindTransitioned}},
		{"moved to done", payloadTransitioned, info{To: done}, []string{kindTransitioned, kindDone}},
		{"cancelled", payloadTransitioned, info{To: cancelled}, []string{kindTransitioned, kindDone}},
		{"done by key without a workflow", payloadTransitioned, info{To: state{Key: "done"}}, []string{kindTransitioned, kindDone}},
		{"commented", payloadCommented, info{}, []string{kindCommented}},
		{"comment edited", payloadCommented, info{Edited: true}, nil},
		{"deleted", payloadDeleted, info{}, []string{kindDeleted}},
		{"other update", strings.Replace(payloadAssigned, `"assignee_actor_id"`, `"title"`, 1), info{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.in
			assert.Equal(t, tc.want, kinds(mustEvent(t, tc.raw), &in))
		})
	}
}

func TestMatchChannels(t *testing.T) {
	subs := []store.Subscription{
		{ChannelID: "ch-work", Project: "WORK", Events: []string{kindCreated, kindTransitioned, kindCommented}},
		{ChannelID: "ch-done", Project: "work", Events: []string{kindDone}},
		{ChannelID: "ch-all", Project: "", Events: []string{kindDeleted, kindTransitioned}},
		{ChannelID: "ch-other", Project: "infra", Events: []string{kindCreated, kindTransitioned}},
		{ChannelID: "ch-work", Project: "", Events: []string{kindTransitioned}},
	}
	assert.Equal(t, []string{"ch-work"}, matchChannels(subs, "work", []string{kindCreated}))
	assert.Equal(t, []string{"ch-work", "ch-all"}, matchChannels(subs, "work", []string{kindTransitioned}))
	assert.Equal(t, []string{"ch-work", "ch-done", "ch-all"}, matchChannels(subs, "work", []string{kindTransitioned, kindDone}))
	assert.Equal(t, []string{"ch-all"}, matchChannels(subs, "work", []string{kindDeleted}))
	assert.Equal(t, []string{"ch-all", "ch-other", "ch-work"}, matchChannels(subs, "INFRA", []string{kindTransitioned}))
	assert.Nil(t, matchChannels(subs, "work", []string{kindAssigned}))
	assert.Nil(t, matchChannels(subs, "work", nil))
}

func TestParseKinds(t *testing.T) {
	ks, err := parseKinds(nil)
	require.NoError(t, err)
	assert.Equal(t, defaultKinds, ks)
	ks, err = parseKinds([]string{"closed,comment", "assigned", "done"})
	require.NoError(t, err)
	assert.Equal(t, []string{kindAssigned, kindCommented, kindDone}, ks)
	ks, err = parseKinds([]string{"all"})
	require.NoError(t, err)
	assert.Equal(t, allKinds, ks)
	_, err = parseKinds([]string{"starred"})
	assert.Error(t, err)
}

func TestMentions(t *testing.T) {
	assert.Equal(t, []string{"bob", "ada.l"}, mentions("ping @bob and @Ada.L. mail me at x@example.com, @bob again"))
	assert.Nil(t, mentions("no one here"))
}

// testDir is a directory of linked people: local -> u-local, bob -> u-bob.
func testDir(off map[string]bool, visible map[string]bool) directory {
	users := map[string]string{actorLocal: "u-local", actorBob: "u-bob"}
	handles := map[string]string{"local": actorLocal, "bob": actorBob}
	return directory{
		userForActor:  func(id string) string { return users[id] },
		userForHandle: func(h string) (string, string) { return users[handles[h]], handles[h] },
		dmsOff:        func(uid string) bool { return off[uid] },
		canSee:        func(uid, _ string) bool { return visible == nil || visible[uid] },
	}
}

func TestRecipients(t *testing.T) {
	assigned := mustEvent(t, payloadAssigned)
	commented := mustEvent(t, payloadCommented)
	moved := mustEvent(t, payloadTransitioned)

	t.Run("assignee hears about it", func(t *testing.T) {
		in := &info{Ref: "work-2", AssigneeID: actorBob, CreatorID: actorLocal}
		got := recipients(assigned, in, targets(assigned, in), testDir(nil, nil))
		assert.Equal(t, []recipient{{userID: "u-bob", reason: reasonAssigned}}, got)
	})
	t.Run("no DM about your own action", func(t *testing.T) {
		in := &info{Ref: "work-2", AssigneeID: actorLocal, CreatorID: actorLocal}
		assert.Empty(t, recipients(assigned, in, targets(assigned, in), testDir(nil, nil)))
		in.Comment = "note to self @local"
		assert.Empty(t, recipients(commented, in, targets(commented, in), testDir(nil, nil)))
	})
	t.Run("prefs off", func(t *testing.T) {
		in := &info{Ref: "work-2", AssigneeID: actorBob, CreatorID: actorLocal}
		assert.Empty(t, recipients(assigned, in, targets(assigned, in), testDir(map[string]bool{"u-bob": true}, nil)))
	})
	t.Run("mention beats comment, one DM each", func(t *testing.T) {
		in := &info{Ref: "work-2", AssigneeID: actorBob, CreatorID: actorBob, Comment: "looks good @bob"}
		got := recipients(commented, in, targets(commented, in), testDir(nil, nil))
		assert.Equal(t, []recipient{{userID: "u-bob", reason: reasonMentioned}}, got)
	})
	t.Run("mention needs the task to be visible", func(t *testing.T) {
		in := &info{Ref: "work-2", Comment: "fyi @bob"}
		assert.Empty(t, recipients(commented, in, targets(commented, in), testDir(nil, map[string]bool{})))
	})
	t.Run("unlinked people are skipped", func(t *testing.T) {
		in := &info{Ref: "work-2", AssigneeID: "01UNLINKED", Comment: "@nobody"}
		assert.Empty(t, recipients(commented, in, targets(commented, in), testDir(nil, nil)))
	})
	t.Run("creator hears when their task is closed", func(t *testing.T) {
		ev := mustEvent(t, strings.Replace(payloadTransitioned, `"actor_id":"`+actorLocal, `"actor_id":"`+actorBob, 1))
		in := &info{Ref: "work-2", CreatorID: actorLocal, To: state{Key: "done", Category: "done"}}
		assert.Equal(t, []recipient{{userID: "u-local", reason: reasonClosed}}, recipients(ev, in, targets(ev, in), testDir(nil, nil)))
		in.To = state{Key: "doing", Category: "in_progress"}
		assert.Empty(t, recipients(ev, in, targets(ev, in), testDir(nil, nil)))
	})
	t.Run("a plain move tells nobody", func(t *testing.T) {
		in := &info{Ref: "work-2", AssigneeID: actorBob, To: state{Key: "doing"}}
		assert.Empty(t, targets(moved, in))
	})
}

func TestTexts(t *testing.T) {
	r := render.New(render.Defaults(), nil, time.UTC, time.Now())
	name := func(id string, ping bool) string {
		n := map[string]string{actorLocal: "alice", actorBob: "bob"}[id]
		if ping {
			return "@" + n
		}
		return n
	}
	in := &info{
		Ref: "work-2", Title: "Fix [backups]", URL: "https://tix.example.com/tasks/work-2",
		From: state{Key: "todo", Label: "To do"}, To: state{Key: "doing", Label: "Doing"}, Known: true, AssigneeID: actorBob,
	}
	assert.Equal(t, `**alice** moved [work-2 Fix \[backups\]](https://tix.example.com/tasks/work-2) **To do → Doing**`,
		feedText(mustEvent(t, payloadTransitioned), in, name, r))
	assert.Equal(t, `**alice** assigned [work-2 Fix \[backups\]](https://tix.example.com/tasks/work-2) to **bob**`,
		feedText(mustEvent(t, payloadAssigned), in, name, r))
	in.Comment = "looks\ngood"
	assert.Equal(t, "**alice** commented on [work-2 Fix \\[backups\\]](https://tix.example.com/tasks/work-2)\n> looks good",
		feedText(mustEvent(t, payloadCommented), in, name, r))
	assert.Equal(t, "**@alice** mentioned you on [work-2 Fix \\[backups\\]](https://tix.example.com/tasks/work-2)\n> looks good",
		dmText(mustEvent(t, payloadCommented), in, reasonMentioned, name, r))

	raw := &info{Ref: "work-2", From: state{Key: "todo"}, To: state{Key: "doing"}}
	assert.Equal(t, "**alice** moved work-2 **todo → doing**", feedText(mustEvent(t, payloadTransitioned), raw, name, r))
	assert.Equal(t, "**alice** changed the assignee of work-2", feedText(mustEvent(t, payloadAssigned), raw, name, r))
}

// TestFeedSettings: the feed detail, comment and people settings, a feed's own overrides and the
// reader's language, on the same events.
func TestFeedSettings(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	due := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	name := func(id string, ping bool) string {
		n := map[string]string{actorLocal: "alice", actorBob: "bob"}[id]
		if ping {
			return "@" + n
		}
		return n
	}
	long := strings.Repeat("word ", 60) + "end"
	in := func() *info {
		return &info{
			Ref: "work-2", Title: "Fix backups", URL: "https://tix.example.com/tasks/work-2", ProjectKey: "work",
			From: state{Key: "todo", Label: "To do"}, To: state{Key: "doing", Label: "Doing", Category: "in_progress"},
			Known: true, AssigneeID: actorBob, Priority: 2, DueAt: &due, Comment: "first line\nsecond line",
		}
	}
	with := func(f render.Feed) render.Options { return render.Defaults().WithFeed(f) }
	link := "[work-2 Fix backups](https://tix.example.com/tasks/work-2)"
	for _, tc := range []struct {
		name    string
		o       render.Options
		lang    string
		ev      string
		comment string
		want    string
	}{
		{"compact move", render.Defaults(), "en", payloadTransitioned, "", "**alice** moved " + link + " **To do → Doing**"},
		{
			"detailed move", with(render.Feed{Detail: render.DetailDetailed}), "en", payloadTransitioned, "",
			"**alice** moved " + link + " **To do → Doing**\nAssignee: **bob** · Priority: **High** · Due: **18.11.2026**",
		},
		{
			"detailed comment keeps the quote last", with(render.Feed{Detail: render.DetailDetailed}), "en", payloadCommented, "",
			"**alice** commented on " + link + "\nAssignee: **bob** · Priority: **High** · Due: **18.11.2026**\n> first line second line",
		},
		{"comment excerpt", render.Defaults(), "en", payloadCommented, "", "**alice** commented on " + link + "\n> first line second line"},
		{
			"comment excerpt is cut at a word", render.Defaults(), "en", payloadCommented, long,
			"**alice** commented on " + link + "\n> " + strings.TrimSpace(strings.Repeat("word ", 40)) + "…",
		},
		{
			"comment in full", with(render.Feed{Comments: render.CommentFull}), "en", payloadCommented, "",
			"**alice** commented on " + link + "\n> first line\n> second line",
		},
		{"no comment text", with(render.Feed{Comments: render.CommentNone}), "en", payloadCommented, "", "**alice** commented on " + link},
		{"mentions ping", with(render.Feed{People: render.PeopleMentions}), "en", payloadAssigned, "", "**@alice** assigned " + link + " to **@bob**"},
		{"names do not", render.Defaults(), "en", payloadAssigned, "", "**alice** assigned " + link + " to **bob**"},
		{"relative due", func() render.Options {
			o := with(render.Feed{Detail: render.DetailDetailed})
			o.DateFormat = render.DateRelative
			return o
		}(), "en", payloadCreated, "", "**alice** created " + link + " for **bob**\nAssignee: **bob** · Priority: **High** · Due: **tomorrow**"},
		{
			"in Bulgarian", with(render.Feed{Detail: render.DetailDetailed}), "bg", payloadTransitioned, "",
			"**alice** премести " + link + " **To do → Doing**\nВъзложена на: **bob** · Приоритет: **Висок** · Срок: **18.11.2026**",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := in()
			if tc.comment != "" {
				x.Comment = tc.comment
			}
			p := feedPost(mustEvent(t, tc.ev), x, name, render.New(tc.o, i18n.For(tc.lang), time.UTC, now))
			require.NotNil(t, p)
			assert.Equal(t, tc.want, p.Message)
		})
	}

	t.Run("card", func(t *testing.T) {
		p := feedPost(mustEvent(t, payloadTransitioned), in(), name, render.New(with(render.Feed{Detail: render.DetailCard}), nil, time.UTC, now))
		require.NotNil(t, p)
		assert.Empty(t, p.Message)
		atts := p.Attachments()
		require.Len(t, atts, 1)
		assert.Equal(t, "**alice** moved "+link+" **To do → Doing**", atts[0].Text)
		assert.Equal(t, "#1E88E5", atts[0].Color)
		assert.Equal(t, "tix · WORK", atts[0].Footer)
		var fields []string
		for _, f := range atts[0].Fields {
			fields = append(fields, f.Title+"="+f.Value.(string))
		}
		assert.Equal(t, []string{"Assignee=bob", "Priority=High", "Due=18.11.2026"}, fields)
	})

	t.Run("a DM in the recipient's language", func(t *testing.T) {
		got := dmText(mustEvent(t, payloadCommented), in(), reasonCommented, name, render.New(render.Defaults(), i18n.For("bg"), time.UTC, now))
		assert.Equal(t, "**@alice** коментира "+link+"\n> first line second line", got)
		got = dmText(mustEvent(t, payloadAssigned), in(), reasonAssigned, name, render.New(render.Defaults(), i18n.For("bg"), time.UTC, now))
		assert.Equal(t, "**@alice** ти възложи "+link, got)
	})
}

// TestFeedOverrides: each channel gets its own subscription's format.
func TestFeedOverrides(t *testing.T) {
	subs := []store.Subscription{
		{ChannelID: "ch-a", Project: "work", Events: []string{kindTransitioned}, Detail: render.DetailCard},
		{ChannelID: "ch-b", Project: "", Events: []string{kindTransitioned}},
		{ChannelID: "ch-a", Project: "", Events: []string{kindTransitioned}, Detail: render.DetailDetailed},
	}
	got := matchSubs(subs, "work", []string{kindTransitioned})
	require.Len(t, got, 2)
	assert.Equal(t, "ch-a", got[0].ChannelID)
	assert.Equal(t, render.DetailCard, got[0].Detail, "the first matching feed of a channel decides")
	assert.Equal(t, "ch-b", got[1].ChannelID)
	o := render.Defaults()
	o.FeedDetail = render.DetailDetailed
	assert.Equal(t, render.DetailCard, o.WithFeed(render.Feed{Detail: got[0].Detail}).FeedDetail)
	assert.Equal(t, render.DetailDetailed, o.WithFeed(render.Feed{Detail: got[1].Detail}).FeedDetail, "no override keeps the setting")
}

func TestProjectKeyOf(t *testing.T) {
	assert.Equal(t, "work", projectKeyOf("work-2"))
	assert.Equal(t, "my-team", projectKeyOf("my-team-17"))
	assert.Equal(t, "", projectKeyOf("nodash"))
}

// fakeAPI is the bit of plugin.API the HTTP middleware and the receiver log through.
type fakeAPI struct{ plugin.API }

func (fakeAPI) LogDebug(string, ...any) {}
func (fakeAPI) LogInfo(string, ...any)  {}
func (fakeAPI) LogWarn(string, ...any)  {}
func (fakeAPI) LogError(string, ...any) {}

func TestWebhookEndpoint(t *testing.T) {
	secret := "the-secret"
	a := app.New(fakeAPI{}, nil, nil, "bot", func() *app.Config { return &app.Config{WebhookSecret: secret} })
	f := New(a, nil)
	h := api.NewHandler(fakeAPI{})
	f.Routes(h)

	ping := `{"seq":30,"id":"01PING","tenant_id":"t","type":"webhook.ping","subject_type":"webhook","subject_id":"w","payload":{},"occurred_at":"2026-11-17T09:00:00Z"}`
	post := func(body, ts, sig string) int {
		r := httptest.NewRequest(http.MethodPost, "/hooks/webhook", strings.NewReader(body))
		r.Header.Set(headerTimestamp, ts)
		r.Header.Set(headerSignature, sig)
		r.Header.Set(headerEvent, "webhook.ping")
		w := httptest.NewRecorder()
		h.ServeHTTP(&plugin.Context{}, w, r)
		return w.Code
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)

	assert.Equal(t, http.StatusUnauthorized, post(ping, now, ""), "unsigned")
	assert.Equal(t, http.StatusUnauthorized, post(ping, now, sign("wrong", now, []byte(ping))), "wrong secret")
	old := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339Nano)
	assert.Equal(t, http.StatusUnauthorized, post(ping, old, sign(secret, old, []byte(ping))), "stale")
	assert.Equal(t, http.StatusBadRequest, post(`{}`, now, sign(secret, now, []byte(`{}`))), "signed but not an event")
	assert.Equal(t, http.StatusAccepted, post(ping, now, sign(secret, now, []byte(ping))), "valid")
	assert.Equal(t, http.StatusOK, post(ping, now, sign(secret, now, []byte(ping))), "repeat is acknowledged and ignored")

	r := httptest.NewRequest(http.MethodGet, "/hooks/webhook", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(&plugin.Context{}, w, r)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)

	a2 := app.New(fakeAPI{}, nil, nil, "bot", func() *app.Config { return &app.Config{} })
	h2 := api.NewHandler(fakeAPI{})
	New(a2, nil).Routes(h2)
	r = httptest.NewRequest(http.MethodPost, "/hooks/webhook", strings.NewReader(ping))
	r.Header.Set(headerTimestamp, now)
	r.Header.Set(headerSignature, sign("", now, []byte(ping)))
	w = httptest.NewRecorder()
	h2.ServeHTTP(&plugin.Context{}, w, r)
	assert.Equal(t, http.StatusUnauthorized, w.Code, "no secret configured rejects everything")
}

func TestParseFeedOptions(t *testing.T) {
	opts, rest, err := parseFeedOptions([]string{"created", "--detail", "card", "--comments=none", "commented", "--people", "MENTIONS"})
	require.NoError(t, err)
	assert.Equal(t, render.Feed{Detail: "card", Comments: "none", People: "mentions"}, opts)
	assert.Equal(t, []string{"created", "commented"}, rest)
	assert.Equal(t, "--detail card --comments none --people mentions", feedFormat(store.Subscription{Detail: "card", Comments: "none", People: "mentions"}))
	assert.Empty(t, feedFormat(store.Subscription{}), "old records have no overrides")
	for _, bad := range [][]string{{"--detail"}, {"--detail", "huge"}, {"--colour", "red"}, {"--people=all"}} {
		_, _, err := parseFeedOptions(bad)
		assert.Error(t, err, bad)
	}
}
