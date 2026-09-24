package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// MusicBrainz is the open music catalog used to attach durable MBIDs when
// service APIs only give display names or lack ISRC/UPC (issue #4).
const (
	musicBrainzBase      = "https://musicbrainz.org/ws/2"
	musicBrainzUserAgent = "Aegis/1.0 (https://github.com/fudomunro/aegis)"
	// mbLookupBudget caps remote lookups per library request so a large
	// list cannot stall the handler or hammer the public API.
	mbLookupBudget = 8
	// mbMinScore is the minimum Lucene score for a confident hit.
	mbMinScore = 95
	mbCacheTTL = 24 * time.Hour
	mbNegTTL   = 2 * time.Hour
)

// mbLookupBase is overridable in tests.
var mbLookupBase = musicBrainzBase

type mbSearchResult struct {
	// Artists / Recordings / ReleaseGroups share id/name/score shape.
	Artists       []mbHit `json:"artists"`
	Recordings    []mbHit `json:"recordings"`
	ReleaseGroups []mbHit `json:"release-groups"`
}

type mbHit struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Score int    `json:"score"`
}

func artistMBIDCacheKey(name string) string {
	return "mbid:artist:" + NormalizeIdentityKey(name)
}

func recordingMBIDCacheKey(title, artist string) string {
	return "mbid:rec:" + NormalizeIdentityKey(title) + "|" + NormalizeIdentityKey(artist)
}

func releaseGroupMBIDCacheKey(title, artist string) string {
	return "mbid:rg:" + NormalizeIdentityKey(title) + "|" + NormalizeIdentityKey(artist)
}

// mbGetJSON performs a MusicBrainz search and returns the raw body.
func (s *Server) mbGetJSON(ctx context.Context, path, query string) ([]byte, bool) {
	u := fmt.Sprintf("%s/%s?query=%s&fmt=json&limit=5",
		strings.TrimRight(mbLookupBase, "/"),
		path,
		url.QueryEscape(query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", musicBrainzUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := aegisHTTPClient.Do(req)
	if err != nil {
		slog.Debug("musicbrainz lookup failed", "path", path, "error", err)
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Debug("musicbrainz lookup status", "path", path, "status", resp.StatusCode)
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, false
	}
	return data, true
}

// mbPickHit returns the first high-confidence hit whose name matches want.
func mbPickHit(hits []mbHit, want string) string {
	wantKey := NormalizeIdentityKey(want)
	if wantKey == "" {
		return ""
	}
	for _, hit := range hits {
		if hit.Score < mbMinScore || hit.ID == "" {
			continue
		}
		if NormalizeIdentityKey(hit.Name) == wantKey {
			return hit.ID
		}
	}
	return ""
}

func (s *Server) cacheMBID(key, value string) string {
	if value == "" {
		s.cache.Set(key, "-", mbNegTTL)
		return ""
	}
	s.cache.Set(key, value, mbCacheTTL)
	return value
}

func (s *Server) cachedMBID(key string) (string, bool) {
	v, ok := s.cache.Get(key)
	if !ok {
		return "", false
	}
	if v == "-" {
		return "", true
	}
	return v, true
}

// lookupArtistMBID queries MusicBrainz for a high-confidence artist MBID.
func (s *Server) lookupArtistMBID(ctx context.Context, name string) string {
	query := strings.TrimSpace(name)
	if query == "" {
		return ""
	}
	query = strings.TrimSuffix(query, " - Topic")
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}

	key := artistMBIDCacheKey(query)
	if cached, ok := s.cachedMBID(key); ok {
		return cached
	}

	data, ok := s.mbGetJSON(ctx, "artist", "artist:"+query)
	if !ok {
		return ""
	}
	var parsed mbSearchResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return s.cacheMBID(key, mbPickHit(parsed.Artists, query))
}

// lookupRecordingMBID finds a MusicBrainz recording for title + artist.
func (s *Server) lookupRecordingMBID(ctx context.Context, title, artist string) string {
	title = strings.TrimSpace(title)
	artist = strings.TrimSuffix(strings.TrimSpace(artist), " - Topic")
	if title == "" || artist == "" {
		return ""
	}
	key := recordingMBIDCacheKey(title, artist)
	if cached, ok := s.cachedMBID(key); ok {
		return cached
	}

	q := fmt.Sprintf(`recording:%s AND artist:%s`,
		quoteMBField(title), quoteMBField(artist))
	data, ok := s.mbGetJSON(ctx, "recording", q)
	if !ok {
		return ""
	}
	var parsed mbSearchResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return s.cacheMBID(key, mbPickHit(parsed.Recordings, title))
}

// lookupReleaseGroupMBID finds a MusicBrainz release-group for title + artist.
func (s *Server) lookupReleaseGroupMBID(ctx context.Context, title, artist string) string {
	title = strings.TrimSpace(title)
	artist = strings.TrimSuffix(strings.TrimSpace(artist), " - Topic")
	if title == "" || artist == "" {
		return ""
	}
	key := releaseGroupMBIDCacheKey(title, artist)
	if cached, ok := s.cachedMBID(key); ok {
		return cached
	}

	q := fmt.Sprintf(`releasegroup:%s AND artist:%s`,
		quoteMBField(title), quoteMBField(artist))
	data, ok := s.mbGetJSON(ctx, "release-group", q)
	if !ok {
		return ""
	}
	var parsed mbSearchResult
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}
	return s.cacheMBID(key, mbPickHit(parsed.ReleaseGroups, title))
}

// quoteMBField wraps a Lucene field value in double quotes (escaping quotes).
func quoteMBField(s string) string {
	s = strings.ReplaceAll(s, `"`, ``)
	return `"` + s + `"`
}

func primaryArtistName(artists []Artist) string {
	if len(artists) == 0 {
		return ""
	}
	return artists[0].Name
}

// resolveArtistEntities attaches MusicBrainz MBIDs to artists that lack an
// EntityID. Failures are ignored — name matching remains the fallback.
func (s *Server) resolveArtistEntities(ctx context.Context, artists []Artist) {
	if s == nil || len(artists) == 0 {
		return
	}
	lookups := 0
	for i := range artists {
		if artists[i].EntityID != "" {
			continue
		}
		name := artists[i].Name
		if name == "" {
			continue
		}
		q := strings.TrimSuffix(strings.TrimSpace(name), " - Topic")
		if NormalizeIdentityKey(q) == "" {
			continue
		}
		if lookups >= mbLookupBudget {
			break
		}
		if _, ok := s.cache.Get(artistMBIDCacheKey(q)); !ok {
			lookups++
		}
		if mbid := s.lookupArtistMBID(ctx, name); mbid != "" {
			artists[i].EntityID = mbid
		}
	}
}

// resolveTrackEntities attaches MusicBrainz recording MBIDs to tracks that
// lack EntityID (no ISRC). Used before MergeTracksSync so YouTube-only or
// ISRC-less catalog tracks can still join durable identity groups.
func (s *Server) resolveTrackEntities(ctx context.Context, tracks []Track) {
	if s == nil || len(tracks) == 0 {
		return
	}
	lookups := 0
	for i := range tracks {
		if tracks[i].EntityID != "" {
			continue
		}
		title := tracks[i].Name
		artist := primaryArtistName(tracks[i].Artists)
		if NormalizeIdentityKey(title) == "" || NormalizeIdentityKey(artist) == "" {
			continue
		}
		key := recordingMBIDCacheKey(title, artist)
		if _, ok := s.cache.Get(key); !ok {
			if lookups >= mbLookupBudget {
				break
			}
			lookups++
		}
		if mbid := s.lookupRecordingMBID(ctx, title, artist); mbid != "" {
			tracks[i].EntityID = mbid
		}
	}
}

// resolveAlbumEntities attaches MusicBrainz release-group MBIDs to albums
// that lack EntityID (no UPC).
func (s *Server) resolveAlbumEntities(ctx context.Context, albums []Album) {
	if s == nil || len(albums) == 0 {
		return
	}
	lookups := 0
	for i := range albums {
		if albums[i].EntityID != "" {
			continue
		}
		title := albums[i].Name
		artist := primaryArtistName(albums[i].Artists)
		if NormalizeIdentityKey(title) == "" || NormalizeIdentityKey(artist) == "" {
			continue
		}
		key := releaseGroupMBIDCacheKey(title, artist)
		if _, ok := s.cache.Get(key); !ok {
			if lookups >= mbLookupBudget {
				break
			}
			lookups++
		}
		if mbid := s.lookupReleaseGroupMBID(ctx, title, artist); mbid != "" {
			albums[i].EntityID = mbid
		}
	}
}
