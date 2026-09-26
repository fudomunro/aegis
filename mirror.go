package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

// Mirror kinds supported by POST /v1/library/mirror.
const (
	MirrorKindPlaylists = "playlists"
	MirrorKindTracks    = "tracks"
)

// mirrorTrackCap bounds a single mirror call so a huge library cannot
// turn into an unbounded write storm. Callers can re-run for the rest.
const mirrorTrackCap = 200

// MirrorRequest is the agent-facing body for POST /v1/library/mirror.
type MirrorRequest struct {
	// Kind is "playlists" or "tracks" (liked/saved tracks).
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

// MirrorResult reports what a mirror planned or executed.
type MirrorResult struct {
	Kind         string       `json:"kind"`
	Source       MirrorRef    `json:"source"`
	Target       MirrorRef    `json:"target"`
	DryRun       bool         `json:"dry_run"`
	Planned      int          `json:"planned"`
	Copied       int          `json:"copied"`
	Skipped      int          `json:"skipped"`
	Skips        []MirrorSkip `json:"skips,omitempty"`
	Truncated    bool         `json:"truncated,omitempty"`
	IdentityNote string       `json:"identity_note"`
}

// mirrorTrack is a neutral track used while copying between services.
type mirrorTrack struct {
	Name       string
	Artists    []string
	Album      string
	DurationMs int
	// SourceID / SourceURI are set when the source service can name the item.
	SourceID  string
	SourceURI string
}

func (t mirrorTrack) display() string {
	artists := strings.Join(t.Artists, ", ")
	if artists == "" {
		return t.Name
	}
	return t.Name + " — " + artists
}

func (t mirrorTrack) searchQuery() string {
	if len(t.Artists) == 0 {
		return t.Name
	}
	return t.Name + " " + t.Artists[0]
}

// HandleLibraryMirror copies a playlist or liked-tracks list from one service
// to another (issue #16 phase 3). Aegis still stores no media; it reads
// from the source service and writes through the target's upstream API using
// the stored OAuth token.
//
// POST /v1/library/mirror
func (s *Server) HandleLibraryMirror(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req MirrorRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeBodyReadError(w, err)
			return
		}
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	req.Kind = strings.ToLower(strings.TrimSpace(req.Kind))
	req.SourceService = strings.TrimSpace(req.SourceService)
	req.TargetService = strings.TrimSpace(req.TargetService)
	req.SourceID = strings.TrimSpace(req.SourceID)

	switch req.Kind {
	case MirrorKindPlaylists, MirrorKindTracks:
	default:
		writeJSONError(w, http.StatusBadRequest, `kind must be "playlists" or "tracks"`)
		return
	}
	if req.TargetService == "" {
		writeJSONError(w, http.StatusBadRequest, "target_service is required")
		return
	}
	if req.Kind == MirrorKindPlaylists && req.SourceID == "" {
		writeJSONError(w, http.StatusBadRequest, "source_id is required for kind=playlists")
		return
	}

	logger := withPeerLog(slog.With("handler", "library_mirror",
		"kind", req.Kind, "target", req.TargetService, "dry_run", req.DryRun), r)

	if err := s.requireMirrorWritable(req.TargetService, req.Kind); err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	sourceService := req.SourceService
	if sourceService == "" {
		sourceService = s.defaultMirrorSource(req.Kind)
	}
	if sourceService == "" {
		writeJSONError(w, http.StatusBadRequest, "source_service is required (no canonical home configured)")
		return
	}
	if sourceService == req.TargetService {
		writeJSONError(w, http.StatusBadRequest, "source_service and target_service must differ")
		return
	}
	if err := s.requireMirrorReadable(sourceService, req.Kind); err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}

	src, err := s.mirrorSourceFor(sourceService, req.Kind)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	dst, err := s.mirrorTargetFor(req.TargetService, req.Kind)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx := r.Context()
	var tracks []mirrorTrack
	var srcRef MirrorRef
	var truncated bool

	switch req.Kind {
	case MirrorKindPlaylists:
		pl, items, tr, err := src.loadPlaylist(ctx, req.SourceID)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, sourceService, err)
			return
		}
		truncated = tr
		tracks = items
		srcRef = MirrorRef{Service: sourceService, ID: pl.ID, Name: pl.Name, URL: pl.URL, Count: len(items)}
	case MirrorKindTracks:
		items, tr, err := src.loadLikedTracks(ctx)
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

	// Service-local IDs/URIs do not transfer across APIs. Force the target
	// adapter to search by name/artist when source and target differ.
	if sourceService != req.TargetService {
		for i := range tracks {
			tracks[i].SourceID = ""
			tracks[i].SourceURI = ""
		}
	}

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
		IdentityNote: "Cross-service track matching is name-based (issue #4). Skipped items need manual review before treating this as a complete backup.",
	}

	if req.DryRun {
		result.Target = MirrorRef{Service: req.TargetService, Name: name}
		result.Skipped = 0
		writeJSON(w, result)
		logger.Info("mirror dry-run", "source", sourceService, "planned", len(tracks))
		return
	}

	var skips []MirrorSkip
	switch req.Kind {
	case MirrorKindPlaylists:
		id, url, copied, sk, err := dst.createPlaylistWithTracks(ctx, name, tracks)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		result.Target = MirrorRef{Service: req.TargetService, ID: id, Name: name, URL: url, Count: copied}
		result.Copied = copied
		skips = sk
		s.recordMirrorCopy(MirrorKindPlaylists, sourceService, req.SourceID, req.TargetService, id, name, copied, "")
	case MirrorKindTracks:
		copied, sk, err := dst.saveLikedTracks(ctx, tracks)
		if err != nil {
			s.writeMirrorUpstreamError(w, logger, req.TargetService, err)
			return
		}
		result.Target = MirrorRef{Service: req.TargetService, Name: "liked/saved tracks", Count: copied}
		result.Copied = copied
		skips = sk
		s.recordMirrorCopy(MirrorKindTracks, sourceService, "", req.TargetService, "", "liked/saved tracks", copied, "")
	}
	result.Skips = skips
	result.Skipped = len(skips)

	logger.Info("mirror complete",
		"source", sourceService, "target", req.TargetService,
		"planned", result.Planned, "copied", result.Copied, "skipped", result.Skipped)
	writeJSON(w, result)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}

func (s *Server) writeMirrorUpstreamError(w http.ResponseWriter, logger *slog.Logger, serviceID string, err error) {
	logger.Warn("mirror upstream failed", "service", serviceID, "error", err)
	if errorsIsUnauthorized(err) {
		writeJSONError(w, http.StatusUnauthorized,
			fmt.Sprintf("no valid token for %s — authorize at /v1/auth/%s/login", serviceID, serviceID))
		return
	}
	writeJSONError(w, http.StatusBadGateway, fmt.Sprintf("mirror failed on %s: %v", serviceID, err))
}

func errorsIsUnauthorized(err error) bool {
	return err != nil && (err == ErrUnauthorized || strings.Contains(err.Error(), "unauthorized"))
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
	if !svc.Permissions.AllowsWrite(kind) {
		return fmt.Errorf("permission denied: agent write access to %s on %s is %q",
			kind, serviceID, svc.Permissions.For(kind))
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
	if !svc.Permissions.AllowsRead(kind) {
		return fmt.Errorf("permission denied: agent read access to %s on %s is %q",
			kind, serviceID, svc.Permissions.For(kind))
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

type mirrorSource interface {
	loadPlaylist(ctx context.Context, id string) (ref MirrorRef, tracks []mirrorTrack, truncated bool, err error)
	loadLikedTracks(ctx context.Context) (tracks []mirrorTrack, truncated bool, err error)
}

type mirrorTarget interface {
	createPlaylistWithTracks(ctx context.Context, name string, tracks []mirrorTrack) (id, url string, copied int, skips []MirrorSkip, err error)
	saveLikedTracks(ctx context.Context, tracks []mirrorTrack) (copied int, skips []MirrorSkip, err error)
}

func (s *Server) mirrorSourceFor(serviceID, kind string) (mirrorSource, error) {
	switch serviceID {
	case "spotify":
		return &spotifyMirror{s: s}, nil
	case "youtube-music":
		return &youtubeMirror{s: s}, nil
	}
	return nil, fmt.Errorf("mirror source %q is not supported yet (supported: spotify, youtube-music)", serviceID)
}

func (s *Server) mirrorTargetFor(serviceID, kind string) (mirrorTarget, error) {
	switch serviceID {
	case "spotify":
		return &spotifyMirror{s: s}, nil
	case "youtube-music":
		return &youtubeMirror{s: s}, nil
	}
	return nil, fmt.Errorf("mirror target %q is not supported yet (supported: spotify, youtube-music)", serviceID)
}

// mirrorUpstream performs a raw JSON call to a service's configured endpoint
// using the stored OAuth token. Unlike HandleServiceProxy it does not run
// connector response transforms, so callers can use native API shapes.
func (s *Server) mirrorUpstream(ctx context.Context, serviceID, method, path string, query url.Values, body any) (map[string]any, int, error) {
	svc := s.config.GetService(serviceID)
	if svc == nil {
		return nil, 0, fmt.Errorf("service %q not configured", serviceID)
	}
	if svc.Endpoint == "" {
		return nil, 0, fmt.Errorf("service %q has no upstream endpoint", serviceID)
	}
	token := s.getValidToken(serviceID, slog.Default())
	if token == "" {
		return nil, 0, ErrUnauthorized
	}

	u := strings.TrimRight(svc.Endpoint, "/") + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		bodyReader = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, resp.StatusCode, ErrUnauthorized
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(data))
		if len(msg) > 240 {
			msg = msg[:240] + "…"
		}
		return nil, resp.StatusCode, fmt.Errorf("upstream %s %s returned %d: %s", method, path, resp.StatusCode, msg)
	}
	if len(data) == 0 {
		return map[string]any{}, resp.StatusCode, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("decode upstream response: %w", err)
	}
	return parsed, resp.StatusCode, nil
}
