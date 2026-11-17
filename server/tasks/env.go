// SPDX-License-Identifier: AGPL-3.0-only

package tasks

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// env is everything one command needs, acting as one Mattermost user. The feature builds it
// from the app; tests build it around an httptest tix.
type env struct {
	tc      *tix.Client
	actorID string
	// projects and workflows are what the user can see, cached by the app.
	projects  func() ([]tix.Project, error)
	workflows func() ([]tix.Workflow, error)
	// mention renders a tix actor, as an @mention when they are linked.
	mention func(actorID string) string
	taskURL func(*tix.Task) string
	// mmActor maps a Mattermost username to the tix actor linked to it: ok is false when no
	// such Mattermost user exists, and err is set when the user exists but is not linked.
	mmActor func(username string) (actorID string, ok bool, err error)
	now     func() time.Time
}

// isProject tells whether a word is the key of a project the user can see.
func (e *env) isProject(word string) bool {
	ps, err := e.projects()
	if err != nil {
		return false
	}
	for _, p := range ps {
		if strings.EqualFold(p.Key, word) && p.ArchivedAt == "" {
			return true
		}
	}
	return false
}

// workflowFor returns the workflow of a project, by id or key.
func (e *env) workflowFor(projectID string) (*tix.Workflow, *tix.Project, error) {
	ps, err := e.projects()
	if err != nil {
		return nil, nil, err
	}
	var proj *tix.Project
	for i := range ps {
		if ps[i].ID == projectID || strings.EqualFold(ps[i].Key, projectID) {
			proj = &ps[i]
			break
		}
	}
	if proj == nil {
		return nil, nil, fmt.Errorf("project %s not found", projectID)
	}
	ws, err := e.workflows()
	if err != nil {
		return nil, proj, err
	}
	for i := range ws {
		if ws[i].ID == proj.WorkflowID {
			return &ws[i], proj, nil
		}
	}
	return nil, proj, fmt.Errorf("the workflow of project %s is not visible to you", proj.Key)
}

// errUnlinkedUser is returned when an @mention names a Mattermost user without a tix link.
var errUnlinkedUser = errors.New("has not connected tix")

// actorFor turns what a person typed for an assignee into what tix takes: me is the caller,
// none (or nobody) is nobody, @user is the tix account linked to that Mattermost user, and
// anything else is a tix handle.
func (e *env) actorFor(who string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(who)) {
	case "":
		return "", errors.New("say who: @user, me or none")
	case "me", "@me":
		return e.actorID, nil
	case "none", "nobody", "-":
		return "", nil
	}
	if name, ok := strings.CutPrefix(who, "@"); ok {
		id, found, err := e.mmActor(name)
		if err != nil {
			return "", fmt.Errorf("@%s %w; they can run `/tix connect`, or name their tix handle without the @", name, errUnlinkedUser)
		}
		if found {
			return id, nil
		}
		return name, nil
	}
	return who, nil
}

// filterActor is actorFor for list filters, where tix takes me as is.
func (e *env) filterActor(who string) (string, error) {
	if strings.EqualFold(who, "@me") || strings.EqualFold(who, "me") {
		return "me", nil
	}
	return e.actorFor(who)
}

// ctx returns a context for one round of tix calls.
func ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 20*time.Second)
}

// linkActor resolves a Mattermost user to their tix actor through the store link.
func linkActor(st *store.Store, userID string) (string, error) {
	l, err := st.GetLink(userID)
	if err != nil {
		return "", err
	}
	return l.ActorID, nil
}
