package main

import (
	"context"
	"fmt"
	"net/url"
)

// youtubeMirror implements mirrorSource and mirrorTarget against YouTube Data API v3
// (the same upstream the youtube-music connector uses).
type youtubeMirror struct {
	s *Server
}

func (m *youtubeMirror) loadPlaylist(ctx context.Context, id string) (MirrorRef, []mirrorTrack, bool, error) {
	raw, _, err := m.s.mirrorUpstream(ctx, "youtube-music", "GET", "/playlists", url.Values{
		"part": []string{"snippet,contentDetails"},
		"id":   []string{id},
	}, nil)
	if err != nil {
		return MirrorRef{}, nil, false, err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return MirrorRef{}, nil, false, fmt.Errorf("playlist %q not found on youtube-music", id)
	}
	pl, _ := items[0].(map[string]any)
	snippet, _ := pl["snippet"].(map[string]any)
	title, _ := snippet["title"].(string)
	ref := MirrorRef{
		Service: "youtube-music",
		ID:      id,
		Name:    title,
		URL:     "https://www.youtube.com/playlist?list=" + id,
	}

	tracks, truncated, err := m.collectPlaylistItems(ctx, id)
	if err != nil {
		return ref, nil, truncated, err
	}
	ref.Count = len(tracks)
	return ref, tracks, truncated, nil
}

func (m *youtubeMirror) collectPlaylistItems(ctx context.Context, playlistID string) ([]mirrorTrack, bool, error) {
	var out []mirrorTrack
	q := url.Values{
		"part":       []string{"snippet,contentDetails"},
		"playlistId": []string{playlistID},
		"maxResults": []string{"50"},
	}
	for page := 0; page < libraryPageCap; page++ {
		raw, _, err := m.s.mirrorUpstream(ctx, "youtube-music", "GET", "/playlistItems", q, nil)
		if err != nil {
			return out, false, err
		}
		items, _ := raw["items"].([]any)
		for _, item := range items {
			e, ok := item.(map[string]any)
			if !ok {
				continue
			}
			snippet, _ := e["snippet"].(map[string]any)
			if snippet == nil {
				continue
			}
			title, _ := snippet["title"].(string)
			videoOwnerChannelTitle, _ := snippet["videoOwnerChannelTitle"].(string)
			res, _ := snippet["resourceId"].(map[string]any)
			videoID, _ := res["videoId"].(string)
			if videoID == "" {
				continue
			}
			artists := []string{}
			if videoOwnerChannelTitle != "" {
				artists = []string{videoOwnerChannelTitle}
			}
			out = append(out, mirrorTrack{
				Name:     title,
				Artists:  artists,
				SourceID: videoID,
			})
		}
		next, _ := raw["nextPageToken"].(string)
		if next == "" {
			return out, false, nil
		}
		q.Set("pageToken", next)
	}
	return out, true, nil
}

func (m *youtubeMirror) loadLikedTracks(ctx context.Context) ([]mirrorTrack, bool, error) {
	conn, ok := m.s.connectors["youtube-music"].(*YouTubeMusicConnector)
	if !ok {
		return nil, false, fmt.Errorf("youtube-music connector not registered")
	}
	liked, truncated, err := conn.ListUserLikedTracks(ctx)
	if err != nil {
		return nil, truncated, err
	}
	out := make([]mirrorTrack, 0, len(liked))
	for _, t := range liked {
		id := ""
		if len(t.Sources) > 0 {
			id = t.Sources[0].ID
		}
		var artists []string
		for _, a := range t.Artists {
			if a.Name != "" {
				artists = append(artists, a.Name)
			}
		}
		out = append(out, mirrorTrack{
			Name:       t.Name,
			Artists:    artists,
			DurationMs: t.DurationMs,
			SourceID:   id,
		})
	}
	return out, truncated, nil
}

func (m *youtubeMirror) createPlaylistWithTracks(ctx context.Context, name string, tracks []mirrorTrack) (string, string, int, []MirrorSkip, error) {
	created, _, err := m.s.mirrorUpstream(ctx, "youtube-music", "POST", "/playlists",
		url.Values{"part": []string{"snippet,status"}},
		map[string]any{
			"snippet": map[string]any{
				"title":       name,
				"description": "Copied by Aegis",
			},
			"status": map[string]any{"privacyStatus": "private"},
		})
	if err != nil {
		return "", "", 0, nil, err
	}
	playlistID, _ := created["id"].(string)
	if playlistID == "" {
		return "", "", 0, nil, fmt.Errorf("youtube create playlist returned no id")
	}
	link := "https://www.youtube.com/playlist?list=" + playlistID

	var skips []MirrorSkip
	copied := 0
	for _, t := range tracks {
		videoID := t.SourceID
		if videoID == "" {
			found, err := m.searchVideoID(ctx, t)
			if err != nil {
				skips = append(skips, MirrorSkip{Name: t.display(), Reason: err.Error()})
				continue
			}
			videoID = found
		}
		if videoID == "" {
			skips = append(skips, MirrorSkip{Name: t.display(), Reason: "no match on YouTube Music"})
			continue
		}
		_, _, err := m.s.mirrorUpstream(ctx, "youtube-music", "POST", "/playlistItems",
			url.Values{"part": []string{"snippet"}},
			map[string]any{
				"snippet": map[string]any{
					"playlistId": playlistID,
					"resourceId": map[string]any{
						"kind":    "youtube#video",
						"videoId": videoID,
					},
				},
			})
		if err != nil {
			return playlistID, link, copied, skips, err
		}
		copied++
	}
	return playlistID, link, copied, skips, nil
}

func (m *youtubeMirror) searchVideoID(ctx context.Context, t mirrorTrack) (string, error) {
	q := url.Values{
		"part":            []string{"snippet"},
		"q":               []string{t.searchQuery()},
		"type":            []string{"video"},
		"maxResults":      []string{"1"},
		"videoCategoryId": []string{"10"}, // Music
	}
	raw, _, err := m.s.mirrorUpstream(ctx, "youtube-music", "GET", "/search", q, nil)
	if err != nil {
		return "", err
	}
	items, _ := raw["items"].([]any)
	if len(items) == 0 {
		return "", nil
	}
	first, ok := items[0].(map[string]any)
	if !ok {
		return "", nil
	}
	id, _ := first["id"].(map[string]any)
	videoID, _ := id["videoId"].(string)
	return videoID, nil
}

func (m *youtubeMirror) saveLikedTracks(ctx context.Context, tracks []mirrorTrack) (int, []MirrorSkip, error) {
	var skips []MirrorSkip
	copied := 0
	for _, t := range tracks {
		videoID := t.SourceID
		if videoID == "" {
			found, err := m.searchVideoID(ctx, t)
			if err != nil {
				skips = append(skips, MirrorSkip{Name: t.display(), Reason: err.Error()})
				continue
			}
			videoID = found
		}
		if videoID == "" {
			skips = append(skips, MirrorSkip{Name: t.display(), Reason: "no match on YouTube Music"})
			continue
		}
		_, _, err := m.s.mirrorUpstream(ctx, "youtube-music", "POST", "/videos/rate",
			url.Values{"id": []string{videoID}, "rating": []string{"like"}}, nil)
		if err != nil {
			return copied, skips, err
		}
		copied++
	}
	return copied, skips, nil
}
