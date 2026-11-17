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
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
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
	loc := f.a.Location(userID)
	lang := f.a.Locale(userID)
	return &env{
		tc:        tc,
		actorID:   l.ActorID,
		projects:  func() ([]tix.Project, error) { return f.a.Projects(userID) },
		workflows: func() ([]tix.Workflow, error) { return f.a.Workflows(userID) },
		mention:   func(actorID string) string { return f.a.MentionFor(lang, userID, actorID) },
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
		now:    func() time.Time { return time.Now().In(loc) },
		format: f.a.Config().Format,
		lang:   lang,
	}, nil
}

func (f *Feature) url(route string) string {
	return "/plugins/" + app.PluginID + "/api/v1" + route
}

// Commands returns the feature's /tix subcommands.
func (f *Feature) Commands() []command.Sub {
	refArg := func(n *model.AutocompleteData, l *i18n.Localizer) {
		n.AddDynamicListArgument(l.T("tasks.cmd.arg_ref"), f.url(routeRefs), true)
	}
	withRef := func(hint, helpID string) func(*model.AutocompleteData, *i18n.Localizer) {
		return func(n *model.AutocompleteData, l *i18n.Localizer) {
			refArg(n, l)
			n.AddTextArgument(l.T(helpID), hint, "")
		}
	}
	return []command.Sub{
		{
			Name: "new", Aliases: []string{"add", "create"},
			Hint:   "[project] <title> [--assign @user|me] [--priority high] [--post]",
			HelpID: "tasks.cmd.new",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				n.AddDynamicListArgument(l.T("tasks.cmd.new_project"), f.url(routeProjects), false)
				n.AddTextArgument(l.T("tasks.cmd.new_title"), "<title>", "")
			},
			Run: f.cmdNew,
		},
		{
			Name: "search", Aliases: []string{"find"}, Hint: "<text> [project] [filter] [open]",
			HelpID: "tasks.cmd.search",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				n.AddTextArgument(l.T("tasks.cmd.search_text"), "<text> [project] [filter] [open]", "")
			},
			Run: f.cmdSearch,
		},
		{Name: "mine", Hint: "[filter]", HelpID: "tasks.cmd.mine", Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
			n.AddTextArgument(l.T("tasks.cmd.mine_filter"), "[filter]", "")
		}, Run: f.cmdMine},
		{Name: "due", Hint: "[project] [filter]", HelpID: "tasks.cmd.due", Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
			n.AddDynamicListArgument(l.T("tasks.cmd.due_project"), f.url(routeProjects), false)
		}, Run: f.cmdDue},
		{
			Name: "channel", Hint: "[project <key>|none]",
			HelpID: "tasks.cmd.channel",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				p := model.NewAutocompleteData("project", "<key>|none", l.T("tasks.cmd.channel_project"))
				p.AddDynamicListArgument(l.T("tasks.cmd.channel_project_key"), f.url(routeProjects)+"?channel=1", true)
				n.AddCommand(p)
			},
			Run: f.cmdChannel,
		},
		{Name: "show", Aliases: []string{"view"}, Hint: "<ref>", HelpID: "tasks.cmd.show", Autocomplete: refArg, Run: f.cmdShow},
		{
			Name: "list", Aliases: []string{"ls"}, Hint: "[mine|all|project] [filter]",
			HelpID: "tasks.cmd.list",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				n.AddDynamicListArgument(l.T("tasks.cmd.list_scope"), f.url(routeProjects)+"?list=1", false)
				n.AddTextArgument(l.T("tasks.cmd.list_filter"), "[filter]", "")
			},
			Run: f.cmdList,
		},
		{Name: "start", Hint: "<ref> [comment]", HelpID: "tasks.cmd.start", Autocomplete: withRef("[comment]", i18n.M("tasks.cmd.arg_comment")), Run: f.moveCmd(toStart)},
		{Name: "done", Hint: "<ref> [comment]", HelpID: "tasks.cmd.done", Autocomplete: withRef("[comment]", i18n.M("tasks.cmd.arg_comment")), Run: f.moveCmd(toDone)},
		{Name: "close", Aliases: []string{"cancel"}, Hint: "<ref> [comment]", HelpID: "tasks.cmd.close", Autocomplete: withRef("[comment]", i18n.M("tasks.cmd.arg_comment")), Run: f.moveCmd(toClose)},
		{Name: "reopen", Hint: "<ref> [comment]", HelpID: "tasks.cmd.reopen", Autocomplete: withRef("[comment]", i18n.M("tasks.cmd.arg_comment")), Run: f.moveCmd(toReopen)},
		{
			Name: "move", Aliases: []string{"mv"}, Hint: "<ref> <state> [comment]", HelpID: "tasks.cmd.move",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				refArg(n, l)
				n.AddDynamicListArgument(l.T("tasks.cmd.move_state"), f.url(routeStates), true)
				n.AddTextArgument(l.T("tasks.cmd.arg_comment"), "[comment]", "")
			},
			Run: f.cmdMove,
		},
		{Name: "comment", Hint: "<ref> <text>", HelpID: "tasks.cmd.comment", Autocomplete: withRef("<text>", i18n.M("tasks.cmd.comment_text")), Run: f.cmdComment},
		{Name: "assign", Hint: "<ref> <@user|me|none>", HelpID: "tasks.cmd.assign", Autocomplete: withRef("<@user|me|none>", i18n.M("tasks.cmd.assign_who")), Run: f.cmdAssign},
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
		return command.Ephemeral(app.ErrText(c.L(), err))
	}
	msg, err := fn(e)
	if err != nil {
		return command.Ephemeral(app.ErrText(c.L(), err))
	}
	return command.Ephemeral(msg)
}

func needRef(args []string, usage string) (string, error) {
	if len(args) == 0 || normRef(args[0]) == "" {
		return "", i18n.NewError("tasks.err.usage", "command", usage)
	}
	return normRef(args[0]), nil
}

func (f *Feature) cmdNew(c *command.Ctx, args []string) *model.CommandResponse {
	l := c.L()
	e, err := f.envFor(c.Args.UserId)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	na, err := parseNew(args, e.isProject)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	if na.Project == "" {
		na.Project = f.ChannelProject(c.Args.UserId, c.Args.ChannelId)
	}
	cx, cancel := ctx()
	defer cancel()
	t, err := e.create(cx, na)
	if err != nil {
		return command.Ephemeral(app.ErrText(l, err))
	}
	wf, _, _ := e.workflowFor(t.ProjectID)
	if na.Post {
		// The channel reads the post: the server's language.
		sl := f.a.ServerLocale()
		who := sl.T("tasks.someone")
		if u, appErr := f.a.API.GetUser(c.Args.UserId); appErr == nil {
			who = "@" + u.Username
		}
		post := &model.Post{
			UserId: f.a.BotID, ChannelId: c.Args.ChannelId, RootId: c.Args.RootId,
			Message: e.cardIn(sl, t, wf, i18n.M("tasks.new.posted"), "who", who),
		}
		if _, appErr := f.a.API.CreatePost(post); appErr == nil {
			return &model.CommandResponse{}
		}
		return command.Ephemeral(e.card(t, wf, i18n.M("tasks.new.created")) + "\n\n" + l.T("tasks.new.post_failed"))
	}
	return command.Ephemeral(e.card(t, wf, i18n.M("tasks.new.created")))
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
		return e.flat(cx, q, i18n.M("tasks.list.none"))
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
		return e.flat(cx, q, i18n.M("tasks.due.none"))
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
			return "", i18n.NewError("tasks.err.usage", "command", "/tix move <ref> <state> [comment]")
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
			return "", i18n.NewError("tasks.err.usage", "command", "/tix assign <ref> <@user|me|none>")
		}
		cx, cancel := ctx()
		defer cancel()
		return e.assign(cx, normRef(args[0]), args[1])
	})
}
