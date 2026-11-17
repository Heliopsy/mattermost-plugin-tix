// SPDX-License-Identifier: AGPL-3.0-only

// Package i18n translates the server's user-facing text. Messages live in embedded JSON catalogs,
// assets/i18n/<language>.json, keyed by message id. A message is a string, or an object of CLDR
// plural forms ("one", "other") for text with a count. Placeholders are {name}; there is no
// template language, so a translation can only rearrange the values it is given.
//
// The plugin API has no translator for plugin strings, and the go-i18n the server uses is an old
// fork behind global state, so this small package does the job with the standard library.
package i18n

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/heliopsy/mattermost-plugin-tix/assets"
)

// Fallback is the language every message exists in.
const Fallback = "en"

// message is one catalog entry: Other alone, or plural forms.
type message struct {
	One, Other string
	Plural     bool
}

func (m *message) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &m.Other)
	}
	var forms map[string]string
	if err := json.Unmarshal(b, &forms); err != nil {
		return err
	}
	for k := range forms {
		if k != "one" && k != "other" {
			return fmt.Errorf("unknown plural form %q", k)
		}
	}
	if forms["other"] == "" {
		return fmt.Errorf("plural message without an other form")
	}
	m.One, m.Other, m.Plural = forms["one"], forms["other"], true
	return nil
}

// Catalog is every language's messages.
type Catalog struct {
	langs map[string]map[string]message
}

// Load reads every i18n/*.json in fsys.
func Load(fsys fs.FS) (*Catalog, error) {
	files, err := fs.Glob(fsys, "i18n/*.json")
	if err != nil {
		return nil, err
	}
	c := &Catalog{langs: map[string]map[string]message{}}
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, err
		}
		var msgs map[string]message
		if err := json.Unmarshal(raw, &msgs); err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		c.langs[strings.TrimSuffix(path.Base(f), ".json")] = msgs
	}
	if _, ok := c.langs[Fallback]; !ok {
		return nil, fmt.Errorf("no %s catalog", Fallback)
	}
	return c, nil
}

// Languages lists the catalog's languages, sorted.
func (c *Catalog) Languages() []string {
	out := make([]string, 0, len(c.langs))
	for l := range c.langs {
		out = append(out, l)
	}
	sort.Strings(out)
	return out
}

// IDs lists the message ids of a language, sorted.
func (c *Catalog) IDs(lang string) []string {
	out := make([]string, 0, len(c.langs[lang]))
	for id := range c.langs[lang] {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

var placeholder = regexp.MustCompile(`\{([A-Za-z][A-Za-z0-9_]*)\}`)

// Placeholders lists the placeholder names a message uses in any of its forms, sorted.
func (c *Catalog) Placeholders(lang, id string) []string {
	m, ok := c.langs[lang][id]
	if !ok {
		return nil
	}
	seen := map[string]bool{}
	for _, s := range []string{m.One, m.Other} {
		for _, p := range placeholder.FindAllStringSubmatch(s, -1) {
			seen[p[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// IsPlural says whether a message has plural forms.
func (c *Catalog) IsPlural(lang, id string) bool {
	m, ok := c.langs[lang][id]
	return ok && m.Plural
}

var (
	defaultOnce sync.Once
	defaultCat  *Catalog
)

// Default is the embedded catalog. It panics if the catalogs do not parse, which the package's
// tests rule out before a build ships.
func Default() *Catalog {
	defaultOnce.Do(func() {
		c, err := Load(assets.I18n)
		if err != nil {
			panic("tix: loading the message catalogs: " + err.Error())
		}
		defaultCat = c
	})
	return defaultCat
}

// Localizer renders messages in one language, falling back to English for a missing message.
type Localizer struct {
	c    *Catalog
	lang string
}

// For returns a localizer of the embedded catalog for a Mattermost locale such as "bg" or
// "pt-BR"; an unknown locale gets English.
func For(locale string) *Localizer { return Default().For(locale) }

// For returns a localizer for a locale: the exact language, else its base language, else English.
func (c *Catalog) For(locale string) *Localizer {
	loc := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(locale), "_", "-"))
	if _, ok := c.langs[loc]; ok {
		return &Localizer{c: c, lang: loc}
	}
	if base, _, ok := strings.Cut(loc, "-"); ok {
		if _, ok := c.langs[base]; ok {
			return &Localizer{c: c, lang: base}
		}
	}
	return &Localizer{c: c, lang: Fallback}
}

// Lang is the language the localizer renders in.
func (l *Localizer) Lang() string { return l.lang }

func (l *Localizer) lookup(id string) (message, bool) {
	if m, ok := l.c.langs[l.lang][id]; ok {
		return m, true
	}
	m, ok := l.c.langs[Fallback][id]
	return m, ok
}

// T renders message id with args, given as name, value pairs: T("cards.assigned", "ref", ref).
// A missing message renders as its id, so a gap shows instead of vanishing.
func (l *Localizer) T(id string, args ...any) string {
	m, ok := l.lookup(id)
	if !ok {
		return id
	}
	return fill(m.Other, args)
}

// N renders a message with plural forms for count n, which is also its {count} placeholder.
func (l *Localizer) N(id string, n int, args ...any) string {
	m, ok := l.lookup(id)
	if !ok {
		return id
	}
	text := m.Other
	if m.Plural && m.One != "" && pluralOne(l.lang, n) {
		text = m.One
	}
	return fill(text, append([]any{"count", n}, args...))
}

// pluralOne is the CLDR "one" category for integers. English and Bulgarian both use one for
// exactly 1 and other for everything else; a language with more forms needs its rule here.
func pluralOne(_ string, n int) bool { return n == 1 }

func fill(text string, args []any) string {
	if len(args) == 0 || !strings.Contains(text, "{") {
		return text
	}
	vals := make(map[string]string, len(args)/2)
	for i := 0; i+1 < len(args); i += 2 {
		k, ok := args[i].(string)
		if !ok {
			continue
		}
		vals[k] = fmt.Sprint(args[i+1])
	}
	return placeholder.ReplaceAllStringFunc(text, func(p string) string {
		if v, ok := vals[p[1:len(p)-1]]; ok {
			return v
		}
		return p
	})
}

// M marks a message id used through a table or a variable, so the catalog test finds it. It
// returns the id unchanged.
func M(id string) string { return id }

// Error is an error whose text is a catalog message, rendered in the reader's language by Text.
type Error struct {
	ID   string
	Args []any
	// Err is the underlying cause, if any; it is never shown to people.
	Err error
}

// NewError returns an error rendering message id with args.
func NewError(id string, args ...any) *Error { return &Error{ID: id, Args: args} }

// Wrap returns an error rendering message id with args that wraps cause.
func Wrap(cause error, id string, args ...any) *Error { return &Error{ID: id, Args: args, Err: cause} }

// Error is the English text, for logs.
func (e *Error) Error() string {
	s := For(Fallback).T(e.ID, e.Args...)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Is matches another *Error with the same message id, so sentinel errors work with errors.Is.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.ID == e.ID && t.Err == nil && len(t.Args) == 0
}

// Text renders the error in l's language.
func (e *Error) Text(l *Localizer) string { return l.T(e.ID, e.Args...) }
