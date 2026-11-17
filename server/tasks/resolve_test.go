// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// testWorkflow: todo -> doing -> review -> done (needs a comment); todo and doing -> wontdo;
// done and wontdo -> todo. review has no category, so it derives in_progress.
func testWorkflow() *tix.Workflow {
	wf := &tix.Workflow{ID: "wf1", Key: "dev", Name: "Dev"}
	wf.Definition.Initial = "todo"
	wf.Definition.States = []tix.State{
		{Key: "todo", Label: "To do", Category: "todo"},
		{Key: "doing", Label: "In progress", Category: "in_progress"},
		{Key: "review", Label: "In review"},
		{Key: "done", Label: "Done", Terminal: true},
		{Key: "wontdo", Label: "Won't do", Category: "cancelled", Terminal: true},
	}
	wf.Definition.Transitions = []tix.Transition{
		{From: "todo", To: "doing"},
		{From: "doing", To: "review"},
		{From: "review", To: "done", RequiresComment: true},
		{From: "review", To: "doing"},
		{From: "todo", To: "wontdo"},
		{From: "doing", To: "wontdo"},
		{From: "done", To: "todo"},
		{From: "wontdo", To: "todo"},
	}
	return wf
}

// simpleWorkflow has no cancelled state: open -> closed, closed -> open.
func simpleWorkflow() *tix.Workflow {
	wf := &tix.Workflow{ID: "wf2", Key: "simple", Name: "Simple"}
	wf.Definition.Initial = "open"
	wf.Definition.States = []tix.State{{Key: "open", Label: "Open"}, {Key: "closed", Label: "Closed", Terminal: true}}
	wf.Definition.Transitions = []tix.Transition{{From: "open", To: "closed"}, {From: "closed", To: "open"}}
	return wf
}

func TestCategory(t *testing.T) {
	wf := testWorkflow()
	for key, want := range map[string]string{"todo": catTodo, "doing": catInProgress, "review": catInProgress, "done": catDone, "wontdo": catCancelled} {
		s, _ := wf.StateByKey(key)
		assert.Equal(t, want, category(wf, s), key)
	}
	s := simpleWorkflow()
	open, _ := s.StateByKey("open")
	assert.Equal(t, catTodo, category(s, open))
}

func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wf       *tix.Workflow
		from     string
		in       intent
		target   string
		to       string // expected transition target
		already  bool
		comment  bool
		route    []string
		goalErr  bool
		reachLen int
	}{
		{name: "start from todo", wf: testWorkflow(), from: "todo", in: toStart, to: "doing"},
		{name: "start when started", wf: testWorkflow(), from: "doing", in: toStart, already: true},
		{name: "start from review counts as started", wf: testWorkflow(), from: "review", in: toStart, already: true},
		{name: "done needs a comment", wf: testWorkflow(), from: "review", in: toDone, to: "done", comment: true},
		{name: "done not reachable directly", wf: testWorkflow(), from: "todo", in: toDone, route: []string{"doing", "review", "done"}, reachLen: 2},
		{name: "close goes to cancelled", wf: testWorkflow(), from: "doing", in: toClose, to: "wontdo"},
		{name: "close when done", wf: testWorkflow(), from: "done", in: toClose, already: true},
		{name: "close without a cancelled state", wf: simpleWorkflow(), from: "open", in: toClose, to: "closed"},
		{name: "reopen done", wf: testWorkflow(), from: "done", in: toReopen, to: "todo"},
		{name: "reopen open", wf: testWorkflow(), from: "doing", in: toReopen, already: true},
		{name: "move by label", wf: testWorkflow(), from: "doing", in: toState, target: "In Review", to: "review"},
		{name: "move by key", wf: testWorkflow(), from: "review", in: toState, target: "doing", to: "doing"},
		{name: "move to current", wf: testWorkflow(), from: "doing", in: toState, target: "in_progress", already: true},
		{name: "move unreachable", wf: testWorkflow(), from: "todo", in: toState, target: "review", route: []string{"doing", "review"}, reachLen: 2},
		{name: "move unknown", wf: testWorkflow(), from: "todo", in: toState, target: "nope", goalErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g, err := goalFor(tc.wf, tc.in, tc.target)
			if tc.goalErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "States:")
				return
			}
			require.NoError(t, err)
			r := resolve(tc.wf, tc.from, g)
			assert.Equal(t, tc.already, r.Already, "already")
			if tc.to == "" {
				assert.Nil(t, r.Transition)
			} else {
				require.NotNil(t, r.Transition)
				assert.Equal(t, tc.to, r.Transition.To)
				assert.Equal(t, tc.comment, r.Transition.RequiresComment)
			}
			var route []string
			for _, s := range r.Route {
				route = append(route, s.Key)
			}
			assert.Equal(t, tc.route, route)
			if tc.reachLen > 0 {
				assert.Len(t, r.Reachable, tc.reachLen)
			}
		})
	}
}

func TestTerminalKeys(t *testing.T) {
	other := simpleWorkflow()
	other.Definition.States = append(other.Definition.States, tix.State{Key: "done", Label: "Done but open"})
	assert.Equal(t, []string{"closed", "wontdo"}, terminalKeys([]tix.Workflow{*testWorkflow(), *other}))
}
