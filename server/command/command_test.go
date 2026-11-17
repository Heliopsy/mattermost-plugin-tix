// SPDX-License-Identifier: AGPL-3.0-only

package command

import (
	"reflect"
	"testing"

	"github.com/mattermost/mattermost/server/public/model"

	"github.com/heliopsy/mattermost-plugin-tix/server/i18n"
)

func TestSplitArgs(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{"/tix new infra fix it", []string{"/tix", "new", "infra", "fix", "it"}},
		{`/tix new infra "fix the  thing" now`, []string{"/tix", "new", "infra", "fix the  thing", "now"}},
		{`/tix comment a-1 ""`, []string{"/tix", "comment", "a-1", ""}},
		{"  ", nil},
	} {
		if got := SplitArgs(tc.in); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("SplitArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestAutocompleteLanguage: the autocomplete is built once, in the language it is given (the
// server default), with the subcommands' help and arguments in it.
func TestAutocompleteLanguage(t *testing.T) {
	r := NewRegistry(nil)
	r.Add(Sub{Name: "sync", Hint: "on|off", HelpID: "cards.cmd.sync", Autocomplete: func(n *model.AutocompleteData, l *i18n.Localizer) {
		n.AddCommand(model.NewAutocompleteData("on", "", l.T("cards.cmd.sync_on")))
	}})
	for lang, want := range map[string][3]string{
		"en": {"Work with tix tasks", "Mirror replies in this channel's task card threads to tix comments, and back (channel admins)", "Mirror card threads and tix comments"},
		"bg": {"Работа със задачи в tix", "Копирай отговорите в нишките на картите в канала като коментари в tix и обратно (за администратори на канала)", "Синхронизирай нишките на картите с коментарите в tix"},
	} {
		cmd := r.command(i18n.For(lang))
		if cmd.AutoCompleteDesc != want[0] {
			t.Errorf("%s: root %q", lang, cmd.AutoCompleteDesc)
		}
		sync := cmd.AutocompleteData.SubCommands[0]
		if sync.Trigger != "sync" || sync.HelpText != want[1] || sync.SubCommands[0].HelpText != want[2] {
			t.Errorf("%s: sync node %q / %q", lang, sync.HelpText, sync.SubCommands[0].HelpText)
		}
		if last := cmd.AutocompleteData.SubCommands[len(cmd.AutocompleteData.SubCommands)-1]; last.Trigger != "help" {
			t.Errorf("%s: help node missing", lang)
		}
	}
}
