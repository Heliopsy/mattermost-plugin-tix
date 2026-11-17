// SPDX-License-Identifier: AGPL-3.0-only

// Package notify receives tix webhooks: it posts project activity into subscribed channels, DMs people about their tasks and redraws task cards.
package notify

import (
	"net/http"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// Refresher redraws task cards (the cards feature).
type Refresher interface{ Refresh(ref string) }

// Feature is the notify feature.
type Feature struct {
	a     *app.App
	cards Refresher
	now   func() time.Time

	mu        sync.Mutex
	seen      map[string]time.Time
	projects  map[string]tix.Project
	workflows map[string]tix.Workflow
	cachedAt  time.Time
}

// seenTTL is how long a delivered event id is remembered, longer than tix's retry schedule.
const seenTTL = 72 * time.Hour

// New returns the feature.
func New(a *app.App, cards Refresher) *Feature {
	return &Feature{a: a, cards: cards, now: time.Now, seen: map[string]time.Time{}}
}

// Commands are /tix webhook, subscribe, unsubscribe, subscriptions and notifications.
func (f *Feature) Commands() []command.Sub {
	return []command.Sub{
		{
			Name: "webhook", Hint: "setup|status|remove", Help: "Register, check or remove the webhook tix sends events to", AdminOnly: true,
			Autocomplete: func(n *model.AutocompleteData) {
				n.AddCommand(model.NewAutocompleteData("setup", "", "Register or update the plugin's webhook in tix"))
				n.AddCommand(model.NewAutocompleteData("status", "", "Show the webhook and its recent deliveries"))
				n.AddCommand(model.NewAutocompleteData("remove", "", "Remove the plugin's webhook from tix"))
			},
			Run: f.webhookCmd,
		},
		{
			Name: "subscribe", Hint: "<project|all> [created transitioned assigned commented done deleted]",
			Help: "Post a project's tix activity in this channel", Run: f.subscribe,
		},
		{Name: "unsubscribe", Hint: "<project|all>", Help: "Stop posting a project's tix activity in this channel", Run: f.unsubscribe},
		{Name: "subscriptions", Hint: "[all]", Help: "List the tix feeds of this channel", Run: f.subscriptions},
		{
			Name: "notifications", Hint: "on|off", Help: "Turn your personal tix DMs on or off",
			Autocomplete: func(n *model.AutocompleteData) {
				n.AddCommand(model.NewAutocompleteData("on", "", "Get DMs about your tasks"))
				n.AddCommand(model.NewAutocompleteData("off", "", "Stop DMs about your tasks"))
			},
			Run: f.notifications,
		},
	}
}

// Routes mounts the webhook receiver. It has no Mattermost session; the signature proves the caller.
func (f *Feature) Routes(h *api.Handler) {
	h.Public.HandleFunc("/webhook", f.webhook).Methods(http.MethodPost)
}

// firstSight records an event id and reports whether it is new. The KV record makes it hold
// across cluster nodes; the in-memory map spares a KV write for a quick repeat.
func (f *Feature) firstSight(id string) bool {
	now := f.now()
	f.mu.Lock()
	for k, t := range f.seen {
		if now.Sub(t) > time.Hour {
			delete(f.seen, k)
		}
	}
	if _, ok := f.seen[id]; ok {
		f.mu.Unlock()
		return false
	}
	f.seen[id] = now
	f.mu.Unlock()
	if f.a == nil || f.a.Store == nil {
		return true
	}
	ok, err := f.a.Store.Set("notify", "seen_"+id, 1, pluginapi.SetAtomic(nil), pluginapi.SetExpiry(seenTTL))
	if err != nil {
		return true
	}
	return ok
}

// projectAndWorkflow returns a project by id and its workflow, cached briefly.
func (f *Feature) projectAndWorkflow(c *tix.Client, projectID string) (*tix.Project, *tix.Workflow) {
	f.mu.Lock()
	if f.now().Sub(f.cachedAt) > 2*time.Minute {
		f.projects, f.workflows = nil, nil
	}
	p, ok := f.projects[projectID]
	f.mu.Unlock()
	if !ok {
		ctx, cancel := app.Ctx()
		defer cancel()
		ps, err := c.ListProjects(ctx)
		if err != nil {
			return nil, nil
		}
		ws, _ := c.ListWorkflows(ctx)
		f.mu.Lock()
		if f.projects == nil {
			f.projects, f.workflows, f.cachedAt = map[string]tix.Project{}, map[string]tix.Workflow{}, f.now()
		}
		for _, x := range ps {
			f.projects[x.ID] = x
		}
		for _, w := range ws {
			f.workflows[w.ID] = w
		}
		p, ok = f.projects[projectID]
		f.mu.Unlock()
		if !ok {
			return nil, nil
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.workflows[p.WorkflowID]; ok {
		return &p, &w
	}
	return &p, nil
}
