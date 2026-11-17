// SPDX-License-Identifier: AGPL-3.0-only

package render

import "time"

// Due is a task's due date as people mean it. tix stores a bare due date (no time of day) as
// midnight UTC of that day and treats it as a floating date: the same calendar day in every time
// zone (tix docs/calendar.md, "Dates and time zones"). Any other instant is a due time.
type Due struct {
	At time.Time
	// DateOnly is true for a bare due date: the whole day, in whatever zone the reader is in.
	DateOnly bool
}

// DueOf reads a task's due_at; ok is false when the task has none.
func DueOf(at *time.Time) (Due, bool) {
	if at == nil || at.IsZero() {
		return Due{}, false
	}
	u := at.UTC()
	return Due{At: u, DateOnly: u.Hour() == 0 && u.Minute() == 0 && u.Second() == 0 && u.Nanosecond() == 0}, true
}

// Date is the calendar day the task is due on for a reader in loc, as midnight UTC of that day:
// a bare date is its own day everywhere, a due time falls on its local date.
func (d Due) Date(loc *time.Location) time.Time {
	if d.DateOnly {
		return d.At
	}
	return civil(d.At.In(locOrUTC(loc)))
}

// Overdue says whether the task is late at now for a reader in loc: a bare date once its day is
// over there, a due time once it has passed.
func (d Due) Overdue(now time.Time, loc *time.Location) bool {
	if d.DateOnly {
		return d.At.Before(Today(now, loc))
	}
	return d.At.Before(now)
}

// DueToday says whether the task is due later today for a reader in loc (and not yet overdue).
func (d Due) DueToday(now time.Time, loc *time.Location) bool {
	return !d.Overdue(now, loc) && d.Date(loc).Equal(Today(now, loc))
}

// Today is the reader's calendar day at now, as midnight UTC of that day.
func Today(now time.Time, loc *time.Location) time.Time {
	return civil(now.In(locOrUTC(loc)))
}

// DueTodayBound is an inclusive due_before bound for a tix listing that returns every task due
// today or earlier for a reader in loc, bare dates and due times alike. A listing fetched with it
// still needs Overdue or DueToday to drop the few tasks it lets through from tomorrow.
func DueTodayBound(now time.Time, loc *time.Location) time.Time {
	loc = locOrUTC(loc)
	l := now.In(loc)
	endOfDay := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc).Add(-time.Second)
	if today := Today(now, loc); today.After(endOfDay) {
		return today
	}
	return endOfDay
}

// TodayFrom is an inclusive due_after bound for a listing of the tasks due today for a reader in
// loc; filter its result with IsToday.
func TodayFrom(now time.Time, loc *time.Location) time.Time {
	loc = locOrUTC(loc)
	l := now.In(loc)
	start := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	if today := Today(now, loc); today.Before(start) {
		return today
	}
	return start
}

// IsToday says whether the task falls on the reader's today, overdue or not.
func (d Due) IsToday(now time.Time, loc *time.Location) bool {
	return d.Date(loc).Equal(Today(now, loc))
}

// OverdueBound is an inclusive due_before bound for a listing of overdue tasks for a reader in
// loc; filter its result with Overdue.
func OverdueBound(now time.Time, loc *time.Location) time.Time {
	lastBareDay := Today(now, loc).Add(-time.Second)
	if lastBareDay.After(now) {
		return lastBareDay
	}
	return now
}

// civil is the calendar date of t in its own location, as midnight UTC.
func civil(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func locOrUTC(loc *time.Location) *time.Location {
	if loc == nil {
		return time.UTC
	}
	return loc
}
