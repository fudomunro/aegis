package connectors

import (
	"context"
	"fmt"
	"strings"

	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/shared"
)

// Apple Music onboarding (issue #265 seam): the MusicKit credential setup,
// declared and driven here while core hosts it generically. Every string
// below is operator-visible copy — the admin panel renders it and must not
// restate it.
//
// Apple Music is not an OAuth provider. The operator supplies the MusicKit
// signing key (App Store Connect) and a Music User Token (MusicKit JS in a
// browser); Aegis mints the developer token itself and never exposes either
// credential to agents.

const (
	appleMusicGuideIntro     = "intro"
	appleMusicGuideKey       = "key"
	appleMusicGuideUserToken = "user-token"
)

// Onboarding returns Apple Music's declared setup contract.
func (c *AppleMusicConnector) Onboarding() shared.OnboardingSpec {
	return shared.OnboardingSpec{
		ServiceID:   AppleMusicServiceID,
		DisplayName: AppleMusicServiceName,
		Title:       "Apple Music — library playlists and tracks",
		Connection: shared.ConnectionMetadata{
			Name:        AppleMusicServiceName,
			Action:      "inject",
			CacheTTL:    "5m",
			Description: "Apple Music library playlists, tracks, albums, and artists for /v1/library, plus mirror/converge placement.",
			Aliases:     []string{"apple music", "musickit", "itunes"},
			SecretHeaders: []string{
				appleMusicKeyIDHeader,
				appleMusicIssuerIDHeader,
				appleMusicPrivateKeyHeader,
				appleMusicUserTokenHeader,
			},
		},
		Fields: []shared.OnboardingField{
			{
				ID:          "endpoint",
				Label:       "API endpoint",
				Kind:        "url",
				Placeholder: appleMusicAPIBase,
			},
			{
				ID:          "key_id",
				Label:       "MusicKit Key ID",
				Kind:        "text",
				Placeholder: "ABCDE12345",
				Help:        "From App Store Connect → Users and Access → Integrations → MusicKit.",
			},
			{
				ID:          "issuer_id",
				Label:       "Issuer ID",
				Kind:        "text",
				Placeholder: "00000000-0000-0000-0000-000000000000",
				Help:        "The team issuer UUID shown beside the MusicKit key.",
			},
			{
				ID:          "private_key",
				Label:       "MusicKit private key (.p8)",
				Kind:        "password",
				Placeholder: "-----BEGIN PRIVATE KEY-----",
				Secret:      true,
				Help:        "The full .p8 file contents. Aegis signs the developer token from it; the key never leaves the server.",
			},
			{
				ID:          "music_user_token",
				Label:       "Music User Token",
				Kind:        "password",
				Placeholder: "Music-User-Token value",
				Secret:      true,
				Help:        "From the MusicKit JS authorize step below. Long-lived, revocable from the user's Apple ID.",
			},
		},
		Guides: []shared.OnboardingGuide{
			{
				ID: appleMusicGuideIntro,
				Body: "Library playlists and tracks for `GET /v1/library/playlists` and `GET /v1/library/tracks`, and a mirror target that matches on ISRC. " +
					"An [Apple Developer Program](https://developer.apple.com/programs/) membership is required to create the MusicKit key. " +
					"Credentials are stored encrypted — never shown again after save, never sent to agents.",
			},
			{
				ID:    appleMusicGuideKey,
				Title: "Create the MusicKit key",
				Steps: []shared.OnboardingGuideStep{
					{ID: "connect", Title: "Open App Store Connect", Detail: "Users and Access → Integrations → MusicKit.", Command: "https://appstoreconnect.apple.com/access/integrations/api"},
					{ID: "generate", Title: "Generate a key", Detail: "Download the .p8 file once — Apple does not offer it again."},
					{ID: "copy", Title: "Copy the Key ID and Issuer ID", Detail: "Both appear beside the key; paste them into the form."},
				},
			},
			{
				ID:    appleMusicGuideUserToken,
				Title: "Get a Music User Token",
				Body: "Apple issues the Music User Token in a browser through MusicKit JS — there is no redirect-code flow. " +
					"Open a local HTML page with the snippet below (embedding a developer token minted from your MusicKit key), sign in with the user's Apple ID, and copy the token it prints into the form. " +
					"The token is long-lived and can be revoked from the user's Apple ID; when Apple revokes it, the surface reports `unauthorized` until a new one is saved.",
				Command: `<!doctype html>
<html><head><meta charset="utf-8"><title>Music User Token</title>
<script src="https://js-cdn.music.apple.com/musickit/v3/musickit.js"></script>
</head><body>
<p>Sign in, then copy the token from the console / page.</p>
<button id="go">Authorize with Apple Music</button>
<pre id="out"></pre>
<script>
  document.addEventListener("musickitloaded", async () => {
    const music = await MusicKit.configure({ developerToken: "PASTE_DEVELOPER_TOKEN", app: { name: "Aegis setup", build: "1" } });
    document.getElementById("go").onclick = async () => {
      await music.authorize();
      const token = music.musicUserToken;
      document.getElementById("out").textContent = token;
      console.log("MUSIC_USER_TOKEN=" + token);
    };
  });
</script></body></html>`,
				Label:  "Save as musickit-token.html, replace PASTE_DEVELOPER_TOKEN with a developer token you mint from your MusicKit key (see docs/connect/apple-music.md), open in a browser",
				Button: "Copy snippet",
			},
		},
		Actions: []shared.OnboardingAction{
			{
				ID:        "probe",
				Label:     "Test",
				BusyLabel: "Testing Apple Music…",
				Kind:      shared.ActionProbe,
				Placement: shared.ActionInHeader,
			},
			{
				ID:        "save",
				Label:     "Save connection",
				BusyLabel: "Saving…",
				Kind:      shared.ActionSave,
				Placement: shared.ActionInHeader,
				Fields:    []string{"endpoint", "key_id", "issuer_id", "private_key", "music_user_token"},
			},
			{
				ID:        "save_test",
				Label:     "Save & test",
				BusyLabel: "Saving…",
				Kind:      shared.ActionSave,
				Placement: shared.ActionInForm,
				Fields:    []string{"endpoint", "key_id", "issuer_id", "private_key", "music_user_token"},
			},
		},
	}
}

// OnboardingStatus derives Apple Music's live view: credential presence, a
// one-line summary, hints, and the connection probe when credentials exist.
// Fail-open — a broken setup reports through hints and the probe.
func (c *AppleMusicConnector) OnboardingStatus(ctx context.Context) shared.OnboardingStatus {
	svc := c.host.ServiceConfig(AppleMusicServiceID)
	configured := svc != nil
	hasKey := c.headerValue(appleMusicKeyIDHeader) != "" && c.headerValue(appleMusicPrivateKeyHeader) != ""
	hasUser := c.headerValue(appleMusicUserTokenHeader) != ""

	status := shared.OnboardingStatus{
		Fields: map[string]shared.OnboardingFieldState{
			"endpoint":         {ID: "endpoint", Value: c.host.UpstreamBaseFor(AppleMusicServiceID, appleMusicAPIBase), Valid: true},
			"key_id":           {ID: "key_id", Value: c.headerValue(appleMusicKeyIDHeader), Valid: true},
			"issuer_id":        {ID: "issuer_id", Value: c.headerValue(appleMusicIssuerIDHeader), Valid: true},
			"private_key":      {ID: "private_key", Set: c.headerValue(appleMusicPrivateKeyHeader) != "", Valid: true},
			"music_user_token": {ID: "music_user_token", Set: hasUser, Valid: true},
		},
	}
	switch {
	case hasKey && hasUser:
		status.Summary = append(status.Summary, "key: configured (MusicKit) · user token: configured")
	case hasKey:
		status.Summary = append(status.Summary, "key: configured (MusicKit) · user token: not set")
	default:
		status.Summary = append(status.Summary, "key: not set · user token: not set")
	}

	if !configured {
		status.Hints = append(status.Hints, "Add an apple-music service (this page can save it for you).")
	}
	if !hasKey {
		status.Hints = append(status.Hints, "Create a MusicKit key in App Store Connect, then paste the Key ID, Issuer ID, and .p8 below.")
	}
	if !hasUser {
		status.Hints = append(status.Hints, "Get a Music User Token with the MusicKit JS snippet below, then paste it below.")
	}
	if !hasKey || !hasUser {
		status.Summary = append(status.Summary, "API: unchecked")
		status.Summary = append(status.Summary, appleMusicLibrarySummary(false))
		return status
	}

	res := c.appleMusicProbe(ctx)
	status.Probe = &res
	status.Summary = append(status.Summary, appleMusicProbeSummary(&res))
	status.Summary = append(status.Summary, appleMusicLibrarySummary(c.host.LibraryReadable("playlists")))
	if res.OK {
		status.Hints = append(status.Hints,
			fmt.Sprintf("Connected — %d playlist(s) visible. Try GET /v1/library/playlists.", res.Metrics["playlist_count"]))
	}
	return status
}

// OnboardingRun executes Apple Music's declared verbs. Save returns a
// ConnectionUpdate for core to persist — this connector never writes tokens
// or config itself. Core recomputes the status after applying the update.
func (c *AppleMusicConnector) OnboardingRun(ctx context.Context, action string, in shared.OnboardingInput) (shared.OnboardingResult, error) {
	kind := ""
	for _, a := range c.Onboarding().Actions {
		if a.ID == action {
			kind = string(a.Kind)
			break
		}
	}

	switch kind {
	case string(shared.ActionSave):
		update, err := appleMusicConnectionUpdate(in)
		if err != nil {
			return shared.OnboardingResult{}, err
		}
		return shared.OnboardingResult{
			Updates: []shared.StateUpdate{{Connection: &update}},
		}, nil

	case string(shared.ActionProbe):
		if !c.hasAppleMusicCreds() {
			return shared.OnboardingResult{Probe: &shared.ProbeResult{
				OK:      false,
				Code:    "unauthorized",
				Message: "no MusicKit key and Music User Token configured",
				Hint:    "Paste the key material and a Music User Token below, then save.",
			}}, nil
		}
		res := c.appleMusicProbe(ctx)
		return shared.OnboardingResult{Probe: &res}, nil

	default:
		return shared.OnboardingResult{}, fmt.Errorf("unknown onboarding action %q", action)
	}
}

// hasAppleMusicCreds reports whether both credential halves are present.
func (c *AppleMusicConnector) hasAppleMusicCreds() bool {
	return c.headerValue(appleMusicKeyIDHeader) != "" &&
		c.headerValue(appleMusicIssuerIDHeader) != "" &&
		c.headerValue(appleMusicPrivateKeyHeader) != "" &&
		c.headerValue(appleMusicUserTokenHeader) != ""
}

// appleMusicConnectionUpdate maps submitted form values to a connection
// save. Blank secret fields leave any stored credential untouched (core
// merges headers key-wise).
func appleMusicConnectionUpdate(in shared.OnboardingInput) (shared.ConnectionUpdate, error) {
	endpoint := strings.TrimSpace(in.Values["endpoint"])
	if endpoint == "" {
		endpoint = appleMusicAPIBase
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return shared.ConnectionUpdate{}, fmt.Errorf("endpoint must be an http(s) URL")
	}
	update := shared.ConnectionUpdate{
		Endpoint: strings.TrimRight(endpoint, "/"),
		// Non-nil so core takes the static-credential path even when no new
		// secret is submitted (matches the admin save semantics).
		SecretHeaders: map[string]string{},
	}
	if v := strings.TrimSpace(in.Values["key_id"]); v != "" {
		update.SecretHeaders[appleMusicKeyIDHeader] = v
	}
	if v := strings.TrimSpace(in.Values["issuer_id"]); v != "" {
		update.SecretHeaders[appleMusicIssuerIDHeader] = v
	}
	if v := strings.TrimSpace(in.Values["private_key"]); v != "" {
		// Validate the key when one is submitted; a partial update that
		// leaves the stored key alone must still save.
		if _, err := parseAppleMusicPrivateKey(v); err != nil {
			return shared.ConnectionUpdate{}, err
		}
		update.SecretHeaders[appleMusicPrivateKeyHeader] = v
	}
	if v := strings.TrimSpace(in.Values["music_user_token"]); v != "" {
		update.SecretHeaders[appleMusicUserTokenHeader] = v
	}
	return update, nil
}

// appleMusicProbe checks reachability and auth with the playlist listing:
// one call that exercises the developer token and the Music User Token.
func (c *AppleMusicConnector) appleMusicProbe(ctx context.Context) shared.ProbeResult {
	raw, _, err := c.apiJSON(ctx, "GET", "/me/library/playlists", map[string][]string{"limit": {"1"}}, nil)
	switch {
	case err == kinds.ErrUnauthorized:
		return shared.ProbeResult{
			OK:      false,
			Code:    "unauthorized",
			Message: "Apple Music rejected the credentials — check the MusicKit key and Music User Token",
			Hint:    "Apple may have revoked the Music User Token; generate a new one and save it again.",
		}
	case err != nil:
		return shared.ProbeResult{OK: false, Code: "error", Message: err.Error()}
	}
	n := 0
	if meta, ok := raw["meta"].(map[string]any); ok {
		n, _ = shared.AsInt(meta["total"])
	}
	return shared.ProbeResult{
		OK:      true,
		Code:    "ok",
		Message: fmt.Sprintf("Apple Music API reachable — %d playlist(s)", n),
		Metrics: map[string]int{"playlist_count": n},
	}
}

// appleMusicProbeSummary is the API half of the panel's status line.
func appleMusicProbeSummary(res *shared.ProbeResult) string {
	if res == nil {
		return ""
	}
	if res.OK {
		return fmt.Sprintf("API: ok · %d playlist(s)", res.Metrics["playlist_count"])
	}
	return "API: " + res.Code
}

// appleMusicLibrarySummary is the library half of the panel's status line.
func appleMusicLibrarySummary(ready bool) string {
	if ready {
		return "library: ready"
	}
	return "library: not ready"
}

// compile-time check: Apple Music drives its own setup (issue #265).
var _ Onboarder = (*AppleMusicConnector)(nil)
