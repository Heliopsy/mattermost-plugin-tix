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
	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

// Commands are /tix digest and /tix reminders.
func (f *Feature) Commands() []command.Sub {
	return []command.Sub{
		{
			Name: "digest", Hint: "on [HH:MM]|off|now|status|days mon-fri|daily|quiet on|off",
			HelpID: "digest.cmd.digest",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				on := model.NewAutocompleteData("on", "[HH:MM]", l.T("digest.cmd.digest_on"))
				n.AddCommand(on)
				n.AddCommand(model.NewAutocompleteData("off", "", l.T("digest.cmd.digest_off")))
				n.AddCommand(model.NewAutocompleteData("now", "", l.T("digest.cmd.digest_now")))
				n.AddCommand(model.NewAutocompleteData("status", "", l.T("digest.cmd.digest_status")))
				days := model.NewAutocompleteData("days", "mon-fri|daily", l.T("digest.cmd.digest_days"))
				days.AddStaticListArgument("", true, []model.AutocompleteListItem{{Item: "mon-fri", HelpText: l.T("digest.cmd.days_mon_fri")}, {Item: "daily", HelpText: l.T("digest.cmd.days_daily")}})
				n.AddCommand(days)
				quiet := model.NewAutocompleteData("quiet", "on|off", l.T("digest.cmd.digest_quiet"))
				quiet.AddStaticListArgument("", true, []model.AutocompleteListItem{{Item: "on"}, {Item: "off"}})
				n.AddCommand(quiet)
			},
			Run: f.digestCmd,
		},
		{
			Name: "reminders", Hint: "on|off|status", HelpID: "digest.cmd.reminders",
			Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
				n.AddCommand(model.NewAutocompleteData("on", "", l.T("digest.cmd.reminders_on")))
				n.AddCommand(model.NewAutocompleteData("off", "", l.T("digest.cmd.reminders_off")))
				n.AddCommand(model.NewAutocompleteData("status", "", l.T("digest.cmd.reminders_status")))
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
		return nil, app.ErrText(c.L(), err)
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
		return nil, c.L().T("digest.cmd.save_failed")
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
				return command.Ephemeral(app.ErrText(c.L(), err))
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
			return command.Ephemeral(c.L().T("digest.cmd.usage"))
		}
		switch strings.ToLower(args[1]) {
		case "mon-fri", "workdays", "weekdays":
			change = func(r *record) { r.Daily = false }
		case "daily", "all", "every":
			change = func(r *record) { r.Daily = true }
		default:
			return command.Ephemeral(c.L().T("digest.cmd.days_usage"))
		}
	case "quiet":
		if len(args) < 2 {
			return command.Ephemeral(c.L().T("digest.cmd.usage"))
		}
		switch strings.ToLower(args[1]) {
		case "on":
			change = func(r *record) { r.Quiet = true }
		case "off":
			change = func(r *record) { r.Quiet = false }
		default:
			return command.Ephemeral(c.L().T("digest.cmd.quiet_usage"))
		}
	default:
		return command.Ephemeral(c.L().T("digest.cmd.usage"))
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
		return command.Ephemeral(c.L().T("digest.cmd.reminders_usage"))
	}
	if _, msg := f.set(c, "tixRemindersSettings", func(r *record) { r.Reminders = on }); msg != "" {
		return command.Ephemeral(msg)
	}
	return command.Ephemeral(f.status(c.Args.UserId))
}

// status describes a person's settings, in their language.
func (f *Feature) status(userID string) string {
	l := f.a.Locale(userID)
	r, _, err := f.load(userID)
	if err != nil {
		return l.T("digest.cmd.read_failed")
	}
	loc := f.location(userID)
	var b strings.Builder
	if r.Digest {
		days := l.T("digest.status.workdays")
		if r.Daily {
			days = l.T("digest.status.every_day")
		}
		quiet := l.T("digest.status.empty_sent")
		if r.Quiet {
			quiet = l.T("digest.status.empty_skipped")
		}
		b.WriteString(l.T("digest.status.on", "days", days, "at", r.at(), "zone", loc.String(), "quiet", quiet))
		if !r.NextRun.IsZero() {
			b.WriteString(" " + l.T("digest.status.next_run", "when", f.renderer(userID, loc, f.now()).DateTime(r.NextRun)))
		}
	} else {
		b.WriteString(l.T("digest.status.off"))
	}
	if r.Reminders {
		b.WriteString("\n" + l.T("digest.status.reminders_on", "at", r.at(), "from", fmt.Sprintf("%02d:00", checkFrom), "until", fmt.Sprintf("%02d:00", checkUntil)))
	} else {
		b.WriteString("\n" + l.T("digest.status.reminders_off"))
	}
	b.WriteString("\n" + l.T("digest.status.separate"))
	return b.String()
}

// sendNow DMs the digest at once without touching the schedule or what counts as new.
func (f *Feature) sendNow(userID string) string {
	l := f.a.Locale(userID)
	tc, _, err := f.a.TixFor(userID)
	if err != nil {
		return app.ErrText(l, err)
	}
	r, _, err := f.load(userID)
	if err != nil {
		return l.T("digest.cmd.read_failed")
	}
	items, err := f.fetch(userID, tc, time.Time{})
	if err != nil {
		return app.ErrText(l, err)
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
		return l.T("digest.cmd.send_failed")
	}
	return l.T("digest.cmd.sent")
}
