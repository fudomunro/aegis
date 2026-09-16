package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"
)

// spotifyMirror implements mirrorSource and mirrorTarget against the Spotify Web API.
type spotifyMirror struct {
	s *Server
}

func (m *spotifyMirror) loadPlaylist(ctx context.Context, id string) (MirrorRef, []mirrorTrack, bool, error) {
	raw, _, err := m.s.mirrorUpstream(ctx, "spotify", "GET", "/playlists/"+url.PathEscape(id), nil, nil)
	if err != nil {
		return MirrorRef{}, nil, false, err
	}
	name, _ := raw["name"].(string)
	ext, _ := raw["external_urls"].(map[string]any)
	link, _ := ext["spotify"].(string)
	ref := MirrorRef{Service: "spotify", ID: id, Name: name, URL: link}

	tracks, truncated, err := m.collectPlaylistTracks(ctx, id)
	if err != nil {
		return ref, nil, truncated, err
	}
	ref.Count = len(tracks)
	return ref, tracks, truncated, nil
}

func (m *spotifyMirror) collectPlaylistTracks(ctx context.Context, playlistID string) ([]mirrorTrack, bool, error) {
	var out []mirrorTrack
	q := url.Values{"limit": []string{"100"}}
	truncated := false
	for page := 0; page < libraryPageCap; page++ {
		raw, _, err := m.s.mirrorUpstream(ctx, "spotify", "GET",
			"/playlists/"+url.PathEscape(playlistID)+"/tracks", q, nil)
		if err != nil {
			return out, truncated, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			e, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := e["track"].(map[string]any)
			if t == nil {
				continue
			}
			out = append(out, mirrorTrackFromSpotify(t))
		}
		next, _ := raw["next"].(string)
		if next == "" {
			return out, false, nil
		}
		if u, err := url.Parse(next); err == nil {
			q = u.Query()
		}
	}
	return out, true, nil
}

func mirrorTrackFromSpotify(t map[string]any) mirrorTrack {
	name, _ := t["name"].(string)
	uri, _ := t["uri"].(string)
	id, _ := t["id"].(string)
	var artists []string
	if arr, ok := t["artists"].([]any); ok {
		for _, a := range arr {
			am, _ := a.(map[string]any)
			if am == nil {
				continue
			}
			if n, _ := am["name"].(string); n != "" {
				artists = append(artists, n)
			}
		}
	}
	album := ""
	if am, ok := t["album"].(map[string]any); ok {
		album, _ = am["name"].(string)
	}
	dur := 0
	switch v := t["duration_ms"].(type) {
	case float64:
		dur = int(v)
	case int:
		dur = v
	}
	return mirrorTrack{
		Name:       name,
		Artists:    artists,
		Album:      album,
		DurationMs: dur,
		SourceID:   id,
		SourceURI:  uri,
	}
}

func (m *spotifyMirror) loadLikedTracks(ctx context.Context) ([]mirrorTrack, bool, error) {
	var out []mirrorTrack
	nextPath := "/me/tracks"
	q := url.Values{"limit": []string{"50"}}
	for page := 0; page < libraryPageCap; page++ {
		raw, _, err := m.s.mirrorUpstream(ctx, "spotify", "GET", nextPath, q, nil)
		if err != nil {
			return out, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			saved, ok := item.(map[string]any)
			if !ok {
				continue
			}
			t, _ := saved["track"].(map[string]any)
			if t == nil {
				continue
			}
			out = append(out, mirrorTrackFromSpotify(t))
		}
		next, _ := raw["next"].(string)
		if next == "" {
			return out, false, nil
		}
		if u, err := url.Parse(next); err == nil {
			nextPath = u.Path
			if strings.HasPrefix(nextPath, "/v1") {
				nextPath = strings.TrimPrefix(nextPath, "/v1")
			}
			q = u.Query()
		}
	}
	return out, true, nil
}

func (m *spotifyMirror) createPlaylistWithTracks(ctx context.Context, name string, tracks []mirrorTrack) (string, string, int, []MirrorSkip, error) {
	me, _, err := m.s.mirrorUpstream(ctx, "spotify", "GET", "/me", nil, nil)
	if err != nil {
		return "", "", 0, nil, err
	}
	userID, _ := me["id"].(string)
	if userID == "" {
		return "", "", 0, nil, fmt.Errorf("spotify /me returned no user id")
	}

	created, _, err := m.s.mirrorUpstream(ctx, "spotify", "POST",
		"/users/"+url.PathEscape(userID)+"/playlists", nil,
		map[string]any{"name": name, "public": false, "description": "Copied by Aperture Guardian"})
	if err != nil {
		return "", "", 0, nil, err
	}
	playlistID, _ := created["id"].(string)
	if playlistID == "" {
		return "", "", 0, nil, fmt.Errorf("spotify create playlist returned no id")
	}
	link := ""
	if ext, ok := created["external_urls"].(map[string]any); ok {
		link, _ = ext["spotify"].(string)
	}

	var uris []string
	var skips []MirrorSkip
	for _, t := range tracks {
		uri := t.SourceURI
		if uri == "" && t.SourceID != "" {
			uri = "spotify:track:" + t.SourceID
		}
		if uri == "" {
			found, err := m.searchTrackURI(ctx, t)
			if err != nil {
				skips = append(skips, MirrorSkip{Name: t.display(), Reason: err.Error()})
				continue
			}
			uri = found
		}
		if uri == "" {
			skips = append(skips, MirrorSkip{Name: t.display(), Reason: "no match on Spotify"})
			continue
		}
		uris = append(uris, uri)
	}

	const chunk = 100
	copied := 0
	for i := 0; i < len(uris); i += chunk {
		end := i + chunk
		if end > len(uris) {
			end = len(uris)
		}
		_, _, err := m.s.mirrorUpstream(ctx, "spotify", "POST",
			"/playlists/"+url.PathEscape(playlistID)+"/tracks", nil,
			map[string]any{"uris": uris[i:end]})
		if err != nil {
			return playlistID, link, copied, skips, err
		}
		copied += end - i
	}
	return playlistID, link, copied, skips, nil
}

func (m *spotifyMirror) searchTrackURI(ctx context.Context, t mirrorTrack) (string, error) {
	q := url.Values{
		"q":     []string{t.searchQuery()},
		"type":  []string{"track"},
		"limit": []string{"1"},
	}
	raw, _, err := m.s.mirrorUpstream(ctx, "spotify", "GET", "/search", q, nil)
	if err != nil {
		return "", err
	}
	tracks, _ := raw["tracks"].(map[string]any)
	items, _ := tracks["items"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", nil
	}
	uri, _ := first["uri"].(string)
	return uri, nil
}

func (m *spotifyMirror) saveLikedTracks(ctx context.Context, tracks []mirrorTrack) (int, []MirrorSkip, error) {
	var ids []string
	var skips []MirrorSkip
	for _, t := range tracks {
		id := t.SourceID
		if id == "" && strings.HasPrefix(t.SourceURI, "spotify:track:") {
			id = strings.TrimPrefix(t.SourceURI, "spotify:track:")
		}
		if id == "" {
			uri, err := m.searchTrackURI(ctx, t)
			if err != nil {
				skips = append(skips, MirrorSkip{Name: t.display(), Reason: err.Error()})
				continue
			}
			if strings.HasPrefix(uri, "spotify:track:") {
				id = strings.TrimPrefix(uri, "spotify:track:")
			}
		}
		if id == "" {
			skips = append(skips, MirrorSkip{Name: t.display(), Reason: "no match on Spotify"})
			continue
		}
		ids = append(ids, id)
	}

	const chunk = 50
	copied := 0
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		q := url.Values{"ids": []string{strings.Join(ids[i:end], ",")}}
		_, _, err := m.s.mirrorUpstream(ctx, "spotify", "PUT", "/me/tracks", q, nil)
		if err != nil {
			return copied, skips, err
		}
		copied += end - i
	}
	return copied, skips, nil
}
