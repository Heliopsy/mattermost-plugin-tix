// SPDX-License-Identifier: AGPL-3.0-only

package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testCatalog(t *testing.T) *Catalog {
	t.Helper()
	c, err := Load(fstest.MapFS{
		"i18n/en.json": {Data: []byte(`{"a.hello": "Hello {name}", "a.tasks": {"one": "{count} task", "other": "{count} tasks"}, "a.only_en": "English"}`)},
		"i18n/bg.json": {Data: []byte(`{"a.hello": "Здравей, {name}", "a.tasks": {"one": "{count} задача", "other": "{count} задачи"}}`)},
	})
	require.NoError(t, err)
	return c
}

func TestLocalizer(t *testing.T) {
	c := testCatalog(t)
	for _, tc := range []struct{ locale, lang string }{
		{"bg", "bg"}, {"BG", "bg"}, {"bg-BG", "bg"}, {"bg_BG", "bg"}, {"en", "en"}, {"pt-BR", "en"}, {"", "en"},
	} {
		assert.Equal(t, tc.lang, c.For(tc.locale).Lang(), tc.locale)
	}
	bg := c.For("bg")
	assert.Equal(t, "Здравей, Ана", bg.T("a.hello", "name", "Ана"))
	assert.Equal(t, "English", bg.T("a.only_en"), "a missing translation falls back to English")
	assert.Equal(t, "a.nope", bg.T("a.nope"), "a missing message shows its id")
	assert.Equal(t, "Hello {name}", c.For("en").T("a.hello"), "a missing value keeps its placeholder")
	for n, want := range map[int]string{0: "0 задачи", 1: "1 задача", 2: "2 задачи", 21: "21 задачи"} {
		assert.Equal(t, want, bg.N("a.tasks", n), n)
	}
	assert.Equal(t, "1 task", c.For("en").N("a.tasks", 1))
	assert.Equal(t, "3 tasks", c.For("en").N("a.tasks", 3))
}

func TestLoadRejectsBadCatalogs(t *testing.T) {
	_, err := Load(fstest.MapFS{"i18n/bg.json": {Data: []byte(`{}`)}})
	assert.Error(t, err, "no English catalog")
	_, err = Load(fstest.MapFS{"i18n/en.json": {Data: []byte(`{"a.x": {"few": "x"}}`)}})
	assert.Error(t, err, "unknown plural form")
	_, err = Load(fstest.MapFS{"i18n/en.json": {Data: []byte(`{"a.x": {"one": "x"}}`)}})
	assert.Error(t, err, "plural without other")
}

func TestError(t *testing.T) {
	c := testCatalog(t)
	e := &Error{ID: "a.hello", Args: []any{"name", "Ана"}}
	assert.Equal(t, "Здравей, Ана", e.Text(c.For("bg")))
	assert.ErrorIs(t, Wrap(os.ErrNotExist, "a.hello"), os.ErrNotExist)
	sentinel := NewError("a.hello")
	assert.ErrorIs(t, &Error{ID: "a.hello"}, sentinel)
}

// TestCatalogsMatch: every language has every English message, with the same placeholders and
// plural forms. Extra messages only warn.
func TestCatalogsMatch(t *testing.T) {
	c := Default()
	en := c.IDs(Fallback)
	require.NotEmpty(t, en)
	require.Contains(t, c.Languages(), "bg")
	for _, lang := range c.Languages() {
		if lang == Fallback {
			continue
		}
		ids := c.IDs(lang)
		for _, id := range en {
			if !slices.Contains(ids, id) {
				t.Errorf("%s.json lacks %s", lang, id)
				continue
			}
			assert.Equal(t, c.Placeholders(Fallback, id), c.Placeholders(lang, id), "%s placeholders of %s", lang, id)
			assert.Equal(t, c.IsPlural(Fallback, id), c.IsPlural(lang, id), "%s plural forms of %s", lang, id)
		}
		for _, id := range ids {
			if !slices.Contains(en, id) {
				t.Logf("warning: %s.json has %s, which en.json does not", lang, id)
			}
		}
	}
}

// idArg is which argument of a call holds a message id: l.T, l.N, and i18n.M, i18n.NewError, i18n.Wrap.
var idArg = map[string]int{"T": 0, "N": 0, "M": 0, "NewError": 0, "Wrap": 1}

// idField are struct fields that hold a message id.
var idField = map[string]bool{"HelpID": true}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)

// usedIDs collects the message ids the server code names.
func usedIDs(t *testing.T) map[string]string {
	t.Helper()
	used := map[string]string{}
	fset := token.NewFileSet()
	root := ".."
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "dist" || d.Name() == "mocks") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		lit := func(e ast.Expr, why string) {
			bl, ok := e.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return
			}
			id, _ := strconv.Unquote(bl.Value)
			if !idPattern.MatchString(id) {
				t.Errorf("%s: %s is given %q, which is not a message id", fset.Position(bl.Pos()), why, id)
				return
			}
			used[id] = fset.Position(bl.Pos()).String()
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.CallExpr:
				name := ""
				if fn, ok := x.Fun.(*ast.SelectorExpr); ok {
					name = fn.Sel.Name
					// Package functions count only from this package: errors.Wrap is not ours.
					if pkg, ok := fn.X.(*ast.Ident); ok && name != "T" && name != "N" && pkg.Name != "i18n" {
						name = ""
					}
				}
				if i, ok := idArg[name]; ok && i < len(x.Args) {
					lit(x.Args[i], name)
				}
			case *ast.KeyValueExpr:
				if k, ok := x.Key.(*ast.Ident); ok && idField[k.Name] {
					lit(x.Value, k.Name)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
	return used
}

// TestUsedIDsExist: every message id in the code is in en.json; English messages no code uses only warn.
func TestUsedIDsExist(t *testing.T) {
	c := Default()
	en := c.IDs(Fallback)
	used := usedIDs(t)
	require.NotEmpty(t, used)
	for id, where := range used {
		if !slices.Contains(en, id) {
			t.Errorf("%s: %s is not in en.json", where, id)
		}
	}
	for _, id := range en {
		if _, ok := used[id]; !ok {
			t.Logf("warning: en.json has %s, which no code uses", id)
		}
	}
}
