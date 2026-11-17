// SPDX-License-Identifier: AGPL-3.0-only

// Package tasks is /tix for working tasks: new, show, list, search, mine, due, start, done,
// close, move, reopen, comment, assign and channel, tasks made from a reaction, and the
// autocomplete lists behind them. Every call acts as the person who caused it, through their
// own tix token.
package tasks

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/api"
	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
	"github.com/heliopsy/mattermost-plugin-tix/server/store"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

// Autocomplete routes, under the plugin's /api/v1.
const (
	routeRefs     = "/autocomplete/refs"
	routeProjects = "/autocomplete/projects"
	routeStates   = "/autocomplete/states"
)

// Feature is the tasks feature.
type Feature struct {
	a *app.App
	// envFor builds the environment of one user; tests replace it.
	envFor func(userID string) (*env, error)
}

// New returns the feature.
func New(a *app.App) *Feature {
	f := &Feature{a: a}
	f.envFor = f.appEnv
	return f
}

// appEnv builds a user's environment from the shared app.
func (f *Feature) appEnv(userID string) (*env, error) {
	tc, l, err := f.a.TixFor(userID)
	if err != nil {
		return nil, err
	}
	return &env{
		tc:        tc,
		actorID:   l.ActorID,
		projects:  func() ([]tix.Project, error) { return f.a.Projects(userID) },
		workflows: func() ([]tix.Workflow, error) { return f.a.Workflows(userID) },
		mention:   func(actorID string) string { return f.a.MentionFor(userID, actorID) },
		taskURL:   f.a.TaskURL,
		mmActor: func(username string) (string, bool, error) {
			u, appErr := f.a.API.GetUserByUsername(strings.ToLower(username))
			if appErr != nil || u == nil {
				return "", false, nil
			}
			id, err := linkActor(f.a.Store, u.Id)
			if errors.Is(err, store.ErrNotFound) {
				return "", true, errUnlinkedUser
			} else if err != nil {
				return "", true, err
			}
			return id, true, nil
		},
		now: time.Now,
	}, nil
}

func (f *Feature) url(route string) string {
	return "/plugins/" + app.PluginID + "/api/v1" + route
}

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub {
	refArg := func(n *model.AutocompleteData) {
		n.AddDynamicListArgument("Task reference, e.g. infra-42", f.url(routeRefs), true)
	}
	withRef := func(hint, help string) func(*model.AutocompleteData) {
		return func(n *model.AutocompleteData) {
			refArg(n)
			n.AddTextArgument(help, hint, "")
		}
	}
	return []command.Sub{
		{
			Name: "new", Aliases: []string{"add", "create"},
			Hint: "[project] <title> [--assign @user|me] [--priority high] [--post]",
			Help: "Create a task, in the channel's default project unless you name one; the title takes tix quick-add tokens: !high #tag @user due:fri",
			Autocomplete: func(n *model.AutocompleteData) {
				n.AddDynamicListArgument("Project (optional; the channel's default is listed first)", f.url(routeProjects), false)
				n.AddTextArgument("Title, with optional !priority #tag @user due:fri --post", "<title>", "")
			},
			Run: f.cmdNew,
		},
		{
			Name: "search", Aliases: []string{"find"}, Hint: "<text> [project] [filter] [open]",
			Help: "Full-text search over titles and bodies, finished tasks included unless you add open",
			Autocomplete: func(n *model.AutocompleteData) {
				n.AddTextArgument("Text, then optionally a project, filter terms (status:todo tag:ops @user) and open", "<text> [project] [filter] [open]", "")
			},
			Run: f.cmdSearch,
		},
		{Name: "mine", Hint: "[filter]", Help: "Your open tasks (same as /tix list mine)", Autocomplete: func(n *model.AutocompleteData) {
			n.AddTextArgument("tix filter terms: status:todo tag:ops priority:high", "[filter]", "")
		}, Run: f.cmdMine},
		{Name: "due", Hint: "[project] [filter]", Help: "Your open tasks due today or overdue, soonest first", Autocomplete: func(n *model.AutocompleteData) {
			n.AddDynamicListArgument("Project (optional)", f.url(routeProjects), false)
		}, Run: f.cmdDue},
		{
			Name: "channel", Hint: "[project <key>|none]",
			Help: "Show this channel's tix settings; channel admins set its default project for /tix new and the create reaction",
			Autocomplete: func(n *model.AutocompleteData) {
				p := model.NewAutocompleteData("project", "<key>|none", "Set or clear this channel's default project (channel admins)")
				p.AddDynamicListArgument("Project key, or none", f.url(routeProjects)+"?channel=1", true)
				n.AddCommand(p)
			},
			Run: f.cmdChannel,
		},
		{Name: "show", Aliases: []string{"view"}, Hint: "<ref>", Help: "Show a task with its latest comments", Autocomplete: refArg, Run: f.cmdShow},
		{
			Name: "list", Aliases: []string{"ls"}, Hint: "[mine|all|project] [filter]",
			Help: "List open tasks: yours by default, a project's, or a tix filter such as status:todo tag:ops",
			Autocomplete: func(n *model.AutocompleteData) {
				n.AddDynamicListArgument("mine, all or a project", f.url(routeProjects)+"?list=1", false)
				n.AddTextArgument("tix filter terms: status:todo tag:ops priority:high @user due:week title~api", "[filter]", "")
			},
			Run: f.cmdList,
		},
		{Name: "start", Hint: "<ref> [comment]", Help: "Move a task to its in-progress state", Autocomplete: withRef("[comment]", "Optional comment"), Run: f.moveCmd(toStart)},
		{Name: "done", Hint: "<ref> [comment]", Help: "Move a task to its done state", Autocomplete: withRef("[comment]", "Optional comment"), Run: f.moveCmd(toDone)},
		{Name: "close", Aliases: []string{"cancel"}, Hint: "<ref> [comment]", Help: "Close a task without doing it (cancelled state, else done)", Autocomplete: withRef("[comment]", "Optional comment"), Run: f.moveCmd(toClose)},
		{Name: "reopen", Hint: "<ref> [comment]", Help: "Move a finished task back to an open state", Autocomplete: withRef("[comment]", "Optional comment"), Run: f.moveCmd(toReopen)},
		{
			Name: "move", Aliases: []string{"mv"}, Hint: "<ref> <state> [comment]", Help: "Move a task to a state of its workflow",
			Autocomplete: func(n *model.AutocompleteData) {
				refArg(n)
				n.AddDynamicListArgument("Target state", f.url(routeStates), true)
				n.AddTextArgument("Optional comment", "[comment]", "")
			},
			Run: f.cmdMove,
		},
		{Name: "comment", Hint: "<ref> <text>", Help: "Comment on a task", Autocomplete: withRef("<text>", "Comment text (Markdown)"), Run: f.cmdComment},
		{Name: "assign", Hint: "<ref> <@user|me|none>", Help: "Assign a task, or unassign it with none", Autocomplete: withRef("<@user|me|none>", "Who: @user, me, none or a tix handle"), Run: f.cmdAssign},
	}
}

// Routes mounts the autocomplete lists.
func (f *Feature) Routes(h *api.Handler) {
	h.User.HandleFunc(routeRefs, f.autocompleteRefs).Methods(http.MethodGet)
	h.User.HandleFunc(routeProjects, f.autocompleteProjects).Methods(http.MethodGet)
	h.User.HandleFunc(routeStates, f.autocompleteStates).Methods(http.MethodGet)
}

// withEnv runs a command body as the caller and turns its answer into an ephemeral reply.
func (f *Feature) withEnv(c *command.Ctx, fn func(e *env) (string, error)) *model.CommandResponse {
	e, err := f.envFor(c.Args.UserId)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	msg, err := fn(e)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	return command.Ephemeral(msg)
}

func needRef(args []string, usage string) (string, error) {
	if len(args) == 0 || normRef(args[0]) == "" {
		return "", errors.New("usage: `" + usage + "`")
	}
	return normRef(args[0]), nil
}

func (f *Feature) cmdNew(c *command.Ctx, args []string) *model.CommandResponse {
	e, err := f.envFor(c.Args.UserId)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	na, err := parseNew(args, e.isProject)
	if err != nil {
		return command.Ephemeral(err.Error())
	}
	if na.Project == "" {
		na.Project = f.ChannelProject(c.Args.UserId, c.Args.ChannelId)
	}
	cx, cancel := ctx()
	defer cancel()
	t, err := e.create(cx, na)
	if err != nil {
		return command.Ephemeral(app.ErrText(err))
	}
	wf, _, _ := e.workflowFor(t.ProjectID)
	if na.Post {
		who := "Someone"
		if u, appErr := f.a.API.GetUser(c.Args.UserId); appErr == nil {
			who = "@" + u.Username
		}
		post := &model.Post{
			UserId: f.a.BotID, ChannelId: c.Args.ChannelId, RootId: c.Args.RootId,
			Message: e.card(t, wf, who+" created "),
		}
		if _, appErr := f.a.API.CreatePost(post); appErr == nil {
			return &model.CommandResponse{}
		}
		return command.Ephemeral(e.card(t, wf, "Created ") + "\n\n(Posting it to the channel failed.)")
	}
	return command.Ephemeral(e.card(t, wf, "Created "))
}

func (f *Feature) cmdShow(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		ref, err := needRef(args, "/tix show <ref>")
		if err != nil {
			return "", err
		}
		cx, cancel := ctx()
		defer cancel()
		return e.show(cx, ref)
	})
}

func (f *Feature) cmdList(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		q, err := parseList(args, e.isProject, e.filterActor, e.now())
		if err != nil {
			return "", err
		}
		cx, cancel := ctx()
		defer cancel()
		return e.list(cx, q)
	})
}

func (f *Feature) cmdSearch(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		q, err := parseSearch(args, e.isProject, e.filterActor, e.now())
		if err != nil {
			return "", err
		}
		cx, cancel := ctx()
		defer cancel()
		return e.flat(cx, q, "No tasks match.")
	})
}

func (f *Feature) cmdMine(c *command.Ctx, args []string) *model.CommandResponse {
	return f.cmdList(c, append([]string{"mine"}, args...))
}

func (f *Feature) cmdDue(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		q, err := parseDue(args, e.isProject, e.filterActor, e.now())
		if err != nil {
			return "", err
		}
		cx, cancel := ctx()
		defer cancel()
		return e.flat(cx, q, "Nothing of yours is due today or overdue.")
	})
}

func (f *Feature) moveCmd(in intent) func(*command.Ctx, []string) *model.CommandResponse {
	return func(c *command.Ctx, args []string) *model.CommandResponse {
		return f.withEnv(c, func(e *env) (string, error) {
			ref, err := needRef(args, fmt.Sprintf("/tix %s <ref> [comment]", in))
			if err != nil {
				return "", err
			}
			cx, cancel := ctx()
			defer cancel()
			return e.move(cx, ref, in, "", restAfter(c.Args.Command, 3))
		})
	}
}

func (f *Feature) cmdMove(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		if len(args) < 2 {
			return "", errors.New("usage: `/tix move <ref> <state> [comment]`")
		}
		cx, cancel := ctx()
		defer cancel()
		return e.move(cx, normRef(args[0]), toState, args[1], restAfter(c.Args.Command, 4))
	})
}

func (f *Feature) cmdComment(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		ref, err := needRef(args, "/tix comment <ref> <text>")
		if err != nil {
			return "", err
		}
		cx, cancel := ctx()
		defer cancel()
		return e.comment(cx, ref, restAfter(c.Args.Command, 3))
	})
}

func (f *Feature) cmdAssign(c *command.Ctx, args []string) *model.CommandResponse {
	return f.withEnv(c, func(e *env) (string, error) {
		if len(args) < 2 {
			return "", errors.New("usage: `/tix assign <ref> <@user|me|none>`")
		}
		cx, cancel := ctx()
		defer cancel()
		return e.assign(cx, normRef(args[0]), args[1])
	})
}
