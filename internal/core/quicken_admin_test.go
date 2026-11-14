package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/connectors"
)

func TestAdminQuicken_ImportFlow(t *testing.T) {
	dir := t.TempDir()
	store, err := connectors.OpenQuickenStore(filepath.Join(dir, "quicken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	qif := filepath.Join(dir, "a.qif")
	if err := os.WriteFile(qif, []byte("!Type:Bank\nD03/05/2024\nT-1.00\nPTest\n^\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.localStores["quicken"] = store
	quickenSpec, ok := connectors.LocalImportStoreSpecFor("quicken")
	if !ok {
		t.Fatal("quicken import store spec missing")
	}
	handle := srv.admin().ImportStoreHandler(quickenSpec)

	req := httptest.NewRequest("GET", "/v1/admin/quicken/status", nil)
	w := httptest.NewRecorder()
	handle(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var st connectors.ImportStoreStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Count != 0 {
		t.Fatalf("status = %+v", st)
	}

	req2 := httptest.NewRequest("POST", "/v1/admin/quicken/import", strings.NewReader(`{"path":"`+qif+`"}`))
	w2 := httptest.NewRecorder()
	handle(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("import %d body=%s", w2.Code, w2.Body.String())
	}
	var out struct {
		Result  connectors.QuickenImportResult `json:"result"`
		Summary string                         `json:"summary"`
		Status  connectors.ImportStoreStatus   `json:"status"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.Stored != 1 || out.Status.Count != 1 {
		t.Fatalf("import = %+v status=%+v", out.Result, out.Status)
	}
	if out.Summary != "1 stored, 0 skipped, 0 errors" {
		t.Fatalf("summary = %q", out.Summary)
	}

	// Bad path
	req3 := httptest.NewRequest("POST", "/v1/admin/quicken/import", strings.NewReader(`{"path":""}`))
	w3 := httptest.NewRecorder()
	handle(w3, req3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("empty path status %d", w3.Code)
	}
}

// TestAdminImportStores_View pins the declared-copy contract between the
// connectors registry and the admin.html panel renderer (issue #393):
// every store appears with the panel copy and live status the generic
// renderer needs. Adding a store must not need UI-side knowledge.
func TestAdminImportStores_View(t *testing.T) {
	dir := t.TempDir()
	store, err := connectors.OpenQuickenStore(filepath.Join(dir, "quicken.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.localStores["quicken"] = store

	req := httptest.NewRequest("GET", "/v1/admin/import-stores", nil)
	w := httptest.NewRecorder()
	srv.admin().HandleAdminImportStores(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var payload struct {
		Stores []struct {
			Stem      string                       `json:"stem"`
			ServiceID string                       `json:"service_id"`
			Panel     connectors.ImportPanel       `json:"panel"`
			FlagName  string                       `json:"flag_name"`
			Status    connectors.ImportStoreStatus `json:"status"`
		} `json:"stores"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	byStem := map[string]bool{}
	for _, s := range payload.Stores {
		byStem[s.Stem] = true
		if s.ServiceID == "" || s.FlagName == "" {
			t.Fatalf("%s: incomplete view %+v", s.Stem, s)
		}
		if s.Panel.Heading == "" || s.Panel.Title == "" || s.Panel.Intro == "" || s.Panel.Placeholder == "" {
			t.Fatalf("%s: panel copy missing in view: %+v", s.Stem, s.Panel)
		}
		if s.Stem == "quicken" && !s.Status.Enabled {
			t.Fatalf("quicken status should report enabled: %+v", s.Status)
		}
	}
	// Every declared store is represented — including ones with no
	// enabled store behind them (the renderer shows the disabled state).
	for _, spec := range connectors.LocalImportStoreSpecs() {
		if !byStem[spec.Stem] {
			t.Errorf("import-stores view missing %q", spec.Stem)
		}
	}
}
