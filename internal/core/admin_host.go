package core

import (
	"context"
	"net/http"
	"sort"
	"net/url"
	"strings"

	"github.com/fudomunro/aegis/internal/admin"
	"github.com/fudomunro/aegis/internal/core/policy"
	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/syncjobs"
)

// adminHost adapts the hub onto the admin.Host seam (issue #358): the config
// and connection views the management surface renders, the hub-persisted
// writes its forms perform, the CSRF perimeter, and cache invalidation.
// Secret values never cross — presence flags only.

// admin returns the management surface, built lazily so &Server{} fixtures
// fail open. Production wires it at startup alongside the data plane.
func (s *Server) admin() *admin.Handlers {
	s.adminOnce.Do(func() {
		if s.adminHandlers == nil {
			s.adminHandlers = &admin.Handlers{
				Host:          s,
				KindCatalog:   s.library(),
				Search:        searchStatus{s},
				Maintenance:   s.runners(),
				Tokens:        s.tokens,
				Apps:          s.apps,
				Grants:        s.grants,
				Scopes:        s.scopes,
				UDKs:          s.udks,
				Ownership:     s.ownership,
				Mail:          s.mail,
				MailIngests:   s.mailIngests,
				Copies:        s.copyLedger,
				SyncJobs:      s.syncJobs,
				SyncRunners:   s.syncRunners,
				SyncWorker:    s.syncWorker,
				SyncSchedules: s.syncSchedules,
				SyncConflicts: s.syncConflicts,
				Conflicts:     s.runners(),
				Metrics:       s.metrics,
				Prom:          s.prom,
				Connectors:    s.connectors,
				LocalStores:   s.localStores,
				Cache:         s.cache,
				MBCache:       s.mbCache,
				HookHealth:    s.hookHealth,
				BaseURL:       s.baseURL,
				DataDir:       s.dataDir,
			}
		}
	})
	return s.adminHandlers
}

// searchStatus reads the search index live so fixtures that disable it
// after wiring still see the change (the mail-archive adapter pattern).
type searchStatus struct{ s *Server }

// Enabled implements admin.SearchStatus.
func (x searchStatus) Enabled() bool { return x.s.search != nil && x.s.search.Enabled() }

// Path implements admin.SearchStatus.
func (x searchStatus) Path() string {
	if x.s.search == nil {
		return ""
	}
	return x.s.search.Path()
}

// CountsByKind implements admin.SearchStatus.
func (x searchStatus) CountsByKind() (map[string]int, error) {
	if x.s.search == nil {
		return map[string]int{}, nil
	}
	return x.s.search.CountsByKind()
}

// serviceFactsOf maps a configured service row to the admin display facts
// (presence flags and labels only — never header values).
func (s *Server) serviceFactsOf(svc Service) admin.ServiceFacts {
	rows := make([]shared.SyncRelationship, 0, len(svc.SyncRelationships))
	for kind, canonical := range svc.SyncRelationships {
		rows = append(rows, shared.SyncRelationship{
			Kind:      kind,
			Canonical: canonical,
			ReadOnly:  !s.syncKindWriteCapable(kind, s.connectors[svc.ID]),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Kind < rows[j].Kind })
	return admin.ServiceFacts{
		ID:                    svc.ID,
		Name:                  svc.Name,
		Action:                svc.Action,
		Description:           svc.Description,
		Endpoint:              svc.Endpoint,
		Connection:            svc.Connection,
		Dataset:               svc.Dataset,
		Sync:                  rows,
		CanonicalFor:          svc.CanonicalFor,
		CacheTTL:              svc.CacheTTL.String(),
		Aliases:               svc.Aliases,
		HasStaticAPIKey:       svc.HasStaticAPIKey(),
		StaticCredentialLabel: staticCredentialLabel(svc),
	}
}

// staticCredentialLabel returns a short human label when the service holds a
// static credential in its headers (values are never read aloud).
func staticCredentialLabel(svc Service) string {
	for k, v := range svc.Headers {
		if strings.TrimSpace(v) == "" {
			continue
		}
		lower := strings.ToLower(k)
		switch {
		case strings.EqualFold(k, "Authorization"), strings.Contains(lower, "token"):
			return "API token"
		case strings.Contains(lower, "key"):
			return "API key"
		}
	}
	return ""
}

// ServiceRows implements admin.Host. Sync rows carry the full group
// membership (every source with a row for that kind, issue #365).
func (s *Server) ServiceRows() []admin.ServiceFacts {
	snapshots := s.config.ServiceSnapshots()
	out := make([]admin.ServiceFacts, 0, len(snapshots))
	members := map[string][]string{} // kind -> service IDs in the group
	for _, svc := range snapshots {
		for kind := range svc.SyncRelationships {
			members[kind] = append(members[kind], svc.ID)
		}
	}
	for _, svc := range snapshots {
		facts := s.serviceFactsOf(svc)
		for i := range facts.Sync {
			ids := append([]string(nil), members[facts.Sync[i].Kind]...)
			sort.Strings(ids)
			facts.Sync[i].Members = ids
		}
		out = append(out, facts)
	}
	return out
}

// ServiceRow implements admin.Host.
func (s *Server) ServiceRow(id string) (admin.ServiceFacts, bool) {
	for _, facts := range s.ServiceRows() {
		if facts.ID == id {
			return facts, true
		}
	}
	return admin.ServiceFacts{}, false
}

// SetSyncRelationship implements admin.Host — the admin edit of one
// relationship row (issue #365). Canonical=true makes this source the kind's
// hub (reassigning across the group); false drops the designation.
func (s *Server) SetSyncRelationship(serviceID string, rel shared.SyncRelationship) error {
	return s.config.SetSyncRelationship(serviceID, rel)
}

// DataGrants implements admin.Host.
func (s *Server) DataGrants() policy.DataGrants {
	return s.config.DataGrants()
}

// SetDataGrants implements admin.Host.
func (s *Server) SetDataGrants(policies policy.DataGrants) error {
	return s.config.SetDataGrants(policies)
}

// DataGrantsFormatVersion implements admin.Host.
func (s *Server) DataGrantsFormatVersion() int {
	return dataGrantsFileVersion
}

// ConnectionIDForService implements admin.Host.
func (s *Server) ConnectionIDForService(serviceID string) string {
	return s.config.connectionIDForService(serviceID)
}

// ConnectionSecretState implements admin.Host.
func (s *Server) ConnectionSecretState(serviceID string) (string, bool, bool) {
	connectionID, conn := s.config.ConnectionForService(serviceID)
	if conn == nil {
		return connectionID, false, false
	}
	return connectionID, conn.ClientID != "", conn.ClientSecret != ""
}

// RequiredScopes implements admin.Host.
func (s *Server) RequiredScopes(serviceID string) []string {
	return s.config.requiredScopes(serviceID)
}

// ConnectionName implements admin.Host.
func (s *Server) ConnectionName(connID string) string {
	if conn, ok := s.config.Connections[connID]; ok {
		return conn.Name
	}
	return ""
}

// SetConnection implements admin.Host — the only path from a setup flow to
// connection writes (issue #265): secrets into the encrypted store first,
// overlay second, aegis.json never rewritten.
func (s *Server) SetConnection(serviceID string, meta shared.ConnectionMetadata, upd shared.ConnectionUpdate) error {
	entry := serviceConnectionEntry{
		Name:              meta.Name,
		Action:            meta.Action,
		Description:       meta.Description,
		CacheTTL:          meta.CacheTTL,
		Aliases:           meta.Aliases,
		Endpoint:          upd.Endpoint,
		Headers:           map[string]string{}, // non-nil: static-credential path
		CanonicalFor:      upd.CanonicalFor,
		ClearCanonicalFor: upd.ClearCanonicalFor,
	}
	for k, v := range upd.SecretHeaders {
		entry.Headers[k] = v
	}
	if err := s.config.SetServiceConnection(serviceID, entry); err != nil {
		return err
	}
	// A save can make a source usable for the first time (issue #365).
	s.EstablishSyncRelationships()
	return nil
}

// SetTailscaleAdminSettings implements admin.Host.
func (s *Server) SetTailscaleAdminSettings(upd shared.TailscaleAdminUpdate) error {
	return s.config.SetTailscaleAdmin(tailscaleAdminEntry{
		APIKey:       upd.APIKey,
		Tailnet:      upd.Tailnet,
		HostDeviceID: upd.HostDeviceID,
		HostTag:      upd.HostTag,
	})
}

// RuntimeFacts implements admin.Host.
func (s *Server) RuntimeFacts() admin.RuntimeFacts {
	allowed := s.allowedLogins
	if allowed == nil {
		allowed = []string{}
	}
	return admin.RuntimeFacts{
		AuthMode:         s.authMode,
		ApertureURL:      s.apertureURL,
		APIKeyConfigured: s.apiKey != "",
		AllowedLogins:    allowed,
	}
}

// RequireFormCSRF implements admin.Host.
func (s *Server) RequireFormCSRF(w http.ResponseWriter, r *http.Request, q url.Values) bool {
	return s.requireFormCSRF(w, r, q)
}

// IssueCSRFToken implements admin.Host.
func (s *Server) IssueCSRFToken() (string, error) {
	return s.issueCSRFToken()
}

// InvalidateCaches implements admin.Host.
func (s *Server) InvalidateCaches() { s.invalidateCaches() }

// InvalidateAllCaches implements admin.Host.
func (s *Server) InvalidateAllCaches() { s.invalidateAllCaches() }

// InvalidateService implements admin.Host.
func (s *Server) InvalidateService(serviceID string) { s.invalidateService(serviceID) }

// InvalidateLibraryKind implements admin.Host.
func (s *Server) InvalidateLibraryKind(kindID string) { s.invalidateLibraryKind(kindID) }

// WithSkipAccessMetrics implements admin.Host.
func (s *Server) WithSkipAccessMetrics(r *http.Request) *http.Request {
	return withSkipAccessMetrics(r)
}

// syncRunners returns the background-consistency executor, built lazily so
// &Server{} fixtures fail open.
func (s *Server) runners() *syncjobs.Runners {
	s.syncDomainOnce.Do(func() {
		if s.syncDomain == nil {
			s.syncDomain = &syncjobs.Runners{
				Host:        s,
				Mirror:      s.library(),
				Mail:        s.mail,
				MailIngests: s.mailIngests,
				Connectors:  s.connectors,
				Copies:      s.copyLedger,
				Jobs:        s.syncJobs,
				Conflicts:   s.syncConflicts,
				DataDir:     s.dataDir,
				Version:     Version,
				Commit:      Commit,
			}
		}
	})
	return s.syncDomain
}

// ServiceBlocked implements syncjobs.Host: a configured service is blocked
// (or missing). Ingest-only sync locations are not services.
func (s *Server) ServiceBlocked(serviceID string) bool {
	svc := s.config.GetService(serviceID)
	return svc == nil || svc.Action == "block"
}

// SyncGroup implements syncjobs.Host (issue #365): one kind's sync group —
// its designated canonical home and every member row, in configuration
// order. An empty hub means no member is designated.
func (s *Server) SyncGroup(kind string) (string, []string) {
	kind = strings.ToLower(strings.TrimSpace(kind))
	hub := ""
	var members []string
	for _, svc := range s.config.ServiceSnapshots() {
		flag, ok := svc.SyncRelationships[kind]
		if !ok {
			continue
		}
		members = append(members, svc.ID)
		if flag {
			hub = svc.ID
		}
	}
	return hub, members
}

// RunMailIngest implements admin.Host — one ingest walk shared with the sync
// queue runner (issue #227).
func (s *Server) RunMailIngest(ctx context.Context, service string, opts shared.MailIngestOptions) (shared.MailIngestResult, error) {
	return s.runners().DoMailIngest(ctx, service, opts)
}

var _ admin.Host = (*Server)(nil)
