package core

import (
	"log/slog"
	"net/http"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

// kindsHost adapts the hub onto the kinds.Host seam (issue #312). The
// pipeline lives in internal/kinds; this file is the only bridge — narrow
// and neutral, no *Service, *Config, or *AppIdentity crosses it.

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
		Dataset:      svc.Dataset,
		CanonicalFor: svc.CanonicalFor,
	}
}

// RequestLevel implements kinds.Host.
func (s *Server) RequestLevel(r *http.Request, serviceID, kind string) string {
	svc := s.config.GetService(serviceID)
	if svc == nil {
		return ""
	}
	return s.requestLevel(r, svc, kind)
}

// OwnerLevel implements kinds.Host.
func (s *Server) OwnerLevel(serviceID, kind string) string {
	svc := s.config.GetService(serviceID)
	if svc == nil {
		return ""
	}
	return s.effectivePermissions(svc).AccessForKind(kind)
}

// AppLevel implements kinds.Host. The app's raw scope level for kind is
// what crosses the seam; allowance and cache partitioning derive from it
// in the kinds package.
func (s *Server) AppLevel(r *http.Request, kind string) (string, bool) {
	app := AppIdentityFrom(r)
	if app == nil {
		return "", false
	}
	return app.Scopes.For(kind), true
}

// RecordAccess implements kinds.Host.
func (s *Server) RecordAccess(r *http.Request, ev kinds.AccessEvent) {
	s.recordAccessFrom(r, AccessEvent{
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
func (s *Server) CopyInfo(kind, serviceID, externalID string) *types.CopyInfo {
	return s.copyInfoFor(kind, serviceID, externalID)
}

var _ kinds.Host = (*Server)(nil)

// builtinKinds returns the kinds the kinds package owns so far, wired over
// this server. Built lazily so &Server{} fixtures fail open (issue #312);
// the list grows as kinds move out of the hub. // transitional (#312)
func (s *Server) builtinKinds() []kinds.Kind {
	s.builtinKindsOnce.Do(func() {
		s.kindList = kinds.Builtin(s, s.libraryEngine(), kinds.Deps{
			PlaylistLinkKey: s.links.PlaylistLinkKey,
			ResolveTracks:   s.resolveTrackEntities,
			ResolveAlbums:   s.resolveAlbumEntities,
			ResolveArtists:  s.resolveArtistEntities,
		})
	})
	return s.kindList
}

// builtinKind returns the moved kind for id. // transitional (#312)
func (s *Server) builtinKind(id string) (kinds.Kind, bool) {
	for _, k := range s.builtinKinds() {
		if k.Def().ID == id {
			return k, true
		}
	}
	return nil, false
}

// libraryHandler resolves a kind's list handler through the route
// registry — the reach-through for tests and admin dispatch (issue #312).
func (s *Server) libraryHandler(kindID string) http.HandlerFunc {
	k := LibraryKindByID(kindID)
	if k == nil {
		return nil
	}
	return s.libraryRouteHandlers()[k.Path]
}

// routeHandler resolves any registered library route pattern, including
// kind subroutes (issue #312 reach-through for tests).
func (s *Server) routeHandler(pattern string) http.HandlerFunc {
	return s.libraryRouteHandlers()[pattern]
}

// libraryEngine returns the aggregate pipeline engine. Fixtures that skip
// engine wiring get one lazily so &Server{} tests fail open instead of
// panicking (issue #312).
func (s *Server) libraryEngine() *kinds.Engine {
	s.kindEngineOnce.Do(func() {
		if s.kindEngine == nil {
			cache := s.cache
			if cache == nil {
				cache = NewCache()
			}
			s.kindEngine = kinds.NewEngine(cache, s.search)
		}
	})
	return s.kindEngine
}

// waitIndex blocks until scheduled search-index write-behind work finishes
// (issue #312: the WaitGroup lives on the kinds engine now).
func (s *Server) waitIndex() {
	s.libraryEngine().WaitIndex()
}
