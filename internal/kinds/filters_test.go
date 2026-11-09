package kinds

import (
	"net/http/httptest"
	"testing"
)

func TestMessageFilters_Parse(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/library/messages?q=hello&from=ada%40example.com&to=bob&after=2024-01-01&before=2024-02-01&label=INBOX&label=UNREAD&sort=asc&include_body=1", nil)
	f := ParseMessageFilters(req)
	if f.Q != "hello" || f.From != "ada@example.com" || f.To != "bob" {
		t.Fatalf("filters = %+v", f)
	}
	if f.After != "2024-01-01" || f.Before != "2024-02-01" {
		t.Fatalf("bounds = %+v", f)
	}
	if len(f.Labels) != 2 || f.Labels[0] != "INBOX" || f.Labels[1] != "UNREAD" {
		t.Fatalf("labels = %v", f.Labels)
	}
	if !f.Ascending() || !f.IncludeBody || !f.QueryMode() || !f.NeedBody() {
		t.Fatalf("flags = %+v", f)
	}
	if f.Empty() {
		t.Fatal("filters with values must not be empty")
	}

	req = httptest.NewRequest("GET", "/v1/library/messages?include_body=true", nil)
	f = ParseMessageFilters(req)
	if !f.IncludeBody || !f.QueryMode() {
		t.Fatalf("include_body=true not parsed: %+v", f)
	}
	if !f.NeedBody() {
		t.Fatal("include_body needs bodies")
	}

	req = httptest.NewRequest("GET", "/v1/library/messages", nil)
	f = ParseMessageFilters(req)
	if !f.Empty() || f.QueryMode() || f.NeedBody() {
		t.Fatalf("bare request flags = %+v", f)
	}
}

func TestMessageFilters_QueryMode(t *testing.T) {
	// The bare request is the collection overview; anything explicitly
	// passed switches to full-history retrieval (issue #311).
	if (MessageFilters{Sort: "desc"}).QueryMode() == false {
		t.Fatal("explicit sort must walk history")
	}
	if !(MessageFilters{Sort: "asc"}).QueryMode() {
		t.Fatal("asc sort must walk history")
	}
	if !(MessageFilters{Q: "x"}).QueryMode() {
		t.Fatal("q must walk history")
	}
	if !(MessageFilters{Labels: []string{"INBOX"}}).QueryMode() {
		t.Fatal("label must walk history")
	}
}

func TestMessageFilters_Matches(t *testing.T) {
	msg := Message{
		Subject: "Quarterly report draft",
		Snippet: "Please review the numbers",
		Body:    "The vault combination is on the last page",
		From:    &Contact{Name: "Ada Lovelace", Emails: []string{"Ada@Example.com"}},
		To:      []Contact{{Name: "Me", Emails: []string{"me@example.com"}}},
		CC:      []Contact{{Name: "Grace Hopper", Emails: []string{"grace@example.com"}}},
		SentAt:  "2024-03-05T10:00:00Z",
		Labels:  []string{"INBOX", "UNREAD"},
	}

	for _, tc := range []struct {
		name string
		f    MessageFilters
		want bool
	}{
		{"empty matches", MessageFilters{}, true},
		{"q subject token", MessageFilters{Q: "quarterly"}, true},
		{"q body token", MessageFilters{Q: "vault"}, true},
		{"q participants", MessageFilters{Q: "grace"}, true},
		{"q labels", MessageFilters{Q: "unread"}, true},
		{"q token AND", MessageFilters{Q: "vault report"}, true},
		{"q AND miss", MessageFilters{Q: "vault dragons"}, false},
		{"q operators stripped", MessageFilters{Q: `"quarterly"*`}, true},
		{"from address", MessageFilters{From: "ada@example.com"}, true},
		{"from name", MessageFilters{From: "ada"}, true},
		{"from miss", MessageFilters{From: "mallory"}, false},
		{"to address", MessageFilters{To: "grace@example.com"}, true},
		{"to miss", MessageFilters{To: "linus"}, false},
		{"after date window", MessageFilters{After: "2024-03-05"}, true},
		{"after later date", MessageFilters{After: "2024-03-06"}, false},
		{"before date window", MessageFilters{Before: "2024-03-06"}, true},
		{"before includes named day", MessageFilters{Before: "2024-03-05"}, true},
		{"before earlier day", MessageFilters{Before: "2024-03-04"}, false},
		{"after RFC3339", MessageFilters{After: "2024-03-05T10:00:00Z"}, true},
		{"after RFC3339 strict", MessageFilters{After: "2024-03-05T10:00:01Z"}, false},
		{"before RFC3339 strict", MessageFilters{Before: "2024-03-05T10:00:00Z"}, false},
		{"label exact", MessageFilters{Labels: []string{"INBOX"}}, true},
		{"label case-insensitive", MessageFilters{Labels: []string{"inbox"}}, true},
		{"label substring", MessageFilters{Labels: []string{"inb"}}, true},
		{"label AND", MessageFilters{Labels: []string{"INBOX", "UNREAD"}}, true},
		{"label miss", MessageFilters{Labels: []string{"INBOX", "STARRED"}}, false},
		{"combined", MessageFilters{Q: "report", From: "ada", After: "2024-03-01"}, true},
		{"combined fail", MessageFilters{Q: "report", From: "mallory"}, false},
	} {
		if got := tc.f.Matches(msg); got != tc.want {
			t.Errorf("%s: Matches = %v, want %v", tc.name, got, tc.want)
		}
	}

	// Unparseable sent_at falls back to string comparison (issue #311),
	// mirroring the archive store's bound check.
	odd := Message{SentAt: "not-a-time", Subject: "x"}
	if !(MessageFilters{After: "2024-01-01"}).Matches(odd) {
		t.Fatal("string fallback must keep rows past the after bound")
	}
	if (MessageFilters{Before: "2024-01-01"}).Matches(odd) {
		t.Fatal("string fallback must drop rows past the before bound")
	}
}

func TestMessageFilters_CacheKeySuffix(t *testing.T) {
	if (MessageFilters{}).CacheKeySuffix() != "" {
		t.Fatal("empty filters must not perturb the cache key")
	}
	a := MessageFilters{Q: "Roses", Labels: []string{"inbox"}, IncludeBody: true}
	b := MessageFilters{Q: "roses", Labels: []string{"INBOX"}, IncludeBody: true}
	if a.CacheKeySuffix() != b.CacheKeySuffix() {
		t.Fatalf("cache key must normalize case: %q vs %q", a.CacheKeySuffix(), b.CacheKeySuffix())
	}
	if (MessageFilters{Sort: "asc"}).CacheKeySuffix() == "" {
		t.Fatal("asc sort must perturb the cache key")
	}
	if (MessageFilters{From: "ada"}).CacheKeySuffix() == (MessageFilters{To: "ada"}).CacheKeySuffix() {
		t.Fatal("from and to must not share cache keys")
	}
}

func TestFilterSortStripMessages(t *testing.T) {
	in := []Message{
		{Subject: "old", SentAt: "2024-01-01T00:00:00Z", Body: "b"},
		{Subject: "new", SentAt: "2024-06-01T00:00:00Z", Body: "b"},
		{Subject: "mid", SentAt: "2024-03-01T00:00:00Z", Body: "b"},
	}
	filtered := filterMessages(in, MessageFilters{After: "2024-02-01"})
	if len(filtered) != 2 {
		t.Fatalf("filtered = %+v", filtered)
	}
	sortMessages(filtered, true)
	if filtered[0].Subject != "mid" || filtered[1].Subject != "new" {
		t.Fatalf("asc order = %+v", filtered)
	}
	sortMessages(filtered, false)
	if filtered[0].Subject != "new" || filtered[1].Subject != "mid" {
		t.Fatalf("desc order = %+v", filtered)
	}
	stripMessageBodies(filtered)
	for _, m := range filtered {
		if m.Body != "" {
			t.Fatalf("body not stripped: %+v", m)
		}
	}

	// Empty-last ordering keeps missing timestamps out of oldest/newest
	// answers (issue #77 semantics).
	gaps := []Message{{Subject: "gap"}, {Subject: "old", SentAt: "2020-01-01T00:00:00Z"}}
	sortMessages(gaps, true)
	if gaps[0].Subject != "old" {
		t.Fatalf("asc gap order = %+v", gaps)
	}
	sortMessages(gaps, false)
	if gaps[0].Subject != "old" {
		t.Fatalf("desc gap order = %+v", gaps)
	}
}
