// SPDX-License-Identifier: AGPL-3.0-only

package digest

import (
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/app"
	"github.com/heliopsy/mattermost-plugin-tix/server/render"
	"github.com/heliopsy/mattermost-plugin-tix/server/tix"
)

const (
	maxPerTick = 50               // people handled per tick; the rest wait for the next one
	workers    = 4                // people handled at once, so one slow tix call stalls nobody else
	tickBudget = 50 * time.Second // no new person is started after this, so ticks do not pile up
	grace      = 3 * time.Hour    // a morning run later than this is skipped, not sent late
	retryAfter = 5 * time.Minute  // after a transient tix failure

	dueTodayTTL = 48 * time.Hour
	overdueTTL  = 2 * recentDays * 24 * time.Hour

	kindDueToday = "today"
	kindOverdue  = "overdue"
)

// Tick is the job's callback: it handles the people whose morning run or overdue check is due.
func (f *Feature) Tick() {
	began := time.Now() // the budget is wall time, whatever the feature's clock says
	start := f.now()
	ids, err := f.index()
	if err != nil {
		f.a.API.LogWarn("tix digest: reading the index", "err", err.Error())
		return
	}
	var due []string
	for _, id := range ids {
		r, _, err := f.load(id)
		if err != nil {
			f.a.API.LogWarn("tix digest: reading a record", "user_id", id, "err", err.Error())
			continue
		}
		if r.dueAt(start) {
			due = append(due, id)
		}
		if len(due) == maxPerTick {
			break
		}
	}
	queue := make(chan string)
	var wg sync.WaitGroup
	for range min(workers, len(due)) {
		wg.Go(func() {
			for id := range queue {
				f.safeRun(id)
			}
		})
	}
	for _, id := range due {
		if time.Since(began) > tickBudget {
			break
		}
		queue <- id
	}
	close(queue)
	wg.Wait()
}

// dueAt says whether the record has work at now; a record with no schedule yet is due so it gets one.
func (r *record) dueAt(now time.Time) bool {
	if !r.active() {
		return false
	}
	if r.NextRun.IsZero() {
		return true
	}
	if !now.Before(r.NextRun) && !now.Before(r.RetryAt) {
		return true
	}
	return r.Reminders && !r.NextCheck.IsZero() && !now.Before(r.NextCheck)
}

func (f *Feature) safeRun(userID string) {
	defer func() {
		if x := recover(); x != nil {
			f.a.API.LogError("tix digest: panic", "user_id", userID, "panic", fmt.Sprint(x))
		}
	}()
	if err := f.run(userID); err != nil && !errors.Is(err, errChanged) {
		f.a.API.LogWarn("tix digest: run failed", "user_id", userID, "err", err.Error())
	}
}

// outcome is what one run decided to send.
type outcome struct {
	digest   string
	dueToday []item // reminders, claimed before sending
	overdue  []item
	silent   []item // tasks the digest already shows, claimed so no reminder repeats them
}

// run does one person's morning run or overdue check. The new schedule is saved before
// anything is sent, so a crash or a second node can lose a message but never repeat one.
func (f *Feature) run(userID string) error {
	now := f.now()
	r, _, err := f.load(userID)
	if err != nil {
		return err
	}
	if !r.dueAt(now) {
		return nil
	}
	loc := f.location(userID)
	seenRun, seenCheck := r.NextRun, r.NextCheck
	guard := func(cur *record) error {
		if !cur.NextRun.Equal(seenRun) || !cur.NextCheck.Equal(seenCheck) {
			return errChanged
		}
		return nil
	}

	if r.NextRun.IsZero() {
		_, err = f.update(userID, func(cur *record) error {
			if gerr := guard(cur); gerr != nil {
				return gerr
			}
			cur.NextRun = nextSlot(now, loc, cur.at())
			cur.NextCheck = nextCheck(now, loc, cur.NextRun)
			return nil
		})
		return err
	}

	morning := !now.Before(r.NextRun)
	if morning && now.Sub(r.NextRun) > grace {
		_, err = f.update(userID, func(cur *record) error {
			if gerr := guard(cur); gerr != nil {
				return gerr
			}
			cur.NextRun = nextSlot(now, loc, cur.at())
			cur.NextCheck = nextCheck(now, loc, cur.NextRun)
			cur.RetryAt = time.Time{}
			return nil
		})
		return err
	}

	tc, _, err := f.a.TixFor(userID)
	if errors.Is(err, app.ErrNotLinked) {
		f.forget(userID)
		return nil
	} else if err != nil {
		return err
	}
	var dueBefore time.Time
	if !morning {
		dueBefore = render.OverdueBound(now, loc)
	}
	items, err := f.fetch(userID, tc, dueBefore)
	if err != nil {
		permanent := tix.IsStatus(err, 401) || tix.IsStatus(err, 403)
		_, uerr := f.update(userID, func(cur *record) error {
			if gerr := guard(cur); gerr != nil {
				return gerr
			}
			switch {
			case morning && permanent:
				cur.NextRun = nextSlot(now, loc, cur.at())
				cur.NextCheck = nextCheck(now, loc, cur.NextRun)
				cur.RetryAt = time.Time{}
			case morning:
				cur.RetryAt = now.Add(retryAfter)
			default:
				cur.NextCheck = nextCheck(now, loc, cur.NextRun)
			}
			return nil
		})
		return errors.Join(err, uerr)
	}

	var out outcome
	_, err = f.update(userID, func(cur *record) error {
		if gerr := guard(cur); gerr != nil {
			return gerr
		}
		out = f.decide(cur, items, now, loc, morning)
		return nil
	})
	if err != nil {
		return err
	}
	return f.deliver(userID, &out, loc)
}

// decide fills in what to send and moves the record's schedule on.
func (f *Feature) decide(r *record, items []item, now time.Time, loc *time.Location, morning bool) outcome {
	var out outcome
	recent := now.Add(-recentDays * 24 * time.Hour)
	if morning {
		slot := r.NextRun
		var seen []string
		if r.Baseline {
			seen = r.Seen
			if seen == nil {
				seen = []string{}
			}
		}
		s := classify(items, now, loc, seen)
		if r.Digest && digestDay(slot.In(loc), r.Daily) && !sameDay(r.LastDigest, slot, loc) {
			if !s.empty() || !r.Quiet {
				out.digest = f.renderDigest(s, now, loc)
			}
			r.LastDigest = now
			r.Seen, r.Baseline = s.ids, true
			if r.Reminders {
				out.silent = append(slices.Clone(s.DueToday), s.Overdue...)
			}
		} else if r.Reminders {
			out.dueToday = s.DueToday
			for _, it := range s.Overdue {
				if it.t.DueAt.After(recent) {
					out.overdue = append(out.overdue, it)
				}
			}
		}
		r.NextRun = nextSlot(now, loc, r.at())
		r.RetryAt = time.Time{}
		r.NextCheck = nextCheck(now, loc, r.NextRun)
		return out
	}
	for _, it := range items {
		if due, ok := render.DueOf(it.t.DueAt); ok && due.Overdue(now, loc) && due.At.After(recent) {
			out.overdue = append(out.overdue, it)
		}
	}
	r.NextCheck = nextCheck(now, loc, r.NextRun)
	return out
}

// deliver claims the reminders and sends one DM.
func (f *Feature) deliver(userID string, out *outcome, loc *time.Location) error {
	for _, it := range out.silent {
		kind := kindOf(it, f.now(), loc)
		f.claim(userID, it.t.ID, kind, *it.t.DueAt, ttlOf(kind))
	}
	var today, overdue []item
	for _, it := range out.dueToday {
		if f.claim(userID, it.t.ID, kindDueToday, *it.t.DueAt, dueTodayTTL) {
			today = append(today, it)
		}
	}
	for _, it := range out.overdue {
		if f.claim(userID, it.t.ID, kindOverdue, *it.t.DueAt, overdueTTL) {
			overdue = append(overdue, it)
		}
	}
	msg := out.digest
	if rem := f.renderReminders(today, overdue, loc); rem != "" {
		if msg != "" {
			msg += "\n\n"
		}
		msg += rem
	}
	if msg == "" {
		return nil
	}
	return f.a.DM(userID, &model.Post{Message: msg})
}

func kindOf(it item, now time.Time, loc *time.Location) string {
	if due, ok := render.DueOf(it.t.DueAt); ok && due.Overdue(now, loc) {
		return kindOverdue
	}
	return kindDueToday
}

func ttlOf(kind string) time.Duration {
	if kind == kindOverdue {
		return overdueTTL
	}
	return dueTodayTTL
}
