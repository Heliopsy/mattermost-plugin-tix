// SPDX-License-Identifier: AGPL-3.0-only

package notify

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// Kinds of activity a channel can subscribe to.
const (
	kindCreated      = "created"
	kindTransitioned = "transitioned"
	kindAssigned     = "assigned"
	kindCommented    = "commented"
	kindDone         = "done"
	kindDeleted      = "deleted"
)

// allKinds are the kinds in display order.
var allKinds = []string{kindCreated, kindTransitioned, kindAssigned, kindCommented, kindDone, kindDeleted}

// defaultKinds is what /tix subscribe picks when no kinds are named.
var defaultKinds = []string{kindCreated, kindTransitioned, kindCommented}

// kindAliases maps accepted spellings to kinds.
var kindAliases = map[string]string{
	"created": kindCreated, "create": kindCreated, "new": kindCreated,
	"transitioned": kindTransitioned, "transition": kindTransitioned, "moved": kindTransitioned, "move": kindTransitioned,
	"assigned": kindAssigned, "assign": kindAssigned,
	"commented": kindCommented, "comment": kindCommented, "comments": kindCommented,
	"done": kindDone, "closed": kindDone, "close": kindDone,
	"deleted": kindDeleted, "delete": kindDeleted,
}

// webhookEventTypes are the tix event types the plugin registers for.
var webhookEventTypes = []string{
	"task.created", "task.updated", "task.transitioned", "task.deleted",
	"task.claimed", "task.released", "task.lease_expired", "task.reclaimed",
	"comment.added",
}

// state is one workflow state as shown to people.
type state struct {
	Key, Label, Category string
	Terminal             bool
}

func (s state) name() string {
	if s.Label != "" {
		return s.Label
	}
	return s.Key
}

// isClosed says whether moving into s finishes the task (done or cancelled).
func (s state) isClosed() bool {
	if s.Category != "" {
		return s.Category == "done" || s.Category == "cancelled"
	}
	switch strings.ToLower(s.Key) {
	case "done", "closed", "cancelled", "canceled", "resolved":
		return true
	}
	return false
}

// info is what an event is about, read from tix when a reader is available and from the payload otherwise.
type info struct {
	Ref        string
	Title      string
	URL        string
	ProjectKey string
	AssigneeID string
	CreatorID  string
	From, To   state
	Comment    string
	Edited     bool
	// Known is true when the task was read from tix, so AssigneeID and CreatorID are reliable.
	Known bool
}

// kinds says which subscribable kinds an event is.
func kinds(ev *Event, in *info) []string {
	switch ev.Type {
	case "task.created":
		return []string{kindCreated}
	case "task.transitioned":
		if in.To.isClosed() {
			return []string{kindTransitioned, kindDone}
		}
		return []string{kindTransitioned}
	case "task.updated":
		if slices.Contains(ev.Strings("fields"), "assignee_actor_id") {
			return []string{kindAssigned}
		}
	case "comment.added":
		if !in.Edited {
			return []string{kindCommented}
		}
	case "task.deleted":
		return []string{kindDeleted}
	}
	return nil
}

// projectKeyOf takes the project key from a task reference: infra-42 -> infra.
func projectKeyOf(ref string) string {
	i := strings.LastIndex(ref, "-")
	if i <= 0 {
		return ""
	}
	return ref[:i]
}

// matchChannels returns the channels whose subscriptions want an event of these kinds in project, each once.
func matchChannels(subs []store.Subscription, project string, ks []string) []string {
	if len(ks) == 0 {
		return nil
	}
	var out []string
	for _, s := range subs {
		if s.Project != "" && !strings.EqualFold(s.Project, project) {
			continue
		}
		hit := false
		for _, k := range ks {
			if slices.Contains(s.Events, k) {
				hit = true
				break
			}
		}
		if hit && !slices.Contains(out, s.ChannelID) {
			out = append(out, s.ChannelID)
		}
	}
	return out
}

// Reasons for a personal message, strongest first: a person gets one DM per event.
const (
	reasonMentioned = iota
	reasonAssigned
	reasonCommented
	reasonClosed
)

// target is one person to tell, by tix actor (or by handle, for a mention).
type target struct {
	actorID string
	handle  string
	reason  int
}

// mentionPattern is tix's own rule for @handle (internal/service/inbox.go).
var mentionPattern = regexp.MustCompile(`(?:^|[^\w@.])@([A-Za-z0-9][A-Za-z0-9._-]*)`)

// mentions lists the handles text names, lowercased, each once.
func mentions(text string) []string {
	var out []string
	for _, m := range mentionPattern.FindAllStringSubmatch(text, -1) {
		h := strings.ToLower(strings.TrimRight(m[1], ".-_"))
		if h != "" && !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	return out
}

// targets lists who should hear about an event personally, before linking, self and preference checks.
func targets(ev *Event, in *info) []target {
	var out []target
	switch ev.Type {
	case "task.created":
		for _, h := range mentions(in.Comment) {
			out = append(out, target{handle: h, reason: reasonMentioned})
		}
		if in.AssigneeID != "" {
			out = append(out, target{actorID: in.AssigneeID, reason: reasonAssigned})
		}
	case "task.updated":
		if slices.Contains(ev.Strings("fields"), "assignee_actor_id") && in.AssigneeID != "" {
			out = append(out, target{actorID: in.AssigneeID, reason: reasonAssigned})
		}
	case "comment.added":
		if in.Edited {
			return nil
		}
		for _, h := range mentions(in.Comment) {
			out = append(out, target{handle: h, reason: reasonMentioned})
		}
		for _, id := range []string{in.AssigneeID, in.CreatorID} {
			if id != "" {
				out = append(out, target{actorID: id, reason: reasonCommented})
			}
		}
	case "task.transitioned":
		if in.To.isClosed() && in.CreatorID != "" {
			out = append(out, target{actorID: in.CreatorID, reason: reasonClosed})
		}
	}
	return out
}

// directory answers who is who on the Mattermost side. Tests swap it.
type directory struct {
	// userForActor is the linked Mattermost user of a tix actor, or "".
	userForActor func(actorID string) string
	// userForHandle is the linked Mattermost user and tix actor of a tix handle, or "".
	userForHandle func(handle string) (userID, actorID string)
	// dmsOff says whether the user turned personal notifications off.
	dmsOff func(userID string) bool
	// canSee says whether the user can read the task in tix with their own token.
	canSee func(userID, ref string) bool
}

// recipient is one Mattermost user to DM and why.
type recipient struct {
	userID string
	reason int
}

// recipients resolves targets to Mattermost users: linked only, never the actor, at most one DM each,
// none for people who turned DMs off, and mentions only for people who can see the task.
func recipients(ev *Event, in *info, ts []target, d directory) []recipient {
	best := map[string]int{}
	var order []string
	for _, t := range ts {
		uid, actorID := "", t.actorID
		if t.handle != "" {
			uid, actorID = d.userForHandle(t.handle)
		} else {
			uid = d.userForActor(t.actorID)
		}
		if uid == "" || (actorID != "" && actorID == ev.ActorID) {
			continue
		}
		if r, ok := best[uid]; ok {
			if t.reason < r {
				best[uid] = t.reason
			}
			continue
		}
		best[uid] = t.reason
		order = append(order, uid)
	}
	var out []recipient
	for _, uid := range order {
		if d.dmsOff(uid) {
			continue
		}
		if best[uid] == reasonMentioned && !d.canSee(uid, in.Ref) {
			continue
		}
		out = append(out, recipient{userID: uid, reason: best[uid]})
	}
	return out
}

// namer renders a tix actor for a message; ping says whether an @mention is wanted.
type namer func(actorID string, ping bool) string

func (in *info) link() string {
	label := in.Ref
	if in.Title != "" {
		label += " " + in.Title
	}
	label = escapeLink(label)
	if in.URL == "" {
		return label
	}
	return fmt.Sprintf("[%s](%s)", label, in.URL)
}

// escapeLink keeps a title from closing the markdown link early.
func escapeLink(s string) string {
	return strings.NewReplacer("[", "\\[", "]", "\\]").Replace(s)
}

// excerpt is text on one line, cut to n runes.
func excerpt(text string, n int) string {
	line := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(line) <= n {
		return line
	}
	return strings.TrimSpace(string([]rune(line)[:n-1])) + "…"
}

// feedText is the channel post for an event. People are named without pinging them.
func feedText(ev *Event, in *info, name namer) string {
	who := "**" + name(ev.ActorID, false) + "**"
	if ev.ActorID == "" {
		who = "tix"
	}
	switch ev.Type {
	case "task.created":
		s := fmt.Sprintf("%s created %s", who, in.link())
		if in.AssigneeID != "" {
			s += " for **" + name(in.AssigneeID, false) + "**"
		}
		return s
	case "task.transitioned":
		return fmt.Sprintf("%s moved %s **%s → %s**", who, in.link(), in.From.name(), in.To.name())
	case "task.updated":
		if !in.Known {
			return fmt.Sprintf("%s changed the assignee of %s", who, in.link())
		}
		if in.AssigneeID == "" {
			return fmt.Sprintf("%s unassigned %s", who, in.link())
		}
		return fmt.Sprintf("%s assigned %s to **%s**", who, in.link(), name(in.AssigneeID, false))
	case "comment.added":
		s := fmt.Sprintf("%s commented on %s", who, in.link())
		if in.Comment != "" {
			s += "\n> " + excerpt(in.Comment, 280)
		}
		return s
	case "task.deleted":
		return fmt.Sprintf("%s deleted %s", who, in.link())
	}
	return ""
}

// dmText is the personal message for a reason.
func dmText(ev *Event, in *info, reason int, name namer) string {
	who := "**" + name(ev.ActorID, true) + "**"
	if ev.ActorID == "" {
		who = "tix"
	}
	quote := ""
	if in.Comment != "" {
		quote = "\n> " + excerpt(in.Comment, 280)
	}
	switch reason {
	case reasonMentioned:
		return fmt.Sprintf("%s mentioned you on %s%s", who, in.link(), quote)
	case reasonAssigned:
		return fmt.Sprintf("%s assigned you %s", who, in.link())
	case reasonCommented:
		return fmt.Sprintf("%s commented on %s%s", who, in.link(), quote)
	case reasonClosed:
		return fmt.Sprintf("%s moved your task %s to **%s**", who, in.link(), in.To.name())
	}
	return ""
}

// handle turns one verified event into card refreshes, channel posts and DMs.
func (f *Feature) handle(ev *Event) {
	ref := ev.Ref()
	if ref != "" && (strings.HasPrefix(ev.Type, "task.") || strings.HasPrefix(ev.Type, "comment.")) && f.cards != nil {
		f.cards.Refresh(ref)
	}
	if ev.Type == "webhook.ping" || ref == "" {
		return
	}
	in := f.describe(ev)
	ks := kinds(ev, in)
	if len(ks) == 0 {
		return
	}
	name := f.namer(ev)

	subs, err := f.a.Store.Subscriptions()
	if err != nil {
		f.a.API.LogWarn("tix: reading subscriptions", "err", err.Error())
	}
	// A comment the cards feature mirrored from a thread is not posted back into that channel.
	var mirror *store.Mirror
	if ev.Type == "comment.added" && f.a.Store != nil {
		mirror = f.a.Store.MirroredComment(ref, ev.ActorID, in.Comment, ev.SubjectID)
	}
	var posted []string
	if text := feedText(ev, in, name); text != "" {
		for _, ch := range matchChannels(subs, in.ProjectKey, ks) {
			if mirror != nil && ch == mirror.ChannelID {
				continue
			}
			if _, appErr := f.a.API.CreatePost(&model.Post{UserId: f.a.BotID, ChannelId: ch, Message: text}); appErr != nil {
				f.a.API.LogWarn("tix: posting to a subscribed channel", "channel_id", ch, "err", appErr.Error())
				continue
			}
			posted = append(posted, ch)
		}
	}
	if ts, ok := f.cards.(ThreadSyncer); ok && ev.Type == "comment.added" {
		ts.CommentAdded(ref, ev.SubjectID, ev.ActorID, name(ev.ActorID, false), in.Comment, posted)
	}

	for _, r := range recipients(ev, in, targets(ev, in), f.directory()) {
		if err := f.a.DM(r.userID, &model.Post{Message: dmText(ev, in, r.reason, name)}); err != nil {
			f.a.API.LogWarn("tix: sending a notification DM", "user_id", r.userID, "err", err.Error())
		}
	}
}

// reader is a tix client for reading what an event is about: the admin token when configured,
// else the token of the person who caused the event (they can see what they just changed).
func (f *Feature) reader(ev *Event) *tix.Client {
	if tok := f.a.Config().AdminToken; tok != "" {
		if c, err := f.a.NewTix(tok); err == nil {
			return c
		}
	}
	if uid, err := f.a.Store.UserForActor(ev.ActorID); err == nil && ev.ActorID != "" {
		if c, _, err := f.a.TixFor(uid); err == nil {
			return c
		}
	}
	return nil
}

// describe fills info from the payload and, when possible, from tix.
func (f *Feature) describe(ev *Event) *info {
	in := &info{
		Ref:        ev.Ref(),
		Title:      ev.Str("title"),
		ProjectKey: projectKeyOf(ev.Ref()),
		From:       state{Key: ev.Str("from")},
		To:         state{Key: ev.Str("to")},
		Edited:     ev.Bool("edited"),
	}
	in.URL = f.a.TixURL() + "/tasks/" + in.Ref
	if ev.Type == "task.deleted" {
		return in
	}
	c := f.reader(ev)
	if c == nil {
		return in
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	t, err := c.GetTask(ctx, in.Ref)
	if err != nil {
		f.a.API.LogDebug("tix: reading the task of an event", "type", ev.Type, "err", err.Error())
		return in
	}
	in.Known = true
	in.Title, in.URL = t.Title, f.a.TaskURL(t)
	in.AssigneeID, in.CreatorID = t.AssigneeActorID, t.CreatorActorID
	if ev.Type == "task.created" {
		in.Comment = t.Body
	}
	if p, w := f.projectAndWorkflow(c, t.ProjectID); p != nil {
		in.ProjectKey = p.Key
		if w != nil {
			in.From, in.To = stateOf(w, in.From.Key), stateOf(w, in.To.Key)
		}
	}
	if ev.Type == "comment.added" {
		if cs, err := c.ListComments(ctx, in.Ref, nil); err == nil {
			for _, cm := range cs.Items {
				if cm.ID == ev.SubjectID {
					in.Comment = cm.Body
					break
				}
			}
		}
	}
	return in
}

func stateOf(w *tix.Workflow, key string) state {
	if key == "" {
		return state{}
	}
	if s, ok := w.StateByKey(key); ok {
		return state{Key: s.Key, Label: s.Label, Category: s.Category, Terminal: s.Terminal}
	}
	return state{Key: key}
}

// namer names actors: linked people by Mattermost username, others by their tix name
// (the event's own actor_handle when nothing better is known).
func (f *Feature) namer(ev *Event) namer {
	names := map[string]string{}
	if h := ev.Str("actor_handle"); h != "" && ev.ActorID != "" {
		names[ev.ActorID] = h
	}
	return func(actorID string, ping bool) string {
		if actorID == "" {
			return "nobody"
		}
		if uid, err := f.a.Store.UserForActor(actorID); err == nil {
			if u, appErr := f.a.API.GetUser(uid); appErr == nil {
				if ping {
					return "@" + u.Username
				}
				return u.Username
			}
		}
		if n, ok := names[actorID]; ok {
			return n
		}
		n := shortID(actorID)
		if tok := f.a.Config().AdminToken; tok != "" {
			if c, err := f.a.NewTix(tok); err == nil {
				ctx, cancel := app.Ctx()
				if a, err := c.GetActor(ctx, actorID); err == nil {
					n = a.Name()
				}
				cancel()
			}
		}
		names[actorID] = n
		return n
	}
}

// directory wires the Mattermost side of recipients to the store and tix.
func (f *Feature) directory() directory {
	var handles map[string]store.Link
	return directory{
		userForActor: func(actorID string) string {
			uid, err := f.a.Store.UserForActor(actorID)
			if err != nil {
				return ""
			}
			return uid
		},
		userForHandle: func(h string) (string, string) {
			if handles == nil {
				handles = map[string]store.Link{}
				links, err := f.a.Store.ListLinks()
				if err != nil {
					f.a.API.LogWarn("tix: listing links for mentions", "err", err.Error())
				}
				for _, l := range links {
					if l.Handle != "" {
						handles[strings.ToLower(l.Handle)] = l
					}
				}
			}
			l, ok := handles[h]
			if !ok {
				return "", ""
			}
			return l.UserID, l.ActorID
		},
		dmsOff: f.dmsOff,
		canSee: func(userID, ref string) bool {
			c, _, err := f.a.TixFor(userID)
			if err != nil {
				return false
			}
			ctx, cancel := app.Ctx()
			defer cancel()
			_, err = c.GetTask(ctx, ref)
			return err == nil
		},
	}
}

// prefs is a person's notification choice; the zero value means DMs on.
type prefs struct {
	DMsOff bool `json:"dms_off"`
}

func (f *Feature) dmsOff(userID string) bool {
	var p prefs
	if err := f.a.Store.Get("notify", "prefs_"+userID, &p); err != nil {
		return false
	}
	return p.DMsOff
}

// shortID is the tail of an id, enough to tell actors apart in a message.
func shortID(id string) string {
	if len(id) > 8 {
		return "…" + id[len(id)-6:]
	}
	return id
}
