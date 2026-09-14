package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrUnauthorized is returned by PlaylistProvider when a service has no valid token.
var ErrUnauthorized = errors.New("unauthorized")

// PlaylistProvider is implemented by connectors that can return the user's
// playlists in Guardian's neutral format. The aggregate library endpoint uses
// this so agents can ask for "my playlists" once and receive every service.
// Each returned Playlist should carry one PlaylistSource for that service.
type PlaylistProvider interface {
	// ListUserPlaylists fetches the user's playlists from the upstream service
	// and maps them into neutral Playlist values (service identity in Sources).
	// Return ErrUnauthorized when no valid token is available.
	ListUserPlaylists(ctx context.Context) ([]Playlist, error)
}

// LikedTrackProvider is implemented by connectors that can return the user's
// liked/saved tracks in Guardian's neutral format. The aggregate library
// endpoint uses this so agents can ask for "my liked songs" once and receive
// every service. Each returned Track should carry one TrackSource for that
// service.
type LikedTrackProvider interface {
	// ListUserLikedTracks fetches the user's liked/saved tracks from the
	// upstream service and maps them into neutral Track values (service
	// identity in Sources). Return ErrUnauthorized when no valid token is
	// available.
	ListUserLikedTracks(ctx context.Context) ([]Track, error)
}

// HandleLibraryTracks returns the user's liked/saved tracks from every
// connected service in one neutral list: GET /v1/library/tracks
func (s *Server) HandleLibraryTracks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logger := withPeerLog(slog.With("handler", "library_tracks"), r)

	cacheKey := "library:tracks"
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Guardian-Cache", "hit")
		w.Write([]byte(cached))
		return
	}

	type serviceTarget struct {
		id       string
		provider LikedTrackProvider
	}

	var targets []serviceTarget
	var deniedTracks []string
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := connector.(LikedTrackProvider)
		if !ok {
			continue
		}
		if !svc.Permissions.AllowsRead("tracks") {
			deniedTracks = append(deniedTracks, id)
			logger.Info("library tracks denied by permission", "service", id,
				"permission", svc.Permissions.For("tracks"))
			continue
		}
		targets = append(targets, serviceTarget{id: id, provider: provider})
	}

	if len(targets) == 0 {
		if len(deniedTracks) > 0 {
			writeJSONError(w, http.StatusForbidden, "permission denied: agent access to tracks is not allowed")
			return
		}
		writeJSONError(w, http.StatusNotFound, "no liked-track-capable services configured")
		return
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		all   []Track
		bySvc = make(map[string]LibraryServiceStatus, len(targets))
	)

	ctx := r.Context()
	for _, t := range targets {
		wg.Add(1)
		go func(t serviceTarget) {
			defer wg.Done()
			tracks, err := t.provider.ListUserLikedTracks(ctx)
			var status LibraryServiceStatus
			switch {
			case err == nil:
				status = LibraryServiceStatus{Status: LibraryStatusOK, Count: len(tracks)}
				mu.Lock()
				all = append(all, tracks...)
				mu.Unlock()
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library tracks unauthorized", "service", t.id)
				status = LibraryServiceStatus{Status: LibraryStatusUnauthorized, Error: "not authorized — connect at /v1/auth/" + t.id + "/login"}
			default:
				logger.Warn("library tracks failed", "service", t.id, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: "failed to fetch liked tracks"}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	sort.SliceStable(all, func(i, j int) bool {
		si, sj := "", ""
		if len(all[i].Sources) > 0 {
			si = all[i].Sources[0].ServiceName
		}
		if len(all[j].Sources) > 0 {
			sj = all[j].Sources[0].ServiceName
		}
		if si != sj {
			return si < sj
		}
		return trackIdentityKey(all[i]) < trackIdentityKey(all[j])
	})

	merged := MergeTracksSync(all)

	resp := TrackList{
		Tracks:    merged,
		Total:     len(merged),
		Synced:    true,
		ByService: bySvc,
	}
	if resp.Tracks == nil {
		resp.Tracks = []Track{}
	}

	result, err := json.Marshal(resp)
	if err != nil {
		logger.Error("failed to marshal library tracks", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build track list")
		return
	}

	s.cache.Set(cacheKey, string(result), 60*time.Second)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.Write(result)
}

// hasLikedTrackProviders reports whether any registered connector can list
// neutral liked tracks for the library endpoint.
func (s *Server) hasLikedTrackProviders() bool {
	for _, connector := range s.connectors {
		if _, ok := connector.(LikedTrackProvider); ok {
			return true
		}
	}
	return false
}

// tracksLibraryReadable reports whether at least one configured service allows
// read access to tracks (used to decide whether to inject library hints).
func (s *Server) tracksLibraryReadable() bool {
	for _, svc := range s.config.Services {
		if svc.Action == "block" {
			continue
		}
		if svc.Permissions.AllowsRead("tracks") {
			return true
		}
	}
	return false
}

// libraryTracksSystemInstructions tells agents about the cross-service liked
// tracks endpoint.
func (s *Server) libraryTracksSystemInstructions() string {
	root := strings.TrimRight(s.baseURL, "/")
	return fmt.Sprintf(
		"Unified liked-songs library (use when the user asks about songs they like, saved tracks, or favorites):\n"+
			"GET %s/v1/library/tracks — all liked/saved tracks from every connected music service in one "+
			"neutral list (name, artists, album, duration, sources[]).\n"+
			"Same-named tracks by the same primary artist are synced into one entry with multiple sources[]. "+
			"Prefer this endpoint for questions like \"what songs have I liked?\" so the answer includes "+
			"Spotify, YouTube Music, and any other connected service.",
		root,
	)
}

// mentionsLikedTracks detects liked-song questions that don't name a service.
func mentionsLikedTracks(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range []string{
		"liked song",
		"liked track",
		"liked music",
		"saved song",
		"saved track",
		"favorite song",
		"favourite song",
		"favorite track",
		"favourite track",
		"have i liked",
		"songs i like",
		"tracks i like",
		"songs i've liked",
		"tracks i've liked",
		"songs have i liked",
		"tracks have i liked",
		"my likes",
		"my favorites",
		"my favourites",
	} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// AlbumProvider is implemented by connectors that can return the user's
// saved albums in Guardian's neutral format.
type AlbumProvider interface {
	ListUserSavedAlbums(ctx context.Context) ([]Album, error)
}

// ArtistProvider is implemented by connectors that can return the user's
// followed or top artists in Guardian's neutral format.
type ArtistProvider interface {
	ListUserFollowedArtists(ctx context.Context) ([]Artist, error)
}

// HandleLibraryAlbums returns the user's saved albums from every connected
// service in one neutral list: GET /v1/library/albums
func (s *Server) HandleLibraryAlbums(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logger := withPeerLog(slog.With("handler", "library_albums"), r)
	cacheKey := "library:albums"
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Guardian-Cache", "hit")
		w.Write([]byte(cached))
		return
	}

	type serviceTarget struct {
		id       string
		provider AlbumProvider
	}
	var targets []serviceTarget
	var denied []string
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := connector.(AlbumProvider)
		if !ok {
			continue
		}
		if !svc.Permissions.AllowsRead("albums") {
			denied = append(denied, id)
			logger.Info("library albums denied by permission", "service", id,
				"permission", svc.Permissions.For("albums"))
			continue
		}
		targets = append(targets, serviceTarget{id: id, provider: provider})
	}

	if len(targets) == 0 {
		if len(denied) > 0 {
			writeJSONError(w, http.StatusForbidden, "permission denied: agent access to albums is not allowed")
			return
		}
		writeJSONError(w, http.StatusNotFound, "no album-capable services configured")
		return
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		all   []Album
		bySvc = make(map[string]LibraryServiceStatus, len(targets))
	)
	ctx := r.Context()
	for _, t := range targets {
		wg.Add(1)
		go func(t serviceTarget) {
			defer wg.Done()
			albums, err := t.provider.ListUserSavedAlbums(ctx)
			var status LibraryServiceStatus
			switch {
			case err == nil:
				status = LibraryServiceStatus{Status: LibraryStatusOK, Count: len(albums)}
				mu.Lock()
				all = append(all, albums...)
				mu.Unlock()
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library albums unauthorized", "service", t.id)
				status = LibraryServiceStatus{Status: LibraryStatusUnauthorized, Error: "not authorized — connect at /v1/auth/" + t.id + "/login"}
			default:
				logger.Warn("library albums failed", "service", t.id, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: "failed to fetch albums"}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	sort.SliceStable(all, func(i, j int) bool {
		si, sj := "", ""
		if len(all[i].Sources) > 0 {
			si = all[i].Sources[0].ServiceName
		}
		if len(all[j].Sources) > 0 {
			sj = all[j].Sources[0].ServiceName
		}
		if si != sj {
			return si < sj
		}
		return albumIdentityKey(all[i]) < albumIdentityKey(all[j])
	})

	merged := MergeAlbumsSync(all)
	resp := AlbumList{Albums: merged, Total: len(merged), Synced: true, ByService: bySvc}
	if resp.Albums == nil {
		resp.Albums = []Album{}
	}
	result, err := json.Marshal(resp)
	if err != nil {
		logger.Error("failed to marshal library albums", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build album list")
		return
	}
	s.cache.Set(cacheKey, string(result), 60*time.Second)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.Write(result)
}

// HandleLibraryArtists returns the user's followed/top artists from every
// connected service in one neutral list: GET /v1/library/artists
func (s *Server) HandleLibraryArtists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logger := withPeerLog(slog.With("handler", "library_artists"), r)
	cacheKey := "library:artists"
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Guardian-Cache", "hit")
		w.Write([]byte(cached))
		return
	}

	type serviceTarget struct {
		id       string
		provider ArtistProvider
	}
	var targets []serviceTarget
	var denied []string
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := connector.(ArtistProvider)
		if !ok {
			continue
		}
		if !svc.Permissions.AllowsRead("artists") {
			denied = append(denied, id)
			logger.Info("library artists denied by permission", "service", id,
				"permission", svc.Permissions.For("artists"))
			continue
		}
		targets = append(targets, serviceTarget{id: id, provider: provider})
	}

	if len(targets) == 0 {
		if len(denied) > 0 {
			writeJSONError(w, http.StatusForbidden, "permission denied: agent access to artists is not allowed")
			return
		}
		writeJSONError(w, http.StatusNotFound, "no artist-capable services configured")
		return
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		all   []Artist
		bySvc = make(map[string]LibraryServiceStatus, len(targets))
	)
	ctx := r.Context()
	for _, t := range targets {
		wg.Add(1)
		go func(t serviceTarget) {
			defer wg.Done()
			artists, err := t.provider.ListUserFollowedArtists(ctx)
			var status LibraryServiceStatus
			switch {
			case err == nil:
				status = LibraryServiceStatus{Status: LibraryStatusOK, Count: len(artists)}
				mu.Lock()
				all = append(all, artists...)
				mu.Unlock()
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library artists unauthorized", "service", t.id)
				status = LibraryServiceStatus{Status: LibraryStatusUnauthorized, Error: "not authorized — connect at /v1/auth/" + t.id + "/login"}
			default:
				logger.Warn("library artists failed", "service", t.id, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: "failed to fetch artists"}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	sort.SliceStable(all, func(i, j int) bool {
		si, sj := "", ""
		if len(all[i].Sources) > 0 {
			si = all[i].Sources[0].ServiceName
		}
		if len(all[j].Sources) > 0 {
			sj = all[j].Sources[0].ServiceName
		}
		if si != sj {
			return si < sj
		}
		return NormalizeIdentityKey(all[i].Name) < NormalizeIdentityKey(all[j].Name)
	})

	merged := mergeArtistLibrary(all)
	resp := ArtistList{Artists: merged, Total: len(merged), Synced: true, ByService: bySvc}
	if resp.Artists == nil {
		resp.Artists = []Artist{}
	}
	result, err := json.Marshal(resp)
	if err != nil {
		logger.Error("failed to marshal library artists", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build artist list")
		return
	}
	s.cache.Set(cacheKey, string(result), 60*time.Second)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.Write(result)
}

func (s *Server) hasAlbumProviders() bool {
	for _, connector := range s.connectors {
		if _, ok := connector.(AlbumProvider); ok {
			return true
		}
	}
	return false
}

func (s *Server) hasArtistProviders() bool {
	for _, connector := range s.connectors {
		if _, ok := connector.(ArtistProvider); ok {
			return true
		}
	}
	return false
}

func (s *Server) albumsLibraryReadable() bool {
	for _, svc := range s.config.Services {
		if svc.Action == "block" {
			continue
		}
		if svc.Permissions.AllowsRead("albums") {
			return true
		}
	}
	return false
}

func (s *Server) artistsLibraryReadable() bool {
	for _, svc := range s.config.Services {
		if svc.Action == "block" {
			continue
		}
		if svc.Permissions.AllowsRead("artists") {
			return true
		}
	}
	return false
}

func (s *Server) libraryAlbumsSystemInstructions() string {
	root := strings.TrimRight(s.baseURL, "/")
	return fmt.Sprintf(
		"Unified saved-albums library (use when the user asks about albums they saved or their album library):\n"+
			"GET %s/v1/library/albums — all saved albums from every connected music service in one "+
			"neutral list (name, artists, sources[]).\n"+
			"Same-named albums by the same primary artist are synced into one entry with multiple sources[]. "+
			"Prefer this endpoint for questions like \"what albums have I saved?\".",
		root,
	)
}

func (s *Server) libraryArtistsSystemInstructions() string {
	root := strings.TrimRight(s.baseURL, "/")
	return fmt.Sprintf(
		"Unified followed-artists library (use when the user asks about artists they follow or favorite artists):\n"+
			"GET %s/v1/library/artists — all followed/top artists from every connected music service in one "+
			"neutral list (name, sources[]).\n"+
			"Same-named artists are synced into one entry with multiple sources[]. "+
			"Prefer this endpoint for questions like \"what artists do I follow?\".",
		root,
	)
}

func mentionsAlbums(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range []string{
		"saved album",
		"liked album",
		"favorite album",
		"favourite album",
		"albums i've saved",
		"albums have i saved",
		"albums i saved",
		"my albums",
		"album library",
	} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

func mentionsArtists(msg string) bool {
	lower := strings.ToLower(msg)
	for _, keyword := range []string{
		"followed artist",
		"artists i follow",
		"who do i follow",
		"artists i've followed",
		"favorite artist",
		"favourite artist",
		"my artists",
		"top artists",
		"artists i like",
	} {
		if strings.Contains(lower, keyword) {
			return true
		}
	}
	return false
}

// HandleLibraryPlaylists returns the user's playlists from every connected
// service in one neutral list: GET /v1/library/playlists
func (s *Server) HandleLibraryPlaylists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logger := withPeerLog(slog.With("handler", "library_playlists"), r)

	// Prefer cache for the full aggregate response.
	cacheKey := "library:playlists"
	if cached, ok := s.cache.Get(cacheKey); ok {
		logger.Debug("cache hit")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Guardian-Cache", "hit")
		w.Write([]byte(cached))
		return
	}

	type serviceTarget struct {
		id       string
		provider PlaylistProvider
	}

	// Collect services that have a connector supporting neutral playlists
	// and that the agent is allowed to read.
	var targets []serviceTarget
	var deniedPlaylists []string
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := connector.(PlaylistProvider)
		if !ok {
			continue
		}
		if !svc.Permissions.AllowsRead("playlists") {
			deniedPlaylists = append(deniedPlaylists, id)
			logger.Info("library playlists denied by permission", "service", id,
				"permission", svc.Permissions.For("playlists"))
			continue
		}
		targets = append(targets, serviceTarget{id: id, provider: provider})
	}

	if len(targets) == 0 {
		if len(deniedPlaylists) > 0 {
			writeJSONError(w, http.StatusForbidden, "permission denied: agent access to playlists is not allowed")
			return
		}
		writeJSONError(w, http.StatusNotFound, "no playlist-capable services configured")
		return
	}

	var (
		mu    sync.Mutex
		wg    sync.WaitGroup
		all   []Playlist
		bySvc = make(map[string]LibraryServiceStatus, len(targets))
	)

	ctx := r.Context()
	for _, t := range targets {
		wg.Add(1)
		go func(t serviceTarget) {
			defer wg.Done()
			playlists, err := t.provider.ListUserPlaylists(ctx)
			var status LibraryServiceStatus
			switch {
			case err == nil:
				status = LibraryServiceStatus{Status: LibraryStatusOK, Count: len(playlists)}
				mu.Lock()
				all = append(all, playlists...)
				mu.Unlock()
			case errors.Is(err, ErrUnauthorized):
				logger.Info("library playlists unauthorized", "service", t.id)
				status = LibraryServiceStatus{Status: LibraryStatusUnauthorized, Error: "not authorized — connect at /v1/auth/" + t.id + "/login"}
			default:
				logger.Warn("library playlists failed", "service", t.id, "error", err)
				status = LibraryServiceStatus{Status: LibraryStatusError, Error: "failed to fetch playlists"}
			}
			mu.Lock()
			bySvc[t.id] = status
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	// Stable order before merge: by service name, then playlist name.
	sort.SliceStable(all, func(i, j int) bool {
		si, sj := "", ""
		if len(all[i].Sources) > 0 {
			si = all[i].Sources[0].ServiceName
		}
		if len(all[j].Sources) > 0 {
			sj = all[j].Sources[0].ServiceName
		}
		if si != sj {
			return si < sj
		}
		return all[i].Name < all[j].Name
	})

	// Sync: same-named playlists across services become one user-facing
	// playlist with multiple sources.
	merged := MergePlaylistsSync(all)

	resp := PlaylistList{
		Playlists: merged,
		Total:     len(merged),
		Synced:    true,
		ByService: bySvc,
	}
	if resp.Playlists == nil {
		resp.Playlists = []Playlist{}
	}

	result, err := json.Marshal(resp)
	if err != nil {
		logger.Error("failed to marshal library playlists", "error", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to build playlist list")
		return
	}

	// Short TTL: aggregate spans multiple APIs and should stay fresh.
	s.cache.Set(cacheKey, string(result), 60*time.Second)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.Write(result)
}
