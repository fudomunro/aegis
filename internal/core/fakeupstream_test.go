package core

import (
	"log/slog"
	"testing"
)

func TestSeedFakeUpstreamTokens(t *testing.T) {
	srv := testServerWithBase(&Config{Services: []Service{
		{ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: "http://localhost:8080" + fakeUpstreamMount},
		{ID: "other", Name: "Other", Action: "inject", Endpoint: "https://api.example.com"},
	}}, "https://aegis.example.com")

	if n := seedFakeUpstreamTokens(srv); n != 1 {
		t.Fatalf("seeded = %d, want 1", n)
	}
	if srv.GetValidToken("spotify", slog.Default()) == "" {
		t.Fatal("service pointed at the fake mount must get a placeholder token")
	}
	if srv.GetValidToken("other", slog.Default()) != "" {
		t.Fatal("unrelated service must not be seeded")
	}
	if n := seedFakeUpstreamTokens(srv); n != 0 {
		t.Fatalf("re-seed = %d, want 0 (existing tokens stand)", n)
	}
}
