// SPDX-License-Identifier: AGPL-3.0-only

package digest

import (
	"fmt"
	"strings"
	"time"

	"github.com/mattermost/mattermost/server/public/model"
	"github.com/mattermost/mattermost/server/public/plugin"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/command"
)

const (
	digestUsage    = "Usage: `/tix digest on [HH:MM]|off|now|status|days mon-fri|daily|quiet on|off`"
	remindersUsage = "Usage: `/tix reminders on|off|status`"
)

// Commands are /tix digest and /tix reminders.
func (f *Feature) Commands() []command.Sub {
	return []command.Sub{
		{
			Name: "digest", Hint: "on [HH:MM]|off|now|status|days mon-fri|daily|quiet on|off",
			Help: "Get a daily DM summary of your tix tasks",
			Autocomplete: func(n *model.AutocompleteData) {
				on := model.NewAutocompleteData("on", "[HH:MM]", "Send it every workday at this time (default 09:00, your time zone)")
				n.AddCommand(on)
				n.AddCommand(model.NewAutocompleteData("off", "", "Stop the digest"))
				n.AddCommand(model.NewAutocompleteData("now", "", "Send the digest now"))
				n.AddCommand(model.NewAutocompleteData("status", "", "Show your digest settings"))
				days := model.NewAutocompleteData("days", "mon-fri|daily", "Workdays only, or every day")
				days.AddStaticListArgument("", true, []model.AutocompleteListItem{{Item: "mon-fri", HelpText: "Monday to Friday"}, {Item: "daily", HelpText: "Every day"}})
				n.AddCommand(days)
				quiet := model.NewAutocompleteData("quiet", "on|off", "Skip a digest with nothing to report")
				quiet.AddStaticListArgument("", true, []model.AutocompleteListItem{{Item: "on"}, {Item: "off"}})
				n.AddCommand(quiet)
			},
			Run: f.digestCmd,
		},
		{
			Name: "reminders", Hint: "on|off|status", Help: "Get a DM when a task assigned to you is due today or overdue",
			Autocomplete: func(n *model.AutocompleteData) {
				n.AddCommand(model.NewAutocompleteData("on", "", "Remind me of due and overdue tasks"))
				n.AddCommand(model.NewAutocompleteData("off", "", "Stop due reminders"))
				n.AddCommand(model.NewAutocompleteData("status", "", "Show whether reminders are on"))
			},
			Run: f.remindersCmd,
		},
	}
}

func (f *Feature) audit(c *command.Ctx, event string) (*model.AuditRecord, func()) {
	pc := c.Plugin
	if pc == nil {
		pc = &plugin.Context{}
	}
	rec := plugin.MakeAuditRecordWithContext(event, model.AuditStatusFail, pc, c.Args.UserId, "/tix")
	return rec, func() { f.a.API.LogAuditRec(rec) }
}

// set changes a person's settings and resets the schedule, after checking they are linked.
func (f *Feature) set(c *command.Ctx, event string, change func(r *record)) (*record, string) {
	userID := c.Args.UserId
	rec, done := f.audit(c, event)
	defer done()
	if _, _, err := f.a.TixFor(userID); err != nil {
		return nil, app.ErrText(app.EN, err)
	}
	loc := f.location(userID)
	now := f.now()
	r, err := f.update(userID, func(r *record) error {
		change(r)
		if r.active() {
			r.NextRun = nextSlot(now, loc, r.at())
			r.NextCheck = nextCheck(now, loc, r.NextRun)
			r.RetryAt = time.Time{}
		}
		return nil
	})
	if err != nil {
		f.a.API.LogError("tix digest: saving settings", "user_id", userID, "err", err.Error())
		return nil, "Could not save that."
	}
	rec.Success()
	return r, ""
}

func (f *Feature) digestCmd(c *command.Ctx, args []string) *model.CommandResponse {
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	var change func(r *record)
	switch sub {
	case "status":
		return command.Ephemeral(f.status(c.Args.UserId))
	case "now":
		return command.Ephemeral(f.sendNow(c.Args.UserId))
	case "on":
		at := ""
		if len(args) > 1 {
			var err error
			if at, err = parseClock(args[1]); err != nil {
				return command.Ephemeral(err.Error())
			}
		}
		change = func(r *record) {
			r.Digest = true
			if at != "" {
				r.At = at
			}
		}
	case "off":
		change = func(r *record) { r.Digest = false }
	case "days":
		if len(args) < 2 {
			return command.Ephemeral(digestUsage)
		}
		switch strings.ToLower(args[1]) {
		case "mon-fri", "workdays", "weekdays":
			change = func(r *record) { r.Daily = false }
		case "daily", "all", "every":
			change = func(r *record) { r.Daily = true }
		default:
			return command.Ephemeral("Use `/tix digest days mon-fri` or `/tix digest days daily`.")
		}
	case "quiet":
		if len(args) < 2 {
			return command.Ephemeral(digestUsage)
		}
		switch strings.ToLower(args[1]) {
		case "on":
			change = func(r *record) { r.Quiet = true }
		case "off":
			change = func(r *record) { r.Quiet = false }
		default:
			return command.Ephemeral("Use `/tix digest quiet on` or `/tix digest quiet off`.")
		}
	default:
		return command.Ephemeral(digestUsage)
	}
	if _, msg := f.set(c, "tixDigestSettings", change); msg != "" {
		return command.Ephemeral(msg)
	}
	return command.Ephemeral(f.status(c.Args.UserId))
}

func (f *Feature) remindersCmd(c *command.Ctx, args []string) *model.CommandResponse {
	sub := "status"
	if len(args) > 0 {
		sub = strings.ToLower(args[0])
	}
	var on bool
	switch sub {
	case "status":
		return command.Ephemeral(f.status(c.Args.UserId))
	case "on":
		on = true
	case "off":
	default:
		return command.Ephemeral(remindersUsage)
	}
	if _, msg := f.set(c, "tixRemindersSettings", func(r *record) { r.Reminders = on }); msg != "" {
		return command.Ephemeral(msg)
	}
	return command.Ephemeral(f.status(c.Args.UserId))
}

// status describes a person's settings.
func (f *Feature) status(userID string) string {
	r, _, err := f.load(userID)
	if err != nil {
		return "Could not read your digest settings."
	}
	loc := f.location(userID)
	var b strings.Builder
	if r.Digest {
		days := "Monday to Friday"
		if r.Daily {
			days = "every day"
		}
		quiet := "an empty one says All clear"
		if r.Quiet {
			quiet = "empty ones are skipped"
		}
		fmt.Fprintf(&b, "Your tix digest is **on**: %s at %s (%s); %s.", days, r.at(), loc, quiet)
		if !r.NextRun.IsZero() {
			fmt.Fprintf(&b, " Next run: %s.", f.renderer(userID, loc, f.now()).DateTime(r.NextRun))
		}
	} else {
		b.WriteString("Your tix digest is **off**. `/tix digest on [HH:MM]` turns it on.")
	}
	if r.Reminders {
		fmt.Fprintf(&b, "\nDue reminders are **on**: tasks due today in the morning run at %s, overdue tasks between %02d:00 and %02d:00.", r.at(), checkFrom, checkUntil)
	} else {
		b.WriteString("\nDue reminders are **off**. `/tix reminders on` turns them on.")
	}
	b.WriteString("\nThese are separate from `/tix notifications`, which covers DMs about changes to your tasks.")
	return b.String()
}

// sendNow DMs the digest at once without touching the schedule or what counts as new.
func (f *Feature) sendNow(userID string) string {
	tc, _, err := f.a.TixFor(userID)
	if err != nil {
		return app.ErrText(app.EN, err)
	}
	r, _, err := f.load(userID)
	if err != nil {
		return "Could not read your digest settings."
	}
	items, err := f.fetch(userID, tc, time.Time{})
	if err != nil {
		return app.ErrText(app.EN, err)
	}
	loc := f.location(userID)
	var seen []string
	if r.Baseline {
		seen = r.Seen
		if seen == nil {
			seen = []string{}
		}
	}
	now := f.now()
	if err := f.a.DM(userID, &model.Post{Message: f.renderDigest(classify(items, now, loc, seen), f.renderer(userID, loc, now))}); err != nil {
		f.a.API.LogWarn("tix digest: sending", "user_id", userID, "err", err.Error())
		return "Could not send the digest."
	}
	return "Sent your digest to your DM with @tix."
}
