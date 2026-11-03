package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func fakeGet(t *testing.T, base, path string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	resp.Body.Close()
	return resp, string(body)
}

func TestFakeUpstream_HealthyPaging(t *testing.T) {
	srv := httptest.NewServer(NewFakeUpstream())
	defer srv.Close()

	resp, body := fakeGet(t, srv.URL, "/some/prefix/v1/me/tracks?limit=2&offset=1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, body %s", resp.StatusCode, body)
	}
	var page struct {
		Items []any `json:"items"`
		Total int   `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if page.Total != 3 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
}

func TestFakeUpstream_Modes(t *testing.T) {
	fu := NewFakeUpstream()
	srv := httptest.NewServer(fu)
	defer srv.Close()

	cases := []struct {
		mode       string
		wantStatus int
		wantItems  int
		wantTotal  int
		renamedTo  string
		garbage    bool
	}{
		{FakeModeHealthy, http.StatusOK, 3, 3, "", false},
		{FakeModeEmpty, http.StatusOK, 0, 0, "", false},
		{FakeModeHTTP500, http.StatusInternalServerError, 0, 0, "", false},
		{FakeModeHTTP401, http.StatusUnauthorized, 0, 0, "", false},
		{FakeModeGarbage, http.StatusOK, 0, 0, "", true},
		// Renamed rows carry the same three items under the new key.
		{FakeModeDriftItems, http.StatusOK, 3, 3, fakeDriftField, false},
		{FakeModeDriftUnmapped, http.StatusOK, 3, 3, fakeUnmappedField, false},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			if err := fu.SetMode(tc.mode); err != nil {
				t.Fatalf("SetMode: %v", err)
			}
			resp, body := fakeGet(t, srv.URL, "/v1/me/tracks")
			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", resp.StatusCode, tc.wantStatus, body)
			}
			if tc.garbage {
				if json.Valid([]byte(body)) {
					t.Fatalf("garbage mode must not serve valid JSON: %s", body)
				}
				return
			}
			var page map[string]any
			if err := json.Unmarshal([]byte(body), &page); err != nil {
				t.Fatalf("decode: %v (%s)", err, body)
			}
			if tc.renamedTo != "" {
				if _, ok := page["items"]; ok {
					t.Fatalf("drift must rename items: %s", body)
				}
				if _, ok := page[tc.renamedTo]; !ok {
					t.Fatalf("drift must keep the renamed array %q: %s", tc.renamedTo, body)
				}
			}
			items, _ := page["items"].([]any)
			if tc.renamedTo != "" {
				items, _ = page[tc.renamedTo].([]any)
			}
			if len(items) != tc.wantItems {
				t.Fatalf("items = %d, want %d (%s)", len(items), tc.wantItems, body)
			}
			if total := asIntTest(page["total"]); total != tc.wantTotal {
				t.Fatalf("total = %d, want %d", total, tc.wantTotal)
			}
		})
	}
}

func asIntTest(v any) int {
	f, _ := v.(float64)
	return int(f)
}

func TestFakeUpstream_Control(t *testing.T) {
	fu := NewFakeUpstream()
	srv := httptest.NewServer(fu)
	defer srv.Close()

	resp, body := fakeGet(t, srv.URL, "/v1/control")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"mode":"healthy"`) {
		t.Fatalf("GET control = %d %s", resp.StatusCode, body)
	}
	if !strings.Contains(body, FakeModeDriftItems) {
		t.Fatalf("control must list modes: %s", body)
	}

	post := func(payload string) (*http.Response, string) {
		resp, err := http.Post(srv.URL+"/v1/control", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatalf("POST control: %v", err)
		}
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		resp.Body.Close()
		return resp, string(buf[:n])
	}

	if resp, body = post(`{"mode":"drift-items"}`); resp.StatusCode != http.StatusOK || fu.Mode() != FakeModeDriftItems {
		t.Fatalf("POST set mode = %d %s (mode %s)", resp.StatusCode, body, fu.Mode())
	}
	if resp, body = post(`{"mode":"nope"}`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown mode must 400: %d %s", resp.StatusCode, body)
	}
	if fu.Mode() != FakeModeDriftItems {
		t.Fatalf("rejected mode must not apply: %s", fu.Mode())
	}
	if resp, _ = post(`not json`); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad body must 400: %d", resp.StatusCode)
	}
}

func TestFakeUpstream_NotFound(t *testing.T) {
	srv := httptest.NewServer(NewFakeUpstream())
	defer srv.Close()
	resp, _ := fakeGet(t, srv.URL, "/v1/unknown")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

// TestFakeUpstream_DriftFixtureProvenance pins the issue #240 repro fixture
// to the fake's drift-items output: the fixture is the captured evidence,
// and this keeps it honest as the fake evolves.
func TestFakeUpstream_DriftFixtureProvenance(t *testing.T) {
	fu := NewFakeUpstream()
	if err := fu.SetMode(FakeModeDriftItems); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(fu)
	defer srv.Close()

	resp, body := fakeGet(t, srv.URL, "/v1/me/tracks?limit=50")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	fx, err := LoadReproFixture("testdata/repro/spotify/me-tracks-items-renamed/fixture.json")
	if err != nil {
		t.Fatalf("LoadReproFixture: %v", err)
	}
	var fromFake, fromFixture any
	if err := json.Unmarshal([]byte(body), &fromFake); err != nil {
		t.Fatalf("fake body: %v", err)
	}
	if err := json.Unmarshal([]byte(fx.Exchanges[0].Response.Body), &fromFixture); err != nil {
		t.Fatalf("fixture body: %v", err)
	}
	got, _ := json.Marshal(fromFake)
	want, _ := json.Marshal(fromFixture)
	if string(got) != string(want) {
		t.Fatalf("fixture drifted from the fake:\n fake: %s\nfixture: %s", got, want)
	}
}
