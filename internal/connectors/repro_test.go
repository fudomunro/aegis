package connectors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScrubText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"mail for bob@corp.example failed", "mail for user@example.com failed"},
		{"Authorization: Bearer sk-abc123xyz", "Authorization: Bearer REDACTED"},
		{"?access_token=abc12345&q=1", "?access_token=REDACTED&q=1"},
		{"clean payload", "clean payload"},
	}
	for _, tc := range cases {
		if got := ScrubText(tc.in); got != tc.want {
			t.Fatalf("ScrubText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func dirtyFixture() *ReproFixture {
	return &ReproFixture{
		Service:   "spotify",
		Signature: "spotify/example",
		Notes:     "captured for bob@corp.example",
		Exchanges: []ReproExchange{{
			Request: ReproRequest{
				Method: "GET",
				Path:   "/v1/me?access_token=abc12345",
				Headers: map[string]string{
					"Authorization": "Bearer sk-live-123",
					"X-Api-Key":     "hunter2",
					"Accept":        "application/json",
				},
			},
			Response: ReproResponse{
				Status: 200,
				Headers: map[string]string{
					"Set-Cookie":   "session=secret",
					"Content-Type": "application/json",
				},
				Body: `{"email":"bob@corp.example","note":"Bearer sk-live-999"}`,
			},
		}},
	}
}

func TestScrubFixture(t *testing.T) {
	got := ScrubFixture(dirtyFixture())
	ex := got.Exchanges[0]
	if _, ok := ex.Request.Headers["Authorization"]; ok {
		t.Fatal("authorization header must be dropped")
	}
	if _, ok := ex.Response.Headers["Set-Cookie"]; ok {
		t.Fatal("set-cookie header must be dropped")
	}
	if ex.Request.Headers["X-Api-Key"] != "REDACTED" {
		t.Fatalf("api key header = %q", ex.Request.Headers["X-Api-Key"])
	}
	if ex.Request.Headers["Accept"] != "application/json" {
		t.Fatalf("innocent header clobbered: %q", ex.Request.Headers["Accept"])
	}
	if strings.Contains(ex.Request.Path, "abc12345") {
		t.Fatalf("query secret survived: %q", ex.Request.Path)
	}
	for _, s := range []string{ex.Request.Path, ex.Request.Body, ex.Response.Body, got.Notes} {
		if strings.Contains(s, "bob@corp.example") || strings.Contains(s, "sk-live") {
			t.Fatalf("scrub left secrets in %q", s)
		}
	}
	if !strings.Contains(ex.Response.Body, "user@example.com") {
		t.Fatalf("email placeholder missing: %q", ex.Response.Body)
	}

	again := ScrubFixture(got)
	r1, _ := json.Marshal(got)
	r2, _ := json.Marshal(again)
	if string(r1) != string(r2) {
		t.Fatal("ScrubFixture must be idempotent")
	}
}

func TestSaveReproFixture_ScrubsOnWrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "fixture.json")
	if err := SaveReproFixture(path, dirtyFixture()); err != nil {
		t.Fatalf("SaveReproFixture: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "sk-live") || strings.Contains(string(raw), "bob@corp.example") {
		t.Fatalf("unscrubbed capture reached disk: %s", raw)
	}
	fx, err := LoadReproFixture(path)
	if err != nil {
		t.Fatalf("LoadReproFixture: %v", err)
	}
	if fx.Signature != "spotify/example" {
		t.Fatalf("signature = %q", fx.Signature)
	}

	if err := SaveReproFixture(path, &ReproFixture{Service: "x"}); err == nil {
		t.Fatal("missing signature must error")
	}
}

func TestLoadReproFixture_Errors(t *testing.T) {
	if _, err := LoadReproFixture(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("missing file must error")
	}
	empty := filepath.Join(t.TempDir(), "empty.json")
	if err := os.WriteFile(empty, []byte(`{"service":"x","signature":"s"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadReproFixture(empty); err == nil {
		t.Fatal("fixture without exchanges must error")
	}
}

func TestReproRecorder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"echo":"` + string(body) + `","mail":"bob@corp.example"}`))
	}))
	defer upstream.Close()

	rec := &ReproRecorder{Service: "demo", Signature: "demo/echo"}
	client := &http.Client{Transport: rec}
	req, err := http.NewRequest(http.MethodPost, upstream.URL+"/v1/echo?access_token=abc12345", strings.NewReader("hi"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer sk-live-123")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	got, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "hi") {
		t.Fatalf("stream broken by recorder: %s", got)
	}

	fx := rec.Fixture()
	if len(fx.Exchanges) != 1 {
		t.Fatalf("exchanges = %d, want 1", len(fx.Exchanges))
	}
	ex := fx.Exchanges[0]
	if ex.Request.Method != http.MethodPost {
		t.Fatalf("method = %q", ex.Request.Method)
	}
	if strings.Contains(ex.Request.Path, "abc12345") {
		t.Fatalf("query secret survived in %q", ex.Request.Path)
	}
	if _, ok := ex.Request.Headers["Authorization"]; ok {
		t.Fatal("authorization must not be recorded")
	}
	if strings.Contains(ex.Response.Body, "bob@corp.example") {
		t.Fatalf("response body not scrubbed: %q", ex.Response.Body)
	}
}

func TestNewReplayServer_MatchAndMiss(t *testing.T) {
	fx := &ReproFixture{
		Signature: "demo/one",
		Exchanges: []ReproExchange{{
			Request:  ReproRequest{Method: "GET", Path: "/v1/thing?x=1"},
			Response: ReproResponse{Status: 200, Body: `{"ok":true}`},
		}},
	}
	srv := NewReplayServer(fx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/thing?x=1")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"ok":true`) {
		t.Fatalf("replay = %d %s", resp.StatusCode, body)
	}

	miss, err := http.Get(srv.URL + "/v1/nope")
	if err != nil {
		t.Fatal(err)
	}
	missBody, _ := io.ReadAll(miss.Body)
	miss.Body.Close()
	if miss.StatusCode != http.StatusNotFound {
		t.Fatalf("miss status = %d, want 404", miss.StatusCode)
	}
	if !strings.Contains(string(missBody), "demo/one") {
		t.Fatalf("miss body should name the fixture: %s", missBody)
	}
}

func TestNewReplayServer_RewritesPaginationLinks(t *testing.T) {
	fx := &ReproFixture{
		Signature: "demo/pages",
		Exchanges: []ReproExchange{
			{
				Request:  ReproRequest{Method: "GET", Path: "/v1/items?limit=10"},
				Response: ReproResponse{Status: 200, Body: `{"items":[1],"next":"https://api.example.com/v1/items?offset=10&limit=10"}`},
			},
			{
				Request:  ReproRequest{Method: "GET", Path: "/v1/items?offset=10&limit=10"},
				Response: ReproResponse{Status: 200, Body: `{"items":[2],"next":null}`},
			},
		},
	}
	srv := NewReplayServer(fx)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/items?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var page1 struct {
		Next string `json:"next"`
	}
	if err := json.Unmarshal(body, &page1); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if page1.Next != srv.URL+"/v1/items?offset=10&limit=10" {
		t.Fatalf("next = %q, want replay URL", page1.Next)
	}

	resp2, err := http.Get(page1.Next)
	if err != nil {
		t.Fatal(err)
	}
	body2, _ := io.ReadAll(resp2.Body)
	resp2.Body.Close()
	if !strings.Contains(string(body2), `"items":[2]`) {
		t.Fatalf("page 2 = %s", body2)
	}
}
