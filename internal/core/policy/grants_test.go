package policy

import (
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func TestLevels_MaterializesRuleMap(t *testing.T) {
	legacy := DataGrants{
		"tracks":          {Default: "rw"},
		"playlists":       {Default: "ro"},
		"places":          {Default: ""},
		"events":          {},
		shared.DefaultKey: {Default: "read"},
	}
	got := legacy.Levels()
	want := shared.GrantMap{
		"tracks":          shared.AccessFull,
		"playlists":       shared.AccessRead,
		"events":          shared.AccessNone,
		"places":          shared.AccessNone,
		shared.DefaultKey: shared.AccessRead,
	}
	if len(got) != len(want) {
		t.Fatalf("Levels() = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Fatalf("Levels()[%q] = %q, want %q", k, got[k], v)
		}
	}

}

func TestLevels_EmptyIsDenyAll(t *testing.T) {
	if got := (DataGrants{}).Levels(); got != nil {
		t.Fatalf("empty DataGrants must materialize to nil, got %v", got)
	}
	if got := ((DataGrants)(nil)).Levels(); got != nil {
		t.Fatalf("nil DataGrants must materialize to nil, got %v", got)
	}
	// Listed kinds without a stated level resolve to none.
	empty := DataGrants{"places": {}, "tracks": {Default: ""}}.Levels()
	for _, kind := range []string{"places", "tracks"} {
		if empty.Level(kind) != shared.AccessNone {
			t.Fatalf("empty level for %s = %q, want none", kind, empty.Level(kind))
		}
	}
}
