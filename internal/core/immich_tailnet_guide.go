package core

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
)

// Immich VIP-Service wizard copy (issue #124) lives here until Phase 3 of
// issue #265 moves it beside the Immich connector. The setup flow around it
// already speaks the connector-owned onboarding seam; this file only keeps
// the tailnet walkthrough's guide building and status response alive.

type ImmichTailnetGuide struct {
	// ServiceName is the recommended VIP service name.
	ServiceName string `json:"service_name"`
	// DisplayName is the admin-console label for the service.
	DisplayName string `json:"display_name"`
	// TailnetDNS is the MagicDNS suffix when derivable from -base-url
	// (e.g. your-tailnet.ts.net).
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

// normalizeImmichEndpoint repairs the mistakes first-timers make: missing
// scheme, bare host:port, web-UI URL without /api. Returns the normalized

func slogImmich() *slog.Logger {
	return slog.With("handler", "admin_immich_setup")
}

// mustNorm is the tailnet wizard's shorthand for the connector's endpoint
// repair, which is the one owner of those rules (issue #265).
func mustNorm(raw string) string {
	out, _ := connectors.NormalizeImmichEndpoint(raw)
	return out
}

// ImmichTailnetStatus is GET /v1/admin/immich/tailnet/status: the copyable
// VIP walkthrough plus the live per-step view. The connection setup itself
// lives on /v1/admin/setup/immich.
type ImmichTailnetStatus struct {
	Tailnet     *ImmichTailnetGuide      `json:"tailnet,omitempty"`
	TailnetLive *ImmichTailnetLiveStatus `json:"tailnet_live,omitempty"`
}

func (s *Server) writeImmichTailnetStatus(w http.ResponseWriter) {
	status := ImmichTailnetStatus{
		Tailnet:     immichTailnetGuide(s.baseURL, s.immichConfiguredEndpoint()),
		TailnetLive: s.checkImmichTailnetSteps(context.Background()),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

func immichTailnetGuide(baseURL, configuredEndpoint string) *ImmichTailnetGuide {
	suffix := connectors.TailnetDNSFromBaseURL(baseURL)
	suggested := connectors.ImmichSuggestedEndpoint(suffix)
	if ep := strings.TrimSpace(configuredEndpoint); ep != "" {
		// Keep a live endpoint as the connect target once configured.
		if mustNorm(ep) != "" {
			suggested = mustNorm(ep)
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

// immichHeaderValue returns a header value case-insensitively. Kept here so
// the tailnet wizard can preserve an existing API key without depending on
// the setup flow (issue #265).
func immichHeaderValue(headers map[string]string) string {
	for k, v := range headers {
		if strings.EqualFold(k, "X-API-Key") {
			return v
		}
	}
	return ""
}

// handleImmichTailnetRoutes serves the VIP-Service wizard under
// /v1/admin/immich/… (issue #124). Connection setup moved to the generic
// /v1/admin/setup/{id} seam (issue #265).
func (s *Server) handleImmichTailnetRoutes(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/immich"), "/")
	if !dispatchImmichTailnetSubroute(s, w, r, path) {
		writeJSONError(w, http.StatusNotFound, "unknown admin endpoint")
	}
}
