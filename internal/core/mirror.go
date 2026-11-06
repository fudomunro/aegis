package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/shared"
	"github.com/fudomunro/aegis/internal/types"
)

// Mirror kinds supported by POST /v1/library/mirror.
const (
	MirrorKindPlaylists = "playlists"
	MirrorKindTracks    = "tracks"
	MirrorKindContacts  = "contacts"
	MirrorKindEvents    = "events"
)

// mirrorTrackCap bounds a single mirror call so a huge library cannot
// turn into an unbounded write storm. Callers can re-run for the rest.
const mirrorTrackCap = 200

// MirrorRequest is the agent-facing body for POST /v1/library/mirror.
type MirrorRequest struct {
	// Kind is "playlists", "tracks", "contacts", or "events".
	Kind string `json:"kind"`
	// SourceService is the service to read from. Empty uses the configured
	// canonical home for Kind, else the first connected provider.
	SourceService string `json:"source_service,omitempty"`
	// SourceID is the service-local playlist ID (required for kind=playlists).
	// Ignored for kind=tracks (mirrors the whole liked/saved list, capped).
	SourceID string `json:"source_id,omitempty"`
	// TargetService is the service to write to (required).
	TargetService string `json:"target_service"`
	// Name overrides the playlist title on the target (playlists only).
	Name string `json:"name,omitempty"`
	// DryRun plans the copy without writing to the target.
	DryRun bool `json:"dry_run,omitempty"`
}

// MirrorRef identifies one service-local item involved in a mirror.
type MirrorRef struct {
	Service string `json:"service"`
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	URL     string `json:"url,omitempty"`
	Count   int    `json:"count,omitempty"`
}

// MirrorSkip is one track that could not be placed on the target.
type MirrorSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

// MirrorConflict is one item the mirror refused to overwrite: the remote
// changed since the watermark (If-Match / etag mismatch).
type MirrorConflict struct {
	Name    string `json:"name"`
	Service string `json:"service"`
	ID      string `json:"id,omitempty"`
	Reason  string `json:"reason"`
}

// MirrorResult reports what a mirror planned or executed.
type MirrorResult struct {
	Kind         string           `json:"kind"`
	Source       MirrorRef        `json:"source"`
	Target       MirrorRef        `json:"target"`
	DryRun       bool             `json:"dry_run"`
	Planned      int              `json:"planned"`
	Copied       int              `json:"copied"`
	Skipped      int              `json:"skipped"`
	Skips        []MirrorSkip     `json:"skips,omitempty"`
	Conflicts    []MirrorConflict `json:"conflicts,omitempty"`
	Truncated    bool             `json:"truncated,omitempty"`
	IdentityNote string           `json:"identity_note"`
}

// HandleLibraryMirror copies a playlist or liked-tracks list from one service
// to another (issue #16 phase 3). Aegis still stores no media; it reads
// from the source service and writes through the target's upstream API using
// the stored OAuth token. Placement is reconciliation over data types
// (issue #254): core plans against neutral models, connectors execute the
// placement and report per-item skips.
//
// POST /v1/library/mirror
func (s *Server) HandleLibraryMirror(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req MirrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeBodyReadError(w, err)
			return
		}
		shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.SourceService = strings.TrimSpace(req.SourceService)
	req.TargetService = strings.TrimSpace(req.TargetService)
	req.SourceID = strings.TrimSpace(req.SourceID)

	switch req.Kind {
	case MirrorKindPlaylists, MirrorKindTracks, MirrorKindContacts, MirrorKindEvents:
	default:
		shared.WriteJSONError(w, http.StatusBadRequest, `kind must be "playlists", "tracks", "contacts", or "events"`)
		return
	}
	if req.TargetService == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "target_service is required")
		return
	}
	if req.Kind == MirrorKindPlaylists && req.SourceID == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_id is required for kind=playlists")
		return
	}
	if req.Kind == MirrorKindContacts || req.Kind == MirrorKindEvents {
		s.handleEntityMirror(w, r, req)
		return
	}

	logger := withPeerLog(slog.With("handler", "library_mirror",
		"kind", req.Kind, "target", req.TargetService, "dry_run", req.DryRun), r)

	if err := s.requireMirrorWritable(req.TargetService, req.Kind); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	sourceService := req.SourceService
	if sourceService == "" {
		sourceService = s.defaultMirrorSource(req.Kind)
	}
	if sourceService == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service is required (no canonical home configured)")
		return
	}
	if sourceService == req.TargetService {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service and target_service must differ")
		return
	}
	if err := s.requireMirrorReadable(sourceService, req.Kind); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	src, err := s.mirrorSourceFor(sourceService, req.Kind)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	dst, err := s.mirrorTargetFor(req.TargetService, req.Kind)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	var tracks []types.Track
	var srcRef MirrorRef
	var truncated bool

	switch req.Kind {
	case MirrorKindPlaylists:
		ref, items, tr, err := loadMirrorPlaylistItems(ctx, src, req.SourceID)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		tracks = items
		srcRef = mirrorRefFromPlaylist(sourceService, ref, len(items))
	case MirrorKindTracks:
		items, tr, err := src.ListUserLikedTracks(ctx)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		tracks = items
		srcRef = MirrorRef{Service: sourceService, Name: "liked/saved tracks", Count: len(items)}
	}

	if len(tracks) > mirrorTrackCap {
		tracks = tracks[:mirrorTrackCap]
		truncated = true
		srcRef.Count = len(tracks)
	}

	// Placement matching lives in the write operations: they honor their own
	// service's local references and catalog-search the rest. Service-local
	// IDs never transfer across APIs, so cross-service copies match by
	// name/artist there.

	name := strings.TrimSpace(req.Name)
	if name == "" {
		if req.Kind == MirrorKindPlaylists {
			name = srcRef.Name
		} else {
			name = "Aegis liked tracks backup"
		}
	}
	if name == "" {
		name = "Aegis mirror"
	}

	result := MirrorResult{
		Kind:         req.Kind,
		Source:       srcRef,
		DryRun:       req.DryRun,
		Planned:      len(tracks),
		Truncated:    truncated,
		IdentityNote: "Cross-service track matching is name-based. Skipped items need manual review before treating this as a complete backup.",
	}

	if req.DryRun {
		result.Target = MirrorRef{Service: req.TargetService, Name: name}
		result.Skipped = 0
		shared.WriteJSON(w, result)
		logger.Info("mirror dry-run", "source", sourceService, "planned", len(tracks))
		return
	}

	var skips []types.PlaceSkip
	switch req.Kind {
	case MirrorKindPlaylists:
		res, err := dst.PlacePlaylistItems(ctx, "", name, tracks)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		result.Target = MirrorRef{Service: req.TargetService, ID: res.PlaylistID, Name: name, URL: res.URL, Count: res.Copied}
		result.Copied = res.Copied
		skips = res.Skips
		s.recordMirrorCopy(MirrorKindPlaylists, sourceService, req.SourceID, req.TargetService, res.PlaylistID, name, res.Copied, "")
	case MirrorKindTracks:
		res, err := dst.SaveLikedTracks(ctx, tracks)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		result.Target = MirrorRef{Service: req.TargetService, Name: "liked/saved tracks", Count: res.Copied}
		result.Copied = res.Copied
		skips = res.Skips
		s.recordMirrorCopy(MirrorKindTracks, sourceService, "", req.TargetService, "", "liked/saved tracks", res.Copied, "")
	}
	result.Skips = toMirrorSkips(skips)
	result.Skipped = len(skips)

	logger.Info("mirror complete",
		"source", sourceService, "target", req.TargetService,
		"planned", result.Planned, "copied", result.Copied, "skipped", result.Skipped)
	shared.WriteJSON(w, result)
}

func (s *Server) writeMirrorUpstreamError(w http.ResponseWriter, logger *slog.Logger, serviceID string, err error) {
	logger.Warn("mirror upstream failed", "service", serviceID, "error", err)
	if errorsIsUnauthorized(err) {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			fmt.Sprintf("no valid token for %s — authorize at /v1/auth/%s/login", serviceID, serviceID))
		return
	}
	shared.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("mirror failed on %s: %v", serviceID, err))
}

func errorsIsUnauthorized(err error) bool {
	return err != nil && (err == types.ErrUnauthorized || strings.Contains(err.Error(), "unauthorized"))
}

// requireMirrorWritable checks the target allows writes for the kind.
func (s *Server) requireMirrorWritable(serviceID, kind string) error {
	svc := s.config.GetService(serviceID)
	if svc == nil {
		return fmt.Errorf("service %q not configured", serviceID)
	}
	if strings.EqualFold(svc.Action, "block") {
		return fmt.Errorf("service %q is blocked", serviceID)
	}
	if !s.effectivePermissions(svc).AllowsWrite(kind) {
		return fmt.Errorf("denied: data grant for agent write access to %s on %s is %q",
			kind, serviceID, s.effectivePermissions(svc).For(kind))
	}
	return nil
}

// requireMirrorReadable checks the source allows reads for the kind.
func (s *Server) requireMirrorReadable(serviceID, kind string) error {
	svc := s.config.GetService(serviceID)
	if svc == nil {
		return fmt.Errorf("service %q not configured", serviceID)
	}
	if strings.EqualFold(svc.Action, "block") {
		return fmt.Errorf("service %q is blocked", serviceID)
	}
	if !s.effectivePermissions(svc).AllowsRead(kind) {
		return fmt.Errorf("denied: data grant for agent read access to %s on %s is %q",
			kind, serviceID, s.effectivePermissions(svc).For(kind))
	}
	return nil
}

// defaultMirrorSource picks the canonical home for kind, else "".
func (s *Server) defaultMirrorSource(kind string) string {
	for _, svc := range s.config.ServiceSnapshots() {
		if svc.IsCanonicalFor(kind) {
			return svc.ID
		}
	}
	return ""
}

// mirrorSource is the read surface the mirror engine needs from a service:
// the type-level list operations reconciliation plans against.
type mirrorSource interface {
	types.PlaylistItemsProvider
	types.LikedTrackProvider
	types.LikedTrackPageProvider
}

// mirrorTarget is the write surface: the type-level placement operations
// (issue #254). Appending to a known playlist is what lets a mirror chain
// spread across job steps without creating a playlist per batch
// (issue #227 phase 3).
type mirrorTarget interface {
	types.PlaylistItemWriteProvider
	types.LikedTrackWriteProvider
}

func (s *Server) mirrorSourceFor(serviceID, kind string) (mirrorSource, error) {
	if s.mirrorSourceFn != nil {
		return s.mirrorSourceFn(serviceID, kind)
	}
	if conn, ok := s.connectors[serviceID].(mirrorSource); ok {
		return conn, nil
	}
	return nil, fmt.Errorf("mirror source %q is not supported yet (supported: %s)",
		serviceID, mirrorSupportedFor[mirrorSource](s))
}

func (s *Server) mirrorTargetFor(serviceID, kind string) (mirrorTarget, error) {
	if s.mirrorTargetFn != nil {
		return s.mirrorTargetFn(serviceID, kind)
	}
	if conn, ok := s.connectors[serviceID].(mirrorTarget); ok {
		return conn, nil
	}
	return nil, fmt.Errorf("mirror target %q is not supported yet (supported: %s)",
		serviceID, mirrorSupportedFor[mirrorTarget](s))
}

// mirrorSupportedFor lists registered services implementing P, so
// "not supported yet" copy stays accurate as connectors gain operations.
func mirrorSupportedFor[P any](s *Server) string {
	ids := make([]string, 0, len(s.connectors))
	for id, conn := range s.connectors {
		if _, ok := any(conn).(P); ok {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return "none"
	}
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

// loadMirrorPlaylistItems pages a source playlist through the items
// operation, up to shared.LibraryPageCap pages. truncated reports the cap was hit
// with more items still upstream.
func loadMirrorPlaylistItems(ctx context.Context, src types.PlaylistItemsProvider, id string) (types.Playlist, []types.Track, bool, error) {
	var ref types.Playlist
	var tracks []types.Track
	cursor := ""
	for page := 0; page < shared.LibraryPageCap; page++ {
		r, p, err := src.ListPlaylistItems(ctx, id, cursor, 100)
		if err != nil {
			return ref, tracks, false, err
		}
		if page == 0 {
			ref = r
		}
		tracks = append(tracks, p.Tracks...)
		if p.Next == "" {
			return ref, tracks, false, nil
		}
		cursor = p.Next
	}
	return ref, tracks, true, nil
}

// mirrorRefFromPlaylist projects a neutral playlist ref onto the API shape.
func mirrorRefFromPlaylist(service string, p types.Playlist, count int) MirrorRef {
	ref := MirrorRef{Service: service, Name: p.Name, Count: count}
	if len(p.Sources) > 0 {
		ref.ID = p.Sources[0].ID
		ref.URL = p.Sources[0].URL
	}
	return ref
}

// toMirrorSkips projects placement skips onto the API shape.
func toMirrorSkips(skips []types.PlaceSkip) []MirrorSkip {
	if len(skips) == 0 {
		return nil
	}
	out := make([]MirrorSkip, 0, len(skips))
	for _, sk := range skips {
		out = append(out, MirrorSkip{Name: sk.Name, Reason: sk.Reason})
	}
	return out
}

// toMirrorConflicts projects placement conflicts onto the API shape.
func toMirrorConflicts(conflicts []types.PlaceConflict) []MirrorConflict {
	if len(conflicts) == 0 {
		return nil
	}
	out := make([]MirrorConflict, 0, len(conflicts))
	for _, c := range conflicts {
		out = append(out, MirrorConflict{Name: c.Name, Service: c.Service, ID: c.ID, Reason: c.Reason})
	}
	return out
}

// mirrorOpFor resolves a provider operation surface P for a service from
// the connector registry, naming what implements P on failure.
func mirrorOpFor[P any](s *Server, serviceID, what string) (P, error) {
	if conn, ok := any(s.connectors[serviceID]).(P); ok {
		return conn, nil
	}
	var zero P
	return zero, fmt.Errorf("%s %q is not supported yet (supported: %s)",
		what, serviceID, mirrorSupportedFor[P](s))
}
