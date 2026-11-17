// SPDX-License-Identifier: AGPL-3.0-only

package command

import (
	"reflect"
	"testing"
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
