package kinds

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/types"
)

// fakeHost implements Host over plain test state — the seam must be usable
// without the core hub (issue #312).
type fakeHost struct {
	connectors map[string]any
	services   []ServiceView
	levels     map[string]string // serviceID → request level
	owner      map[string]string // serviceID → owner level
	appLevel   string
	isApp      bool
	events     []AccessEvent
}

func (f *fakeHost) Connectors() map[string]any { return f.connectors }

func (f *fakeHost) Service(id string) (ServiceView, bool) {
	for _, s := range f.services {
		if s.ID == id {
			return s, true
		}
	}
	return ServiceView{}, false
}

func (f *fakeHost) Services() []ServiceView { return f.services }

func (f *fakeHost) RequestLevel(r *http.Request, serviceID, kind string) string {
	return f.levels[serviceID]
}

func (f *fakeHost) OwnerLevel(serviceID, kind string) string { return f.owner[serviceID] }

func (f *fakeHost) AppLevel(r *http.Request, kind string) (string, bool) {
	return f.appLevel, f.isApp
}

func (f *fakeHost) RecordAccess(r *http.Request, ev AccessEvent) { f.events = append(f.events, ev) }

func (f *fakeHost) Logger(r *http.Request, handler string) *slog.Logger { return slog.Default() }

func (f *fakeHost) Agent(r *http.Request) string { return "test-agent" }

func (f *fakeHost) SourceName(id string) string { return "name:" + id }

type testProvider interface {
	List(context.Context) ([]int, bool, error)
}

type goodProvider struct{}

func (goodProvider) List(context.Context) ([]int, bool, error) { return []int{1}, false, nil }

func TestOwnerView_Marker(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/library/tracks", nil)
	if OwnerView(r) {
		t.Fatal("plain request must not be owner view")
	}
	if OwnerView(OwnerViewRequest(r)) != true {
		t.Fatal("OwnerViewRequest must mark the request")
	}
	if OwnerView(nil) {
		t.Fatal("nil request must not be owner view")
	}
}

func TestCollect_PermissionsAndProviders(t *testing.T) {
	h := &fakeHost{
		connectors: map[string]any{
			"good":    goodProvider{},
			"blocked": goodProvider{},
			"denied":  goodProvider{},
			"notprov": struct{}{},
		},
		services: []ServiceView{
			{ID: "good", Action: "inject"},
			{ID: "blocked", Action: "block"},
			{ID: "denied", Action: "inject"},
			{ID: "notprov", Action: "inject"},
		},
		levels: map[string]string{"good": "read", "denied": "none"},
	}
	targets, denied := Collect[int, testProvider](h,
		httptest.NewRequest(http.MethodGet, "/v1/library/x", nil), "widgets",
		func(p testProvider) func(context.Context) ([]int, bool, error) { return p.List })

	if len(targets) != 1 || targets[0].ID != "good" {
		t.Fatalf("targets = %+v, want only good", targets)
	}
	if len(denied) != 1 || denied[0] != "denied" {
		t.Fatalf("denied = %v, want [denied]", denied)
	}
}

func TestEnrichStatuses_CanonicalRoleAndHooks(t *testing.T) {
	h := &fakeHost{
		services: []ServiceView{
			{ID: "home", CanonicalFor: []string{"widgets"}},
			{ID: "mirror"},
		},
	}
	bySvc := map[string]types.LibraryServiceStatus{
		"home":   {Status: types.LibraryStatusOK, Count: 1},
		"mirror": {Status: types.LibraryStatusOK, Count: 2},
		"local":  {Status: types.LibraryStatusOK, Count: 3},
	}
	hooks := StatusHooks{
		SourceName: func(id string) (string, bool) {
			if id == "local" {
				return "Local store", true
			}
			return "", false
		},
		SourceLastSyncedAt: func(id string) (string, bool) {
			if id == "local" {
				return "2026-01-01T00:00:00Z", true
			}
			return "", false
		},
		Expand: func(bySvc map[string]types.LibraryServiceStatus) {
			bySvc["extra"] = types.LibraryServiceStatus{Status: types.LibraryStatusOK, Count: 7}
		},
	}
	EnrichStatuses(h, "widgets", hooks, bySvc)

	if bySvc["home"].Name != "name:home" || bySvc["home"].Role != "canonical" || !bySvc["home"].Canonical {
		t.Fatalf("home = %+v", bySvc["home"])
	}
	if bySvc["mirror"].Role != "mirror" || bySvc["mirror"].Canonical {
		t.Fatalf("mirror = %+v", bySvc["mirror"])
	}
	if bySvc["local"].Name != "Local store" || bySvc["local"].LastSyncedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("local = %+v", bySvc["local"])
	}
	if bySvc["extra"].Count != 7 {
		t.Fatalf("extra = %+v", bySvc["extra"])
	}
}
