package kinds

import "testing"

func TestNormalizeContactEmail(t *testing.T) {
	cases := map[string]string{
		"  Ada@Example.COM ": "ada@example.com",
		"ADA@example.com":    "ada@example.com",
		"":                   "",
	}
	for in, want := range cases {
		if got := NormalizeContactEmail(in); got != want {
			t.Errorf("NormalizeContactEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeContactPhone(t *testing.T) {
	cases := map[string]string{
		"+1 (555) 123-4567": "+15551234567",
		"555.123.4567":      "5551234567",
		"+44 20 7946 0958":  "+442079460958",
		"":                  "",
		"abc":               "",
	}
	for in, want := range cases {
		if got := NormalizeContactPhone(in); got != want {
			t.Errorf("NormalizeContactPhone(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeContactHandle(t *testing.T) {
	if got := NormalizeContactHandle("  @AdaL "); got != "adal" {
		t.Fatalf("handle = %q, want adal", got)
	}
}

func TestContactIdentityKeys_PrefersEmailThenPhoneThenHandle(t *testing.T) {
	c := Contact{
		Name:    "Ada Lovelace",
		Emails:  []string{"Ada@Example.com"},
		Phones:  []string{"+1 (555) 123-4567"},
		Handles: []string{"@ada"},
	}
	keys := ContactIdentityKeys(c)
	want := []string{
		"email:ada@example.com",
		"phone:+15551234567",
		"handle:ada",
	}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
	if PrimaryContactKey(c) != "email:ada@example.com" {
		t.Fatalf("primary = %q", PrimaryContactKey(c))
	}
}

func TestContactIdentityKeys_NameOnly(t *testing.T) {
	c := Contact{Name: "  Grace   Hopper "}
	keys := ContactIdentityKeys(c)
	if len(keys) != 1 || keys[0] != "name:grace hopper" {
		t.Fatalf("keys = %v", keys)
	}
}

func TestNewContact_Empty(t *testing.T) {
	if got := NewContact("google", "Google", "id1", "  ", nil, nil); got.Name != "" {
		t.Fatalf("expected empty contact, got %+v", got)
	}
}

func TestContactIdentityKeys_UidFirst(t *testing.T) {
	c := Contact{
		Name:   "Ada Lovelace",
		Emails: []string{"Ada@Example.com"},
		Extra:  map[string]any{"uid": "card-1"},
	}
	keys := ContactIdentityKeys(c)
	want := []string{"uid:card-1", "email:ada@example.com"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
	if PrimaryContactKey(c) != "uid:card-1" {
		t.Fatalf("primary = %q", PrimaryContactKey(c))
	}
}

func TestContactIdentityKeys_UidDoesNotSuppressName(t *testing.T) {
	// A UID-only card has no email, phone, or handle — its display name
	// still folds it with a name-only card.
	c := Contact{Name: "Grace Hopper", Extra: map[string]any{"uid": "card-2"}}
	keys := ContactIdentityKeys(c)
	want := []string{"uid:card-2", "name:grace hopper"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys[%d] = %q, want %q", i, keys[i], want[i])
		}
	}
}

func TestContactUIDs(t *testing.T) {
	c := Contact{
		Extra: map[string]any{"uid": "card-3"},
		Sources: []ContactSource{
			{Service: "dav", Extra: map[string]any{"uid": "card-3"}},
			{Service: "other", Extra: map[string]any{"uid": "card-9"}},
			{Service: "google", ID: "people/c1"},
		},
	}
	uids := ContactUIDs(c)
	if len(uids) != 2 || uids[0] != "card-3" || uids[1] != "card-9" {
		t.Fatalf("uids = %v", uids)
	}
	if ContactUID(c) != "card-3" {
		t.Fatalf("primary uid = %q", ContactUID(c))
	}
	// Source-local ids are not UIDs.
	plain := Contact{Sources: []ContactSource{{Service: "google", ID: "people/c1"}}}
	if ContactUID(plain) != "" {
		t.Fatalf("source id must not act as a uid, got %q", ContactUID(plain))
	}
}
