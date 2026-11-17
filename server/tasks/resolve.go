// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"fmt"
	"strings"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// Workflow state categories (tix StateCategory).
const (
	catTodo       = "todo"
	catInProgress = "in_progress"
	catBlocked    = "blocked"
	catWaiting    = "waiting"
	catDone       = "done"
	catCancelled  = "cancelled"
)

// category is a state's category, derived the way tix derives an empty one: done when
// terminal, todo for the initial state, in progress otherwise.
func category(wf *tix.Workflow, s tix.State) string {
	switch {
	case s.Category != "":
		return s.Category
	case s.Terminal:
		return catDone
	case s.Key == wf.Definition.Initial:
		return catTodo
	default:
		return catInProgress
	}
}

// categoryRank orders status groups: work in hand first, finished work last.
func categoryRank(c string) int {
	switch c {
	case catInProgress:
		return 0
	case catBlocked:
		return 1
	case catWaiting:
		return 2
	case catTodo:
		return 3
	case catDone:
		return 4
	case catCancelled:
		return 5
	}
	return 6
}

// label is a state's display name, falling back to its key.
func label(wf *tix.Workflow, key string) string {
	if wf != nil {
		if s, ok := wf.StateByKey(key); ok && s.Label != "" {
			return s.Label
		}
	}
	return key
}

// intent is what a command wants to do with a task's status.
type intent int

const (
	toStart intent = iota
	toDone
	toClose
	toReopen
	toState
)

func (i intent) String() string {
	return [...]string{"start", "done", "close", "reopen", "move"}[i]
}

// goal describes the states that satisfy an intent.
type goal struct {
	in   intent
	want func(tix.State) bool
	// whatID names the goal for messages, e.g. "a done state"; whatState is the state of a /tix move.
	whatID    string
	whatState string
	// already tells whether the task is already where the intent leads; nil means want.
	already func(tix.State) bool
	// prefer is a state key picked first when several transitions fit.
	prefer string
}

// goalFor builds the goal of an intent; target is the state named by /tix move.
func goalFor(wf *tix.Workflow, in intent, target string) (*goal, error) {
	g := &goal{in: in}
	switch in {
	case toStart:
		g.want = func(s tix.State) bool { return category(wf, s) == catInProgress }
		g.whatID = i18n.M("tasks.goal.in_progress")
	case toDone:
		g.want = func(s tix.State) bool { return category(wf, s) == catDone }
		g.whatID = i18n.M("tasks.goal.done")
	case toClose:
		hasCancelled := false
		for _, s := range wf.Definition.States {
			if s.Terminal && category(wf, s) == catCancelled {
				hasCancelled = true
			}
		}
		g.already = func(s tix.State) bool { return s.Terminal }
		if hasCancelled {
			g.want = func(s tix.State) bool { return s.Terminal && category(wf, s) == catCancelled }
			g.whatID = i18n.M("tasks.goal.cancelled")
		} else {
			g.want = func(s tix.State) bool { return category(wf, s) == catDone }
			g.whatID = i18n.M("tasks.goal.done_no_cancelled")
		}
	case toReopen:
		g.want = func(s tix.State) bool { return !s.Terminal }
		g.whatID = i18n.M("tasks.goal.open")
		g.prefer = wf.Definition.Initial
	case toState:
		s, ok := findState(wf, target)
		if !ok {
			return nil, i18n.NewError("tasks.err.no_state", "state", target, "states", stateList(i18n.For(i18n.Fallback), wf, wf.Definition.States))
		}
		g.want = func(x tix.State) bool { return x.Key == s.Key }
		g.whatID, g.whatState = i18n.M("tasks.goal.state"), labelOf(s)
	}
	return g, nil
}

// describe names the goal in language l.
func (g *goal) describe(l *i18n.Localizer) string { return l.T(g.whatID, "state", g.whatState) }

// findState matches a state by key or label, ignoring case, spaces, hyphens and underscores.
func findState(wf *tix.Workflow, name string) (tix.State, bool) {
	n := squash(name)
	for _, s := range wf.Definition.States {
		if squash(s.Key) == n {
			return s, true
		}
	}
	for _, s := range wf.Definition.States {
		if squash(s.Label) == n {
			return s, true
		}
	}
	return tix.State{}, false
}

func squash(s string) string {
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(strings.ToLower(strings.TrimSpace(s)))
}

func labelOf(s tix.State) string {
	if s.Label != "" {
		return s.Label
	}
	return s.Key
}

func stateList(l *i18n.Localizer, wf *tix.Workflow, states []tix.State) string {
	if len(states) == 0 {
		return l.T("tasks.states.none")
	}
	out := make([]string, 0, len(states))
	for _, s := range states {
		if s.Label != "" && !strings.EqualFold(s.Label, s.Key) {
			out = append(out, fmt.Sprintf("`%s` (%s)", s.Key, s.Label))
		} else {
			out = append(out, "`"+s.Key+"`")
		}
	}
	return strings.Join(out, ", ")
}

// resolution is the outcome of resolving an intent against a task's workflow.
type resolution struct {
	// Transition is the move to make; set when one is allowed directly.
	Transition *tix.Transition
	// Already is set when the task is already where the intent leads.
	Already bool
	// Reachable are the states the task can move to directly.
	Reachable []tix.State
	// Route is the shortest chain of states to the goal when it is not directly reachable.
	Route []tix.State
}

// resolve finds the transition that carries out an intent from the task's current status.
func resolve(wf *tix.Workflow, from string, g *goal) *resolution {
	r := &resolution{}
	already := g.already
	if already == nil {
		already = g.want
	}
	if cur, ok := wf.StateByKey(from); ok && already(cur) {
		r.Already = true
		return r
	}
	var fits []tix.Transition
	seen := map[string]bool{}
	for _, t := range wf.NextStates(from) {
		s, ok := wf.StateByKey(t.To)
		if !ok {
			continue
		}
		if !seen[t.To] {
			seen[t.To] = true
			r.Reachable = append(r.Reachable, s)
		}
		if g.want(s) {
			fits = append(fits, t)
		}
	}
	if len(fits) > 0 {
		pick := fits[0]
		for _, t := range fits {
			if t.To == g.prefer {
				pick = t
				break
			}
		}
		r.Transition = &pick
		return r
	}
	r.Route = route(wf, from, g.want)
	return r
}

// route is the shortest chain of states from from to a state satisfying want, excluding from,
// or nil when there is none.
func route(wf *tix.Workflow, from string, want func(tix.State) bool) []tix.State {
	prev := map[string]string{from: ""}
	queue := []string{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, t := range wf.NextStates(cur) {
			if _, ok := prev[t.To]; ok {
				continue
			}
			prev[t.To] = cur
			s, ok := wf.StateByKey(t.To)
			if !ok {
				continue
			}
			if want(s) {
				var path []tix.State
				for k := t.To; k != from; k = prev[k] {
					st, _ := wf.StateByKey(k)
					path = append([]tix.State{st}, path...)
				}
				return path
			}
			queue = append(queue, t.To)
		}
	}
	return nil
}
