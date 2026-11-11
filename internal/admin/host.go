// Package admin is the management surface: the `/v1/admin/*` handlers, the
// admin UI shell, setup flows, admin-owned writes (data grants, apps, data
// scopes, UDK overlay, connections, ownership), and the admin read models
// over sync jobs, search, mail, metrics, and maintenance. The hub constructs
// Handlers, mounts its routes behind the admin perimeter, and implements
// Host (issue #358).
package admin

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
)

// ServiceFacts is the admin view of one configured service row: display
// fields and secret-presence flags only — never header values or secrets.
type ServiceFacts struct {
	ID           string
	Name         string
	Action       string
	Description  string
	Endpoint     string
	Connection   string
	Dataset      string
	CanonicalFor []string
	// Sync lists this source's sync relationships (issue #365): one row per
	// sync-participating kind it provides, hub flag and read-only membership
	// filled by the hub.
	Sync    []shared.SyncRelationship
	CacheTTL     string
	Aliases      []string
	// HasStaticAPIKey reports a configured static credential (presence only).
	HasStaticAPIKey bool
	// StaticCredentialLabel is the display label for that credential
	// ("API key", "API token", …), or "".
	StaticCredentialLabel string
}

// RuntimeFacts are hub display facts for the admin runtime panel.
type RuntimeFacts struct {
	AuthMode         string
	ApertureURL      string
	APIKeyConfigured bool
	AllowedLogins    []string
}

// Host is the hub-side seam the management surface needs: the config and
// connection views its pages render, the hub-persisted writes its forms
// perform, the request perimeter helpers, and cache invalidation. Core
// implements it; credential and config state never leave the hub.
type Host interface {
	// Logger returns the request-scoped logger for a named handler.
	Logger(r *http.Request, handler string) *slog.Logger
	// OwnerGrants is the owner-tier rule map (kind → access level).
	OwnerGrants() shared.GrantMap
	// WithSkipAccessMetrics marks a request as an internal dispatch so the
	// access metrics ring does not double-count it.
	WithSkipAccessMetrics(r *http.Request) *http.Request

	// ServiceRows returns every configured service's admin facts.
	ServiceRows() []ServiceFacts
	// ServiceRow returns one configured service's admin facts.
	ServiceRow(id string) (ServiceFacts, bool)

	// DataGrants returns the stored agent data grants for the editor.
	DataGrants() policy.DataGrants
	// SetDataGrants validates and persists the admin permissions overlay.
	SetDataGrants(policies policy.DataGrants) error
	// DataGrantsFormatVersion is the on-disk overlay format version
	// reported alongside the grants.
	DataGrantsFormatVersion() int
	// ConnectionIDForService returns the connection id bound to a service.
	ConnectionIDForService(serviceID string) string
	// ConnectionSecretState reports the connection id and secret-presence
	// flags (never the values) for the admin services view.
	ConnectionSecretState(serviceID string) (connID string, hasClientID, hasSecret bool)
	// RequiredScopes returns the OAuth scopes a service surface requires.
	RequiredScopes(serviceID string) []string
	// ConnectionName returns the display name of a connection, or "".
	ConnectionName(connID string) string
	// SetConnection persists admin-owned connection fields (and secret
	// headers into the encrypted store) — the same seam the setup flows use
	// (docs/connector-onboarding.md).
	SetConnection(serviceID string, meta shared.ConnectionMetadata, upd shared.ConnectionUpdate) error
	// SetSyncRelationship designates (or unsets) a source as the canonical
	// home of one kind's sync group (issue #365).
	SetSyncRelationship(serviceID string, rel shared.SyncRelationship) error
	// SetTailscaleAdminSettings persists operator Tailscale Admin settings.
	SetTailscaleAdminSettings(upd shared.TailscaleAdminUpdate) error

	// RuntimeFacts returns hub display facts for the runtime panel
	// (configuration labels and presence flags only).
	RuntimeFacts() RuntimeFacts

	// RequireFormCSRF rejects unsafe cross-site form posts.
	RequireFormCSRF(w http.ResponseWriter, r *http.Request, q url.Values) bool
	// IssueCSRFToken mints a form token for the request.
	IssueCSRFToken() (string, error)

	// RunMailIngest runs one mail ingest walk — the same implementation
	// the sync queue uses (issue #227).
	RunMailIngest(ctx context.Context, service string, opts shared.MailIngestOptions) (shared.MailIngestResult, error)

	// InvalidateCaches drops transport and library cache entries after an
	// admin write.
	InvalidateCaches()
	// InvalidateAllCaches drops every cache including MBID lookups.
	InvalidateAllCaches()
	// InvalidateService drops cached views for one service.
	InvalidateService(serviceID string)
	// InvalidateLibraryKind drops cached aggregates for one library kind.
	InvalidateLibraryKind(kindID string)
}
