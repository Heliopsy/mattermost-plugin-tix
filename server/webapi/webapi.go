// SPDX-License-Identifier: AGPL-3.0-only

// Package webapi is the JSON API the webapp uses: the user's tasks, projects and workflows, and creating, moving and commenting on tasks.
// Every call acts as the requesting Mattermost user through their own tix token; tix decides what they may see and do.
package webapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/mux"
	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/link"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// Connector links a Mattermost user to tix by verified email and returns a message for them.
// It is link.Feature.ConnectByEmail, passed in so tests can replace it.
type Connector func(userID string) (string, error)

// Feature is the webapi feature.
type Feature struct {
	a       *app.App
	connect Connector
	// ChannelProject returns a channel's default project for a user who can read it, else "".
	// The tasks feature owns it; nil leaves default_project out of GET /me.
	ChannelProject func(userID, channelID string) string
}

// New returns the feature. connect may be nil, which turns POST /connect off.
func New(a *app.App, connect Connector) *Feature { return &Feature{a: a, connect: connect} }

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub { return nil }

// Routes mounts the feature's HTTP routes on /api/v1, which requires a Mattermost session.
func (f *Feature) Routes(h *api.Handler) {
	h.User.HandleFunc("/me", f.me).Methods(http.MethodGet)
	h.User.HandleFunc("/connect", f.connectByEmail).Methods(http.MethodPost)
	h.User.HandleFunc("/projects", f.projects).Methods(http.MethodGet)
	h.User.HandleFunc("/workflows", f.workflows).Methods(http.MethodGet)
	h.User.HandleFunc("/tasks", f.listTasks).Methods(http.MethodGet)
	h.User.HandleFunc("/tasks", f.createTask).Methods(http.MethodPost)
	h.User.HandleFunc("/tasks/{ref}", f.getTask).Methods(http.MethodGet)
	h.User.HandleFunc("/tasks/{ref}", f.updateTask).Methods(http.MethodPatch)
	h.User.HandleFunc("/tasks/{ref}/transition", f.transition).Methods(http.MethodPost)
	h.User.HandleFunc("/tasks/{ref}/comments", f.addComment).Methods(http.MethodPost)
}

// Limits on what the webapp may send; tix applies its own rules on top.
const (
	maxTitle     = 500
	maxText      = 64 << 10
	maxQuery     = 200
	maxKey       = 64
	maxStatuses  = 20
	maxTags      = 20
	defaultLimit = 50
	maxLimit     = 100
	maxComments  = 100
)

// task is a tix task with the fields the webapp shows.
type task struct {
	ID              string     `json:"id"`
	Ref             string     `json:"ref"`
	Title           string     `json:"title"`
	Body            string     `json:"body"`
	Status          string     `json:"status"`
	Priority        int        `json:"priority"`
	ProjectID       string     `json:"project_id"`
	AssigneeActorID string     `json:"assignee_actor_id"`
	CreatorActorID  string     `json:"creator_actor_id"`
	Tags            []string   `json:"tags"`
	Blocked         bool       `json:"blocked"`
	DueAt           *time.Time `json:"due_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	Version         int64      `json:"version"`
	WebURL          string     `json:"web_url"`
}

// Person is a tix actor with the Mattermost account linked to it, when there is one.
type Person struct {
	ActorID  string `json:"actor_id"`
	Name     string `json:"name"`
	UserID   string `json:"user_id,omitempty"`
	Username string `json:"username,omitempty"`
}

// State is a workflow state as the webapp shows it.
type State struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	Terminal bool   `json:"terminal"`
}

// TaskView is a task with its project, state and assignee resolved.
type TaskView struct {
	task
	ProjectKey  string  `json:"project_key"`
	ProjectName string  `json:"project_name"`
	State       State   `json:"state"`
	Assignee    *Person `json:"assignee"`
}

// Move is one transition the workflow allows from the task's state.
type Move struct {
	To              string `json:"to"`
	Label           string `json:"label"`
	Category        string `json:"category"`
	RequiresComment bool   `json:"requires_comment"`
}

// CommentView is a comment with its author resolved.
type CommentView struct {
	ID        string    `json:"id"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
	Author    *Person   `json:"author"`
}

// TaskDetail is GET /tasks/{ref}.
type TaskDetail struct {
	Task        TaskView      `json:"task"`
	Comments    []CommentView `json:"comments"`
	Transitions []Move        `json:"transitions"`
}

// errorBody is every error answer; Code lets the webapp react (not_linked shows the connect prompt).
type errorBody struct {
	Error string `json:"error"`
	Code  string `json:"code,omitempty"`
}

func writeErr(w http.ResponseWriter, r *http.Request, status int, code, msg string) {
	api.WriteJSON(w, r, status, errorBody{Error: msg, Code: code})
}

// writeTixErr answers a failed tix call: tix's own refusals pass through, the rest stay in the log.
func writeTixErr(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, app.ErrNotLinked) {
		writeErr(w, r, http.StatusConflict, "not_linked", "Your Mattermost account is not connected to tix yet.")
		return
	}
	var te *tix.Error
	if errors.As(err, &te) {
		switch te.Status {
		case http.StatusUnauthorized:
			writeErr(w, r, http.StatusConflict, "token_rejected", "tix rejected your token. Connect again.")
			return
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusConflict,
			http.StatusPreconditionFailed, http.StatusUnprocessableEntity, http.StatusRequestEntityTooLarge:
			writeErr(w, r, te.Status, te.Code, app.ErrText(err))
			return
		}
	}
	api.Log(r).WithError(err).Warnf("tix call failed")
	writeErr(w, r, http.StatusBadGateway, "tix_unavailable", "tix did not answer. Try again later.")
}

// client returns the requesting user's tix client, or answers the request and returns nil.
func (f *Feature) client(w http.ResponseWriter, r *http.Request) *tix.Client {
	c, _, err := f.a.TixFor(api.UserID(r))
	if err != nil {
		writeTixErr(w, r, err)
		return nil
	}
	return c
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "The request body is not valid JSON.")
		return false
	}
	return true
}

// audit starts an audit record for a state change; the caller defers f.a.API.LogAuditRec on it.
func (f *Feature) audit(r *http.Request, event string) *model.AuditRecord {
	ctx := api.PluginContext(r)
	if ctx == nil {
		ctx = &plugin.Context{}
	}
	return plugin.MakeAuditRecordWithContext(event, model.AuditStatusFail, ctx, api.UserID(r), r.URL.Path)
}

// validRef accepts a task reference such as infra-42 or a task ID.
func validRef(ref string) bool {
	if ref == "" || len(ref) > maxKey {
		return false
	}
	for _, c := range ref {
		ok := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	return true
}

func refOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	ref := mux.Vars(r)["ref"]
	if !validRef(ref) {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "That is not a task reference.")
		return "", false
	}
	return ref, true
}

// MeResponse is GET /me.
type MeResponse struct {
	Linked     bool    `json:"linked"`
	TixURL     string  `json:"tix_url"`
	CanConnect bool    `json:"can_connect"`
	Actor      *Person `json:"actor,omitempty"`
	TenantKey  string  `json:"tenant_key,omitempty"`
	// DefaultProject is the default project of the channel_id query parameter, when set.
	DefaultProject string `json:"default_project,omitempty"`
}

func (f *Feature) me(w http.ResponseWriter, r *http.Request) {
	uid := api.UserID(r)
	resp := MeResponse{TixURL: f.a.TixURL(), CanConnect: f.connect != nil && f.a.Config().AdminToken != ""}
	l, err := f.a.Store.GetLink(uid)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		api.WriteError(w, r, http.StatusInternalServerError, "Could not read your tix connection.", err)
		return
	default:
		resp.Linked = true
		resp.TenantKey = l.TenantKey
		resp.Actor = &Person{ActorID: l.ActorID, Name: l.Handle, UserID: uid}
		if u, appErr := f.a.API.GetUser(uid); appErr == nil {
			resp.Actor.Username = u.Username
		}
		if ch := r.URL.Query().Get("channel_id"); f.ChannelProject != nil && model.IsValidId(ch) {
			resp.DefaultProject = f.ChannelProject(uid, ch)
		}
	}
	api.WriteJSON(w, r, http.StatusOK, resp)
}

func (f *Feature) connectByEmail(w http.ResponseWriter, r *http.Request) {
	uid := api.UserID(r)
	rec := f.audit(r, "tixConnect")
	defer f.a.API.LogAuditRec(rec)
	if f.connect == nil {
		writeErr(w, r, http.StatusConflict, "connect_unavailable", "Run /tix connect to connect your account.")
		return
	}
	msg, err := f.connect(uid)
	switch {
	case err == nil:
	case errors.Is(err, link.ErrNoAdminToken):
		writeErr(w, r, http.StatusConflict, "connect_unavailable", "Run /tix connect with a token made in tix to connect your account.")
		return
	case errors.Is(err, link.ErrNoMatch):
		writeErr(w, r, http.StatusConflict, "no_match", "No tix account has your verified Mattermost email. Run /tix connect with a token made in tix.")
		return
	default:
		api.Log(r).WithError(err).Warnf("Connecting by email failed")
		writeErr(w, r, http.StatusBadGateway, "connect_failed", "Connecting to tix failed. Try /tix connect.")
		return
	}
	rec.Success()
	api.WriteJSON(w, r, http.StatusOK, map[string]string{"message": msg})
}

// ProjectView is one project in GET /projects.
type ProjectView struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Color      string `json:"color"`
	WorkflowID string `json:"workflow_id"`
}

func (f *Feature) projects(w http.ResponseWriter, r *http.Request) {
	ps, err := f.a.Projects(api.UserID(r))
	if err != nil {
		writeTixErr(w, r, err)
		return
	}
	out := make([]ProjectView, 0, len(ps))
	for _, p := range ps {
		if p.ArchivedAt != "" {
			continue
		}
		out = append(out, ProjectView{ID: p.ID, Key: p.Key, Name: p.Name, Color: p.Color, WorkflowID: p.WorkflowID})
	}
	api.WriteJSON(w, r, http.StatusOK, out)
}

func (f *Feature) workflows(w http.ResponseWriter, r *http.Request) {
	ws, err := f.a.Workflows(api.UserID(r))
	if err != nil {
		writeTixErr(w, r, err)
		return
	}
	if ws == nil {
		ws = []tix.Workflow{}
	}
	api.WriteJSON(w, r, http.StatusOK, ws)
}

// catalog resolves projects, workflows and people for one request.
type catalog struct {
	f        *Feature
	viewer   string
	projects map[string]tix.Project
	flows    map[string]tix.Workflow
	people   map[string]*Person
}

func (f *Feature) catalog(viewer string) (*catalog, error) {
	ps, err := f.a.Projects(viewer)
	if err != nil {
		return nil, err
	}
	ws, err := f.a.Workflows(viewer)
	if err != nil {
		return nil, err
	}
	c := &catalog{f: f, viewer: viewer, projects: map[string]tix.Project{}, flows: map[string]tix.Workflow{}, people: map[string]*Person{}}
	for _, p := range ps {
		c.projects[p.ID] = p
	}
	for _, w := range ws {
		c.flows[w.ID] = w
	}
	return c, nil
}

func (c *catalog) projectByKey(key string) (tix.Project, bool) {
	for _, p := range c.projects {
		if strings.EqualFold(p.Key, key) {
			return p, true
		}
	}
	return tix.Project{}, false
}

func (c *catalog) workflow(projectID string) (tix.Workflow, bool) {
	p, ok := c.projects[projectID]
	if !ok {
		return tix.Workflow{}, false
	}
	w, ok := c.flows[p.WorkflowID]
	return w, ok
}

func (c *catalog) state(projectID, key string) State {
	if w, ok := c.workflow(projectID); ok {
		if s, ok := w.StateByKey(key); ok {
			return State{Key: s.Key, Label: s.Label, Category: s.Category, Terminal: s.Terminal}
		}
	}
	return State{Key: key, Label: key}
}

// person resolves an actor to a name and, when linked, a Mattermost user.
func (c *catalog) person(actorID string) *Person {
	if actorID == "" {
		return nil
	}
	if p, ok := c.people[actorID]; ok {
		return p
	}
	p := &Person{ActorID: actorID, Name: actorID}
	if uid, err := c.f.a.Store.UserForActor(actorID); err == nil {
		if u, appErr := c.f.a.API.GetUser(uid); appErr == nil {
			p.UserID, p.Username = u.Id, u.Username
			p.Name = u.GetDisplayName(model.ShowNicknameFullName)
		}
	}
	if p.UserID == "" {
		if act, err := c.f.a.Actor(c.viewer, actorID); err == nil && act != nil {
			p.Name = act.Name()
		}
	}
	c.people[actorID] = p
	return p
}

func (c *catalog) view(t task) TaskView {
	v := TaskView{task: t, State: c.state(t.ProjectID, t.Status), Assignee: c.person(t.AssigneeActorID)}
	if p, ok := c.projects[t.ProjectID]; ok {
		v.ProjectKey, v.ProjectName = p.Key, p.Name
	}
	if v.WebURL == "" {
		v.WebURL = c.f.a.TixURL() + "/tasks/" + url.PathEscape(t.Ref)
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v
}

// TaskList is GET /tasks.
type TaskList struct {
	Items      []TaskView `json:"items"`
	NextCursor string     `json:"next_cursor"`
}

// listTasks passes the filters through to tix GET /tasks: mine (assignee=me), open (no terminal state),
// project (a key), q (free text), status (repeatable), cursor and limit.
func (f *Feature) listTasks(w http.ResponseWriter, r *http.Request) {
	uid := api.UserID(r)
	tc := f.client(w, r)
	if tc == nil {
		return
	}
	in := r.URL.Query()
	q := url.Values{}
	limit := defaultLimit
	if s := in.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeErr(w, r, http.StatusBadRequest, "bad_request", "limit must be a positive number.")
			return
		}
		limit = min(n, maxLimit)
	}
	q.Set("limit", strconv.Itoa(limit))
	if s := in.Get("cursor"); s != "" {
		if len(s) > 512 {
			writeErr(w, r, http.StatusBadRequest, "bad_request", "cursor is too long.")
			return
		}
		q.Set("cursor", s)
	}
	if s := strings.TrimSpace(in.Get("q")); s != "" {
		if len(s) > maxQuery {
			writeErr(w, r, http.StatusBadRequest, "bad_request", "The search is too long.")
			return
		}
		q.Set("q", s)
	}
	statuses := in["status"]
	if len(statuses) > maxStatuses {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "Too many statuses.")
		return
	}
	for _, s := range statuses {
		if !validRef(s) {
			writeErr(w, r, http.StatusBadRequest, "bad_request", "That is not a status key.")
			return
		}
		q.Add("status", s)
	}
	if mine, _ := strconv.ParseBool(in.Get("mine")); mine {
		q.Set("assignee", tc.Me)
	}

	cat, err := f.catalog(uid)
	if err != nil {
		writeTixErr(w, r, err)
		return
	}
	var proj *tix.Project
	if key := in.Get("project"); key != "" {
		p, ok := cat.projectByKey(key)
		if !ok {
			writeErr(w, r, http.StatusNotFound, "not_found", "No such project.")
			return
		}
		proj = &p
		q.Set("project", p.Key)
	}
	open, _ := strconv.ParseBool(in.Get("open"))
	if open {
		for _, s := range closedStates(cat, proj) {
			q.Add("not_status", s)
		}
	}

	var page tix.Page[task]
	ctx, cancel := app.Ctx()
	defer cancel()
	if err := tc.Do(ctx, http.MethodGet, "/tasks", q, nil, &page); err != nil {
		writeTixErr(w, r, err)
		return
	}
	out := TaskList{Items: make([]TaskView, 0, len(page.Items)), NextCursor: page.NextCursor}
	for _, t := range page.Items {
		v := cat.view(t)
		if open && v.State.Terminal {
			continue
		}
		out.Items = append(out.Items, v)
	}
	api.WriteJSON(w, r, http.StatusOK, out)
}

// closedStates are the state keys to leave out of an open listing: the terminal states of the project's
// workflow, or, across projects, the keys that are terminal in every workflow that declares them.
func closedStates(cat *catalog, proj *tix.Project) []string {
	if proj != nil {
		var out []string
		if wf, ok := cat.flows[proj.WorkflowID]; ok {
			for _, s := range wf.Definition.States {
				if s.Terminal {
					out = append(out, s.Key)
				}
			}
		}
		return out
	}
	terminal := map[string]bool{}
	var order []string
	for _, wf := range cat.flows {
		for _, s := range wf.Definition.States {
			prev, seen := terminal[s.Key]
			if !seen {
				order = append(order, s.Key)
				terminal[s.Key] = s.Terminal
			} else {
				terminal[s.Key] = prev && s.Terminal
			}
		}
	}
	var out []string
	for _, k := range order {
		if terminal[k] {
			out = append(out, k)
		}
	}
	return out
}

func (f *Feature) getTask(w http.ResponseWriter, r *http.Request) {
	ref, ok := refOf(w, r)
	if !ok {
		return
	}
	tc := f.client(w, r)
	if tc == nil {
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	var t task
	if err := tc.Do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(ref), nil, nil, &t); err != nil {
		writeTixErr(w, r, err)
		return
	}
	cms, err := tc.ListComments(ctx, ref, url.Values{"limit": {strconv.Itoa(maxComments)}})
	if err != nil {
		writeTixErr(w, r, err)
		return
	}
	cat, err := f.catalog(api.UserID(r))
	if err != nil {
		writeTixErr(w, r, err)
		return
	}
	d := TaskDetail{Task: cat.view(t), Comments: []CommentView{}, Transitions: []Move{}}
	for _, c := range cms.Items {
		d.Comments = append(d.Comments, CommentView{ID: c.ID, Body: c.Body, CreatedAt: c.CreatedAt, Author: cat.person(c.AuthorActorID)})
	}
	if wf, ok := cat.workflow(t.ProjectID); ok {
		for _, tr := range wf.NextStates(t.Status) {
			s := cat.state(t.ProjectID, tr.To)
			d.Transitions = append(d.Transitions, Move{To: tr.To, Label: s.Label, Category: s.Category, RequiresComment: tr.RequiresComment})
		}
	}
	api.WriteJSON(w, r, http.StatusOK, d)
}

// actorForUser maps a Mattermost user to the tix actor they connected as.
func (f *Feature) actorForUser(w http.ResponseWriter, r *http.Request, userID string) (string, bool) {
	if !model.IsValidId(userID) {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "That is not a Mattermost user.")
		return "", false
	}
	l, err := f.a.Store.GetLink(userID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, r, http.StatusUnprocessableEntity, "assignee_not_linked", "That person has not connected Mattermost to tix, so they cannot be assigned here.")
		return "", false
	} else if err != nil {
		api.WriteError(w, r, http.StatusInternalServerError, "Could not read the assignee's tix connection.", err)
		return "", false
	}
	return l.ActorID, true
}

// CreateRequest is POST /tasks. AssigneeUserID is a Mattermost user id, mapped to their tix actor.
type CreateRequest struct {
	Project        string   `json:"project"`
	Title          string   `json:"title"`
	Body           string   `json:"body"`
	Priority       int      `json:"priority"`
	AssigneeUserID string   `json:"assignee_user_id"`
	Tags           []string `json:"tags"`
}

func (f *Feature) createTask(w http.ResponseWriter, r *http.Request) {
	rec := f.audit(r, "tixCreateTask")
	defer f.a.API.LogAuditRec(rec)
	var in CreateRequest
	if !decode(w, r, &in) {
		return
	}
	in.Title = strings.TrimSpace(in.Title)
	switch {
	case in.Title == "" || len(in.Title) > maxTitle:
		writeErr(w, r, http.StatusBadRequest, "bad_request", "The title must be 1 to 500 characters.")
		return
	case len(in.Body) > maxText:
		writeErr(w, r, http.StatusBadRequest, "bad_request", "The description is too long.")
		return
	case in.Priority < 0 || in.Priority > 5:
		writeErr(w, r, http.StatusBadRequest, "bad_request", "Priority runs from 1 (highest) to 5 (lowest).")
		return
	case in.Project != "" && !validRef(in.Project):
		writeErr(w, r, http.StatusBadRequest, "bad_request", "That is not a project key.")
		return
	case len(in.Tags) > maxTags:
		writeErr(w, r, http.StatusBadRequest, "bad_request", "Too many tags.")
		return
	}
	tc := f.client(w, r)
	if tc == nil {
		return
	}
	body := map[string]any{"title": in.Title}
	if in.Project != "" {
		body["project_ref"] = in.Project
	}
	if in.Body != "" {
		body["body"] = in.Body
	}
	if in.Priority != 0 {
		body["priority"] = in.Priority
	}
	if len(in.Tags) > 0 {
		body["tags"] = in.Tags
	}
	if in.AssigneeUserID != "" {
		actor, ok := f.actorForUser(w, r, in.AssigneeUserID)
		if !ok {
			return
		}
		body["assignee_actor_id"] = actor
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	var t task
	if err := tc.Do(ctx, http.MethodPost, "/tasks", nil, body, &t); err != nil {
		writeTixErr(w, r, err)
		return
	}
	rec.AddMeta("task_ref", t.Ref)
	rec.Success()
	f.writeTask(w, r, http.StatusCreated, t)
}

func (f *Feature) writeTask(w http.ResponseWriter, r *http.Request, status int, t task) {
	cat, err := f.catalog(api.UserID(r))
	if err != nil {
		cat = &catalog{f: f, viewer: api.UserID(r), people: map[string]*Person{}}
	}
	api.WriteJSON(w, r, status, cat.view(t))
}

// UpdateRequest is PATCH /tasks/{ref}; nil fields are left alone and an empty AssigneeUserID unassigns.
type UpdateRequest struct {
	Title          *string `json:"title"`
	Priority       *int    `json:"priority"`
	AssigneeUserID *string `json:"assignee_user_id"`
}

func (f *Feature) updateTask(w http.ResponseWriter, r *http.Request) {
	rec := f.audit(r, "tixUpdateTask")
	defer f.a.API.LogAuditRec(rec)
	ref, ok := refOf(w, r)
	if !ok {
		return
	}
	rec.AddMeta("task_ref", ref)
	var in UpdateRequest
	if !decode(w, r, &in) {
		return
	}
	fields := map[string]any{}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" || len(t) > maxTitle {
			writeErr(w, r, http.StatusBadRequest, "bad_request", "The title must be 1 to 500 characters.")
			return
		}
		fields["title"] = t
	}
	if in.Priority != nil {
		if *in.Priority < 1 || *in.Priority > 5 {
			writeErr(w, r, http.StatusBadRequest, "bad_request", "Priority runs from 1 (highest) to 5 (lowest).")
			return
		}
		fields["priority"] = *in.Priority
	}
	if in.AssigneeUserID != nil {
		if *in.AssigneeUserID == "" {
			fields["assignee_actor_id"] = ""
		} else {
			actor, ok := f.actorForUser(w, r, *in.AssigneeUserID)
			if !ok {
				return
			}
			fields["assignee_actor_id"] = actor
		}
	}
	if len(fields) == 0 {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "Nothing to change.")
		return
	}
	tc := f.client(w, r)
	if tc == nil {
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	var t task
	if err := tc.Do(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(ref), nil, fields, &t); err != nil {
		writeTixErr(w, r, err)
		return
	}
	rec.Success()
	f.writeTask(w, r, http.StatusOK, t)
}

// TransitionRequest is POST /tasks/{ref}/transition.
type TransitionRequest struct {
	To      string `json:"to"`
	Comment string `json:"comment"`
}

func (f *Feature) transition(w http.ResponseWriter, r *http.Request) {
	rec := f.audit(r, "tixTransitionTask")
	defer f.a.API.LogAuditRec(rec)
	ref, ok := refOf(w, r)
	if !ok {
		return
	}
	rec.AddMeta("task_ref", ref)
	var in TransitionRequest
	if !decode(w, r, &in) {
		return
	}
	if !validRef(in.To) {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "Pick a status to move to.")
		return
	}
	if len(in.Comment) > maxText {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "The comment is too long.")
		return
	}
	tc := f.client(w, r)
	if tc == nil {
		return
	}
	body := map[string]any{"to": in.To}
	if c := strings.TrimSpace(in.Comment); c != "" {
		body["comment"] = c
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	var t task
	if err := tc.Do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(ref)+"/transition", nil, body, &t); err != nil {
		writeTixErr(w, r, err)
		return
	}
	rec.AddMeta("to", in.To)
	rec.Success()
	f.writeTask(w, r, http.StatusOK, t)
}

// CommentRequest is POST /tasks/{ref}/comments.
type CommentRequest struct {
	Body string `json:"body"`
}

func (f *Feature) addComment(w http.ResponseWriter, r *http.Request) {
	rec := f.audit(r, "tixAddComment")
	defer f.a.API.LogAuditRec(rec)
	ref, ok := refOf(w, r)
	if !ok {
		return
	}
	rec.AddMeta("task_ref", ref)
	var in CommentRequest
	if !decode(w, r, &in) {
		return
	}
	body := strings.TrimSpace(in.Body)
	if body == "" || len(body) > maxText {
		writeErr(w, r, http.StatusBadRequest, "bad_request", "Write a comment of up to 64 KiB.")
		return
	}
	tc := f.client(w, r)
	if tc == nil {
		return
	}
	ctx, cancel := app.Ctx()
	defer cancel()
	c, err := tc.AddComment(ctx, ref, body)
	if err != nil {
		writeTixErr(w, r, err)
		return
	}
	rec.Success()
	cat := &catalog{f: f, viewer: api.UserID(r), people: map[string]*Person{}}
	api.WriteJSON(w, r, http.StatusCreated, CommentView{ID: c.ID, Body: c.Body, CreatedAt: c.CreatedAt, Author: cat.person(c.AuthorActorID)})
}
