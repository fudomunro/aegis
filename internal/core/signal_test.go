package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

func TestSignalLibraryKindRegistered(t *testing.T) {
	if library.LibraryKindByID("messages") == nil {
		t.Fatal("messages kind missing")
	}
	if library.LibraryKindByID("contacts") == nil {
		t.Fatal("contacts kind missing")
	}
}

func TestSignalImportStatusHints(t *testing.T) {
	srv := &Server{}
	st := signalStatusOf(srv)
	if st.Enabled {
		t.Fatal("nil store should be disabled")
	}
	if len(st.Hints) == 0 {
		t.Fatal("expected hints when disabled")
	}

	dir := t.TempDir()
	store, err := connectors.OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv = &Server{localStores: map[string]connectors.ImportStore{"signal": store}, baseURL: "http://aegis.test"}
	st = signalStatusOf(srv)
	if !st.Enabled || !st.StoreEmpty {
		t.Fatalf("status = %+v", st)
	}
	if st.LibraryURL == "" {
		t.Fatal("missing library url")
	}
}

// signalStatusOf renders the Signal import status through the store's
// declared connectors.ImportAdmin copy (issue #254).
func signalStatusOf(srv *Server) connectors.ImportStoreStatus {
	spec, _ := connectors.LocalImportStoreSpecFor("signal")
	return admin.ImportStoreAdmin{Spec: spec, Store: srv.localStores["signal"]}.Status(srv.baseURL)
}

func TestPermissionsOverview_SignalImport(t *testing.T) {
	dir := t.TempDir()
	store, err := connectors.OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.localStores["signal"] = store
	srv.connectors[connectors.SignalServiceID] = connectors.NewSignalConnector(store)

	overview := srv.admin().PermissionsOverview()
	byID := map[string]policy.KindAccess{}
	for _, row := range overview.Rows {
		byID[row.ID] = row
	}

	for _, kind := range []string{"messages", "contacts"} {
		row, ok := byID[kind]
		if !ok {
			t.Fatalf("missing %s row", kind)
		}
		found := false
		for _, acc := range row.Accessors {
			if acc.ID != "sync:signal" {
				continue
			}
			found = true
			if acc.Kind != policy.AccessorSync || acc.Level != shared.AccessRead {
				t.Fatalf("%s signal row = %+v", kind, acc)
			}
		}
		if !found {
			t.Fatalf("%s accessors = %+v, want signal import", kind, row.Accessors)
		}
	}

	// Signal connector feeds both library kinds.
	if !library.KindHasProviders("messages", srv.library()) {
		t.Fatal("messages should have providers when Signal is mounted")
	}
	if !library.KindHasProviders("contacts", srv.library()) {
		t.Fatal("contacts should have providers when Signal is mounted")
	}
}

func TestResolveStatePaths_LocalStores(t *testing.T) {
	paths, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	// Every declared store defaults to its spec file name under the data
	// dir (issue #393) — a new store is covered without edits here.
	for _, spec := range connectors.LocalImportStoreSpecs() {
		got := paths.LocalStores[spec.Stem]
		if got == "" || filepath.Base(got) != spec.DefaultFile {
			t.Fatalf("%s default path = %q, want file %q", spec.Stem, got, spec.DefaultFile)
		}
	}

	empty, err := ResolveStatePaths(StatePathOverrides{
		Dir:        t.TempDir(),
		LocalStores: map[string]LocalStoreOverride{"signal": {Set: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if empty.LocalStores["signal"] != "" {
		t.Fatalf("explicit empty Signal path = %q, want disabled", empty.LocalStores["signal"])
	}

	custom := filepath.Join(t.TempDir(), "custom-signal.db")
	got, err := ResolveStatePaths(StatePathOverrides{
		Dir:        t.TempDir(),
		LocalStores: map[string]LocalStoreOverride{"signal": {Path: custom, Set: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.LocalStores["signal"] != custom {
		t.Fatalf("custom Signal path = %q, want %q", got.LocalStores["signal"], custom)
	}
}

func TestImportStoreHandler_Signal(t *testing.T) {
	dir := t.TempDir()
	store, err := connectors.OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.localStores["signal"] = store
	srv.cache = shared.NewCache()
	srv.mbCache = shared.NewCache()
	signalSpec, ok := connectors.LocalImportStoreSpecFor("signal")
	if !ok {
		t.Fatal("signal import store spec missing")
	}
	handle := srv.admin().ImportStoreHandler(signalSpec)

	// Status
	req := httptest.NewRequest("GET", "/v1/admin/signal/status", nil)
	w := httptest.NewRecorder()
	handle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w.Code, w.Body.String())
	}

	// Wrong method on status
	w2 := httptest.NewRecorder()
	handle(w2, httptest.NewRequest("POST", "/v1/admin/signal/status", nil))
	if w2.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status POST code %d", w2.Code)
	}

	// Import
	path := filepath.Join(dir, "export.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"message","id":"m1","body":"hi","sent_at":"2024-01-01T00:00:00Z"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.NewReader(`{"path":"` + path + `"}`)
	req3 := httptest.NewRequest("POST", "/v1/admin/signal/import", body)
	w3 := httptest.NewRecorder()
	handle(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("import code %d body=%s", w3.Code, w3.Body.String())
	}

	// Import missing path
	w4 := httptest.NewRecorder()
	handle(w4, httptest.NewRequest("POST", "/v1/admin/signal/import", strings.NewReader(`{}`)))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("empty path code %d", w4.Code)
	}

	// Unknown subpath
	w5 := httptest.NewRecorder()
	handle(w5, httptest.NewRequest("GET", "/v1/admin/signal/nope", nil))
	if w5.Code != http.StatusNotFound {
		t.Fatalf("nope code %d", w5.Code)
	}

	// Disabled store import
	srv2 := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv2.cache = shared.NewCache()
	srv2.mbCache = shared.NewCache()
	signalSpec2, _ := connectors.LocalImportStoreSpecFor("signal")
	handle2 := srv2.admin().ImportStoreHandler(signalSpec2)
	w6 := httptest.NewRecorder()
	handle2(w6, httptest.NewRequest("POST", "/v1/admin/signal/import", strings.NewReader(`{"path":"/tmp/x"}`)))
	if w6.Code != http.StatusBadRequest {
		t.Fatalf("disabled import code %d", w6.Code)
	}
}
