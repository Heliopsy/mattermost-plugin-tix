// SPDX-License-Identifier: AGPL-3.0-only

// Package command runs /tix. Features add subcommands with Registry.Add; this
// package owns only the trigger, the dispatch and the help.
package command

import (
	"fmt"
	"sort"
	"strings"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"
	"github.com/mattermost/mattermost/server/public/pluginapi"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

// Trigger is the slash command.
const Trigger = "tix"

// Ctx is one invocation.
type Ctx struct {
	App    *app.App
	Args   *model.CommandArgs
	Plugin *plugin.Context

	lang *i18n.Localizer
}

// Sub is one subcommand, e.g. /tix new. Name, Aliases and Hint are syntax and stay English;
// HelpID is the message id of what it does.
type Sub struct {
	Name    string
	Aliases []string
	Hint    string
	HelpID  string
	// AdminOnly hides the subcommand from people who are not system admins.
	AdminOnly bool
	// Autocomplete adds argument suggestions to the subcommand's node, in l's language.
	Autocomplete func(n *model.AutocompleteData, l *i18n.Localizer)
	Run          func(c *Ctx, args []string) *model.CommandResponse
}

// L is the language of the person who ran the command: replies to them use it.
func (c *Ctx) L() *i18n.Localizer {
	if c.lang == nil {
		if c.App == nil || c.App.API == nil || c.Args == nil {
			c.lang = i18n.For(i18n.Fallback)
		} else {
			c.lang = c.App.Locale(c.Args.UserId)
		}
	}
	return c.lang
}

// Registry holds every subcommand.
type Registry struct {
	app  *app.App
	subs []Sub
}

// NewRegistry returns an empty registry.
func NewRegistry(a *app.App) *Registry { return &Registry{app: a} }

// Add registers subcommands.
func (r *Registry) Add(subs ...Sub) { r.subs = append(r.subs, subs...) }

// Register tells the server about /tix with autocomplete for every subcommand. The server keeps
// one autocomplete for everyone, so it is in the server's default language (app.ServerLocale).
func (r *Registry) Register(client *pluginapi.Client) error {
	return client.SlashCommand.Register(r.command(r.app.ServerLocale()))
}

// command is /tix with its autocomplete in l's language.
func (r *Registry) command(l *i18n.Localizer) *model.Command {
	root := model.NewAutocompleteData(Trigger, "[command]", l.T("command.root"))
	for _, s := range r.sorted() {
		node := model.NewAutocompleteData(s.Name, s.Hint, l.T(s.HelpID))
		if s.AdminOnly {
			node.RoleID = model.SystemAdminRoleId
		}
		if s.Autocomplete != nil {
			s.Autocomplete(node, l)
		}
		root.AddCommand(node)
	}
	root.AddCommand(model.NewAutocompleteData("help", "", l.T("command.help")))
	return &model.Command{
		Trigger:          Trigger,
		AutoComplete:     true,
		AutoCompleteDesc: l.T("command.root"),
		AutoCompleteHint: "[command]",
		DisplayName:      "tix",
		AutocompleteData: root,
	}
}

func (r *Registry) sorted() []Sub {
	out := append([]Sub(nil), r.subs...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) find(name string) (Sub, bool) {
	for _, s := range r.subs {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
		for _, a := range s.Aliases {
			if strings.EqualFold(a, name) {
				return s, true
			}
		}
	}
	return Sub{}, false
}

// Execute runs one /tix invocation.
func (r *Registry) Execute(pc *plugin.Context, args *model.CommandArgs) *model.CommandResponse {
	c := &Ctx{App: r.app, Args: args, Plugin: pc}
	fields := SplitArgs(args.Command)
	if len(fields) < 2 || strings.EqualFold(fields[1], "help") {
		return Ephemeral(r.help(c))
	}
	s, ok := r.find(fields[1])
	if !ok {
		return Ephemeral(c.L().T("command.unknown", "command", fields[1]) + "\n\n" + r.help(c))
	}
	if s.AdminOnly && !r.app.API.HasPermissionTo(args.UserId, model.PermissionManageSystem) {
		return Ephemeral(c.L().T("command.admins_only"))
	}
	return s.Run(c, fields[2:])
}

// help is the command table in the caller's language.
func (r *Registry) help(c *Ctx) string {
	l := c.L()
	admin := r.app.API.HasPermissionTo(c.Args.UserId, model.PermissionManageSystem)
	var b strings.Builder
	fmt.Fprintf(&b, "#### tix\n| %s | %s |\n| --- | --- |\n", l.T("command.help_command"), l.T("command.help_what"))
	for _, s := range r.sorted() {
		if s.AdminOnly && !admin {
			continue
		}
		fmt.Fprintf(&b, "| `/tix %s %s` | %s |\n", s.Name, s.Hint, l.T(s.HelpID))
	}
	return b.String()
}

// Ephemeral is a response only the caller sees.
func Ephemeral(text string) *model.CommandResponse {
	return &model.CommandResponse{ResponseType: model.CommandResponseTypeEphemeral, Text: text}
}

// SplitArgs splits a command line on spaces, keeping "double quoted" parts together.
func SplitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	inQuote, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			inQuote = !inQuote
			has = true
		case (r == ' ' || r == '\t' || r == '\n') && !inQuote:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		out = append(out, cur.String())
	}
	return out
}
