package core

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/core/tokens"
)

func canaryTestServer(t *testing.T, mode string) (*Server, *connectors.FakeUpstream) {
	t.Helper()
	fu := connectors.NewFakeUpstream()
	if err := fu.SetMode(mode); err != nil {
		t.Fatal(err)
	}
	up := httptest.NewServer(fu)
	t.Cleanup(up.Close)

	srv := testServerWithBase(&Config{Services: []Service{{
		ID: "spotify", Name: "Spotify", Action: "inject", Endpoint: up.URL,
	}}}, "https://aegis.example.com")
	srv.tokens.Save("spotify", &tokens.OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
	srv.connectors["spotify"] = connectors.NewSpotifyConnector(srv)
	return srv, fu
}

func canaryRun(t *testing.T, srv *Server, service string) (SyncStep, error) {
	t.Helper()
	r := &canaryRunner{srv: srv}
	return r.Run(context.Background(), SyncJob{
		Kind: "tracks", JobType: SyncJobTypeCanary, Service: service,
	})
}

func TestCanaryRunner_Surface(t *testing.T) {
	r := &canaryRunner{}
	if got := r.Kind(); got != "tracks" {
		t.Fatalf("kind = %q", got)
	}
	if got := r.JobTypes(); len(got) != 1 || got[0] != SyncJobTypeCanary {
		t.Fatalf("job types = %v", got)
	}
}

func TestCanaryRunner_HealthyAndEmptyAreHealth(t *testing.T) {
	for _, mode := range []string{connectors.FakeModeHealthy, connectors.FakeModeEmpty} {
		t.Run(mode, func(t *testing.T) {
			srv, _ := canaryTestServer(t, mode)
			step, err := canaryRun(t, srv, "spotify")
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if !step.Done {
				t.Fatal("canary must complete in one step")
			}
			if mode == connectors.FakeModeEmpty && step.Progress["total"] != 0 {
				t.Fatalf("empty library must report total 0: %+v", step.Progress)
			}
		})
	}
}

func TestCanaryRunner_UnmappedDriftIsTerminal(t *testing.T) {
	srv, _ := canaryTestServer(t, connectors.FakeModeDriftUnmapped)
	_, err := canaryRun(t, srv, "spotify")
	if err == nil {
		t.Fatal("an unmapped rename must fail the canary")
	}
	if !errors.Is(err, ErrSyncTerminal) {
		t.Fatalf("shape drift must be terminal, got %v", err)
	}
}

func TestCanaryRunner_LearnedRenameIsHealth(t *testing.T) {
	// The items→entries rename is the issue #240 migration: once the
	// adapter reads it, the same payload is health, not drift.
	srv, _ := canaryTestServer(t, connectors.FakeModeDriftItems)
	step, err := canaryRun(t, srv, "spotify")
	if err != nil {
		t.Fatalf("learned rename must pass: %v", err)
	}
	if step.Progress["listed"] != 3 {
		t.Fatalf("progress = %+v, want 3 listed", step.Progress)
	}
}

func TestCanaryRunner_UpstreamErrors(t *testing.T) {
	for _, mode := range []string{connectors.FakeModeHTTP500, connectors.FakeModeHTTP401, connectors.FakeModeGarbage} {
		t.Run(mode, func(t *testing.T) {
			srv, _ := canaryTestServer(t, mode)
			if _, err := canaryRun(t, srv, "spotify"); err == nil {
				t.Fatalf("%s must fail the canary", mode)
			}
		})
	}
}

func TestCanaryRunner_UnsupportedServiceIsTerminal(t *testing.T) {
	srv, _ := canaryTestServer(t, connectors.FakeModeHealthy)
	_, err := canaryRun(t, srv, "no-such-service")
	if err == nil || !errors.Is(err, ErrSyncTerminal) {
		t.Fatalf("unsupported mirror source must be terminal, got %v", err)
	}
}
