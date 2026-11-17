// SPDX-License-Identifier: AGPL-3.0-only

package render

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

func TestParse(t *testing.T) {
	off := false
	o, problems := Parse(Raw{})
	assert.Equal(t, Defaults(), o, "nothing set keeps the defaults")
	assert.Empty(t, problems)

	o, problems = Parse(Raw{
		FeedDetail: "Card", CommentText: "none", FeedPeople: "mentions", DateFormat: "relative",
		CardFields: "due, status, colour, due", DigestSections: "counts overdue nope", CardButtons: "1",
		ShowAssignButton: &off,
	})
	assert.Equal(t, DetailCard, o.FeedDetail)
	assert.Equal(t, CommentNone, o.CommentText)
	assert.Equal(t, PeopleMentions, o.FeedPeople)
	assert.Equal(t, DateRelative, o.DateFormat)
	assert.Equal(t, []string{FieldDue, FieldStatus}, o.CardFields, "order kept, duplicates and unknown names dropped")
	assert.Equal(t, []string{SectionCounts, SectionOverdue}, o.DigestSections)
	assert.Equal(t, 1, o.CardButtons)
	assert.False(t, o.AssignButton)
	assert.True(t, o.CommentButton, "an unset bool is on")
	assert.Equal(t, []string{"CardFields: unknown field colour", "DigestSections: unknown section nope"}, problems)

	o, problems = Parse(Raw{FeedDetail: "verbose", CardButtons: "7", DateFormat: "us"})
	assert.Equal(t, Defaults(), o, "invalid values fall back to the defaults")
	assert.Len(t, problems, 3)

	o, _ = Parse(Raw{CardButtons: "0", DigestSections: "due-today"})
	assert.Equal(t, 0, o.CardButtons)
	assert.Equal(t, []string{SectionDueToday}, o.DigestSections, "a dash works for an underscore")
}

func TestFeedOverride(t *testing.T) {
	o := Defaults().WithFeed(Feed{Detail: DetailDetailed, People: PeopleMentions})
	assert.Equal(t, DetailDetailed, o.FeedDetail)
	assert.Equal(t, CommentExcerpt, o.CommentText, "an empty override keeps the setting")
	assert.Equal(t, PeopleMentions, o.FeedPeople)
	assert.True(t, Feed{}.Valid())
	assert.False(t, Feed{Detail: "huge"}.Valid())
	assert.Equal(t, Defaults(), Options{}.OrDefaults())
}

func TestDates(t *testing.T) {
	now := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	r := func(format, lang string) *R {
		o := Defaults()
		o.DateFormat = format
		return New(o, i18n.For(lang), time.UTC, now)
	}
	for _, tc := range []struct {
		format, lang string
		day          time.Time
		want         string
	}{
		{DateEU, "en", day(19), "19.11.2026"},
		{DateISO, "en", day(19), "2026-11-19"},
		{DateRelative, "en", day(17), "today"},
		{DateRelative, "en", day(18), "tomorrow"},
		{DateRelative, "en", day(16), "yesterday"},
		{DateRelative, "en", day(19), "in 2 days"},
		{DateRelative, "en", day(14), "3 days ago"},
		{DateRelative, "en", day(1), "01.11.2026"}, // beyond two weeks: the date
		{DateRelative, "en", time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), "in 14 days"},
		{DateRelative, "en", time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC), "02.12.2026"},
		{DateRelative, "bg", day(17), "днес"},
		{DateRelative, "bg", day(18), "утре"},
		{DateRelative, "bg", day(19), "след 2 дни"},
		{DateRelative, "bg", day(14), "преди 3 дни"},
	} {
		assert.Equal(t, tc.want, r(tc.format, tc.lang).Day(tc.day), "%s %s %s", tc.format, tc.lang, tc.day.Format("2006-01-02"))
	}
	assert.Equal(t, "in 1 day", r(DateRelative, "en").L.N("render.date.in_days", 1))

	sofia := mustLoc(t, "Europe/Sofia")
	rs := New(Defaults(), nil, sofia, now)
	timed, _ := DueOf(new(time.Date(2026, 11, 17, 15, 30, 0, 0, time.UTC)))
	bare, _ := DueOf(new(day(18)))
	assert.Equal(t, "17.11.2026 17:30", rs.Due(timed), "a due time shows its local time")
	assert.Equal(t, "18.11.2026", rs.Due(bare), "a bare date shows no time")
	assert.Equal(t, "17.11.2026", rs.AbsDay(day(17)))
	assert.Equal(t, "вторник", New(Defaults(), i18n.For("bg"), nil, now).Weekday(time.Tuesday))
	assert.Equal(t, "Висок", New(Defaults(), i18n.For("bg"), nil, now).Priority(2))
	assert.Empty(t, rs.Priority(0))
}

func TestComment(t *testing.T) {
	long := strings.Repeat("слово ", 50) + "край"
	for _, tc := range []struct {
		setting, text, want string
	}{
		{CommentExcerpt, "looks\ngood", "\n> looks good"},
		{CommentExcerpt, long, "\n> " + strings.TrimSpace(strings.Repeat("слово ", 33)) + "…"},
		{CommentFull, "looks\ngood", "\n> looks\n> good"},
		{CommentNone, "looks good", ""},
		{CommentExcerpt, "  ", ""},
	} {
		o := Defaults()
		o.CommentText = tc.setting
		assert.Equal(t, tc.want, New(o, nil, nil, time.Now()).Comment(tc.text), tc.setting)
	}
	got := Excerpt(long, ExcerptRunes)
	assert.LessOrEqual(t, len([]rune(got)), ExcerptRunes+1)
	assert.True(t, strings.HasSuffix(got, "слово…"), "cut at a word")
	o := Defaults()
	o.CommentText = CommentFull
	full := New(o, nil, nil, time.Now()).Comment(strings.Repeat("x", 5000))
	assert.LessOrEqual(t, len(full), MaxFullComment+4)
}
