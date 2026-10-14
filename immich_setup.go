package main

import (
	"github.com/fudomunro/aegis/internal/connectors"

	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// immichAPIKeyPermissions is the single Immich v3 checkbox set for one key
// used by both Aegis (list/thumbnail/download) and immich-go Takeout import.
// Keep in sync with web/admin.html and docs/immich-live-testing.md.
var immichAPIKeyPermissions = []string{
	// Aegis photo library + proxy downloads
	"asset.read", "asset.view", "asset.download",
	"album.read", "user.read",
	// immich-go upload from-google-photos (album/tag/stack + upload)
	"asset.upload", "asset.update", "asset.copy", "asset.delete", "asset.statistics",
	"album.create", "albumAsset.create",
	"stack.create", "tag.create", "tag.asset",
	"server.about",
	// immich-go pauses jobs during import (admin-linked key)
	"job.create", "job.read",
}

func immichAPIKeyPermissionsSummary() string {
	return strings.Join(immichAPIKeyPermissions, ", ")
}

// Immich setup admin API (issues #39, #121). Guides operators through connecting a
// self-hosted Immich as the local/foreign canonical photo store without
// leaving the Aegis admin UI or hand-editing aegis.json. Issue #121 adds
// first-timer hand-holding: endpoint normalization, actionable diagnostics,
// and a live next-step prompt.

// ImmichSetupCheck is one row of the live acceptance checklist.
type ImmichSetupCheck struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
}

// ImmichSetupStatus is GET /v1/admin/immich/setup.
type ImmichSetupStatus struct {
	Configured        bool               `json:"configured"`
	Endpoint          string             `json:"endpoint,omitempty"`
	HasAPIKey         bool               `json:"has_api_key"`
	CanonicalFor      []string           `json:"canonical_for,omitempty"`
	CanonicalConflict string             `json:"canonical_conflict,omitempty"`
	Snippet           string             `json:"snippet,omitempty"`
	Checks            []ImmichSetupCheck `json:"checks,omitempty"`
	LibraryStatus     string             `json:"library_status,omitempty"`
	LibraryCount      int                `json:"library_count,omitempty"`
	LibraryError      string             `json:"library_error,omitempty"`
	// Steps is the guided first-run ladder (issue #121).
	Steps []ImmichSetupStep `json:"steps,omitempty"`
	// NextStep is a short, imperative hint for what to do next.
	NextStep string `json:"next_step,omitempty"`
	// LastProbe is the most recent connection diagnosis (when available).
	LastProbe *ImmichTestResponse `json:"last_probe,omitempty"`
	// Tailnet guides operators to a friendly ts.net URL via Tailscale
	// VIP Services (issue #124). Optional automation is a later phase.
	Tailnet *ImmichTailnetGuide `json:"tailnet,omitempty"`
}

// ImmichTailnetGuide is the issue #124 operator path: give Immich a stable
// VIP Service name (svc:immich) instead of a raw host:port. Aegis only
// emits copyable steps — it does not mutate tailnet topology.
type ImmichTailnetGuide struct {
	// ServiceName is the recommended VIP service name.
	ServiceName string `json:"service_name"`
	// DisplayName is the admin-console label for the service.
	DisplayName string `json:"display_name"`
	// TailnetDNS is the MagicDNS suffix when derivable from -base-url
	// (e.g. heron-census.ts.net).
	TailnetDNS string `json:"tailnet_dns,omitempty"`
	// SuggestedEndpoint is the Immich API base once the service is hosted.
	SuggestedEndpoint string `json:"suggested_endpoint,omitempty"`
	// Why is a one-line rationale for the wizard.
	Why string `json:"why"`
	// Requirements are hard preconditions (tags, scopes, co-location).
	Requirements []string `json:"requirements,omitempty"`
	// Steps are ordered operator actions; Command is copyable when set.
	Steps []ImmichTailnetStep `json:"steps"`
	// Commands is one pasteable script covering the whole path.
	Commands string `json:"commands,omitempty"`
}

// ImmichTailnetStep is one copyable VIP Service action (issue #124).
type ImmichTailnetStep struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Detail  string `json:"detail"`
	Command string `json:"command,omitempty"`
}

// ImmichSetupStep is one row of the guided setup ladder.
type ImmichSetupStep struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
	Detail  string `json:"detail,omitempty"`
	Current bool   `json:"current,omitempty"`
}

// ImmichTestRequest is POST /v1/admin/immich/test. Omitted fields use the
// configured service. The API key is never stored or echoed back.
type ImmichTestRequest struct {
	Endpoint string `json:"endpoint,omitempty"`
	APIKey   string `json:"api_key,omitempty"`
}

// ImmichTestResponse is the result of probing Immich. Code classifies the
// outcome for the UI; Hint is an actionable next step (never a secret).
type ImmichTestResponse struct {
	OK      bool   `json:"ok"`
	Status  int    `json:"status,omitempty"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	// SuggestedEndpoint is a normalized URL when the input was ambiguous.
	SuggestedEndpoint string `json:"suggested_endpoint,omitempty"`
}

// Immich probe / diagnosis codes (issue #121).
const (
	immichCodeOK            = "ok"
	immichCodeOKNoKey       = "ok_no_key"
	immichCodeEmptyEndpoint = "empty_endpoint"
	immichCodeBadURL        = "bad_url"
	immichCodeUnreachable   = "unreachable"
	immichCodeTimeout       = "timeout"
	immichCodeUnauthorized  = "unauthorized"
	immichCodeWrongPath     = "wrong_path"
	immichCodeNotImmich     = "not_immich"
	immichCodeHTTPError     = "http_error"
)

// ImmichConfigRequest is POST /v1/admin/immich/config — save connection from
// the admin UI into service_connections.json (not aegis.json).
type ImmichConfigRequest struct {
	Endpoint                string `json:"endpoint"`
	APIKey                  string `json:"api_key,omitempty"`
	ClaimCanonicalForPhotos bool   `json:"claim_canonical_for_photos"`
}

// HandleAdminImmichSetup serves GET /v1/admin/immich/setup and dispatches
// POST sub-actions under /v1/admin/immich/*.
func (s *Server) HandleAdminImmichSetup(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/immich"), "/")
	switch {
	case path == "" || path == "setup":
		if r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeImmichSetupStatus(w, r, true)
	case path == "test":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleImmichTest(w, r)
	case path == "config":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.handleImmichConfig(w, r)
	case path == "check":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		s.writeImmichSetupStatus(w, r, true)
	default:
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}

func (s *Server) writeImmichSetupStatus(w http.ResponseWriter, r *http.Request, withChecks bool) {
	status := s.immichSetupStatus(r.Context(), withChecks)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func (s *Server) immichSetupStatus(ctx context.Context, withChecks bool) ImmichSetupStatus {
	svc := s.config.GetService(connectors.ImmichServiceID)
	status := ImmichSetupStatus{}
	if svc != nil {
		status.Configured = true
		status.Endpoint = svc.Endpoint
		status.HasAPIKey = svc.HasStaticAPIKey()
		status.CanonicalFor = append([]string(nil), svc.CanonicalFor...)
	}
	// Surface who currently owns photos when Immich does not.
	if svc == nil || !svc.IsCanonicalFor("photos") {
		for _, other := range s.config.ServiceSnapshots() {
			if other.ID == connectors.ImmichServiceID {
				continue
			}
			if other.IsCanonicalFor("photos") {
				status.CanonicalConflict = other.ID
				break
			}
		}
	}
	status.Snippet = immichConfigSnippet(status.Endpoint, s.baseURL)
	status.Tailnet = immichTailnetGuide(s.baseURL, status.Endpoint)

	if !withChecks {
		status.Steps = immichGuidedSteps(&status)
		status.NextStep = immichNextStepFrom(status.Steps, status.LastProbe, status.LibraryCount == 0)
		return status
	}
	status.Checks = s.runImmichChecks(ctx, &status)
	status.Steps = immichGuidedSteps(&status)
	status.NextStep = immichNextStepFrom(status.Steps, status.LastProbe, status.LibraryCount == 0)
	return status
}

func immichConfigSnippet(endpoint, baseURL string) string {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = immichSuggestedEndpoint(tailnetDNSFromBaseURL(baseURL))
	}
	// Display-only bootstrap snippet for operators who prefer aegis.json.
	return fmt.Sprintf(`{
  "id": "immich",
  "name": "Immich",
  "action": "inject",
  "endpoint": %q,
  "headers": { "X-API-Key": "YOUR_IMMICH_API_KEY" },
  "cache_ttl": "5m",
  "canonical_for": ["photos"],
  "permissions": { "default": "read", "photos": "read" }
}`, endpoint)
}

// tailnetDNSFromBaseURL extracts the MagicDNS suffix from Aegis's public URL
// (https://aegis.heron-census.ts.net → heron-census.ts.net). Empty when the
// base URL is not a ts.net name.
func tailnetDNSFromBaseURL(baseURL string) string {
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	// node.tailnet.ts.net → tailnet.ts.net
	if i := strings.Index(host, "."); i > 0 {
		suffix := host[i+1:]
		if strings.HasSuffix(suffix, ".ts.net") {
			return suffix
		}
	}
	return ""
}

// immichSuggestedEndpoint is the Immich API base operators should paste.
// Prefer the VIP Service HTTPS name when the tailnet suffix is known;
// otherwise the portable host:port form from the live-testing guide.
func immichSuggestedEndpoint(tailnetDNS string) string {
	if tailnetDNS != "" {
		return "https://immich." + tailnetDNS + "/api"
	}
	return "http://photos.home.ts.net:2283/api"
}

// immichTailnetGuide builds the issue #124 VIP Services walkthrough.
// Aegis never calls the Tailscale API here — this is copyable operator
// guidance so we do not mutate tailnet topology from a request handler.
func immichTailnetGuide(baseURL, configuredEndpoint string) *ImmichTailnetGuide {
	suffix := tailnetDNSFromBaseURL(baseURL)
	suggested := immichSuggestedEndpoint(suffix)
	if ep := strings.TrimSpace(configuredEndpoint); ep != "" {
		// Keep a live endpoint as the connect target once configured.
		if mustNormalize(ep) != "" {
			suggested = mustNormalize(ep)
		}
	}

	g := &ImmichTailnetGuide{
		ServiceName:       "svc:immich",
		DisplayName:       "Immich",
		TailnetDNS:        suffix,
		SuggestedEndpoint: suggested,
		Why:               "A Tailscale VIP Service gives Immich a stable https://…ts.net name (no :2283, real certs) that survives host moves. Serve stays tailnet-only — never Funnel.",
		Requirements: []string{
			"Immich host runs Tailscale and can bind Serve (same machine as Immich, or a reverse proxy to it).",
			"Service hosts must be tagged nodes (tag:immich-host or similar) — user-owned nodes cannot host svc:*.",
			"Tailscale OAuth client scopes: services, devices:core (or an owner API token).",
			"ACL must declare the tag before the device can be tagged.",
		},
	}

	tag := "tag:immich-host"
	api := "https://api.tailscale.com/api/v2"
	tailnetPath := "your-tailnet" // or "-" for the default tailnet in newer API versions

	g.Steps = []ImmichTailnetStep{
		{
			ID:     "acl_tag",
			Title:  "Declare a host tag in the tailnet policy file",
			Detail: "Admin console → Access Controls. Add a tag owner so the Immich machine can be tagged.",
			Command: fmt.Sprintf(`"tagOwners": {
  %q: ["autogroup:admin"]
}`, tag),
		},
		{
			ID:     "create_service",
			Title:  "Create the VIP Service",
			Detail: "Creates a dedicated virtual IP + DNS name for Immich. Ports: tcp:443 (HTTPS). Name must be unique in the tailnet (names already used by a machine cannot be reused).",
			Command: fmt.Sprintf(`curl -sS -u "$TS_API_KEY:" -X PUT \
  -H "Content-Type: application/json" \
  %s/tailnet/%s/services/svc:immich \
  -d '{
    "name": "svc:immich",
    "displayName": "Immich",
    "ports": ["tcp:443"],
    "tags": [%q]
  }'`, api, tailnetPath, tag),
		},
		{
			ID:     "tag_host",
			Title:  "Tag the Immich host",
			Detail: "POST the tag list to the device that runs Immich (device id from GET /tailnet/{tailnet}/devices). Tagging transfers ownership to the tag — expected for service hosts. Keep any existing tags you still need.",
			Command: fmt.Sprintf(`curl -sS -u "$TS_API_KEY:" -X POST \
  -H "Content-Type: application/json" \
  %s/device/$DEVICE_ID/tags \
  -d '{"tags": [%q]}'`, api, tag),
		},
		{
			ID:     "serve",
			Title:  "Host the service on the Immich machine",
			Detail: "On the Immich host (after tagging): map the VIP service to local Immich. Use --bg so it persists. Confirm with tailscale serve status.",
			Command: `tailscale serve --service svc:immich --bg --https=443 http://127.0.0.1:2283
tailscale serve status
# Optional declarative form:
#   tailscale serve get-config --service=svc:immich immich-service.json
#   tailscale serve set-config --service=svc:immich immich-service.json`,
		},
		{
			ID:     "approve",
			Title:  "Approve the host for the service",
			Detail: "If the host is not auto-approved, approve it from the admin API or the Services page.",
			Command: fmt.Sprintf(`curl -sS -u "$TS_API_KEY:" -X POST \
  -H "Content-Type: application/json" \
  %s/tailnet/%s/services/svc:immich/device/$DEVICE_ID/approved \
  -d '{"approved": true}'`, api, tailnetPath),
		},
		{
			ID:     "connect",
			Title:  "Point Aegis at the VIP name",
			Detail: "Use the HTTPS name Tailscale printed in serve status (typically https://immich.<tailnet>.ts.net). Endpoint must end in /api.",
			Command: fmt.Sprintf(`# Aegis endpoint (Admin → Services → Immich):
%s`, suggested),
		},
	}

	// Build commands from steps so UI copy button and API stay in sync.
	var cmds []string
	for _, st := range g.Steps {
		if st.Command == "" {
			continue
		}
		cmds = append(cmds, "# "+st.Title+"\n"+st.Command)
	}
	g.Commands = strings.Join(cmds, "\n\n") + "\n"
	return g
}

// runImmichChecks executes the issue #39 acceptance checklist against the
// live configuration. Fail-open: a broken Immich marks checks failed but never
// errors the admin request.
func (s *Server) runImmichChecks(ctx context.Context, status *ImmichSetupStatus) []ImmichSetupCheck {
	checks := []ImmichSetupCheck{
		{ID: "service_configured", Label: "Immich service configured"},
		{ID: "api_key", Label: "API key present (X-API-Key)"},
		{ID: "canonical_owner", Label: "Immich is canonical_for photos"},
		{ID: "endpoint_reachable", Label: "Immich endpoint reachable"},
		{ID: "library_listing", Label: "Library lists Immich photos"},
		{ID: "canonical_stamp", Label: "Sources stamped canonical + proxy blob_url"},
		{ID: "bytes_stay_upstream", Label: "Photo bytes stay on Immich"},
	}
	byID := map[string]int{}
	for i, c := range checks {
		byID[c.ID] = i
	}
	mark := func(id, detail string, ok bool) {
		i, exists := byID[id]
		if !exists {
			return
		}
		checks[i].OK = ok
		checks[i].Detail = detail
	}

	svc := s.config.GetService(connectors.ImmichServiceID)
	if svc == nil {
		mark("service_configured", "Add Immich with the form above (or aegis.json)", false)
		mark("api_key", "Not configured", false)
		mark("canonical_owner", "Not configured", false)
		mark("endpoint_reachable", "Not configured", false)
		mark("library_listing", "Not configured", false)
		mark("canonical_stamp", "Not configured", false)
		mark("bytes_stay_upstream", "Product rule: Aegis never stores photo bytes", true)
		return checks
	}
	mark("service_configured", "id=immich", true)

	if status.HasAPIKey {
		mark("api_key", "Header set (value not shown)", true)
	} else {
		mark("api_key", "Set X-API-Key via this form or aegis.json headers", false)
	}

	if svc.IsCanonicalFor("photos") {
		mark("canonical_owner", "canonical_for includes photos", true)
	} else if status.CanonicalConflict != "" {
		mark("canonical_owner", "Currently claimed by "+status.CanonicalConflict+" — enable claim below to move it", false)
	} else {
		mark("canonical_owner", "No service claims canonical_for photos (optional)", true)
	}

	// Probe reachability + auth using the configured connector path.
	probe := s.probeImmich(ctx, svc.Endpoint, headerValue(svc.Headers, "X-API-Key"))
	status.LastProbe = &probe
	if probe.OK {
		mark("endpoint_reachable", probe.Message, true)
	} else {
		mark("endpoint_reachable", probe.Message, false)
		mark("library_listing", "Skipped — endpoint/auth failed", false)
		mark("canonical_stamp", "Skipped — endpoint/auth failed", false)
		mark("bytes_stay_upstream", "Product rule: Aegis never stores photo bytes", true)
		return checks
	}

	photos, truncated, err := s.listImmichPhotos(ctx)
	if err != nil {
		status.LibraryStatus = "error"
		if err == types.ErrUnauthorized {
			status.LibraryStatus = "unauthorized"
		}
		status.LibraryError = err.Error()
		mark("library_listing", err.Error(), false)
		mark("canonical_stamp", "Skipped — listing failed", false)
		mark("bytes_stay_upstream", "Product rule: Aegis never stores photo bytes", true)
		return checks
	}
	status.LibraryStatus = "ok"
	status.LibraryCount = len(photos)
	if truncated {
		status.LibraryStatus = "ok"
		status.LibraryError = "truncated at page cap"
	}
	if len(photos) == 0 {
		mark("library_listing", "0 photos — Immich is empty. Add photos via web upload, phone backup, or Google Takeout + immich-go", false)
		if status.LibraryError == "" {
			status.LibraryError = "Immich has no photos yet"
		}
	} else {
		mark("library_listing", fmt.Sprintf("%d photos", len(photos)), true)
	}

	stampOK, stampDetail := immichStampSummary(photos, s.baseURL)
	mark("canonical_stamp", stampDetail, stampOK)

	// Product invariant, not a live probe.
	mark("bytes_stay_upstream", "Metadata + proxy only; downloads stream from Immich", true)
	return checks
}

func headerValue(headers map[string]string, key string) string {
	for k, v := range headers {
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return ""
}

// normalizeImmichEndpoint repairs the mistakes first-timers make: missing
// scheme, bare host:port, web-UI URL without /api. Returns the normalized
// endpoint and whether it changed.
func normalizeImmichEndpoint(raw string) (string, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", false
	}
	original := s
	// Bare host:port or host → assume http and default Immich port when none.
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return strings.TrimRight(original, "/"), false
	}
	if u.Path == "" || u.Path == "/" {
		if u.Port() == "" {
			// Common Immich default when people paste just a hostname.
			if strings.EqualFold(u.Hostname(), "localhost") || strings.EqualFold(u.Hostname(), "127.0.0.1") {
				u.Host = u.Hostname() + ":2283"
			}
		}
		u.Path = "/api"
	} else {
		trimmed := strings.TrimRight(u.Path, "/")
		lower := strings.ToLower(trimmed)
		switch {
		case strings.HasSuffix(lower, "/api") || strings.Contains(lower, "/api/"):
			u.Path = trimmed
		case strings.HasSuffix(lower, "/photos"),
			strings.Contains(lower, "/photos/"),
			strings.HasSuffix(lower, "/album"),
			strings.Contains(lower, "/album/"),
			strings.Contains(lower, "/albums/"),
			strings.Contains(lower, "/share/"):
			// Pasted a web UI deep link — not the API base.
			u.Path = "/api"
		default:
			// Leave explicit non-root paths alone (reverse-proxy prefixes).
		}
	}
	// url.String() may re-encode; rebuild a clean base.
	out := u.Scheme + "://" + u.Host + strings.TrimRight(u.Path, "/")
	if out == "" {
		return original, false
	}
	return out, out != strings.TrimRight(original, "/") && out != original
}

// probeImmich checks endpoint reachability and API key auth with actionable
// diagnostics (issue #121). Tries GET /users/me (auth) then GET /server/ping.
func (s *Server) probeImmich(ctx context.Context, endpoint, apiKey string) ImmichTestResponse {
	raw := strings.TrimSpace(endpoint)
	base, changed := normalizeImmichEndpoint(raw)
	if base == "" {
		return ImmichTestResponse{
			OK:      false,
			Code:    immichCodeEmptyEndpoint,
			Message: "No Immich endpoint yet",
			Hint:    "Install Immich (https://docs.immich.app/overview/quick-start/), then paste its URL — usually http://<host>:2283/api",
		}
	}
	suggested := ""
	if changed {
		suggested = base
	}

	if _, err := url.Parse(base); err != nil {
		return ImmichTestResponse{
			OK:      false,
			Code:    immichCodeBadURL,
			Message: "That does not look like a URL",
			Hint:    "Use something like https://immich.your-tailnet.ts.net/api or http://photos.home.ts.net:2283/api (include http:// or https://). See the VIP Service guide for a friendly ts.net name (issue #124).",
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	// Prefer an authenticated call so we surface bad keys clearly.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/users/me", nil)
	if err == nil {
		if apiKey != "" {
			req.Header.Set("X-API-Key", apiKey)
		}
		resp, err := aegisHTTPClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return ImmichTestResponse{
					OK: true, Status: resp.StatusCode, Code: immichCodeOK,
					Message:           "Immich accepted the API key",
					Hint:              "Save the connection, then run checks.",
					SuggestedEndpoint: suggested,
				}
			}
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return ImmichTestResponse{
					OK: false, Status: resp.StatusCode, Code: immichCodeUnauthorized,
					Message:           "Immich rejected the API key",
					Hint:              "In Immich: Account → API Keys → New key. Tick the single-key set (Aegis + immich-go): " + immichAPIKeyPermissionsSummary() + ". Copy the secret once, paste it here.",
					SuggestedEndpoint: suggested,
				}
			}
		}
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/server/ping", nil)
	if err != nil {
		return ImmichTestResponse{OK: false, Code: immichCodeBadURL, Message: err.Error(), Hint: "Check the endpoint spelling."}
	}
	if apiKey != "" {
		req2.Header.Set("X-API-Key", apiKey)
	}
	resp2, err := aegisHTTPClient.Do(req2)
	if err != nil {
		code := immichCodeUnreachable
		hint := "Start Immich with Docker: save our starter as .env (before the first up), docker compose up -d, then wait for immich_server to be healthy in docker compose ps. If localhost:2283 is refused, check server logs for database \"immich\" does not exist. Use a hostname this Aegis host can reach (LAN IP or tailnet name)."
		if ctx.Err() != nil || strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
			code = immichCodeTimeout
			hint = "The request timed out. Immich may be slow to start, firewalled, or on another network. Try the tailnet hostname if this Aegis runs on a different machine."
		}
		if suggested == "" {
			if norm, ch := normalizeImmichEndpoint(raw); ch {
				suggested = norm
			}
		}
		return ImmichTestResponse{
			OK: false, Code: code,
			Message:           "Could not reach Immich: " + err.Error(),
			Hint:              hint,
			SuggestedEndpoint: suggested,
		}
	}
	defer resp2.Body.Close()
	sniff := make([]byte, 64)
	n, _ := io.ReadFull(resp2.Body, sniff)
	bodyHint := strings.ToLower(string(sniff[:n]))

	if resp2.StatusCode == http.StatusOK {
		if apiKey == "" {
			return ImmichTestResponse{
				OK: true, Status: resp2.StatusCode, Code: immichCodeOKNoKey,
				Message:           "Immich is reachable (no API key set yet)",
				Hint:              "Create one Immich API key for Aegis + immich-go (permissions: " + immichAPIKeyPermissionsSummary() + ") and paste it above so agents can list photos.",
				SuggestedEndpoint: suggested,
			}
		}
		return ImmichTestResponse{
			OK: true, Status: resp2.StatusCode, Code: immichCodeOK,
			Message:           "Immich is reachable",
			Hint:              "If you have not already, paste the API key and save.",
			SuggestedEndpoint: suggested,
		}
	}

	// Classify non-200 responses for first-timers.
	switch {
	case resp2.StatusCode == http.StatusUnauthorized || resp2.StatusCode == http.StatusForbidden:
		return ImmichTestResponse{
			OK: false, Status: resp2.StatusCode, Code: immichCodeUnauthorized,
			Message:           fmt.Sprintf("Immich returned HTTP %d", resp2.StatusCode),
			Hint:              "Check the API key (Account → API Keys). If you just created Immich, finish the web-UI signup first.",
			SuggestedEndpoint: suggested,
		}
	case resp2.StatusCode == http.StatusNotFound && strings.Contains(bodyHint, "<html"):
		return ImmichTestResponse{
			OK: false, Status: resp2.StatusCode, Code: immichCodeWrongPath,
			Message:           "Got a web page, not the Immich API",
			Hint:              "The endpoint must be the API base and end in /api — e.g. http://host:2283/api (not the album /photos page).",
			SuggestedEndpoint: firstNonEmpty(suggested, mustNormalize(base+"/api")),
		}
	case strings.Contains(bodyHint, "<html") || strings.Contains(bodyHint, "<!doctype"):
		return ImmichTestResponse{
			OK: false, Status: resp2.StatusCode, Code: immichCodeNotImmich,
			Message:           "That host answered with a web page, not Immich",
			Hint:              "Double-check the host/port. Immich’s API answers JSON from /server/ping on port 2283 by default.",
			SuggestedEndpoint: suggested,
		}
	default:
		return ImmichTestResponse{
			OK: false, Status: resp2.StatusCode, Code: immichCodeHTTPError,
			Message:           fmt.Sprintf("Immich returned HTTP %d", resp2.StatusCode),
			Hint:              "Open the same URL in a browser or curl. If it is not Immich, fix the endpoint; if Immich is healthy, check reverse-proxy / TLS.",
			SuggestedEndpoint: suggested,
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func mustNormalize(raw string) string {
	out, _ := normalizeImmichEndpoint(raw)
	return out
}

// immichGuidedSteps builds the first-run ladder and the single next step
// (issue #121). Steps are ordered: install → key → connect → verify.
func immichGuidedSteps(status *ImmichSetupStatus) []ImmichSetupStep {
	reachable := false
	switch {
	case status.LastProbe != nil && status.LastProbe.OK:
		reachable = true
	case status.LibraryStatus == "ok":
		reachable = true
	}
	installDetail := "Install with the starter .env (docs/immich-starter.env) + docker compose up -d, wait for healthy. Web UI: http://localhost:2283"
	if status.Endpoint != "" {
		installDetail = "API base: " + status.Endpoint
	}
	if reachable {
		installDetail = "Immich answered on " + firstNonEmpty(status.Endpoint, "the configured endpoint")
	}
	keyDetail := "Immich → Account → API Keys → New key. One key for Aegis + immich-go: " + immichAPIKeyPermissionsSummary() + ". Copy it once."
	if status.HasAPIKey {
		keyDetail = "API key is saved in Aegis (value not shown)."
	}
	connectDetail := "Use Test connection + Save to Aegis below."
	if status.Configured {
		connectDetail = "Connected in Aegis (service_connections.json)."
	}
	verifyDetail := "Run checks after saving."
	verifyOK := false
	if status.Configured && status.HasAPIKey && len(status.Checks) > 0 {
		failed := false
		for _, c := range status.Checks {
			if !c.OK && c.ID != "canonical_owner" {
				failed = true
				break
			}
		}
		if !failed {
			verifyOK = true
			verifyDetail = "Checklist looks good — agents can use /v1/library/photos."
		} else {
			verifyDetail = "Some checks failed — see below."
		}
	} else if status.Configured && status.HasAPIKey {
		verifyDetail = "Run checks to finish verification."
	}

	steps := []ImmichSetupStep{
		{ID: "install", Label: "Immich installed and reachable", OK: reachable, Detail: installDetail},
		{ID: "api_key", Label: "Immich API key created", OK: status.HasAPIKey, Detail: keyDetail},
		{ID: "connect", Label: "Saved into Aegis", OK: status.Configured, Detail: connectDetail},
		{ID: "verify", Label: "Verified end-to-end", OK: verifyOK, Detail: verifyDetail},
	}
	for i := range steps {
		if !steps[i].OK {
			steps[i].Current = true
			break
		}
	}
	return steps
}

func immichNextStepFrom(steps []ImmichSetupStep, probe *ImmichTestResponse, emptyLibrary bool) string {
	for _, st := range steps {
		if st.OK {
			continue
		}
		switch st.ID {
		case "install":
			if probe != nil && probe.Hint != "" {
				return probe.Hint
			}
			return "Install Immich with the starter .env (Admin → Services copy button), then Test connection."
		case "api_key":
			return "Create one Immich API key (Aegis + immich-go: " + immichAPIKeyPermissionsSummary() + ") and paste it above."
		case "connect":
			return "Click Save to Aegis to store the connection."
		case "verify":
			if emptyLibrary {
				return "Immich has no photos yet — upload a few in the web UI, phone-backup an album, or import Google Takeout with immich-go (Services → New to Immich?), then Run checks again."
			}
			return "Click Run checks and fix anything still red."
		}
	}
	return "Immich setup is complete. Agents can list photos via /v1/library/photos."
}

func (s *Server) listImmichPhotos(ctx context.Context) ([]types.Photo, bool, error) {
	conn, ok := s.connectors[connectors.ImmichServiceID].(*connectors.ImmichConnector)
	if !ok || conn == nil {
		// Connector not registered in this process — use a temporary one.
		conn = connectors.NewImmichConnector(s)
	}
	return conn.ListUserPhotos(ctx)
}

func immichStampSummary(photos []types.Photo, baseURL string) (bool, string) {
	if len(photos) == 0 {
		return false, "no photos to inspect"
	}
	proxyPrefix := strings.TrimRight(baseURL, "/") + "/v1/services/" + connectors.ImmichServiceID + "/"
	for _, p := range photos {
		for _, src := range p.Sources {
			if src.Service != connectors.ImmichServiceID {
				continue
			}
			if !src.Canonical {
				return false, "Immich source missing canonical:true"
			}
			if src.BlobURL == "" || !strings.HasPrefix(src.BlobURL, proxyPrefix) {
				return false, "blob_url is not an Aegis proxy URL"
			}
			return true, "canonical + proxy blob_url present"
		}
	}
	return false, "no Immich sources in listing"
}

func (s *Server) handleImmichTest(w http.ResponseWriter, r *http.Request) {
	var req ImmichTestRequest
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if len(strings.TrimSpace(string(body))) > 0 {
			if err := json.Unmarshal(body, &req); err != nil {
				writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	apiKey := strings.TrimSpace(req.APIKey)
	if endpoint == "" || apiKey == "" {
		if svc := s.config.GetService(connectors.ImmichServiceID); svc != nil {
			if endpoint == "" {
				endpoint = svc.Endpoint
			}
			if apiKey == "" {
				apiKey = headerValue(svc.Headers, "X-API-Key")
			}
		}
	}
	result := s.probeImmich(r.Context(), endpoint, apiKey)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (s *Server) handleImmichConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req ImmichConfigRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	endpoint := strings.TrimSpace(req.Endpoint)
	if endpoint == "" {
		writeJSONError(w, http.StatusBadRequest, "endpoint is required — e.g. https://immich.your-tailnet.ts.net/api")
		return
	}
	// Repair common first-timer URLs (scheme, /api, localhost port).
	endpoint, _ = normalizeImmichEndpoint(endpoint)
	if endpoint == "" {
		writeJSONError(w, http.StatusBadRequest, "endpoint is required — e.g. https://immich.your-tailnet.ts.net/api")
		return
	}

	entry := serviceConnectionEntry{
		Name:        "Immich",
		Action:      "inject",
		Endpoint:    endpoint,
		CacheTTL:    "5m",
		Description: "Self-hosted Immich photo library. Photo bytes stay on Immich (foreign/local canonical).",
		Aliases: []string{
			"immich photos", "self-hosted photos", "immich",
			"photo", "photos", "picture", "pictures", "photo library",
		},
		Headers: map[string]string{},
	}
	if key := strings.TrimSpace(req.APIKey); key != "" {
		entry.Headers["X-API-Key"] = key
	}
	if req.ClaimCanonicalForPhotos {
		entry.CanonicalFor = []string{"photos"}
	} else {
		entry.ClearCanonicalFor = true
	}

	if err := s.config.SetServiceConnection(connectors.ImmichServiceID, entry); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Refresh cache so library listings pick up the new home immediately.
	s.cache.Clear()
	withPeerLog(slogImmich(), r).Info("immich connection saved from admin UI",
		"endpoint", entry.Endpoint, "claim_photos", req.ClaimCanonicalForPhotos)

	s.writeImmichSetupStatus(w, r, true)
}

func slogImmich() *slog.Logger {
	return slog.With("handler", "admin_immich_setup")
}
