// SPDX-License-Identifier: AGPL-3.0-only

// Package render holds the formatting rules every feature shares, so notify, cards, tasks and
// digest write dates, comments, people and task facts the same way and none of them reads the
// formatting settings itself: the admin's choices arrive as Options, a person's language as an
// i18n.Localizer and their time zone as a location.
package render

import (
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

// Feed detail levels (the FeedDetail setting and /tix subscribe --detail).
const (
	DetailCompact  = "compact"
	DetailDetailed = "detailed"
	DetailCard     = "card"
)

// Comment text choices (CommentText, --comments).
const (
	CommentFull    = "full"
	CommentExcerpt = "excerpt"
	CommentNone    = "none"
)

// People choices for feeds (FeedPeople, --people).
const (
	PeopleNames    = "names"
	PeopleMentions = "mentions"
)

// Date formats (DateFormat).
const (
	DateEU       = "eu"
	DateISO      = "iso"
	DateRelative = "relative"
)

// Card fields (CardFields).
const (
	FieldStatus   = "status"
	FieldAssignee = "assignee"
	FieldPriority = "priority"
	FieldDue      = "due"
	FieldTags     = "tags"
	FieldProject  = "project"
)

// Digest sections (DigestSections).
const (
	SectionOverdue    = "overdue"
	SectionDueToday   = "due_today"
	SectionNew        = "new"
	SectionBlocked    = "blocked"
	SectionInProgress = "in_progress"
	SectionCounts     = "counts"
)

var (
	// AllCardFields are the card fields in their default order.
	AllCardFields = []string{FieldStatus, FieldAssignee, FieldPriority, FieldDue, FieldTags, FieldProject}
	// AllDigestSections are the digest sections in their default order.
	AllDigestSections = []string{SectionOverdue, SectionDueToday, SectionNew, SectionBlocked, SectionInProgress, SectionCounts}
)

const (
	// MaxCardButtons is the most next-state buttons a card can have.
	MaxCardButtons = 3
	// ExcerptRunes is how much of a comment the excerpt setting keeps.
	ExcerptRunes = 200
	// MaxFullComment caps the full comment setting, in bytes, so one comment cannot fill a post.
	MaxFullComment = 4000
	// relativeDays is how far a relative date reaches before it falls back to the absolute date.
	relativeDays = 14
)

// Options are the formatting settings. The zero value is not useful; use Defaults or Parse.
type Options struct {
	FeedDetail     string
	CommentText    string
	FeedPeople     string
	CardFields     []string
	CardButtons    int
	AssignButton   bool
	CommentButton  bool
	DateFormat     string
	DigestSections []string
}

// Defaults are the settings that keep the plugin's original behaviour.
func Defaults() Options {
	return Options{
		FeedDetail: DetailCompact, CommentText: CommentExcerpt, FeedPeople: PeopleNames,
		CardFields: slices.Clone(AllCardFields), CardButtons: MaxCardButtons, AssignButton: true, CommentButton: true,
		DateFormat: DateEU, DigestSections: slices.Clone(AllDigestSections),
	}
}

// OrDefaults is o, or Defaults when o was never set (a Config built without parsed settings).
func (o Options) OrDefaults() Options {
	if o.FeedDetail == "" {
		return Defaults()
	}
	return o
}

// Raw is the settings as the System Console stores them; empty or nil means the default.
type Raw struct {
	FeedDetail, CommentText, FeedPeople, CardFields, DateFormat, DigestSections string
	// CardButtons is "" for the default, else 0 to 3.
	CardButtons                   string
	ShowAssignButton, ShowComment *bool
}

// Parse reads the settings. Invalid values fall back to their default; the returned problems
// name them, for a log line.
func Parse(r Raw) (Options, []string) {
	o := Defaults()
	var problems []string
	pick := func(name, v string, allowed []string, dst *string) {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" {
			return
		}
		if !slices.Contains(allowed, v) {
			problems = append(problems, name+": unknown value "+v)
			return
		}
		*dst = v
	}
	pick("FeedDetail", r.FeedDetail, []string{DetailCompact, DetailDetailed, DetailCard}, &o.FeedDetail)
	pick("CommentText", r.CommentText, []string{CommentFull, CommentExcerpt, CommentNone}, &o.CommentText)
	pick("FeedPeople", r.FeedPeople, []string{PeopleNames, PeopleMentions}, &o.FeedPeople)
	pick("DateFormat", r.DateFormat, []string{DateEU, DateISO, DateRelative}, &o.DateFormat)
	if strings.TrimSpace(r.CardFields) != "" {
		fields, unknown := List(r.CardFields, AllCardFields)
		o.CardFields = fields
		for _, u := range unknown {
			problems = append(problems, "CardFields: unknown field "+u)
		}
	}
	if strings.TrimSpace(r.DigestSections) != "" {
		secs, unknown := List(r.DigestSections, AllDigestSections)
		o.DigestSections = secs
		for _, u := range unknown {
			problems = append(problems, "DigestSections: unknown section "+u)
		}
	}
	if b := strings.TrimSpace(r.CardButtons); b != "" {
		n := -1
		if len(b) == 1 && b[0] >= '0' && b[0] <= '9' {
			n = int(b[0] - '0')
		}
		if n < 0 || n > MaxCardButtons {
			problems = append(problems, "CardButtons: not a number from 0 to 3: "+b)
		} else {
			o.CardButtons = n
		}
	}
	if r.ShowAssignButton != nil {
		o.AssignButton = *r.ShowAssignButton
	}
	if r.ShowComment != nil {
		o.CommentButton = *r.ShowComment
	}
	return o, problems
}

// List reads a comma (or space) separated list of names from allowed, lower-cased, each once,
// in the order given. Names not in allowed are returned apart. An explicitly empty result is
// allowed: a setting of only unknown names shows nothing of that kind.
func List(s string, allowed []string) (known, unknown []string) {
	known = []string{}
	for _, part := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return r == ',' || unicode.IsSpace(r) }) {
		part = strings.ReplaceAll(part, "-", "_")
		switch {
		case slices.Contains(allowed, part):
			if !slices.Contains(known, part) {
				known = append(known, part)
			}
		case !slices.Contains(unknown, part):
			unknown = append(unknown, part)
		}
	}
	return known, unknown
}

// Feed is the part of Options a channel subscription can override.
type Feed struct {
	Detail, Comments, People string
}

// WithFeed returns o with a subscription's overrides applied; empty fields keep the setting.
func (o Options) WithFeed(f Feed) Options {
	if f.Detail != "" {
		o.FeedDetail = f.Detail
	}
	if f.Comments != "" {
		o.CommentText = f.Comments
	}
	if f.People != "" {
		o.FeedPeople = f.People
	}
	return o
}

// Valid reports whether every field of f is empty or a known value.
func (f Feed) Valid() bool {
	return (f.Detail == "" || slices.Contains([]string{DetailCompact, DetailDetailed, DetailCard}, f.Detail)) &&
		(f.Comments == "" || slices.Contains([]string{CommentFull, CommentExcerpt, CommentNone}, f.Comments)) &&
		(f.People == "" || slices.Contains([]string{PeopleNames, PeopleMentions}, f.People))
}

// categoryColors colour a task by its state's category, so a colour means the same in every tenant.
var categoryColors = map[string]string{
	"todo":        "#8A8F98",
	"in_progress": "#1E88E5",
	"blocked":     "#F5A623",
	"waiting":     "#F5A623",
	"done":        "#2E7D32",
	"cancelled":   "#37474F",
}

// CategoryColor is the attachment colour of a state category; an unknown one is todo's.
func CategoryColor(category string) string {
	if c, ok := categoryColors[category]; ok {
		return c
	}
	return categoryColors["todo"]
}

// R renders for one reader: settings, language, time zone and the current time.
type R struct {
	O   Options
	L   *i18n.Localizer
	Loc *time.Location
	Now time.Time
}

// New returns a renderer. A nil localizer is English, a nil location UTC.
func New(o Options, l *i18n.Localizer, loc *time.Location, now time.Time) *R {
	if l == nil {
		l = i18n.For(i18n.Fallback)
	}
	return &R{O: o, L: l, Loc: locOrUTC(loc), Now: now}
}

// Day writes a calendar day (midnight UTC of it, as Due.Date and Today return) in the date format.
func (r *R) Day(day time.Time) string {
	switch r.O.DateFormat {
	case DateISO:
		return day.Format("2006-01-02")
	case DateRelative:
		if s, ok := r.relative(day); ok {
			return s
		}
	}
	return day.Format("02.01.2006")
}

func (r *R) relative(day time.Time) (string, bool) {
	days := int(day.Sub(Today(r.Now, r.Loc)).Hours() / 24)
	switch {
	case days == 0:
		return r.L.T("render.date.today"), true
	case days == 1:
		return r.L.T("render.date.tomorrow"), true
	case days == -1:
		return r.L.T("render.date.yesterday"), true
	case days > 1 && days <= relativeDays:
		return r.L.N("render.date.in_days", days), true
	case days < -1 && days >= -relativeDays:
		return r.L.N("render.date.days_ago", -days), true
	}
	return "", false
}

// AbsDay writes a calendar day as a date even with the relative format (for headings).
func (r *R) AbsDay(day time.Time) string {
	if r.O.DateFormat == DateISO {
		return day.Format("2006-01-02")
	}
	return day.Format("02.01.2006")
}

// Date writes the day of an instant in the reader's zone.
func (r *R) Date(t time.Time) string { return r.Day(civil(t.In(r.Loc))) }

// DateTime writes an instant as its day and local time of day.
func (r *R) DateTime(t time.Time) string {
	return r.L.T("render.date.at_time", "date", r.Date(t), "time", t.In(r.Loc).Format("15:04"))
}

// Due writes a due date: its day, and for a due time also the time of day.
func (r *R) Due(d Due) string {
	if d.DateOnly {
		return r.Day(d.At)
	}
	return r.DateTime(d.At)
}

// DueDay writes only the day a task is due on.
func (r *R) DueDay(d Due) string { return r.Day(d.Date(r.Loc)) }

// Weekday is the localized name of a day of the week.
func (r *R) Weekday(d time.Weekday) string {
	return r.L.T([]string{
		i18n.M("render.weekday.sunday"), i18n.M("render.weekday.monday"), i18n.M("render.weekday.tuesday"),
		i18n.M("render.weekday.wednesday"), i18n.M("render.weekday.thursday"), i18n.M("render.weekday.friday"),
		i18n.M("render.weekday.saturday"),
	}[d])
}

// Priority is the localized name of a tix priority (1 highest to 5 lowest), or "" for none.
func (r *R) Priority(p int) string {
	switch p {
	case 1:
		return r.L.T("render.priority.highest")
	case 2:
		return r.L.T("render.priority.high")
	case 3:
		return r.L.T("render.priority.normal")
	case 4:
		return r.L.T("render.priority.low")
	case 5:
		return r.L.T("render.priority.lowest")
	}
	return ""
}

// Comment is the quoted comment text to append to a feed line or DM, per the comment setting:
// the whole comment, a one-line excerpt or nothing. It starts with a newline when not empty.
func (r *R) Comment(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	switch r.O.CommentText {
	case CommentNone:
		return ""
	case CommentFull:
		return "\n" + Quote(Truncate(text, MaxFullComment))
	}
	return "\n> " + Excerpt(text, ExcerptRunes)
}

// Mentions says whether feed posts @-mention people (and so ping them).
func (r *R) Mentions() bool { return r.O.FeedPeople == PeopleMentions }

// CardField says whether task cards show a field.
func (r *R) CardField(f string) bool { return slices.Contains(r.O.CardFields, f) }

// Excerpt is text on one line cut to at most n runes at a word boundary, marked with "…".
func Excerpt(text string, n int) string {
	line := strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(line) <= n {
		return line
	}
	runes := []rune(line)[:n]
	cut := len(runes)
	for i := len(runes) - 1; i > n/2; i-- {
		if unicode.IsSpace(runes[i]) {
			cut = i
			break
		}
	}
	return strings.TrimRightFunc(string(runes[:cut]), func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) }) + "…"
}

// Truncate cuts s to at most n bytes on a rune boundary, marking the cut with "…".
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

// Quote turns text into a Markdown block quote.
func Quote(s string) string { return "> " + strings.ReplaceAll(s, "\n", "\n> ") }
