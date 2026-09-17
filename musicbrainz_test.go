package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLookupArtistMBID_ConfidentHit(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("User-Agent") == "" {
			t.Error("missing User-Agent")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"artists":[
			{"id":"mbid-low","name":"Someone Else","score":40},
			{"id":"b975d5a2-2c3b-4b1e-9f0a-0c0e0e0e0e0e","name":"Miles Davis","score":100}
		]}`))
	}))
	defer srv.Close()
	old := mbLookupBase
	mbLookupBase = srv.URL
	defer func() { mbLookupBase = old }()

	g := testServerWithBase(&Config{}, "https://guardian.example.com")
	mbid := g.lookupArtistMBID(context.Background(), "Miles Davis")
	if mbid != "b975d5a2-2c3b-4b1e-9f0a-0c0e0e0e0e0e" {
		t.Fatalf("mbid = %q", mbid)
	}
	// Cached — no second HTTP hit.
	if again := g.lookupArtistMBID(context.Background(), "Miles Davis"); again != mbid {
		t.Fatalf("cached mbid = %q", again)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
}

func TestLookupArtistMBID_RejectsLowScoreAndNameMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"artists":[{"id":"x","name":"Totally Different","score":100}]}`))
	}))
	defer srv.Close()
	old := mbLookupBase
	mbLookupBase = srv.URL
	defer func() { mbLookupBase = old }()

	g := testServerWithBase(&Config{}, "https://guardian.example.com")
	if got := g.lookupArtistMBID(context.Background(), "Miles Davis"); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestLookupArtistMBID_StripsTopicSuffix(t *testing.T) {
	var gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Write([]byte(`{"artists":[{"id":"mb1","name":"Miles Davis","score":100}]}`))
	}))
	defer srv.Close()
	old := mbLookupBase
	mbLookupBase = srv.URL
	defer func() { mbLookupBase = old }()

	g := testServerWithBase(&Config{}, "https://guardian.example.com")
	if mbid := g.lookupArtistMBID(context.Background(), "Miles Davis - Topic"); mbid != "mb1" {
		t.Fatalf("mbid = %q", mbid)
	}
	if gotQuery == "" || !strings.Contains(gotQuery, "Miles") {
		t.Fatalf("query = %q", gotQuery)
	}
}

func TestResolveArtistEntities_BudgetAndAttach(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"artists":[{"id":"mb-hit","name":"Known Artist","score":100}]}`))
	}))
	defer srv.Close()
	old := mbLookupBase
	mbLookupBase = srv.URL
	defer func() { mbLookupBase = old }()

	g := testServerWithBase(&Config{}, "https://guardian.example.com")
	artists := []Artist{
		{Name: "Known Artist"},
		{Name: "Already Has ID", EntityID: "keep-me"},
	}
	g.resolveArtistEntities(context.Background(), artists)
	if artists[0].EntityID != "mb-hit" {
		t.Fatalf("resolved = %+v", artists[0])
	}
	if artists[1].EntityID != "keep-me" {
		t.Fatalf("should not overwrite: %+v", artists[1])
	}
}

func TestMergeArtistLibrary_MBIDFirst(t *testing.T) {
	merged := mergeArtistLibrary([]Artist{
		{
			Name:     "Miles Davis",
			EntityID: "mbid-1",
			Sources:  []ArtistSource{{Service: "spotify", ServiceName: "Spotify", ID: "sp1"}},
		},
		{
			Name:     "Miles  Davis",
			EntityID: "MBID-1",
			Sources:  []ArtistSource{{Service: "youtube-music", ServiceName: "YouTube Music", ID: "ch1"}},
		},
	})
	if len(merged) != 1 {
		t.Fatalf("shared MBID should merge: %+v", merged)
	}
	if merged[0].Match != ArtistMatchMBID {
		t.Fatalf("match = %q", merged[0].Match)
	}
	if len(merged[0].Sources) != 2 {
		t.Fatalf("sources = %+v", merged[0].Sources)
	}

	// Distinct MBIDs with same display name stay separate.
	merged = mergeArtistLibrary([]Artist{
		{Name: "John Smith", EntityID: "a", Sources: []ArtistSource{{Service: "spotify", ID: "1"}}},
		{Name: "John Smith", EntityID: "b", Sources: []ArtistSource{{Service: "spotify", ID: "2"}}},
	})
	if len(merged) != 2 {
		t.Fatalf("distinct MBIDs must not merge: %+v", merged)
	}

	// Name-only absorbs into MBID group.
	merged = mergeArtistLibrary([]Artist{
		{Name: "Miles Davis", EntityID: "mbid-1", Sources: []ArtistSource{{Service: "spotify", ID: "1"}}},
		{Name: "miles davis", Sources: []ArtistSource{{Service: "youtube-music", ID: "2"}}},
	})
	if len(merged) != 1 || merged[0].Match != ArtistMatchMBID {
		t.Fatalf("name fallback should join MBID group: %+v", merged)
	}
}

func TestMBIDCacheNegative(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"artists":[]}`))
	}))
	defer srv.Close()
	old := mbLookupBase
	mbLookupBase = srv.URL
	defer func() { mbLookupBase = old }()

	g := testServerWithBase(&Config{}, "https://guardian.example.com")
	if got := g.lookupArtistMBID(context.Background(), "Nobody"); got != "" {
		t.Fatalf("got %q", got)
	}
	// Negative cache stores "-"
	if v, ok := g.cache.Get(artistMBIDCacheKey("Nobody")); !ok || v != "-" {
		t.Fatalf("neg cache = %q ok=%v", v, ok)
	}
	// Short TTL sanity
	g.cache.Set("x", "y", 10*time.Millisecond)
	time.Sleep(20 * time.Millisecond)
	if _, ok := g.cache.Get("x"); ok {
		t.Fatal("expected expiry")
	}
}
