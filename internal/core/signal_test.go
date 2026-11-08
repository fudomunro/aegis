package core

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/policy"
)

func TestSignalLibraryKindRegistered(t *testing.T) {
	if LibraryKindByID("messages") == nil {
		t.Fatal("messages kind missing")
	}
	if LibraryKindByID("contacts") == nil {
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
	srv = &Server{signal: store, baseURL: "http://aegis.test"}
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
	var store connectors.ImportStore
	if srv.signal != nil {
		store = srv.signal
	}
	return importStoreAdmin{
		prefix:    "/v1/admin/signal",
		serviceID: connectors.SignalServiceID,
		store:     store,
		admin:     srv.signal.Admin(),
	}.status(srv.baseURL)
}

func TestPermissionsOverview_SignalImport(t *testing.T) {
	dir := t.TempDir()
	store, err := connectors.OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.signal = store
	srv.connectors[connectors.SignalServiceID] = connectors.NewSignalConnector(store)

	overview := srv.permissionsOverview()
	byID := map[string]policy.TypeAccess{}
	for _, row := range overview.Types {
		byID[row.ID] = row
	}

	for _, kind := range []string{"messages", "contacts"} {
		row, ok := byID[kind]
		if !ok {
			t.Fatalf("missing %s row", kind)
		}
		found := false
		for _, acc := range row.Accessors {
			if acc.ID != syncSignalID {
				continue
			}
			found = true
			if acc.Kind != policy.AccessorSync || acc.Level != policy.AccessRead {
				t.Fatalf("%s signal row = %+v", kind, acc)
			}
		}
		if !found {
			t.Fatalf("%s accessors = %+v, want signal import", kind, row.Accessors)
		}
	}

	// Signal connector feeds both library kinds.
	if !kindHasProviders("messages", srv) {
		t.Fatal("messages should have providers when Signal is mounted")
	}
	if !kindHasProviders("contacts", srv) {
		t.Fatal("contacts should have providers when Signal is mounted")
	}
}

func TestResolveStatePaths_SignalStore(t *testing.T) {
	paths, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if paths.Signal == "" || filepath.Base(paths.Signal) != "signal.db" {
		t.Fatalf("default Signal path = %q", paths.Signal)
	}

	empty, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir(), SignalSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if empty.Signal != "" {
		t.Fatalf("explicit empty Signal path = %q, want disabled", empty.Signal)
	}

	custom := filepath.Join(t.TempDir(), "custom-signal.db")
	got, err := ResolveStatePaths(StatePathOverrides{Dir: t.TempDir(), Signal: custom, SignalSet: true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Signal != custom {
		t.Fatalf("custom Signal path = %q, want %q", got.Signal, custom)
	}
}

func TestHandleAdminSignal(t *testing.T) {
	dir := t.TempDir()
	store, err := connectors.OpenSignalStore(filepath.Join(dir, "signal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.signal = store
	srv.cache = NewCache()
	srv.mbCache = NewCache()

	// Status
	req := httptest.NewRequest("GET", "/v1/admin/signal/status", nil)
	w := httptest.NewRecorder()
	srv.HandleAdminSignal(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status code %d body=%s", w.Code, w.Body.String())
	}

	// Wrong method on status
	w2 := httptest.NewRecorder()
	srv.HandleAdminSignal(w2, httptest.NewRequest("POST", "/v1/admin/signal/status", nil))
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
	srv.HandleAdminSignal(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("import code %d body=%s", w3.Code, w3.Body.String())
	}

	// Import missing path
	w4 := httptest.NewRecorder()
	srv.HandleAdminSignal(w4, httptest.NewRequest("POST", "/v1/admin/signal/import", strings.NewReader(`{}`)))
	if w4.Code != http.StatusBadRequest {
		t.Fatalf("empty path code %d", w4.Code)
	}

	// Unknown subpath
	w5 := httptest.NewRecorder()
	srv.HandleAdminSignal(w5, httptest.NewRequest("GET", "/v1/admin/signal/nope", nil))
	if w5.Code != http.StatusNotFound {
		t.Fatalf("nope code %d", w5.Code)
	}

	// Disabled store import
	srv2 := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv2.cache = NewCache()
	srv2.mbCache = NewCache()
	w6 := httptest.NewRecorder()
	srv2.HandleAdminSignal(w6, httptest.NewRequest("POST", "/v1/admin/signal/import", strings.NewReader(`{"path":"/tmp/x"}`)))
	if w6.Code != http.StatusBadRequest {
		t.Fatalf("disabled import code %d", w6.Code)
	}
}
