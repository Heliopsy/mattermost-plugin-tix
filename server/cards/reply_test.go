// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"testing"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/command"
)

func newReplyEnv(t *testing.T) *env {
	e := newEnv(t)
	e.style = ""
	return e
}

// cardsFor are the bot card replies answering a post.
func (e *env) cardsFor(src string) []*model.Post {
	e.pmu.Lock()
	defer e.pmu.Unlock()
	var out []*model.Post
	for _, p := range e.created {
		if p.GetProp(propSource) == src {
			if cur, ok := e.posts[p.Id]; ok {
				out = append(out, cur.Clone())
			}
		}
	}
	return out
}

func TestReplyCard(t *testing.T) {
	e := newReplyEnv(t)

	p := e.post(e.alice, "infra-1 and INFRA-2", nil)
	assert.Empty(t, p.Attachments(), "the person's message is left as written")
	assert.Nil(t, p.GetProp(propRefs))
	cs := e.cardsFor(p.Id)
	require.Len(t, cs, 1)
	c := cs[0]
	assert.Equal(t, "botid", c.UserId)
	assert.Equal(t, p.Id, c.RootId, "the card replies in the message's thread")
	assert.Equal(t, []string{"infra-1 · Fix login", "infra-2 · Secret plan"}, cardTitles(c))
	assert.Equal(t, []string{"infra-1", "infra-2"}, refsOf(c))
	recorded, err := e.app.Store.Cards("infra-2")
	require.NoError(t, err)
	require.Len(t, recorded, 1)
	assert.Equal(t, c.Id, recorded[0].PostID)
	assert.Equal(t, p.Id, recorded[0].SourceID)

	// A reply in a thread is answered in that thread; bob's token cannot see infra-2.
	e.quiet = -1
	r := e.postIn(p.Id, e.bob, "what about infra-2?", nil)
	assert.Empty(t, e.cardsFor(r.Id), "bob cannot see infra-2: no card, whoever else could")

	// Carol is not connected: no card.
	q := e.post(e.carol, "infra-1", nil)
	assert.Empty(t, e.cardsFor(q.Id))

	// The bot's own posts are never carded or stripped.
	before := len(e.created)
	e.post("botid", "infra-1", nil)
	assert.Len(t, e.created, before)
}

func TestReplyCardQuietPeriod(t *testing.T) {
	e := newReplyEnv(t)
	p := e.post(e.alice, "infra-1", nil)
	require.Len(t, e.cardsFor(p.Id), 1)

	// Same task, same channel, within the window: no second card, even in another thread.
	q := e.post(e.alice, "infra-1 again", nil)
	assert.Empty(t, e.cardsFor(q.Id))

	// After the window a new thread gets one, but the first thread never gets a second.
	now := time.Now().Add(11 * time.Minute)
	e.f.now = func() time.Time { return now }
	q = e.post(e.alice, "infra-1 later", nil)
	assert.Len(t, e.cardsFor(q.Id), 1)
	r := e.postIn(p.Id, e.alice, "back to infra-1", nil)
	assert.Empty(t, e.cardsFor(r.Id))
}

func TestReplyCardOnEdit(t *testing.T) {
	e := newReplyEnv(t)
	p := e.post(e.alice, "hello", nil)
	assert.Empty(t, e.cardsFor(p.Id))

	edited := p.Clone()
	edited.Message = "hello infra-1"
	e.save(edited)
	e.f.MessageHasBeenUpdated(&plugin.Context{}, edited, p)
	require.Len(t, e.cardsFor(p.Id), 1)

	// Editing again does not duplicate the card in that thread.
	again := edited.Clone()
	again.Message = "hello infra-1!"
	e.f.MessageHasBeenUpdated(&plugin.Context{}, again, edited)
	assert.Len(t, e.cardsFor(p.Id), 1)
}

func TestReplyCardRefreshAndDelete(t *testing.T) {
	e := newReplyEnv(t)
	p := e.post(e.alice, "infra-1", nil)
	c := e.cardsFor(p.Id)[0]

	e.tix.mu.Lock()
	e.tix.tasks["infra-1"].Status = "done"
	e.tix.mu.Unlock()
	e.f.Refresh("infra-1")
	assert.Equal(t, "Done", e.stored(c.Id).Attachments()[0].Fields[0].Value)
	assert.Equal(t, "infra-1", e.stored(p.Id).Message)
	assert.Empty(t, e.stored(p.Id).Attachments())

	// A button on the bot card redraws the bot card.
	e.tix.mu.Lock()
	e.tix.tasks["infra-1"].Status = "todo"
	e.tix.mu.Unlock()
	e.api.On("HasPermissionToChannel", e.alice, "ch1", model.PermissionReadChannelContent).Return(true)
	resp := e.click(e.alice, e.stored(c.Id), map[string]any{"ref": "infra-1", "action": "transition", "to": "doing"})
	require.NotNil(t, resp.Update)
	assert.Equal(t, c.Id, resp.Update.Id)
	assert.Equal(t, "In progress", resp.Update.Attachments()[0].Fields[0].Value)

	// Deleting the message deletes its card.
	e.f.MessageHasBeenDeleted(&plugin.Context{}, p)
	assert.Contains(t, e.deleted, c.Id)
	recorded, err := e.app.Store.Cards("infra-1")
	require.NoError(t, err)
	assert.Empty(t, recorded)

	// Without the hook, the next Refresh notices the source is gone.
	q := e.postIn("", e.alice, "infra-2", nil)
	c2 := e.cardsFor(q.Id)[0]
	e.pmu.Lock()
	delete(e.posts, q.Id)
	e.pmu.Unlock()
	e.f.Refresh("infra-2")
	assert.Contains(t, e.deleted, c2.Id)
}

func (e *env) sync(userID, arg string) *model.CommandResponse {
	return e.f.syncCmd(&command.Ctx{App: e.app, Args: &model.CommandArgs{UserId: userID, ChannelId: "ch1"}}, []string{arg})
}

func TestSyncPermission(t *testing.T) {
	e := newReplyEnv(t)
	e.api.On("GetChannel", "ch1").Return(&model.Channel{Id: "ch1", Type: model.ChannelTypeOpen}, nil)
	e.api.On("HasPermissionToChannel", e.bob, "ch1", model.PermissionManageChannelRoles).Return(false)
	e.api.On("HasPermissionToChannel", e.alice, "ch1", model.PermissionManageChannelRoles).Return(true)

	resp := e.sync(e.bob, "on")
	assert.Contains(t, resp.Text, "Only channel admins")
	assert.False(t, e.f.synced("ch1"))

	e.sync(e.alice, "on")
	assert.True(t, e.f.synced("ch1"))
	assert.Contains(t, e.created[len(e.created)-1].Message, "thread sync on")

	resp = e.sync(e.bob, "off")
	assert.Contains(t, resp.Text, "Only channel admins")
	assert.True(t, e.f.synced("ch1"))
	e.sync(e.alice, "off")
	assert.False(t, e.f.synced("ch1"))

	// Not in a DM.
	e.api.On("GetChannel", "dm").Return(&model.Channel{Id: "dm", Type: model.ChannelTypeDirect}, nil)
	resp = e.f.syncCmd(&command.Ctx{App: e.app, Args: &model.CommandArgs{UserId: e.alice, ChannelId: "dm"}}, []string{"on"})
	assert.Contains(t, resp.Text, "not direct")
}

func TestSyncMirrorsReplies(t *testing.T) {
	e := newReplyEnv(t)
	e.api.On("GetChannel", "ch1").Return(&model.Channel{Id: "ch1", Type: model.ChannelTypeOpen}, nil)
	e.api.On("HasPermissionToChannel", e.alice, "ch1", model.PermissionManageChannelRoles).Return(true)
	var hints []*model.Post
	var hintUsers []string
	e.api.On("SendEphemeralPost", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		hintUsers = append(hintUsers, args.String(0))
		hints = append(hints, args.Get(1).(*model.Post))
	}).Return(&model.Post{})

	p := e.post(e.alice, "infra-1", nil)
	card := e.cardsFor(p.Id)[0]

	// Off: nothing mirrored.
	e.postIn(p.Id, e.bob, "not mirrored", nil)
	assert.Empty(t, e.tix.comments)

	e.sync(e.alice, "on")
	r := e.postIn(p.Id, e.bob, "on it", nil)
	assert.Equal(t, []string{"on it"}, e.tix.comments)
	assert.Equal(t, []string{"tok-bob"}, e.tix.commenters, "mirrored as the replier, with their token")
	m := e.app.Store.MirroredComment("infra-1", "a2", "on it", "c1")
	require.NotNil(t, m)
	assert.Equal(t, "ch1", m.ChannelID)
	assert.Equal(t, p.Id, m.RootID)
	assert.Equal(t, r.Id, m.PostID)

	// The same post again (a retried hook) is not mirrored twice.
	e.f.MessageHasBeenPosted(&plugin.Context{}, e.stored(r.Id))
	assert.Len(t, e.tix.comments, 1)

	// Edits, bot posts and system posts are not mirrored.
	edited := e.stored(r.Id)
	edited.Message = "on it, edited"
	e.f.MessageHasBeenUpdated(&plugin.Context{}, edited, r)
	e.postIn(p.Id, "botid", "bot says", nil)
	sys := &model.Post{Id: model.NewId(), UserId: e.bob, ChannelId: "ch1", RootId: p.Id, Type: model.PostTypeJoinChannel, Message: "joined"}
	e.f.MessageHasBeenPosted(&plugin.Context{}, e.save(sys))
	assert.Len(t, e.tix.comments, 1)

	// A link the webapp posted for the person (create dialog, "Post link in thread") is not mirrored.
	e.postIn(p.Id, e.bob, "Created tix task infra-1: Fix login", model.StringInterface{PropFromPlugin: "true"})
	assert.Len(t, e.tix.comments, 1)

	// Unlinked: one hint, ever, and nothing mirrored.
	e.postIn(p.Id, e.carol, "me too", nil)
	e.postIn(p.Id, e.carol, "hello?", nil)
	assert.Len(t, e.tix.comments, 1)
	require.Len(t, hints, 1)
	assert.Equal(t, e.carol, hintUsers[0])
	assert.Equal(t, p.Id, hints[0].RootId)
	assert.Contains(t, hints[0].Message, "/tix connect")
	_ = card
}

func TestSyncCommentsIntoThreadWithoutEcho(t *testing.T) {
	e := newReplyEnv(t)
	e.api.On("GetChannel", mock.Anything).Return(&model.Channel{Type: model.ChannelTypeOpen}, nil)
	e.api.On("HasPermissionToChannel", e.alice, mock.Anything, model.PermissionManageChannelRoles).Return(true)
	p := e.post(e.alice, "infra-1", nil)
	e.sync(e.alice, "on")

	threadPosts := func() []*model.Post {
		e.pmu.Lock()
		defer e.pmu.Unlock()
		var out []*model.Post
		for _, x := range e.created {
			if x.RootId == p.Id && x.GetProp(propCommentID) != nil {
				out = append(out, x)
			}
		}
		return out
	}

	// Someone comments in tix: the bot posts it into the synced thread, once.
	e.f.CommentAdded("infra-1", "cx", "a9", "dana", "looks done\nship it", nil)
	e.f.CommentAdded("infra-1", "cx", "a9", "dana", "looks done\nship it", nil)
	got := threadPosts()
	require.Len(t, got, 1)
	assert.Equal(t, "botid", got[0].UserId)
	assert.Equal(t, "**dana** commented on infra-1 in tix:\n> looks done\n> ship it", got[0].Message)

	// notify already posted it to this channel: no second copy here.
	e.f.CommentAdded("infra-1", "cy", "a9", "dana", "feed has it", []string{"ch1"})
	assert.Len(t, threadPosts(), 1)

	// A reply mirrored from this thread does not come back as a bot post (no echo loop).
	e.postIn(p.Id, e.bob, "mirrored reply", nil)
	require.Equal(t, []string{"mirrored reply"}, e.tix.comments)
	e.f.CommentAdded("infra-1", "c1", "a2", "bob", "mirrored reply", nil)
	// Even when the webhook beats the id record: matched by task, author and text.
	e.postIn(p.Id, e.bob, "second reply", nil)
	e.f.CommentAdded("infra-1", "unknown-id", "a2", "bob", "second reply", nil)
	assert.Len(t, threadPosts(), 1)

	// Not synced: nothing.
	e.sync(e.alice, "off")
	e.f.CommentAdded("infra-1", "cz", "a9", "dana", "quiet", nil)
	assert.Len(t, threadPosts(), 1)
}
