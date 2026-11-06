package connectors

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockTailscaleAdmin is a stand-in for api.tailscale.com /api/v2 — just
// enough surface for the VIP wizard's checks and fixes.
type mockTailscaleAdmin struct {
	services map[string]TailscaleService
	devices  []TailscaleDevice
	approved map[string]bool // device id → approved for the VIP service
	policy   TailscalePolicy

	calls []string
}

func newMockTailscaleAdmin() *mockTailscaleAdmin {
	return &mockTailscaleAdmin{
		services: map[string]TailscaleService{},
		approved: map[string]bool{},
		policy:   TailscalePolicy{TagOwners: map[string][]string{}},
	}
}

func tsWriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func (m *mockTailscaleAdmin) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.calls = append(m.calls, r.Method+" "+r.URL.Path)
		// Auth: Basic (tskey) or Bearer.
		user, _, ok := r.BasicAuth()
		bearer := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !((ok && user != "") || bearer != "") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v2")
		switch {
		case r.Method == http.MethodGet && path == "/tailnet/-/acl":
			// Serve HuJSON like the real API (comments allowed).
			w.Header().Set("Content-Type", "application/json")
			var owners []string
			for tag, list := range m.policy.TagOwners {
				quoted := make([]string, 0, len(list))
				for _, o := range list {
					quoted = append(quoted, `"`+o+`"`)
				}
				owners = append(owners, `"`+tag+`": [`+strings.Join(quoted, ", ")+`]`)
			}
			tagOwners := "{}"
			if len(owners) > 0 {
				tagOwners = "{" + strings.Join(owners, ", ") + "}"
			}
			_, _ = w.Write([]byte("// test policy\n{\n  \"tagOwners\": " + tagOwners + ",\n  \"grants\": [],\n}\n"))
		case r.Method == http.MethodPost && path == "/tailnet/-/acl":
			raw, _ := io.ReadAll(r.Body)
			var pol TailscalePolicy
			if err := json.Unmarshal(HuJSONToJSON(raw), &pol); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				tsWriteJSON(w, map[string]string{"message": err.Error()})
				return
			}
			m.policy = pol
			tsWriteJSON(w, m.policy)
		case r.Method == http.MethodGet && path == "/tailnet/-/devices":
			tsWriteJSON(w, map[string]any{"devices": m.devices})
		case r.Method == http.MethodGet && path == "/tailnet/-/services/"+ImmichVIPServiceName:
			svc, ok := m.services[ImmichVIPServiceName]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			tsWriteJSON(w, svc)
		case r.Method == http.MethodPut && path == "/tailnet/-/services/"+ImmichVIPServiceName:
			var svc TailscaleService
			json.NewDecoder(r.Body).Decode(&svc)
			if svc.Name == "" {
				svc.Name = ImmichVIPServiceName
			}
			m.services[svc.Name] = svc
			tsWriteJSON(w, svc)
		case strings.HasPrefix(path, "/tailnet/-/services/"+ImmichVIPServiceName+"/device/") &&
			strings.HasSuffix(path, "/approved"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/tailnet/-/services/"+ImmichVIPServiceName+"/device/"), "/approved")
			if r.Method == http.MethodGet {
				tsWriteJSON(w, map[string]any{"approved": m.approved[id]})
				return
			}
			if r.Method == http.MethodPost {
				var body struct {
					Approved bool `json:"approved"`
				}
				json.NewDecoder(r.Body).Decode(&body)
				m.approved[id] = body.Approved
				tsWriteJSON(w, map[string]any{"approved": body.Approved})
				return
			}
			w.WriteHeader(http.StatusMethodNotAllowed)
		case r.Method == http.MethodPost && strings.HasPrefix(path, "/device/") && strings.HasSuffix(path, "/tags"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/device/"), "/tags")
			var body struct {
				Tags []string `json:"tags"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			for i := range m.devices {
				if m.devices[i].ID == id {
					m.devices[i].Tags = body.Tags
					tsWriteJSON(w, m.devices[i])
					return
				}
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

// startMockTailscale points the client at the mock and returns the host to
// wire into a connector.
func startMockTailscale(t *testing.T, mock *mockTailscaleAdmin) *testHost {
	t.Helper()
	prev := TailscaleAdminAPIBase
	ts := httptest.NewServer(mock.handler())
	TailscaleAdminAPIBase = ts.URL + "/api/v2"
	t.Cleanup(func() {
		TailscaleAdminAPIBase = prev
		ts.Close()
	})
	return newTestHost("https://aegis.example.ts.net")
}

// TestHuJSONToJSON_StripsCommentsAndTrailingCommas pins the policy parser.
func TestHuJSONToJSON_StripsCommentsAndTrailingCommas(t *testing.T) {
	src := []byte(`{
  // line comment
  "tagOwners": {
    "tag:a": ["autogroup:admin"], // trailing comment
    "tag:b": ["autogroup:admin"],
  },
  /* block
     comment */
  "grants": [],
}`)
	var pol TailscalePolicy
	if err := json.Unmarshal(HuJSONToJSON(src), &pol); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(pol.TagOwners["tag:a"]) != 1 || pol.TagOwners["tag:a"][0] != "autogroup:admin" {
		t.Fatalf("tagOwners = %v", pol.TagOwners)
	}
	if len(pol.TagOwners["tag:b"]) != 1 {
		t.Fatalf("tagOwners = %v", pol.TagOwners)
	}
}

// TestTailscaleClient_AuthAndDeclareTag pins the shared client's behaviour.
func TestTailscaleClient_AuthAndDeclareTag(t *testing.T) {
	mock := newMockTailscaleAdmin()
	startMockTailscale(t, mock)

	cl := NewTailscaleAdminClient("tskey-api-test", "")
	devices, err := cl.ListDevices(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(devices) != 0 {
		t.Fatalf("devices = %v", devices)
	}
	if cl.TailnetPath() != "-" {
		t.Fatalf("tailnet path = %q", cl.TailnetPath())
	}

	if err := cl.DeclareTag(t.Context(), "tag:immich-host"); err != nil {
		t.Fatal(err)
	}
	if got := TagOwnersOf(map[string]any{} , "tag:immich-host"); got != nil {
		t.Fatalf("empty policy = %v", got)
	}
	pol, _, err := cl.LoadPolicy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if owners := TagOwnersOf(pol, "tag:immich-host"); len(owners) != 1 || owners[0] != "autogroup:admin" {
		t.Fatalf("owners = %v", owners)
	}
	// A second declare is a no-op, not an error.
	if err := cl.DeclareTag(t.Context(), "tag:immich-host"); err != nil {
		t.Fatal(err)
	}
}
