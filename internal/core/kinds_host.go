package core

import (
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/types"
)

import (
	"context"
	"log/slog"
	"net/http"
	"sort"

	"github.com/fudomunro/aegis/internal/connectors"
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

// builtinKinds returns the wired built-in kinds (issue #312). Built lazily
// so &Server{} fixtures fail open.
func (s *Server) builtinKinds() []kinds.Kind {
	s.builtinKindsOnce.Do(func() {
		s.kindList = kinds.Builtin(s, s.libraryEngine(), kinds.Deps{
			PlaylistLinkKey: s.links.PlaylistLinkKey,
			ResolveTracks:   s.resolveTrackEntities,
			ResolveAlbums:   s.resolveAlbumEntities,
			ResolveArtists:  s.resolveArtistEntities,
			PlacesLocalSource: func() bool {
				return s.timeline != nil && s.timeline.Enabled()
			},
			MailArchive: mailArchive{s},
		})
	})
	return s.kindList
}

// kindRegistry returns the composed kind registry: the wired built-ins
// plus any loaded user-defined kinds, with the shadow check applied
// (issue #312). UDT kinds are composed per call so fixtures that load
// packs late still see them, matching the old per-request route merge.
func (s *Server) kindRegistry() []kinds.Kind {
	builtin := s.builtinKinds()
	if s == nil || s.udts == nil {
		return builtin
	}
	schemas := s.udts.All()
	udts := make([]kinds.Kind, 0, len(schemas))
	for _, schema := range schemas {
		udts = append(udts, kinds.UserDefined(s, s.libraryEngine(), schema, s.userDefinedDeps()))
	}
	merged, _ := kinds.MergeUserDefined(builtin, udts)
	return merged
}

// registryKind returns the registered kind for id (issue #312).
func (s *Server) registryKind(id string) (kinds.Kind, bool) {
	for _, k := range s.kindRegistry() {
		if k.Def().ID == id {
			return k, true
		}
	}
	return nil, false
}

// listHandler resolves a kind's list-route handler (its Def().Path route).
func listHandler(k kinds.Kind) http.HandlerFunc {
	for _, route := range k.Routes() {
		if route.Pattern == k.Def().Path {
			return route.Handler
		}
	}
	return nil
}

// userDefinedDeps wires the user-defined kind constructor args (issue
// #15): overlay-resolved schemas, pack directories, and the data-grant
// gate.
func (s *Server) userDefinedDeps() kinds.UserDefinedDeps {
	return kinds.UserDefinedDeps{
		Schema: func(id string) kinds.UserDefinedTypeSchema {
			if s.udts == nil {
				return kinds.UserDefinedTypeSchema{}
			}
			return s.udts.SchemaWithOverlay(id)
		},
		BaseDir: func(id string) string {
			if s.udts == nil {
				return ""
			}
			return s.udts.DirFor(id)
		},
		Readable: s.userDefinedReadable,
	}
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

// mailArchive adapts the hub's compressed mail archive onto
// kinds.MailArchive (issue #312). Every method reads s.mail live so
// fixtures that enable the archive after wiring still see it; nil or
// failed stores fail open.
type mailArchive struct{ s *Server }

// Available implements kinds.MailArchive.
func (a mailArchive) Available() bool {
	return a.s.mail != nil && a.s.mailReadable()
}

// Readable implements kinds.MailArchive.
func (a mailArchive) Readable(r *http.Request) bool {
	return a.s.mail != nil && a.s.mailReadableFor(r)
}

// List implements kinds.MailArchive.
func (a mailArchive) List(_ context.Context, dataset string, cap int) ([]types.Message, bool, error) {
	if a.s.mail == nil {
		return nil, false, nil
	}
	return a.s.mail.ListMessagesForLibrary(cap, dataset)
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
