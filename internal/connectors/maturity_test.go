package connectors

import "testing"

// TestConnectionMaturityCatalog pins the catalog to registered connectors:
// unique service IDs, non-empty type tables, and only IDs the ServiceName
// registry knows. The cross-check against live Capabilities() and library
// feeds lives in core's maturity_test.go, which also covers the core-local
// import-store entries.
func TestConnectionMaturityCatalog(t *testing.T) {
	cat := ConnectionMaturityCatalog()
	if len(cat) == 0 {
		t.Fatal("empty maturity catalog")
	}
	seen := map[string]bool{}
	for _, m := range cat {
		if m.Service == "" {
			t.Error("catalog entry with empty Service ID")
			continue
		}
		if seen[m.Service] {
			t.Errorf("duplicate catalog entry for %q", m.Service)
		}
		seen[m.Service] = true
		if _, ok := serviceNames[m.Service]; !ok {
			t.Errorf("catalog entry %q is not a registered connector service", m.Service)
		}
		if len(m.Kinds) == 0 {
			t.Errorf("catalog entry %q has no kinds", m.Service)
		}
		for _, dt := range m.Kinds {
			if dt.Name == "" {
				t.Errorf("catalog entry %q has a kind with empty name", m.Service)
			}
		}
	}
	if _, ok := seen[SampleCalendarServiceID]; ok {
		t.Error("sample-calendar must not be in the maturity catalog")
	}
}
