package kinds

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fudomunro/aegis/internal/core/policy"
)

// fakeHost implements Host over plain test state — the seam must be usable
// without the core hub (issue #312).
type fakeHost struct {
	connectors map[string]any
	services   []ServiceView
	grants     policy.Permissions // request key's rule map
	owner      policy.Permissions
	principal  Principal
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

func (f *fakeHost) Grants(r *http.Request) (policy.Permissions, Principal) {
	if OwnerView(r) {
		return f.owner, PrincipalOwner
	}
	return f.grants, f.principal
}

func (f *fakeHost) OwnerGrants() policy.Permissions { return f.owner }

func (f *fakeHost) RecordAccess(r *http.Request, ev AccessEvent) { f.events = append(f.events, ev) }

func (f *fakeHost) Logger(r *http.Request, handler string) *slog.Logger { return slog.Default() }

func (f *fakeHost) Agent(r *http.Request) string { return "test-agent" }

func (f *fakeHost) SourceName(id string) string { return "name:" + id }

func (f *fakeHost) CopyInfo(kind, serviceID, externalID string) *CopyInfo {
	if externalID == "" {
		return nil
	}
	return &CopyInfo{Note: kind + "/" + serviceID + "/" + externalID}
}

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
			"notprov": struct{}{},
		},
		services: []ServiceView{
			{ID: "good", Action: "inject"},
			{ID: "blocked", Action: "block"},
			{ID: "notprov", Action: "inject"},
		},
		grants: policy.Permissions{"widgets": policy.AccessRead},
	}
	collect := func() ([]NamedLister[int], []string) {
		return Collect[int, testProvider](h,
			httptest.NewRequest(http.MethodGet, "/v1/library/x", nil), "widgets",
			func(p testProvider) func(context.Context) ([]int, bool, error) { return p.List })
	}
	targets, denied := collect()
	if len(targets) != 1 || targets[0].ID != "good" {
		t.Fatalf("targets = %+v, want only good", targets)
	}
	if len(denied) != 0 {
		t.Fatalf("denied = %v, want empty", denied)
	}

	// Deny-by-default: an unlisted kind denies every provider.
	h.grants = policy.Permissions{}
	targets, denied = collect()
	if len(targets) != 0 {
		t.Fatalf("targets = %+v, want none", targets)
	}
	if len(denied) != 1 || denied[0] != "good" {
		t.Fatalf("denied = %v, want [good] (blocked and non-providers are skipped)", denied)
	}
}

func TestEnrichStatuses_CanonicalRoleAndHooks(t *testing.T) {
	h := &fakeHost{
		services: []ServiceView{
			{ID: "home", CanonicalFor: []string{"widgets"}},
			{ID: "mirror"},
		},
	}
	bySvc := map[string]LibraryServiceStatus{
		"home":   {Status: LibraryStatusOK, Count: 1},
		"mirror": {Status: LibraryStatusOK, Count: 2},
		"local":  {Status: LibraryStatusOK, Count: 3},
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
		Expand: func(bySvc map[string]LibraryServiceStatus) {
			bySvc["extra"] = LibraryServiceStatus{Status: LibraryStatusOK, Count: 7}
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
