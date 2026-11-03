package core

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/fudomunro/aegis/internal/connectors"
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
	srv.tokens.Save("spotify", &OAuthToken{AccessToken: "tok", Expiry: time.Now().Add(time.Hour)})
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

func TestCanaryRunner_ShapeDriftIsTerminal(t *testing.T) {
	srv, _ := canaryTestServer(t, connectors.FakeModeDriftItems)
	_, err := canaryRun(t, srv, "spotify")
	if err == nil {
		t.Fatal("renamed items array must fail the canary")
	}
	if !errors.Is(err, ErrSyncTerminal) {
		t.Fatalf("shape drift must be terminal, got %v", err)
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
