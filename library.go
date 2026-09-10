package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
)

// ErrUnauthorized is returned by PlaylistProvider when a service has no valid token.
var ErrUnauthorized = errors.New("unauthorized")

// PlaylistProvider is implemented by connectors that can return the user's
// playlists in Guardian's neutral format. The aggregate library endpoint uses
// this so agents can ask for "my playlists" once and receive every service.
type PlaylistProvider interface {
	// ListUserPlaylists fetches the user's playlists from the upstream service
	// and maps them into neutral Playlist values.
	// Return ErrUnauthorized when no valid token is available.
	ListUserPlaylists(ctx context.Context) ([]Playlist, error)
}

// HandleLibraryPlaylists returns the user's playlists from every connected
// service in one neutral list: GET /v1/library/playlists
func (s *Server) HandleLibraryPlaylists(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	logger := slog.With("handler", "library_playlists")

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

	// Collect services that have a connector supporting neutral playlists.
	var targets []serviceTarget
	for id, connector := range s.connectors {
		svc := s.config.GetService(id)
		if svc == nil || svc.Action == "block" {
			continue
		}
		provider, ok := connector.(PlaylistProvider)
		if !ok {
			continue
		}
		targets = append(targets, serviceTarget{id: id, provider: provider})
	}

	if len(targets) == 0 {
		writeJSONError(w, http.StatusNotFound, "no playlist-capable services configured")
		return
	}

	var (
		mu     sync.Mutex
		wg     sync.WaitGroup
		all    []Playlist
		bySvc  = make(map[string]LibraryServiceStatus, len(targets))
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

	// Stable order: by service name, then playlist name.
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].ServiceName != all[j].ServiceName {
			return all[i].ServiceName < all[j].ServiceName
		}
		return all[i].Name < all[j].Name
	})

	resp := PlaylistList{
		Playlists: all,
		Total:     len(all),
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
	s.cache.Set(cacheKey, string(result), 60)

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Guardian-Cache", "miss")
	w.Write(result)
}
