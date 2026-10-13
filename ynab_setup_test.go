package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdminYNABSetup_StatusAndConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Services: []Service{{ID: "ynab", Name: "YNAB", Action: "inject", Endpoint: "https://api.ynab.com/v1"}},
	}
	srv := testServerWithBase(cfg, "https://aegis.example.com")
	if err := srv.config.InitServiceConnectionsStore(filepath.Join(dir, "service_connections.json")); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/v1/admin/ynab/setup", nil)
	w := httptest.NewRecorder()
	srv.HandleAdminYNABSetup(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d body=%s", w.Code, w.Body.String())
	}
	var st YNABSetupStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st.HasToken {
		t.Fatal("expected no token yet")
	}

	req2 := httptest.NewRequest("POST", "/v1/admin/ynab/config", strings.NewReader(`{"endpoint":"https://api.ynab.com/v1","token":"test-pat"}`))
	w2 := httptest.NewRecorder()
	srv.HandleAdminYNABSetup(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("config status %d body=%s", w2.Code, w2.Body.String())
	}
	var st2 YNABSetupStatus
	if err := json.Unmarshal(w2.Body.Bytes(), &st2); err != nil {
		t.Fatal(err)
	}
	if !st2.HasToken {
		t.Fatal("expected token after save")
	}
	// Secret must not be echoed.
	if strings.Contains(w2.Body.String(), "test-pat") {
		t.Fatalf("token leaked in response: %s", w2.Body.String())
	}
	// Overlay file must hold the secret (0600).
	raw, err := os.ReadFile(filepath.Join(dir, "service_connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "test-pat") {
		t.Fatal("token not persisted to service_connections.json")
	}

	req3 := httptest.NewRequest("POST", "/v1/admin/ynab/test", nil)
	w3 := httptest.NewRecorder()
	srv.HandleAdminYNABSetup(w3, req3)
	// Unreachable upstream → not ok, but handler must answer.
	if w3.Code != http.StatusOK {
		t.Fatalf("test status %d body=%s", w3.Code, w3.Body.String())
	}
}

func TestAdminQuicken_ImportFlow(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenQuickenStore(filepath.Join(dir, "quicken.db"))
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
	srv.HandleAdminQuicken(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status %d", w.Code)
	}
	var st QuickenImportStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if !st.Enabled || st.Count != 0 {
		t.Fatalf("status = %+v", st)
	}

	req2 := httptest.NewRequest("POST", "/v1/admin/quicken/import", strings.NewReader(`{"path":"`+qif+`"}`))
	w2 := httptest.NewRecorder()
	srv.HandleAdminQuicken(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("import %d body=%s", w2.Code, w2.Body.String())
	}
	var out struct {
		Result QuickenImportResult `json:"result"`
		Status QuickenImportStatus `json:"status"`
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
	srv.HandleAdminQuicken(w3, req3)
	if w3.Code != http.StatusBadRequest {
		t.Fatalf("empty path status %d", w3.Code)
	}
}
