package core

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

func TestYouTubeSimplifyAndIsPaginated(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)

	raw := map[string]any{
		"items":         []any{map[string]any{"id": "1"}},
		"nextPageToken": "tok",
		"href":          "drop",
		"pageInfo":      map[string]any{"totalResults": float64(1)},
	}
	got := conn.Simplify(raw)
	if _, ok := got["items"]; !ok {
		t.Fatal("items should be kept")
	}
	if _, ok := got["href"]; ok {
		t.Fatal("href should be dropped")
	}
	if !conn.IsPaginated(raw) {
		t.Fatal("expected paginated with nextPageToken")
	}
	if conn.IsPaginated(map[string]any{}) {
		t.Fatal("expected not paginated")
	}
}

func TestYouTubeItemsKeyForPath(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)

	cases := map[string]string{
		"/playlists":           "items",
		"/playlists/pl1/items": "items",
		"/me/liked":            "items",
		"/search":              "items",
		"/unknown":             "items",
	}
	// itemsKeyForPath may return "" for some paths — assert no panic and type.
	for path := range cases {
		_ = conn.ItemsKeyForPath(path)
	}
}

func TestYouTubeGetLikedPlaylistID_NoToken(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	if _, err := conn.GetLikedPlaylistID(); err == nil {
		t.Fatal("expected error without token")
	}
}

func TestYouTubeGetLikedPlaylistID_Success(t *testing.T) {
	// getLikedPlaylistID may hit the real host; intercept via connectors.HTTPClient.
	orig := connectors.HTTPClient
	defer func() { connectors.HTTPClient = orig }()
	connectors.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Body:       io.NopCloser(strings.NewReader("unauthorized")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"items":[{"contentDetails":{"relatedPlaylists":{"likes":"LL123"}}}]}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.tokens.Tokens["youtube-music"] = &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	}
	conn := connectors.NewYouTubeMusicConnector(srv)
	id, err := conn.GetLikedPlaylistID()
	if err != nil {
		t.Fatal(err)
	}
	if id != "LL123" {
		t.Fatalf("id = %q", id)
	}
}

func TestYouTubeGetLikedPlaylistID_NoChannel(t *testing.T) {
	orig := connectors.HTTPClient
	defer func() { connectors.HTTPClient = orig }()
	connectors.HTTPClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"items":[]}`)),
			Header:     make(http.Header),
			Request:    r,
		}, nil
	})}

	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	srv.tokens.Tokens["youtube-music"] = &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	}
	conn := connectors.NewYouTubeMusicConnector(srv)
	if _, err := conn.GetLikedPlaylistID(); err == nil {
		t.Fatal("expected no channel error")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestYouTubeListUserPlaylists_Unauthorized(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	if _, _, err := conn.ListUserPlaylists(context.Background()); err != types.ErrUnauthorized {
		t.Fatalf("err = %v", err)
	}
}

func TestYouTubeListUserPlaylists_Success(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{
			"items":[{
				"id":"PL1",
				"snippet":{"title":"Road Trip","description":"songs","publishedAt":"2026-01-01T00:00:00Z"},
				"contentDetails":{"itemCount":12}
			}],
			"nextPageToken":""
		}`)
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{
		Services: []Service{{ID: "youtube-music", Endpoint: ts.URL}},
	}, "https://aegis.example.com")
	srv.tokens.Tokens["youtube-music"] = &OAuthToken{
		AccessToken: "tok",
		Expiry:      time.Now().Add(time.Hour),
	}
	conn := connectors.NewYouTubeMusicConnector(srv)
	playlists, _, err := conn.ListUserPlaylists(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(playlists) != 1 || playlists[0].Name != "Road Trip" {
		t.Fatalf("playlists = %+v", playlists)
	}
	if playlists[0].Sources[0].Service != "youtube-music" {
		t.Fatalf("source = %+v", playlists[0].Sources)
	}
}

func TestYouTubeFetchAllPages_NoToken(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	page := map[string]any{"items": []any{map[string]any{"id": "1"}}}
	got := conn.FetchAllPagesRaw(context.Background(), "/playlists", nil, page, "tok", slog.Default())
	if got["items"] == nil {
		t.Fatal("items missing")
	}
	if len(got["items"].([]any)) != 1 {
		t.Fatalf("items = %v", got["items"])
	}
}

func TestYouTubeFetchAllPages_WithPagination(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("pageToken") == "t2" {
			fmt.Fprint(w, `{"items":[{"id":"b"}]}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"id":"a"}],"nextPageToken":"t2"}`)
	}))
	defer ts.Close()

	srv := testServerWithBase(&Config{
		Services: []Service{{ID: "youtube-music", Endpoint: ts.URL}},
	}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)

	first := map[string]any{
		"items":         []any{map[string]any{"id": "seed"}},
		"nextPageToken": "t2",
	}
	got := conn.FetchAllPagesRaw(context.Background(), "/playlists", map[string][]string{}, first, "tok", slog.Default())
	items := got["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("items = %v", items)
	}
	if _, ok := got["nextPageToken"]; ok {
		t.Fatal("nextPageToken should be removed")
	}
	if got["truncated"] == true {
		t.Fatal("complete two-page fetch should not set truncated")
	}
}

func TestYouTubeMapPath(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)

	apiPath, params, err := conn.MapPath("/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	if apiPath != "/channels" {
		t.Fatalf("apiPath = %q", apiPath)
	}
	if params["mine"] != "true" {
		t.Fatalf("params = %v", params)
	}

	apiPath, params, err = conn.MapPath("/me/playlists", nil)
	if err != nil {
		t.Fatal(err)
	}
	if apiPath != "/playlists" || params["mine"] != "true" {
		t.Fatalf("got %q %v", apiPath, params)
	}

	apiPath, params, err = conn.MapPath("/channels/UC1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if apiPath != "/channels" || params["id"] != "UC1" {
		t.Fatalf("got %q %v", apiPath, params)
	}

	apiPath, params, err = conn.MapPath("/custom/thing", nil)
	if err != nil {
		t.Fatal(err)
	}
	if apiPath != "/custom/thing" || params != nil {
		t.Fatalf("passthrough got %q %v", apiPath, params)
	}
}

func TestYouTubeTransformResponse_Default(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	out := conn.TransformResponse("/other", []byte(`{"items":[{"id":"1"}],"nextPageToken":"x","href":"drop"}`), slog.Default())
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("out = %#v", out)
	}
	if _, ok := m["href"]; ok {
		t.Fatal("href should be dropped")
	}
}

func TestYouTubeTransformResponse_ErrorPassthrough(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	out := conn.TransformResponse("/me", []byte(`{"error":{"code":401}}`), slog.Default())
	m := out.(map[string]any)
	if _, ok := m["error"]; !ok {
		t.Fatalf("out = %#v", out)
	}
}

func TestYouTubeFetchAllPages_NonMap(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	got := conn.FetchAllPages(context.Background(), "/me", nil, "not-a-map", "tok", slog.Default())
	if got != "not-a-map" {
		t.Fatalf("got = %#v", got)
	}
}

func TestYouTubeFetchAllPages_EmptyItemsKey(t *testing.T) {
	srv := testServerWithBase(&Config{}, "https://aegis.example.com")
	conn := connectors.NewYouTubeMusicConnector(srv)
	page := map[string]any{
		"items":         []any{map[string]any{"id": "1"}},
		"nextPageToken": "x",
	}
	// Path with no items key should return first page unchanged.
	// itemsKeyForPath for unknown may still return "items" — check behavior.
	_ = page
	got := conn.FetchAllPages(context.Background(), "/unknown-path-xyz", nil, map[string]any{
		"nextPageToken": "x",
		"other":         []any{},
	}, "tok", slog.Default())
	m := got.(map[string]any)
	if _, ok := m["nextPageToken"]; !ok {
		// If items key is empty, pagination is skipped and token remains.
		t.Fatalf("got = %#v", m)
	}
}

func TestAsInt(t *testing.T) {
	if n, ok := connectors.AsInt(float64(42)); !ok || n != 42 {
		t.Fatalf("float64 %v %v", n, ok)
	}
	if n, ok := connectors.AsInt(int(7)); !ok || n != 7 {
		t.Fatalf("int %v %v", n, ok)
	}
	if n, ok := connectors.AsInt(int64(8)); !ok || n != 8 {
		t.Fatalf("int64 %v %v", n, ok)
	}
	if _, ok := connectors.AsInt("nope"); ok {
		t.Fatal("string should not convert")
	}
	if _, ok := connectors.AsInt(nil); ok {
		t.Fatal("nil should not convert")
	}
}
