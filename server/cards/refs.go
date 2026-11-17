// SPDX-License-Identifier: AGPL-3.0-only

package cards

import (
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxCards is how many task cards one message gets.
const maxCards = 3

var (
	fencedCode = regexp.MustCompile("(?s)(```|~~~).*?(```|~~~|$)")
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	urlLike    = regexp.MustCompile(`(?i)\b(?:[a-z][a-z0-9+.-]*://|www\.)\S+|\]\([^)]*\)|<[^>\s]+>`)
	validRef   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}-[0-9]{1,9}$`)
)

// stripNonText blanks code blocks, inline code and URLs so references inside them are not matched.
func stripNonText(msg string) string {
	blank := func(s string) string { return strings.Repeat(" ", len(s)) }
	msg = fencedCode.ReplaceAllStringFunc(msg, blank)
	msg = inlineCode.ReplaceAllStringFunc(msg, blank)
	return urlLike.ReplaceAllStringFunc(msg, blank)
}

// extractRefs returns the distinct task references in msg whose project key is one of keys,
// in order of appearance, lower-cased, at most limit of them.
func extractRefs(msg string, keys []string, limit int) []string {
	if len(keys) == 0 || limit <= 0 {
		return nil
	}
	keys = slices.Clone(keys)
	// Longest first, so a key that is a prefix of another does not win the alternation.
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	alt := make([]string, 0, len(keys))
	for _, k := range keys {
		if k != "" {
			alt = append(alt, regexp.QuoteMeta(k))
		}
	}
	if len(alt) == 0 {
		return nil
	}
	re := regexp.MustCompile(`(?i)(` + strings.Join(alt, "|") + `)-([0-9]{1,9})`)
	text := stripNonText(msg)
	var out []string
	for _, m := range re.FindAllStringIndex(text, -1) {
		if !boundaryBefore(text, m[0]) || !boundaryAfter(text, m[1]) {
			continue
		}
		ref := strings.ToLower(text[m[0]:m[1]])
		if !slices.Contains(out, ref) {
			out = append(out, ref)
			if len(out) == limit {
				break
			}
		}
	}
	return out
}

// wordish runes glue a match to its neighbours: infra-42 inside xinfra-42, infra-42x or a/infra-42 is not a reference.
func wordish(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-' || r == '/' || r == '@' || r == '#' || r == '~'
}

func boundaryBefore(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !wordish(r) && r != '.'
}

func boundaryAfter(s string, i int) bool {
	if i >= len(s) {
		return true
	}
	r, n := utf8.DecodeRuneInString(s[i:])
	if r == '.' {
		// "see infra-42." ends a sentence; "infra-42.5" or "infra-42.txt" does not.
		next, _ := utf8.DecodeRuneInString(s[i+n:])
		return i+n >= len(s) || (!unicode.IsLetter(next) && !unicode.IsDigit(next))
	}
	return !wordish(r)
}
