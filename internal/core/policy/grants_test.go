package policy

import "testing"

func TestDataGrant_For(t *testing.T) {
	// Empty default preserves pre-permission behavior (DefaultAccessType).
	var empty DataGrant
	if got := empty.For("spotify"); got != DefaultAccessType {
		t.Fatalf("empty default = %q, want %q", got, DefaultAccessType)
	}

	tp := DataGrant{
		Default: AccessRead,
		Providers: map[string]string{
			"spotify":       AccessNone,
			"youtube-music": AccessFull, // cannot grant above type default
		},
	}
	if got := tp.For("spotify"); got != AccessNone {
		t.Fatalf("provider none = %q", got)
	}
	if got := tp.For("youtube-music"); got != AccessRead {
		t.Fatalf("provider cannot loosen type default: %q", got)
	}
	if got := tp.For("immich"); got != AccessRead {
		t.Fatalf("missing provider uses default: %q", got)
	}
	if got := tp.For(""); got != AccessRead {
		t.Fatalf("empty provider uses default: %q", got)
	}
}

func TestTypePolicies_For(t *testing.T) {
	var nilPolicies DataGrants
	if got := nilPolicies.For("tracks"); got.Default != "" || len(got.Providers) != 0 {
		t.Fatalf("nil policies = %+v", got)
	}
	p := DataGrants{"places": {Default: AccessNone}}
	if got := p.For("places").Default; got != AccessNone {
		t.Fatalf("places default = %q", got)
	}
	if got := p.For("tracks").Default; got != "" {
		t.Fatalf("unknown type = %q", got)
	}
}

func TestStricterAccess(t *testing.T) {
	cases := []struct{ a, b, want string }{
		{AccessNone, AccessRead, AccessNone},
		{AccessRead, AccessNone, AccessNone},
		{AccessRead, AccessFull, AccessRead},
		{AccessFull, AccessRead, AccessRead},
		{AccessCity, AccessRead, AccessCity},
		{AccessWrite, AccessFull, AccessWrite},
	}
	for _, c := range cases {
		if got := stricterAccess(c.a, c.b); got != c.want {
			t.Errorf("stricterAccess(%q, %q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
