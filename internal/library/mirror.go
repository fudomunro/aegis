package library

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Mirror kinds: the reconciliation kind ids. Playlists and tracks are
// supported by POST /v1/library/mirror; albums and artists join for sync
// convergence (issue #375).
const (
	MirrorKindPlaylists = "playlists"
	MirrorKindTracks    = "tracks"
	MirrorKindContacts  = "contacts"
	MirrorKindEvents    = "events"
	MirrorKindAlbums    = "albums"
	MirrorKindArtists   = "artists"
)

// MirrorTrackCap bounds a single mirror call so a huge library cannot
// turn into an unbounded write storm. Callers can re-run for the rest.
const MirrorTrackCap = 200

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
// the stored OAuth token. Placement is reconciliation over kinds
// (issue #254): core plans against neutral models, connectors execute the
// placement and report per-item skips.
//
// POST /v1/library/mirror
func (h *Handlers) HandleLibraryMirror(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req MirrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			shared.WriteBodyReadError(w, err)
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
		h.HandleEntityMirror(w, r, req)
		return
	}

	logger := h.Host.Logger(r, "library_mirror").With(
		"kind", req.Kind, "target", req.TargetService, "dry_run", req.DryRun)

	if err := h.RequireMirrorWritable(h.requestGrants(r), req.TargetService, req.Kind); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	sourceService := req.SourceService
	if sourceService == "" {
		sourceService = h.DefaultMirrorSource(req.Kind)
	}
	if sourceService == "" {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service is required (no canonical home configured)")
		return
	}
	if sourceService == req.TargetService {
		shared.WriteJSONError(w, http.StatusBadRequest, "source_service and target_service must differ")
		return
	}
	if err := h.RequireMirrorReadable(h.requestGrants(r), sourceService, req.Kind); err != nil {
		shared.WriteJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	src, err := h.MirrorSourceFor(sourceService, req.Kind)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	dst, err := h.MirrorTargetFor(req.TargetService, req.Kind)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	var tracks []kinds.Track
	var srcRef MirrorRef
	var truncated bool

	switch req.Kind {
	case MirrorKindPlaylists:
		ref, items, tr, err := loadMirrorPlaylistItems(ctx, src, req.SourceID)
		if err != nil {
			h.WriteMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		tracks = items
		srcRef = MirrorRefFromPlaylist(sourceService, ref, len(items))
	case MirrorKindTracks:
		items, tr, err := src.ListUserLikedTracks(ctx)
		if err != nil {
			h.WriteMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		tracks = items
		srcRef = MirrorRef{Service: sourceService, Name: "liked/saved tracks", Count: len(items)}
	}

	if len(tracks) > MirrorTrackCap {
		tracks = tracks[:MirrorTrackCap]
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
		IdentityNote: "Cross-service track matching prefers durable ids and falls back to duration-guarded names. Skipped items need manual review before treating this as a complete backup.",
	}

	if req.DryRun {
		result.Target = MirrorRef{Service: req.TargetService, Name: name}
		result.Skipped = 0
		shared.WriteJSON(w, result)
		logger.Info("mirror dry-run", "source", sourceService, "planned", len(tracks))
		return
	}

	var skips []kinds.PlaceSkip
	switch req.Kind {
	case MirrorKindPlaylists:
		res, err := dst.PlacePlaylistItems(ctx, "", name, tracks)
		if err != nil {
			h.WriteMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		result.Target = MirrorRef{Service: req.TargetService, ID: res.PlaylistID, Name: name, URL: res.URL, Count: res.Copied}
		result.Copied = res.Copied
		skips = res.Skips
		h.RecordMirrorCopy(MirrorKindPlaylists, sourceService, req.SourceID, req.TargetService, res.PlaylistID, name, res.Copied, "")
	case MirrorKindTracks:
		res, err := dst.SaveLikedTracks(ctx, tracks)
		if err != nil {
			h.WriteMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		result.Target = MirrorRef{Service: req.TargetService, Name: "liked/saved tracks", Count: res.Copied}
		result.Copied = res.Copied
		skips = res.Skips
		h.RecordMirrorCopy(MirrorKindTracks, sourceService, "", req.TargetService, "", "liked/saved tracks", res.Copied, "")
	}
	result.Skips = toMirrorSkips(skips)
	result.Skipped = len(skips)

	logger.Info("mirror complete",
		"source", sourceService, "target", req.TargetService,
		"planned", result.Planned, "copied", result.Copied, "skipped", result.Skipped)
	shared.WriteJSON(w, result)
}

func (h *Handlers) WriteMirrorUpstreamError(w http.ResponseWriter, logger *slog.Logger, serviceID string, err error) {
	logger.Warn("mirror upstream failed", "service", serviceID, "error", err)
	if ErrorsIsUnauthorized(err) {
		shared.WriteJSONError(w, http.StatusUnauthorized,
			fmt.Sprintf("no valid token for %s — authorize at /v1/auth/%s/login", serviceID, serviceID))
		return
	}
	shared.WriteJSONError(w, http.StatusBadGateway, fmt.Sprintf("mirror failed on %s: %v", serviceID, err))
}

func ErrorsIsUnauthorized(err error) bool {
	return err != nil && (err == kinds.ErrUnauthorized || strings.Contains(err.Error(), "unauthorized"))
}

// requireMirrorWritable checks the target allows writes for the kind under
// the caller's grant map.
func (h *Handlers) RequireMirrorWritable(grants shared.GrantMap, serviceID, kind string) error {
	svc, ok := h.Host.Service(serviceID)
	if !ok {
		return fmt.Errorf("service %q not configured", serviceID)
	}
	if strings.EqualFold(svc.Action, "block") {
		return fmt.Errorf("service %q is blocked", serviceID)
	}
	if !grants.AllowsWrite(kind) {
		return fmt.Errorf("denied: data grant for agent write access to %s on %s is %q",
			kind, serviceID, grants.Level(kind))
	}
	return nil
}

// requireMirrorReadable checks the source allows reads for the kind under
// the caller's grant map.
func (h *Handlers) RequireMirrorReadable(grants shared.GrantMap, serviceID, kind string) error {
	svc, ok := h.Host.Service(serviceID)
	if !ok {
		return fmt.Errorf("service %q not configured", serviceID)
	}
	if strings.EqualFold(svc.Action, "block") {
		return fmt.Errorf("service %q is blocked", serviceID)
	}
	if !grants.AllowsRead(kind) {
		return fmt.Errorf("denied: data grant for agent read access to %s on %s is %q",
			kind, serviceID, grants.Level(kind))
	}
	return nil
}

// defaultMirrorSource picks the canonical home for kind, else "".
func (h *Handlers) DefaultMirrorSource(kind string) string {
	for _, svc := range h.Host.Services() {
		if svc.IsCanonicalFor(kind) {
			return svc.ID
		}
	}
	return ""
}

// MirrorSource is the read surface the mirror engine needs from a service:
// the type-level list operations reconciliation plans against.
type MirrorSource interface {
	kinds.PlaylistItemsProvider
	kinds.LikedTrackProvider
	kinds.LikedTrackPageProvider
}

// MirrorTarget is the write surface: the type-level placement operations
// (issue #254). Appending to a known playlist is what lets a mirror chain
// spread across job steps without creating a playlist per batch
// (issue #227 phase 3).
type MirrorTarget interface {
	kinds.PlaylistItemWriteProvider
	kinds.LikedTrackWriteProvider
}

func (h *Handlers) MirrorSourceFor(serviceID, kind string) (MirrorSource, error) {
	if h.MirrorSourceFn != nil {
		return h.MirrorSourceFn(serviceID, kind)
	}
	if conn, ok := h.Connectors[serviceID].(MirrorSource); ok {
		return conn, nil
	}
	return nil, fmt.Errorf("mirror source %q is not supported yet (supported: %s)",
		serviceID, MirrorSupportedFor[MirrorSource](h))
}

func (h *Handlers) MirrorTargetFor(serviceID, kind string) (MirrorTarget, error) {
	if h.MirrorTargetFn != nil {
		return h.MirrorTargetFn(serviceID, kind)
	}
	if conn, ok := h.Connectors[serviceID].(MirrorTarget); ok {
		return conn, nil
	}
	return nil, fmt.Errorf("mirror target %q is not supported yet (supported: %s)",
		serviceID, MirrorSupportedFor[MirrorTarget](h))
}

// mirrorSupportedFor lists registered services implementing P, so
// "not supported yet" copy stays accurate as connectors gain operations.
func MirrorSupportedFor[P any](h *Handlers) string {
	ids := make([]string, 0, len(h.Connectors))
	for id, conn := range h.Connectors {
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
func loadMirrorPlaylistItems(ctx context.Context, src kinds.PlaylistItemsProvider, id string) (kinds.Playlist, []kinds.Track, bool, error) {
	var ref kinds.Playlist
	var tracks []kinds.Track
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
func MirrorRefFromPlaylist(service string, p kinds.Playlist, count int) MirrorRef {
	ref := MirrorRef{Service: service, Name: p.Name, Count: count}
	if len(p.Sources) > 0 {
		ref.ID = p.Sources[0].ID
		ref.URL = p.Sources[0].URL
	}
	return ref
}

// toMirrorSkips projects placement skips onto the API shape.
func toMirrorSkips(skips []kinds.PlaceSkip) []MirrorSkip {
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
func toMirrorConflicts(conflicts []kinds.PlaceConflict) []MirrorConflict {
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
func MirrorOpFor[P any](h *Handlers, serviceID, what string) (P, error) {
	if conn, ok := any(h.Connectors[serviceID]).(P); ok {
		return conn, nil
	}
	var zero P
	return zero, fmt.Errorf("%s %q is not supported yet (supported: %s)",
		what, serviceID, MirrorSupportedFor[P](h))
}
