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
	srv.quicken = store

	req := httptest.NewRequest("GET", "/v1/admin/quicken/status", nil)
	w := httptest.NewRecorder()
	srv.admin().HandleAdminQuicken(w, req)
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
	srv.admin().HandleAdminQuicken(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("import %d body=%s", w2.Code, w2.Body.String())
	}
	var out struct {
		Result connectors.QuickenImportResult `json:"result"`
		Status connectors.ImportStoreStatus   `json:"status"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Result.Stored != 1 || out.Status.Count != 1 {
		t.Fatalf("import = %+v status=%+v", out.Result, out.Status)
	}

	// Bad path
	req3 := httptest.NewRequest("POST", "/v1/admin/quicken/import", strings.NewReader(`{"path":""}`))
	w3 := httptest.NewRecorder()
	srv.admin().HandleAdminQuicken(w3, req3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("empty path status %d", w3.Code)
	}
}
