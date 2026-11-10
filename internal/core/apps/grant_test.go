package apps

import (
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
)

func TestParseScopeList(t *testing.T) {
	entries, err := parseScopeList("freebusy:read, contacts:read")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(entries) != 2 || entries[0].Type != "freebusy" || entries[0].Level != policy.AccessRead {
		t.Fatalf("entries = %+v", entries)
	}
	if _, err := parseScopeList("tracks:freebusy"); err == nil {
		t.Fatal("expected retired freebusy level rejected")
	}
	if _, err := parseScopeList(""); err == nil {
		t.Fatal("expected empty scopes rejected")
	}
	// bare type means read
	entries, err = parseScopeList("events")
	if err != nil || entries[0].Level != policy.AccessRead {
		t.Fatalf("bare = %+v err=%v", entries, err)
	}
}

func TestGrantStore_Expires(t *testing.T) {
	g := NewGrantStore()
	code, err := g.Issue(GrantCode{AppID: "eco", Scopes: policy.GrantMap{"events": policy.AccessRead}})
	if err != nil {
		t.Fatalf("issue: %v", err)
	}
	// Force expiry.
	g.mu.Lock()
	c := g.byCode[code]
	c.Expires = c.Expires.Add(-2 * grantCodeTTL)
	g.byCode[code] = c
	g.mu.Unlock()
	if _, ok := g.Consume(code); ok {
		t.Fatal("expired code must not consume")
	}
}
