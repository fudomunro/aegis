package connectors

import (
	"net/http"
	"strings"
	"testing"

	"github.com/fudomunro/aegis/internal/shared"
)

func TestAppleMusicOnboardingSpec_DeclaresContract(t *testing.T) {
	c := NewAppleMusicConnector(newTestHost("https://aegis.example.com"))
	spec := c.Onboarding()
	if spec.ServiceID != AppleMusicServiceID || spec.Title == "" {
		t.Fatalf("spec = %+v", spec)
	}
	if spec.Connection.SecretHeaders == nil || len(spec.Connection.SecretHeaders) != 4 {
		t.Fatalf("secret headers = %v", spec.Connection.SecretHeaders)
	}
	wantFields := map[string]bool{"endpoint": true, "key_id": true, "issuer_id": true, "private_key": true, "music_user_token": true}
	for _, f := range spec.Fields {
		delete(wantFields, f.ID)
		if (f.ID == "private_key" || f.ID == "music_user_token") && !f.Secret {
			t.Fatalf("field %s must be secret", f.ID)
		}
	}
	if len(wantFields) != 0 {
		t.Fatalf("missing fields: %v", wantFields)
	}
	kinds := map[string]bool{}
	for _, a := range spec.Actions {
		kinds[string(a.Kind)] = true
	}
	if !kinds[string(shared.ActionSave)] || !kinds[string(shared.ActionProbe)] {
		t.Fatalf("actions = %+v", spec.Actions)
	}
	// The setup copy must not cite issue numbers (docs hygiene).
	for _, g := range spec.Guides {
		if strings.Contains(g.Body, "issue #") || strings.Contains(g.Title, "issue #") {
			t.Fatalf("guide %s cites an issue number", g.ID)
		}
	}
}

func TestAppleMusicConnectionUpdate(t *testing.T) {
	_, pemText := testAppleMusicKey(t)
	update, err := appleMusicConnectionUpdate(shared.OnboardingInput{
		Values: map[string]string{
			"endpoint":         "https://api.music.apple.com/v1/",
			"key_id":           "KEY1",
			"issuer_id":        "ISSUER1",
			"private_key":      pemText,
			"music_user_token": "mut-1",
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if update.Endpoint != "https://api.music.apple.com/v1" {
		t.Fatalf("endpoint = %q", update.Endpoint)
	}
	if update.SecretHeaders[appleMusicKeyIDHeader] != "KEY1" ||
		update.SecretHeaders[appleMusicIssuerIDHeader] != "ISSUER1" ||
		update.SecretHeaders[appleMusicUserTokenHeader] != "mut-1" {
		t.Fatalf("headers = %v", update.SecretHeaders)
	}
	if !strings.Contains(update.SecretHeaders[appleMusicPrivateKeyHeader], "BEGIN PRIVATE KEY") {
		t.Fatal("private key not stored")
	}

	// Blank fields leave stored credentials untouched (empty map, no error).
	update, err = appleMusicConnectionUpdate(shared.OnboardingInput{Values: map[string]string{}})
	if err != nil {
		t.Fatalf("blank update: %v", err)
	}
	if len(update.SecretHeaders) != 0 {
		t.Fatalf("blank update wrote headers: %v", update.SecretHeaders)
	}

	// A submitted key must parse.
	if _, err := appleMusicConnectionUpdate(shared.OnboardingInput{
		Values: map[string]string{"private_key": "garbage"},
	}); err == nil {
		t.Fatal("want PEM validation error")
	}
	// Bad endpoint is rejected.
	if _, err := appleMusicConnectionUpdate(shared.OnboardingInput{
		Values: map[string]string{"endpoint": "ftp://x"},
	}); err == nil {
		t.Fatal("want endpoint validation error")
	}
}

func TestAppleMusicOnboardingRun_SaveAndProbe(t *testing.T) {
	_, pemText := testAppleMusicKey(t)
	c := NewAppleMusicConnector(newTestHost("https://aegis.example.com"))

	// Save returns the state update; nothing is written by the connector.
	res, err := c.OnboardingRun(t.Context(), "save", shared.OnboardingInput{
		Values: map[string]string{"key_id": "K", "issuer_id": "I", "private_key": pemText, "music_user_token": "m"},
	})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if len(res.Updates) != 1 || res.Updates[0].Connection == nil {
		t.Fatalf("updates = %+v", res.Updates)
	}

	// Probe with no credentials reports unauthorized, not an error page.
	res, err = c.OnboardingRun(t.Context(), "probe", shared.OnboardingInput{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Probe == nil || res.Probe.OK || res.Probe.Code != "unauthorized" {
		t.Fatalf("probe = %+v", res.Probe)
	}

	if _, err := c.OnboardingRun(t.Context(), "frobnicate", shared.OnboardingInput{}); err == nil {
		t.Fatal("want unknown-action error")
	}
}

func TestAppleMusicOnboardingStatus_ReportsPresence(t *testing.T) {
	c, _ := newAppleMusicTest(t, http200Handler())
	status := c.OnboardingStatus(t.Context())
	if status.Fields["music_user_token"].Set != true {
		t.Fatalf("fields = %+v", status.Fields)
	}
	if status.Probe == nil || !status.Probe.OK {
		t.Fatalf("probe = %+v", status.Probe)
	}

	empty := NewAppleMusicConnector(newTestHost("https://aegis.example.com"))
	got := empty.OnboardingStatus(t.Context())
	if got.Probe != nil {
		t.Fatalf("probe should be skipped without credentials: %+v", got.Probe)
	}
	if len(got.Hints) == 0 {
		t.Fatal("want setup hints")
	}
}

// http200Handler answers the probe's playlist listing with one playlist.
func http200Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeMirrorJSON(w, map[string]any{
			"data": []any{libraryPlaylistResource("p.1", "Road Trip", "")},
			"meta": map[string]any{"total": 1},
		})
	})
}
