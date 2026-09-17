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

// MusicBrainz is the open music catalog used to attach durable artist MBIDs
// when service APIs only give display names (issue #4).
const (
	musicBrainzBase      = "https://musicbrainz.org/ws/2"
	musicBrainzUserAgent = "ApertureGuardian/1.0 (https://github.com/fudomunro/aperture-guardian)"
	// mbLookupBudget caps remote lookups per library request so a large
	// follow list cannot stall the handler or hammer the public API.
	mbLookupBudget = 8
	// mbMinScore is the minimum Lucene score for a confident artist hit.
	mbMinScore = 95
	mbCacheTTL = 24 * time.Hour
	mbNegTTL   = 2 * time.Hour
)

// mbLookupBase is overridable in tests.
var mbLookupBase = musicBrainzBase

type mbArtistSearch struct {
	Artists []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Score int    `json:"score"`
	} `json:"artists"`
}

// artistMBIDCacheKey names the negative/positive cache slot for one artist.
func artistMBIDCacheKey(name string) string {
	return "mbid:artist:" + NormalizeIdentityKey(name)
}

// lookupArtistMBID queries MusicBrainz for a high-confidence MBID.
// Returns "" when there is no confident hit or the lookup fails.
func (s *Server) lookupArtistMBID(ctx context.Context, name string) string {
	query := strings.TrimSpace(name)
	if query == "" {
		return ""
	}
	// YouTube official channels end in " - Topic".
	query = strings.TrimSuffix(query, " - Topic")
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}

	key := artistMBIDCacheKey(query)
	if cached, ok := s.cache.Get(key); ok {
		if cached == "-" {
			return ""
		}
		return cached
	}

	u := fmt.Sprintf("%s/artist?query=%s&fmt=json&limit=3",
		strings.TrimRight(mbLookupBase, "/"),
		url.QueryEscape("artist:"+query))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("User-Agent", musicBrainzUserAgent)
	req.Header.Set("Accept", "application/json")

	resp, err := guardianHTTPClient.Do(req)
	if err != nil {
		slog.Debug("musicbrainz artist lookup failed", "artist", query, "error", err)
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		slog.Debug("musicbrainz artist lookup status", "artist", query, "status", resp.StatusCode)
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ""
	}
	var parsed mbArtistSearch
	if err := json.Unmarshal(data, &parsed); err != nil {
		return ""
	}

	want := NormalizeIdentityKey(query)
	for _, hit := range parsed.Artists {
		if hit.Score < mbMinScore || hit.ID == "" {
			continue
		}
		if NormalizeIdentityKey(hit.Name) != want {
			continue
		}
		s.cache.Set(key, hit.ID, mbCacheTTL)
		return hit.ID
	}
	s.cache.Set(key, "-", mbNegTTL)
	return ""
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
		// Skip obvious non-artists / empty after Topic strip.
		q := strings.TrimSuffix(strings.TrimSpace(name), " - Topic")
		if NormalizeIdentityKey(q) == "" {
			continue
		}
		if lookups >= mbLookupBudget {
			break
		}
		// Count cache misses only against the budget.
		if _, ok := s.cache.Get(artistMBIDCacheKey(q)); !ok {
			lookups++
		}
		if mbid := s.lookupArtistMBID(ctx, name); mbid != "" {
			artists[i].EntityID = mbid
		}
	}
}
