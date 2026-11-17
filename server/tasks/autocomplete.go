// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
)

// maxRefItems caps the reference suggestions.
const maxRefItems = 30

// The Mattermost server calls these while someone types a /tix command, with the session's
// user in Mattermost-User-ID. Any failure (not connected, tix down) answers an empty list:
// the suggestions disappear and the command itself reports the problem.

func (f *Feature) autocomplete(w http.ResponseWriter, r *http.Request, items func(cx context.Context, e *env) []model.AutocompleteListItem) {
	userID := api.UserID(r)
	if userID == "" {
		http.Error(w, "Not authorized", http.StatusUnauthorized)
		return
	}
	out := []model.AutocompleteListItem{}
	// The env speaks the requesting user's language (appEnv sets it).
	if e, err := f.envFor(userID); err == nil {
		cx, cancel := ctx()
		defer cancel()
		if got := items(cx, e); got != nil {
			out = got
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// autocompleteRefs suggests the caller's open assigned tasks, then the ones they changed lately.
func (f *Feature) autocompleteRefs(w http.ResponseWriter, r *http.Request) {
	f.autocomplete(w, r, func(cx context.Context, e *env) []model.AutocompleteListItem {
		return e.refItems(cx)
	})
}

func (e *env) refItems(cx context.Context) []model.AutocompleteListItem {
	var out []model.AutocompleteListItem
	seen := map[string]bool{}
	add := func(q url.Values) {
		page, err := e.tc.ListTasks(cx, q)
		if err != nil {
			return
		}
		for _, t := range page.Items {
			if t.CompletedAt != nil || seen[t.Ref] || len(out) >= maxRefItems {
				continue
			}
			seen[t.Ref] = true
			wf, _, _ := e.workflowFor(t.ProjectID)
			out = append(out, model.AutocompleteListItem{Item: t.Ref, Hint: excerpt(oneLine(t.Title), 60), HelpText: label(wf, t.Status)})
		}
	}
	add(url.Values{"assignee": {"me"}, "limit": {"25"}})
	add(url.Values{"creator": {"me"}, "sort": {"updated_at"}, "direction": {"desc"}, "limit": {"15"}})
	return out
}

// autocompleteProjects suggests project keys, the channel's default first; with list=1 also
// mine and all, for /tix list, and with channel=1 also none, for /tix channel project.
func (f *Feature) autocompleteProjects(w http.ResponseWriter, r *http.Request) {
	forList := r.URL.Query().Get("list") == "1"
	forChannel := r.URL.Query().Get("channel") == "1"
	def := f.ChannelProject(api.UserID(r), r.URL.Query().Get("channel_id"))
	f.autocomplete(w, r, func(_ context.Context, e *env) []model.AutocompleteListItem {
		out := e.projectItems(forList, def)
		if forChannel {
			out = append(out, model.AutocompleteListItem{Item: "none", HelpText: e.l().T("tasks.ac.none")})
		}
		return out
	})
}

// projectItems lists the visible projects; def, when set, comes first and says so.
func (e *env) projectItems(forList bool, def string) []model.AutocompleteListItem {
	var out []model.AutocompleteListItem
	if forList {
		out = append(out,
			model.AutocompleteListItem{Item: "mine", HelpText: e.l().T("tasks.ac.mine")},
			model.AutocompleteListItem{Item: "all", HelpText: e.l().T("tasks.ac.all")},
		)
	}
	ps, err := e.projects()
	if err != nil {
		return out
	}
	var rest []model.AutocompleteListItem
	for _, p := range ps {
		if p.ArchivedAt != "" {
			continue
		}
		if def != "" && strings.EqualFold(p.Key, def) {
			out = append(out, model.AutocompleteListItem{Item: p.Key, HelpText: e.l().T("tasks.ac.default_project", "name", p.Name)})
			continue
		}
		rest = append(rest, model.AutocompleteListItem{Item: p.Key, HelpText: p.Name})
	}
	return append(out, rest...)
}

// autocompleteStates suggests the states the task named earlier on the line can move to.
func (f *Feature) autocompleteStates(w http.ResponseWriter, r *http.Request) {
	ref := refFromLine(r.URL.Query().Get("parsed"))
	if ref == "" {
		ref = refFromLine(r.URL.Query().Get("user_input"))
	}
	f.autocomplete(w, r, func(cx context.Context, e *env) []model.AutocompleteListItem {
		return e.stateItems(cx, ref)
	})
}

// refFromLine picks the task reference out of a typed "/tix move <ref> ..." line.
func refFromLine(line string) string {
	fields := command.SplitArgs(line)
	if len(fields) < 3 {
		return ""
	}
	return normRef(fields[2])
}

func (e *env) stateItems(cx context.Context, ref string) []model.AutocompleteListItem {
	if ref == "" {
		return nil
	}
	t, err := e.tc.GetTask(cx, ref)
	if err != nil {
		return nil
	}
	wf, _, err := e.workflowFor(t.ProjectID)
	if err != nil {
		return nil
	}
	var out []model.AutocompleteListItem
	seen := map[string]bool{}
	for _, tr := range wf.NextStates(t.Status) {
		if seen[tr.To] {
			continue
		}
		seen[tr.To] = true
		s, ok := wf.StateByKey(tr.To)
		if !ok {
			continue
		}
		help := labelOf(s)
		if tr.RequiresComment {
			help = e.l().T("tasks.ac.needs_comment", "state", help)
		}
		out = append(out, model.AutocompleteListItem{Item: s.Key, HelpText: help})
	}
	return out
}
