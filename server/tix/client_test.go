// SPDX-License-Identifier: AGPL-3.0-only

package tix

import (
	"net/url"
	"testing"
)

func TestResolveMe(t *testing.T) {
	q := url.Values{"assignee": {"me"}, "creator": {"@me"}, "q": {"me"}, "not_assignee": {"bob"}}
	got := resolveMe(q, "A1")
	if got.Get("assignee") != "A1" || got.Get("creator") != "A1" || got.Get("q") != "me" || got.Get("not_assignee") != "bob" {
		t.Fatalf("resolveMe = %v", got)
	}
	if q.Get("assignee") != "me" {
		t.Fatal("resolveMe changed its input")
	}
}
