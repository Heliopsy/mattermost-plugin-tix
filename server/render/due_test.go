// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	require.NoError(t, err)
	return loc
}

func TestDueOf(t *testing.T) {
	_, ok := DueOf(nil)
	assert.False(t, ok)
	bare := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	d, ok := DueOf(&bare)
	require.True(t, ok)
	assert.True(t, d.DateOnly)
	timed := time.Date(2026, 11, 17, 9, 30, 0, 0, time.UTC)
	d, _ = DueOf(&timed)
	assert.False(t, d.DateOnly)
	// Midnight in another zone is a due time, not a bare date.
	sofiaMidnight := time.Date(2026, 11, 17, 0, 0, 0, 0, mustLoc(t, "Europe/Sofia"))
	d, _ = DueOf(&sofiaMidnight)
	assert.False(t, d.DateOnly)
}

func TestDueCalendar(t *testing.T) {
	sofia := mustLoc(t, "Europe/Sofia")     // UTC+2 in November
	la := mustLoc(t, "America/Los_Angeles") // UTC-8 in November
	bare := func(y int, m time.Month, d int) Due {
		at := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
		v, _ := DueOf(&at)
		return v
	}
	timed := func(at time.Time) Due {
		v, _ := DueOf(&at)
		return v
	}
	for _, tc := range []struct {
		name           string
		due            Due
		now            time.Time
		loc            *time.Location
		overdue, today bool
		date           string
	}{
		// The reported bug: a task due today was shown as overdue because midnight UTC had passed.
		{"bare today, Sofia morning", bare(2026, 11, 17), time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC), sofia, false, true, "2026-11-17"},
		{"bare today, UTC evening", bare(2026, 11, 17), time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC), time.UTC, false, true, "2026-11-17"},
		{"bare yesterday", bare(2026, 11, 16), time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC), sofia, true, false, "2026-11-16"},
		// 01:00 in Sofia on the 18th is still the 17th in UTC: the 17th is over for the reader.
		{"bare yesterday, Sofia just after midnight", bare(2026, 11, 17), time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC), sofia, true, false, "2026-11-17"},
		// 20:00 on the 16th in Los Angeles is the 17th in UTC: a task due on the 17th is not due yet.
		{"bare tomorrow, LA evening", bare(2026, 11, 17), time.Date(2026, 11, 17, 4, 0, 0, 0, time.UTC), la, false, false, "2026-11-17"},
		{"timed later today", timed(time.Date(2026, 11, 17, 15, 0, 0, 0, time.UTC)), time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), sofia, false, true, "2026-11-17"},
		{"timed passed", timed(time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC)), time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), sofia, true, false, "2026-11-17"},
		// 23:30 UTC is already the 18th in Sofia.
		{"timed falls on the local date", timed(time.Date(2026, 11, 17, 23, 30, 0, 0, time.UTC)), time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC), sofia, false, false, "2026-11-18"},
		{"nil location is UTC", bare(2026, 11, 17), time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC), nil, false, true, "2026-11-17"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.overdue, tc.due.Overdue(tc.now, tc.loc), "overdue")
			assert.Equal(t, tc.today, tc.due.DueToday(tc.now, tc.loc), "due today")
			assert.Equal(t, tc.date, tc.due.Date(tc.loc).Format("2006-01-02"))
		})
	}
}

func TestBounds(t *testing.T) {
	sofia := mustLoc(t, "Europe/Sofia")
	la := mustLoc(t, "America/Los_Angeles")
	now := time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC) // 09:00 in Sofia, 23:00 on the 16th in LA
	assert.Equal(t, "2026-11-17T21:59:59Z", DueTodayBound(now, sofia).UTC().Format(time.RFC3339), "the end of the Sofia day")
	assert.Equal(t, "2026-11-17T07:59:59Z", DueTodayBound(now, la).UTC().Format(time.RFC3339), "the end of the LA day")
	assert.Equal(t, "2026-11-17T23:59:59Z", DueTodayBound(now, time.UTC).Format(time.RFC3339))
	// LA's today is the 16th; a bare date of the 16th sits at midnight UTC, after the LA day began.
	assert.Equal(t, "2026-11-16T00:00:00Z", TodayFrom(now, la).UTC().Format(time.RFC3339))
	assert.Equal(t, "2026-11-16T22:00:00Z", TodayFrom(now, sofia).UTC().Format(time.RFC3339))
	assert.Equal(t, now, OverdueBound(now, sofia))
	late := time.Date(2026, 11, 17, 23, 0, 0, 0, time.UTC) // 01:00 on the 18th in Sofia
	assert.Equal(t, "2026-11-17T23:59:59Z", OverdueBound(late, sofia).UTC().Format(time.RFC3339), "the bare 17th is overdue in Sofia")
}
