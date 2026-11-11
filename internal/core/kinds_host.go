package core

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/admin/apps"
	"github.com/fudomunro/aegis/internal/admin/scopes"
	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/mail"
	"github.com/fudomunro/aegis/internal/core/metrics"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// kindsHost adapts the hub onto the kinds.Host seam (issue #312). The
// pipeline lives in internal/kinds; this file is the only bridge — narrow
// and neutral, no *Service, *Config, or *apps.AppIdentity crosses it.

// Connectors implements kinds.Host.
func (s *Server) Connectors() map[string]any {
	out := make(map[string]any, len(s.connectors))
	for id, c := range s.connectors {
		out[id] = c
	}
	return out
}

// Service implements kinds.Host.
func (s *Server) Service(id string) (kinds.ServiceView, bool) {
	svc := s.config.GetService(id)
	if svc == nil {
		return kinds.ServiceView{}, false
	}
	return serviceViewOf(*svc), true
}

// Services implements kinds.Host.
func (s *Server) Services() []kinds.ServiceView {
	snapshots := s.config.ServiceSnapshots()
	out := make([]kinds.ServiceView, 0, len(snapshots))
	for _, svc := range snapshots {
		out = append(out, serviceViewOf(svc))
	}
	return out
}

func serviceViewOf(svc Service) kinds.ServiceView {
	return kinds.ServiceView{
		ID:           svc.ID,
		Name:         svc.Name,
		Action:       svc.Action,
		Description:  svc.Description,
		Dataset:      svc.Dataset,
		CanonicalFor: svc.CanonicalFor,
	}
}

// Grants implements kinds.Host: the rule map carried by the request's key.
// Internal owner-tier fetches always see the owner map; app tokens carry
// their scopes; data scopes carry their grants; everything else is the
// owner map (agent key, tailnet peers).
func (s *Server) Grants(r *http.Request) (shared.GrantMap, kinds.Principal) {
	if kinds.OwnerView(r) {
		return s.ownerGrants(), kinds.PrincipalOwner
	}
	if app := apps.AppIdentityFrom(r); app != nil {
		return app.Scopes, kinds.PrincipalApp
	}
	if sc := scopes.From(r); sc != nil {
		return sc.Grants.Levels(), kinds.PrincipalScope
	}
	return s.ownerGrants(), kinds.PrincipalOwner
}

// OwnerGrants implements kinds.Host.
func (s *Server) OwnerGrants() shared.GrantMap {
	return s.ownerGrants()
}

// RecordAccess implements kinds.Host.
func (s *Server) RecordAccess(r *http.Request, ev kinds.AccessEvent) {
	s.recordAccessFrom(r, metrics.AccessEvent{
		At:       ev.At,
		Agent:    ev.Agent,
		Surface:  ev.Surface,
		Target:   ev.Target,
		Kind:     ev.Kind,
		Cache:    ev.Cache,
		Decision: ev.Decision,
		Reason:   ev.Reason,
		Method:   ev.Method,
		Status:   ev.Status,
		ClientIP: ev.ClientIP,
	})
}

// Logger implements kinds.Host.
func (s *Server) Logger(r *http.Request, handler string) *slog.Logger {
	return withPeerLog(slog.With("handler", handler), r)
}

// Agent implements kinds.Host.
func (s *Server) Agent(r *http.Request) string {
	return agentForRequest(r)
}

// SourceName implements kinds.Host: connector registry name, else
// configured service name, else the raw id. Kind-owned sources override
// this through their StatusHooks (issue #312).
func (s *Server) SourceName(id string) string {
	if name, ok := connectors.ServiceName(id); ok {
		return name
	}
	if svc := s.config.GetService(id); svc != nil && svc.Name != "" {
		return svc.Name
	}
	return id
}

// CopyInfo implements kinds.Host.
func (s *Server) CopyInfo(kind, serviceID, externalID string) *kinds.CopyInfo {
	return s.copyInfoFor(kind, serviceID, externalID)
}

var _ kinds.Host = (*Server)(nil)

// mailArchive adapts the hub's compressed mail archive onto
// kinds.MailArchive (issue #312). Every method reads s.mail live so
// fixtures that enable the archive after wiring still see it; nil or
// failed stores fail open.
type mailArchive struct{ s *Server }

// Available implements kinds.MailArchive.
func (a mailArchive) Available() bool {
	return a.s.mail != nil && a.s.library().MailReadable()
}

// Readable implements kinds.MailArchive.
func (a mailArchive) Readable(r *http.Request) bool {
	return a.s.mail != nil && a.s.library().MailReadableFor(r)
}

// List implements kinds.MailArchive (issue #311): the capped collection
// overview (Cap > 0) or the full matching history for filtered queries.
func (a mailArchive) List(_ context.Context, q kinds.MailQuery) ([]kinds.Message, bool, error) {
	if a.s.mail == nil {
		return nil, false, nil
	}
	sq := mail.SearchQuery{
		Q:        q.Filters.Q,
		From:     q.Filters.From,
		To:       q.Filters.To,
		After:    q.Filters.After,
		Before:   q.Filters.Before,
		Labels:   q.Filters.Labels,
		WithBody: q.WithBody,
		Dataset:  q.Dataset,
	}
	if q.Cap > 0 {
		sq.Limit = q.Cap
		resp, err := a.s.mail.Search(sq)
		if err != nil {
			return nil, false, err
		}
		return resp.Messages, resp.Total > len(resp.Messages), nil
	}
	sq.All = true
	resp, err := a.s.mail.Search(sq)
	if err != nil {
		return nil, false, err
	}
	return resp.Messages, resp.Truncated, nil
}

// Get implements kinds.MailArchive: one message with body, sibling sources
// merged into the collection identity entry (issue #311).
func (a mailArchive) Get(dataset, service, id string) (kinds.Message, bool) {
	if a.s.mail == nil {
		return kinds.Message{}, false
	}
	return a.s.mail.GetMerged(dataset, service, id)
}

// Inventory implements kinds.MailArchive (issue #162): per-ingest-source
// counts with last-sync cursors, falling back to the archive's own stamp.
func (a mailArchive) Inventory() []kinds.MailSourceStatus {
	if a.s.mail == nil {
		return nil
	}
	st, err := a.s.mail.Status()
	if err != nil {
		return nil
	}
	out := make([]kinds.MailSourceStatus, 0, len(st.ByService))
	for svc, n := range st.ByService {
		if n <= 0 {
			continue
		}
		last := ""
		if cur, err := a.s.mail.LoadSyncCursor(svc); err == nil {
			last = cur.LastSyncAt
		}
		if last == "" {
			last = st.LastSyncAt
		}
		out = append(out, kinds.MailSourceStatus{ID: svc, Count: n, LastSyncedAt: last})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SourceLastSynced implements kinds.MailArchive: the archive cursor for
// the archive itself and its registered ingest sources only.
func (a mailArchive) SourceLastSynced(id string) (string, bool) {
	if a.s.mail == nil {
		return "", false
	}
	if id != kinds.MailArchiveSourceID && a.s.mailIngestFor(id) == nil {
		return "", false
	}
	st, err := a.s.mail.Status()
	if err != nil {
		return "", true
	}
	if cur, err := a.s.mail.LoadSyncCursor(id); err == nil && cur.LastSyncAt != "" {
		return cur.LastSyncAt, true
	}
	return st.LastSyncAt, true
}

// Parent implements kinds.MailArchive: ingest grouping is declared beside
// the ingest (issue #286).
func (a mailArchive) Parent(id string) string {
	return connectors.MailSourceFor(id).Parent
}

// library returns the agent data plane, built lazily so &Server{} fixtures
// fail open. Production wires it at startup (after user-defined kinds load)
// so the UDK registry is captured.
func (s *Server) library() *library.Handlers {
	s.libraryOnce.Do(func() {
		if s.lib == nil {
			s.lib = &library.Handlers{
				Host:       s,
				Deps:       s.libraryDeps(),
				Cache:      s.cache,
				Search:     s.search,
				Links:      s.links,
				Copies:     s.copyLedger,
				UDKs:       s.udks,
				Connectors: s.connectors,
				BaseURL:    s.baseURL,
			}
		}
	})
	return s.lib
}

// libraryDeps wires the kind-constructor args (issue #312): entity-link
// keys, MBID resolvers, local-source probes, and the mail archive adapter.
func (s *Server) libraryDeps() kinds.Deps {
	return kinds.Deps{
		PlaylistLinkKey: s.links.PlaylistLinkKey,
		ResolveTracks:   s.resolveTrackEntities,
		ResolveAlbums:   s.resolveAlbumEntities,
		ResolveArtists:  s.resolveArtistEntities,
		PlacesLocalSource: func() bool {
			return s.timeline != nil && s.timeline.Enabled()
		},
		MailArchive: mailArchive{s},
	}
}

// serviceViewsOf converts config service rows to the neutral view slice.
func serviceViewsOf(services []Service) []kinds.ServiceView {
	views := make([]kinds.ServiceView, 0, len(services))
	for _, svc := range services {
		views = append(views, serviceViewOf(svc))
	}
	return views
}
