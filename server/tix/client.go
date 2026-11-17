// SPDX-License-Identifier: AGPL-3.0-only

// Package tix is a small client for the tix HTTP API (/api/v1). It marshals and
// nothing else: validation and rules belong to the tix server.
package tix

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client calls one tix server as the holder of one token.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client for baseURL (the server root, without /api/v1).
func New(baseURL, token string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Token:   token,
		HTTP:    &http.Client{Timeout: 15 * time.Second},
	}
}

// Error is a non-2xx answer from tix, decoded from its {"error":{code,message}} envelope.
type Error struct {
	Status  int
	Code    string
	Message string
}

type errorEnvelope struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (e *Error) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("tix: %d %s", e.Status, e.Message)
	}
	return fmt.Sprintf("tix: %d", e.Status)
}

// IsStatus reports whether err is a tix answer with the given HTTP status.
func IsStatus(err error, status int) bool {
	var te *Error
	return errors.As(err, &te) && te.Status == status
}

// Do sends one request and decodes a JSON answer into out when out is not nil.
func (c *Client) Do(ctx context.Context, method, path string, query url.Values, in, out any) error {
	u := c.BaseURL + "/api/v1" + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encoding %s %s: %w", method, path, err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("tix %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("reading tix answer: %w", err)
	}
	if resp.StatusCode >= 300 {
		te := &Error{Status: resp.StatusCode}
		var env errorEnvelope
		if json.Unmarshal(raw, &env) == nil {
			te.Code, te.Message = env.Error.Code, env.Error.Message
		}
		if te.Message == "" {
			te.Message = strings.TrimSpace(string(raw))
			if len(te.Message) > 300 {
				te.Message = te.Message[:300]
			}
		}
		return te
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("decoding tix %s %s: %w", method, path, err)
	}
	return nil
}

// Page is one keyset page of a listing.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
	Total      *int   `json:"total"`
}

// Actor is a person or agent in a tenant (GET /whoami, /actors).
type Actor struct {
	ID          string   `json:"id"`
	AccountID   string   `json:"account_id"`
	Handle      string   `json:"handle"`
	DisplayName string   `json:"display_name"`
	Email       string   `json:"email"`
	Kind        string   `json:"kind"`
	Role        string   `json:"role"`
	TenantID    string   `json:"tenant_id"`
	TenantKey   string   `json:"tenant_key"`
	TenantName  string   `json:"tenant_name"`
	TokenID     string   `json:"token_id"`
	Scopes      []string `json:"scopes"`
}

// Name is the best human label for an actor.
func (a Actor) Name() string {
	switch {
	case a.DisplayName != "":
		return a.DisplayName
	case a.Handle != "":
		return a.Handle
	default:
		return a.ID
	}
}

// Task is a tix task. Fields the plugin does not use are omitted.
type Task struct {
	ID               string         `json:"id"`
	Ref              string         `json:"ref"`
	Title            string         `json:"title"`
	Body             string         `json:"body"`
	Status           string         `json:"status"`
	Priority         string         `json:"priority"`
	ProjectID        string         `json:"project_id"`
	AssigneeActorID  string         `json:"assignee_actor_id"`
	CreatorActorID   string         `json:"creator_actor_id"`
	ClaimedByActorID string         `json:"claimed_by_actor_id"`
	Tags             []string       `json:"tags"`
	Blocked          bool           `json:"blocked"`
	DueAt            *time.Time     `json:"due_at"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	CompletedAt      *time.Time     `json:"completed_at"`
	Version          int64          `json:"version"`
	WebURL           string         `json:"web_url"`
	CustomFields     map[string]any `json:"custom_fields"`
}

// Project is a tix project.
type Project struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Color       string `json:"color"`
	WorkflowID  string `json:"workflow_id"`
	ArchivedAt  string `json:"archived_at"`
}

// State is one workflow state.
type State struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Category string `json:"category"`
	Terminal bool   `json:"terminal"`
}

// Transition is one allowed move between states.
type Transition struct {
	From            string `json:"from"`
	To              string `json:"to"`
	RequiresComment bool   `json:"requires_comment"`
	RequiresScope   string `json:"requires_scope"`
}

// Workflow is a tenant's state machine for tasks.
type Workflow struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Name       string `json:"name"`
	Definition struct {
		Initial     string       `json:"initial"`
		States      []State      `json:"states"`
		Transitions []Transition `json:"transitions"`
	} `json:"definition"`
}

// StateByKey returns a state of the workflow.
func (w Workflow) StateByKey(key string) (State, bool) {
	for _, s := range w.Definition.States {
		if s.Key == key {
			return s, true
		}
	}
	return State{}, false
}

// NextStates returns the transitions allowed out of from.
func (w Workflow) NextStates(from string) []Transition {
	var out []Transition
	for _, t := range w.Definition.Transitions {
		if t.From == from {
			out = append(out, t)
		}
	}
	return out
}

// Comment is a comment on a task.
type Comment struct {
	ID            string    `json:"id"`
	TaskID        string    `json:"task_id"`
	AuthorActorID string    `json:"author_actor_id"`
	Body          string    `json:"body"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateTask is the body of POST /tasks.
type CreateTask struct {
	Project  string   `json:"project_ref,omitempty"`
	Title    string   `json:"title"`
	Body     string   `json:"body,omitempty"`
	Assignee string   `json:"assignee_actor_id,omitempty"`
	Priority string   `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Status   string   `json:"status,omitempty"`
}

// WhoAmI returns the actor the token belongs to.
func (c *Client) WhoAmI(ctx context.Context) (*Actor, error) {
	var a Actor
	return &a, c.Do(ctx, http.MethodGet, "/whoami", nil, nil, &a)
}

// GetTask reads one task by reference, e.g. infra-42.
func (c *Client) GetTask(ctx context.Context, ref string) (*Task, error) {
	var t Task
	return &t, c.Do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(ref), nil, nil, &t)
}

// ListTasks lists tasks; q carries the filters of GET /tasks (project, status, assignee, q, limit...).
func (c *Client) ListTasks(ctx context.Context, q url.Values) (*Page[Task], error) {
	var p Page[Task]
	return &p, c.Do(ctx, http.MethodGet, "/tasks", q, nil, &p)
}

// CreateTask creates a task.
func (c *Client) CreateTask(ctx context.Context, in CreateTask) (*Task, error) {
	var t Task
	return &t, c.Do(ctx, http.MethodPost, "/tasks", nil, in, &t)
}

// UpdateTask patches a task with the fields of UpdateTaskInput (title, body, assignee_actor_id, priority, tags...).
func (c *Client) UpdateTask(ctx context.Context, ref string, fields map[string]any) (*Task, error) {
	var t Task
	return &t, c.Do(ctx, http.MethodPatch, "/tasks/"+url.PathEscape(ref), nil, fields, &t)
}

// Transition moves a task to state to, with an optional comment.
func (c *Client) Transition(ctx context.Context, ref, to, comment string) (*Task, error) {
	in := map[string]any{"to": to}
	if comment != "" {
		in["comment"] = comment
	}
	var t Task
	return &t, c.Do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(ref)+"/transition", nil, in, &t)
}

// AddComment comments on a task.
func (c *Client) AddComment(ctx context.Context, ref, body string) (*Comment, error) {
	var cm Comment
	return &cm, c.Do(ctx, http.MethodPost, "/tasks/"+url.PathEscape(ref)+"/comments", nil, map[string]any{"body": body}, &cm)
}

// ListComments lists a task's comments.
func (c *Client) ListComments(ctx context.Context, ref string, q url.Values) (*Page[Comment], error) {
	var p Page[Comment]
	return &p, c.Do(ctx, http.MethodGet, "/tasks/"+url.PathEscape(ref)+"/comments", q, nil, &p)
}

// ListProjects lists the projects the token can see.
func (c *Client) ListProjects(ctx context.Context) ([]Project, error) {
	var all []Project
	q := url.Values{"limit": {"500"}}
	for {
		var p Page[Project]
		if err := c.Do(ctx, http.MethodGet, "/projects", q, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if p.NextCursor == "" {
			return all, nil
		}
		q.Set("cursor", p.NextCursor)
	}
}

// ListWorkflows lists the tenant's workflows.
func (c *Client) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	var p Page[Workflow]
	return p.Items, c.Do(ctx, http.MethodGet, "/workflows", nil, nil, &p)
}

// GetActor reads one actor by id.
func (c *Client) GetActor(ctx context.Context, id string) (*Actor, error) {
	var a Actor
	return &a, c.Do(ctx, http.MethodGet, "/actors/"+url.PathEscape(id), nil, nil, &a)
}

// ListActors lists the tenant's actors (people and agents).
func (c *Client) ListActors(ctx context.Context) ([]Actor, error) {
	var all []Actor
	q := url.Values{"limit": {"500"}}
	for {
		var p Page[Actor]
		if err := c.Do(ctx, http.MethodGet, "/actors", q, nil, &p); err != nil {
			return nil, err
		}
		all = append(all, p.Items...)
		if p.NextCursor == "" {
			return all, nil
		}
		q.Set("cursor", p.NextCursor)
	}
}

// IssuedToken is a freshly minted API token; Token is shown exactly once.
type IssuedToken struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Token  string   `json:"token"`
	Scopes []string `json:"scopes"`
}

// CreateToken mints a token for actorID (empty: the caller). The caller needs token:admin.
func (c *Client) CreateToken(ctx context.Context, name, actorID string, scopes []string) (*IssuedToken, error) {
	in := map[string]any{"name": name, "scopes": scopes}
	if actorID != "" {
		in["actor_id"] = actorID
	}
	var t IssuedToken
	return &t, c.Do(ctx, http.MethodPost, "/tokens", nil, in, &t)
}

// RevokeToken deletes a token by id.
func (c *Client) RevokeToken(ctx context.Context, id string) error {
	return c.Do(ctx, http.MethodDelete, "/tokens/"+url.PathEscape(id), nil, nil, nil)
}

// MemberScopes are what a tix member role grants; a minted personal token never carries more.
var MemberScopes = []string{
	"task:read", "task:write", "task:transition", "task:claim",
	"project:read", "workflow:read", "comment:write", "artifact:write",
	"event:subscribe", "audit:read",
}

// ViewerScopes are what a tix viewer role grants.
var ViewerScopes = []string{"task:read", "project:read", "workflow:read", "event:subscribe", "audit:read"}
