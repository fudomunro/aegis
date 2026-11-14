package connectors

import (
	"strings"
	"testing"
)

// TestLocalImportStoreSpecs pins the wiring contract of the local import
// store registry (issue #393): every declared store is complete and
// constructible, and the naming rules hold, so a new store needs exactly
// one spec entry and its file — never hub edits. This is the guard the
// generic core/admin wiring relies on.
func TestLocalImportStoreSpecs(t *testing.T) {
	specs := LocalImportStoreSpecs()
	if len(specs) < 4 {
		t.Fatalf("registry lists %d stores, expected at least the four known ones", len(specs))
	}
	seenStem := map[string]bool{}
	seenID := map[string]bool{}
	for _, spec := range specs {
		if spec.Stem == "" || seenStem[spec.Stem] {
			t.Errorf("stem %q missing or duplicated", spec.Stem)
		}
		seenStem[spec.Stem] = true
		id := spec.Service.ID
		if id == "" || seenID[id] {
			t.Errorf("service id %q missing or duplicated", id)
		}
		seenID[id] = true

		if spec.Service.Name == "" || spec.Service.Action == "" || spec.Service.Description == "" {
			t.Errorf("%s: incomplete service card %+v", spec.Stem, spec.Service)
		}
		if spec.DefaultFile != spec.Stem+".db" {
			t.Errorf("%s: DefaultFile = %q, want %q", spec.Stem, spec.DefaultFile, spec.Stem+".db")
		}
		if spec.StoreFlagName() != "-"+spec.Stem+"-store" || spec.ImportFlagName() != "-import-"+spec.Stem {
			t.Errorf("%s: derived flag names wrong: %q %q", spec.Stem, spec.StoreFlagName(), spec.ImportFlagName())
		}
		if spec.AdminPrefix() != "/v1/admin/"+spec.Stem {
			t.Errorf("%s: admin prefix = %q", spec.Stem, spec.AdminPrefix())
		}
		if strings.TrimSpace(spec.StoreFlagHelp) == "" || strings.TrimSpace(spec.ImportFlagHelp) == "" {
			t.Errorf("%s: flag help text missing", spec.Stem)
		}
		if spec.Open == nil || spec.NewConnector == nil || spec.Maturity == nil {
			t.Fatalf("%s: spec missing Open/NewConnector/Maturity", spec.Stem)
		}

		// Open("") must describe a disabled store, never fail.
		store, err := spec.Open("")
		if err != nil {
			t.Fatalf("%s: Open(\"\") = %v", spec.Stem, err)
		}
		if store == nil || store.Enabled() {
			t.Fatalf("%s: Open(\"\") should return a disabled store", spec.Stem)
		}
		adminCopy := store.Admin()
		if adminCopy.FlagName != spec.StoreFlagName() {
			t.Errorf("%s: Admin().FlagName = %q, want %q", spec.Stem, adminCopy.FlagName, spec.StoreFlagName())
		}
		if adminCopy.Panel.Heading == "" || adminCopy.Panel.Title == "" || adminCopy.Panel.Intro == "" ||
			adminCopy.Panel.Placeholder == "" {
			t.Errorf("%s: incomplete panel copy %+v", spec.Stem, adminCopy.Panel)
		}
		if adminCopy.PathHint == "" || adminCopy.SamplePath == "" || adminCopy.LibraryPath == "" {
			t.Errorf("%s: incomplete admin copy (path hint / sample / library path)", spec.Stem)
		}
		if adminCopy.ArchiveDetail == nil || adminCopy.AccessorDetail == "" {
			t.Errorf("%s: ownership/permissions copy missing", spec.Stem)
		}
		if adminCopy.Import == nil || adminCopy.Counts == nil || adminCopy.Hints == nil {
			t.Errorf("%s: admin behaviors missing", spec.Stem)
		}

		// Connector construction works over the disabled store and nil.
		conn := spec.NewConnector(store)
		if conn == nil {
			t.Fatalf("%s: NewConnector returned nil", spec.Stem)
		}
		if spec.NewConnector(nil) == nil {
			t.Fatalf("%s: NewConnector(nil) returned nil", spec.Stem)
		}

		// Maturity names the same service the registry does.
		m := spec.Maturity()
		if m.Service != spec.Service.ID {
			t.Errorf("%s: maturity service = %q, want %q", spec.Stem, m.Service, spec.Service.ID)
		}
		if m.Transport != TransportArchive {
			t.Errorf("%s: transport = %q, want %q", spec.Stem, m.Transport, TransportArchive)
		}
		if m.Start.Kind == "" || m.Start.Label == "" {
			t.Errorf("%s: maturity Start declaration incomplete", spec.Stem)
		}
	}

	// The lookup helper sees the same registry.
	for _, spec := range specs {
		got, ok := LocalImportStoreSpecFor(spec.Stem)
		if !ok || got.Service.ID != spec.Service.ID {
			t.Errorf("LocalImportStoreSpecFor(%q) lost the spec", spec.Stem)
		}
	}
	if _, ok := LocalImportStoreSpecFor("no-such-store"); ok {
		t.Error("LocalImportStoreSpecFor must miss unknown stems")
	}
}
