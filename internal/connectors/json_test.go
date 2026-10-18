package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, map[string]any{"ok": true})
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["ok"] != true {
		t.Fatalf("body = %v", body)
	}
}

func TestWriteJSONError(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSONError(w, http.StatusForbidden, "nope")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["error"] != "nope" {
		t.Fatalf("body = %v", body)
	}
}
