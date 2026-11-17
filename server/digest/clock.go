// SPDX-License-Identifier: AGPL-3.0-only

package digest

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

const (
	// checkFrom and checkUntil bound the local hours of the hourly overdue checks, so an
	// overdue reminder never arrives in the night; what became overdue overnight waits for the morning.
	checkFrom  = 8
	checkUntil = 21
)

// parseClock reads HH:MM (or H:MM, or HH).
func parseClock(s string) (string, error) {
	h, m, ok := strings.Cut(strings.TrimSpace(s), ":")
	if !ok {
		m = "0"
	}
	hh, err1 := strconv.Atoi(h)
	mm, err2 := strconv.Atoi(m)
	if err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 || len(m) > 2 {
		return "", i18n.NewError("digest.err.bad_clock", "value", s)
	}
	return fmt.Sprintf("%02d:%02d", hh, mm), nil
}

func clockOf(at string) (int, int) {
	c, err := parseClock(at)
	if err != nil {
		c = defaultAt
	}
	h, _ := strconv.Atoi(c[:2])
	m, _ := strconv.Atoi(c[3:])
	return h, m
}

// nextSlot is the first instant after t whose wall clock in loc reads at. On a day when at does
// not exist (spring forward) it is the end of the gap; on a day it happens twice (fall back) the first.
func nextSlot(t time.Time, loc *time.Location, at string) time.Time {
	h, m := clockOf(at)
	lt := t.In(loc)
	for d := range 3 {
		s := time.Date(lt.Year(), lt.Month(), lt.Day()+d, h, m, 0, 0, loc)
		// In a spring-forward gap time.Date may land before it; walk to the first instant after.
		for i := 0; i < 180 && wallBefore(s.In(loc), h, m); i++ {
			s = s.Add(time.Minute)
		}
		if s.After(t) {
			return s
		}
	}
	return t.Add(24 * time.Hour)
}

// nextCheck is the next overdue check after t: an hour on, inside the waking hours, never later than the morning run.
func nextCheck(t time.Time, loc *time.Location, morning time.Time) time.Time {
	n := t.Add(time.Hour).In(loc)
	switch {
	case n.Hour() >= checkUntil:
		n = time.Date(n.Year(), n.Month(), n.Day()+1, checkFrom, 0, 0, 0, loc)
	case n.Hour() < checkFrom:
		n = time.Date(n.Year(), n.Month(), n.Day(), checkFrom, 0, 0, 0, loc)
	}
	if !morning.IsZero() && n.After(morning) {
		return morning
	}
	return n
}

// digestDay says whether the person gets a digest on the local day of t.
func digestDay(t time.Time, daily bool) bool {
	if daily {
		return true
	}
	wd := t.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

func sameDay(a, b time.Time, loc *time.Location) bool {
	if a.IsZero() || b.IsZero() {
		return false
	}
	ay, am, ad := a.In(loc).Date()
	by, bm, bd := b.In(loc).Date()
	return ay == by && am == bm && ad == bd
}

// location is the person's Mattermost time zone, UTC when unset or unknown.
func (f *Feature) location(userID string) *time.Location { return f.a.Location(userID) }

func wallBefore(t time.Time, h, m int) bool {
	return t.Hour() < h || (t.Hour() == h && t.Minute() < m)
}
