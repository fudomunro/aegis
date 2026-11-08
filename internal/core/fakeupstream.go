package core

import (
	"log/slog"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/core/tokens"
)

// Fake-upstream host support (issue #86): the dev-only mount that serves
// connectors.FakeUpstream so the sync-maintenance loop can be driven with
// injected drift before real drift appears. Never enabled by default.

// fakeUpstreamMount is where -fake-upstream serves the injectable provider.
// Services that should talk to it point their configured endpoint here.
const fakeUpstreamMount = "/demo/fake-upstream"

// seedFakeUpstreamTokens gives configured services that point at the mounted
// fake upstream a placeholder token, so fetch paths (which always attach
// one) run without real OAuth. Dev-only companion to -fake-upstream: real
// instances never enable the mount, so this never touches their stores.
func seedFakeUpstreamTokens(srv *Server) int {
	if srv == nil || srv.config == nil || srv.tokens == nil {
		return 0
	}
	seeded := 0
	for _, svc := range srv.config.Services {
		if !strings.Contains(svc.Endpoint, fakeUpstreamMount) {
			continue
		}
		if srv.GetValidToken(svc.ID, slog.Default()) != "" {
			continue
		}
		srv.tokens.Save(svc.ID, &tokens.OAuthToken{
			AccessToken: "fake-upstream-token",
			Expiry:      time.Now().Add(10 * 365 * 24 * time.Hour),
		})
		slog.Info("seeded placeholder token for fake upstream", "service", svc.ID)
		seeded++
	}
	return seeded
}
