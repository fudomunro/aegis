package main

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestInitLogger_LevelsAndFormats(t *testing.T) {
	cases := []struct {
		level  string
		format string
	}{
		{"debug", "json"},
		{"info", "text"},
		{"warn", "json"},
		{"error", "text"},
		{"", "json"}, // defaults to info
	}
	for _, tc := range cases {
		logger := initLogger(tc.level, tc.format, nil)
		if logger == nil {
			t.Fatalf("initLogger(%q, %q) returned nil", tc.level, tc.format)
		}
		logger.Info("ok")
	}
}

func TestInitLogger_EnabledLevels(t *testing.T) {
	if !initLogger("info", "json", nil).Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should be enabled at info level")
	}
	if initLogger("error", "json", nil).Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("info should be disabled at error level")
	}
	if !initLogger("debug", "text", nil).Enabled(context.Background(), slog.LevelDebug) {
		t.Fatal("debug should be enabled at debug level")
	}
}

func TestBuildIdentityResolver_APIKeyMode(t *testing.T) {
	if buildIdentityResolver(AuthModeAPIKey, "") != nil {
		t.Fatal("api-key mode should not install identity resolvers")
	}
}

func TestBuildIdentityResolver_TailnetMode(t *testing.T) {
	r := buildIdentityResolver(AuthModeTailnet, "/tmp/does-not-exist.sock")
	if r == nil {
		t.Fatal("expected MultiResolver in tailnet mode")
	}
	mr, ok := r.(MultiResolver)
	if !ok {
		t.Fatalf("got %T, want MultiResolver", r)
	}
	if len(mr.Resolvers) < 1 {
		t.Fatal("expected at least ServeHeaderResolver")
	}
	if _, ok := mr.Resolvers[0].(ServeHeaderResolver); !ok {
		t.Fatalf("first resolver = %T, want ServeHeaderResolver", mr.Resolvers[0])
	}
}

func TestWhoIsResolver_MissingSocket(t *testing.T) {
	w := WhoIsResolver{SocketPath: filepath.Join(t.TempDir(), "missing.sock")}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "100.64.0.1:1234"
	// Configured path that does not exist still attempts a dial and errors.
	id, err := w.Resolve(context.Background(), req)
	if err == nil && id != nil {
		t.Fatalf("expected dial failure or nil identity, got %+v", id)
	}
}

func TestServeHeaderResolver_Variants(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		wantNil bool
		login   string
		display string
		tags    []string
	}{
		{
			name:    "login header",
			headers: map[string]string{headerLogin: "alice@example.com", headerName: "Alice", headerTags: "tag:a,tag:b"},
			login:   "alice@example.com",
			display: "Alice",
			tags:    []string{"tag:a", "tag:b"},
		},
		{
			name:    "identity fallback",
			headers: map[string]string{headerIdentity: "bob@example.com"},
			login:   "bob@example.com",
		},
		{
			name:    "tags only",
			headers: map[string]string{headerTags: "tag:aegis-client"},
			login:   "tag:aegis-client",
			tags:    []string{"tag:aegis-client"},
		},
		{
			name:    "no headers",
			wantNil: true,
		},
	}
	var resolver ServeHeaderResolver
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/", nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			id, err := resolver.Resolve(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantNil {
				if id != nil {
					t.Fatalf("got %+v, want nil", id)
				}
				return
			}
			if id == nil || id.LoginName != tc.login || id.DisplayName != tc.display || id.Source != "serve-headers" {
				t.Fatalf("got %+v", id)
			}
			if len(id.Tags) != len(tc.tags) {
				t.Fatalf("tags = %v, want %v", id.Tags, tc.tags)
			}
		})
	}
}

func TestMultiResolver_AllMissAndErrorFallback(t *testing.T) {
	empty := MultiResolver{Resolvers: []IdentityResolver{ServeHeaderResolver{}, nil}}
	req := httptest.NewRequest("GET", "/", nil)
	id, err := empty.Resolve(context.Background(), req)
	if err != nil || id != nil {
		t.Fatalf("got %+v err=%v", id, err)
	}

	m := MultiResolver{Resolvers: []IdentityResolver{
		stubResolver{err: context.DeadlineExceeded},
		stubResolver{id: &PeerIdentity{LoginName: "dave@example.com", Source: "whois"}},
	}}
	id, err = m.Resolve(context.Background(), req)
	if err != nil || id == nil || id.LoginName != "dave@example.com" {
		t.Fatalf("got %+v err=%v", id, err)
	}
}

type stubResolver struct {
	id  *PeerIdentity
	err error
}

func (s stubResolver) Resolve(context.Context, *http.Request) (*PeerIdentity, error) {
	return s.id, s.err
}

func TestParseTagsHeader_WhitespaceAndSeparators(t *testing.T) {
	if tags := parseTagsHeader("   "); tags != nil {
		t.Fatalf("got %v", tags)
	}
	tags := parseTagsHeader("tag:a, tag:b;tag:c  tag:d")
	if len(tags) != 4 || tags[0] != "tag:a" || tags[3] != "tag:d" {
		t.Fatalf("got %v", tags)
	}
}

func TestLoginAllowed_Extended(t *testing.T) {
	id := &PeerIdentity{LoginName: "alice@example.com"}
	if loginAllowed(nil, nil) {
		t.Fatal("nil identity should not be allowed")
	}
	if !loginAllowed(id, []string{"Alice@Example.com"}) {
		t.Fatal("case-insensitive match should allow")
	}
	if loginAllowed(id, []string{"bob@example.com"}) {
		t.Fatal("non-matching login should deny")
	}
	tagged := &PeerIdentity{LoginName: "tag:a,tag:b", Tags: []string{"tag:a", "tag:b"}}
	if !loginAllowed(tagged, []string{"tag:a,tag:b"}) {
		t.Fatal("tag identity should match allowlist entry")
	}
}
