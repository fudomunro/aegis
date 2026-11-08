package connectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// Immich VIP-Service wizard (issue #124, folded into the onboarding seam by
// issue #265): give Immich a stable https://…ts.net name via a Tailscale VIP
// Service. This file owns the walkthrough copy, the live per-step checks, and
// the auto-fix rules. Core owns nothing here but the state writes the
// connector asks for (StateUpdate) and the admin perimeter.
//
// Never mutate tailnet topology from the pre-request hook: only the explicit
// tailnet_apply verb reaches the fix methods.

// Immich VIP service name and default service-host tag.
const (
	ImmichVIPServiceName = "svc:immich"
	immichDefaultHostTag = "tag:immich-host"
	immichGuideIDTailnet = "tailnet"
)

// immichTailnetGuide is the copyable VIP walkthrough. Its steps, details and
// commands are the operator contract; the admin panel renders them and must
// not restate them.
func immichTailnetGuide(baseURL, configuredEndpoint string) types.OnboardingGuide {
	suffix := TailnetDNSFromBaseURL(baseURL)
	suggested := ImmichSuggestedEndpoint(suffix)
	if ep := strings.TrimSpace(configuredEndpoint); ep != "" {
		// Keep a live endpoint as the connect target once configured.
		if n := mustNormalize(ep); n != "" {
			suggested = n
		}
	}

	tag := immichDefaultHostTag
	api := TailscaleAdminAPIBase
	tailnetPath := "your-tailnet" // or "-" for the default tailnet in newer API versions

	g := types.OnboardingGuide{
		ID:          immichGuideIDTailnet,
		Title:       "Friendly tailnet URL (VIP Service)",
		Short:       "Tailnet URL",
		Walkthrough: true,
		Body: "Give Immich a stable `https://…ts.net` name with a Tailscale **VIP Service** — no `:2283`, real certs, host-move safe. " +
			"Tailnet-only Serve; never Funnel. A Tailscale VIP Service gives Immich a stable https://…ts.net name " +
			"(no :2283, real certs) that survives host moves. Serve stays tailnet-only — never Funnel.",
		Label:     "VIP Service walkthrough (copyable)",
		LiveLabel: "Live step status",
		Button:    "Copy all commands",
		Requirements: []string{
			"Immich host runs Tailscale and can bind Serve (same machine as Immich, or a reverse proxy to it).",
			"Service hosts must be tagged nodes (tag:immich-host or similar) — user-owned nodes cannot host svc:*.",
			"Tailscale OAuth client scopes: services, devices:core (or an owner API token).",
			"ACL must declare the tag before the device can be tagged.",
		},
		Steps: []types.OnboardingGuideStep{
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
				ID:    "serve",
				Title: "Host the service on the Immich machine",
				Detail: "On the Immich host (after tagging): map the VIP service to local Immich. Use --bg so it persists. " +
					"Confirm with tailscale serve status.",
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
		},
	}

	// One pasteable script covering the whole path, built from the steps so
	// the copy button and the per-step commands cannot drift.
	var cmds []string
	for _, st := range g.Steps {
		if st.Command == "" {
			continue
		}
		cmds = append(cmds, "# "+st.Title+"\n"+st.Command)
	}
	g.Command = strings.Join(cmds, "\n\n") + "\n"
	return g
}

// ImmichTailnetApplyResult reports what the tailnet_apply verb fixed. The
// refreshed per-step state rides back in the status the runner recomputes.
type ImmichTailnetApplyResult struct {
	Applied []string
	Skipped []string
	Failed  []string
	Details []string
}

// Summary is the one-line operator report the panel shows after an apply.
func (r ImmichTailnetApplyResult) Summary() string {
	parts := []string{}
	if len(r.Applied) > 0 {
		parts = append(parts, "fixed: "+strings.Join(r.Applied, ", "))
	}
	if len(r.Skipped) > 0 {
		parts = append(parts, "skipped: "+strings.Join(r.Skipped, ", "))
	}
	if len(r.Failed) > 0 {
		parts = append(parts, "failed: "+strings.Join(r.Failed, ", "))
	}
	return strings.Join(parts, " · ")
}

// immichTailnetSettings reads the operator's VIP-wizard settings.
func (c *ImmichConnector) immichTailnetSettings() (tailnet, hostDeviceID, hostTag string) {
	s := c.host.SetupSettings(types.SetupFlowTailscale)
	tailnet = strings.TrimSpace(s["tailnet"])
	hostDeviceID = strings.TrimSpace(s["host_device_id"])
	hostTag = strings.TrimSpace(s["host_tag"])
	if hostTag == "" {
		hostTag = immichDefaultHostTag
	}
	return tailnet, hostDeviceID, hostTag
}

// immichTailnetClient builds the Admin API client, or nil when the operator
// has not pasted a key yet.
func (c *ImmichConnector) immichTailnetClient() *TailscaleAdminClient {
	key := strings.TrimSpace(c.host.SetupSecret(types.SetupSecretTailscaleAPIKey))
	if key == "" {
		return nil
	}
	tailnet, _, _ := c.immichTailnetSettings()
	return NewTailscaleAdminClient(key, tailnet)
}

// pickImmichHostDevice prefers an already-tagged service host; otherwise
// leaves the choice to the operator.
func pickImmichHostDevice(devices []TailscaleDevice) string {
	for _, d := range devices {
		for _, t := range d.Tags {
			if t == immichDefaultHostTag || strings.HasPrefix(t, "tag:immich") {
				return d.ID
			}
		}
	}
	return ""
}

// immichTailnetState runs the live per-step verification (issue #124).
// Fail-open: API errors surface as state.Error and unchecked steps, never an
// error status.
func (c *ImmichConnector) immichTailnetState(ctx context.Context) types.OnboardingGuideState {
	guide := immichTailnetGuide(c.host.BaseURL(), c.immichConfiguredEndpoint())
	state := types.OnboardingGuideState{
		Steps: make([]types.OnboardingGuideStepState, 0, len(guide.Steps)),
	}
	for _, st := range guide.Steps {
		state.Steps = append(state.Steps, types.OnboardingGuideStepState{
			ID: st.ID, Title: st.Title, Command: st.Command,
		})
	}
	_, savedHost, hostTag := c.immichTailnetSettings()

	cl := c.immichTailnetClient()
	if cl == nil {
		state.Error = "Tailscale API key not configured — paste one to enable live checks and one-click fixes."
		return state
	}

	// Discover host candidates once; used by tag_host / approve checks and by
	// the picker's live options.
	devices, devErr := cl.ListDevices(ctx)
	hostID := savedHost
	if hostID == "" {
		hostID = pickImmichHostDevice(devices)
	}

	byID := map[string]*types.OnboardingGuideStepState{}
	for i := range state.Steps {
		byID[state.Steps[i].ID] = &state.Steps[i]
	}

	// Order matters: the tag must be declared before create/tag/approve.
	c.tsCheckACLTag(ctx, cl, byID["acl_tag"], hostTag)
	c.tsCheckCreateService(ctx, cl, byID["create_service"])
	c.tsCheckTagHost(ctx, cl, devices, byID["tag_host"], hostID, hostTag)
	c.tsCheckServe(ctx, byID["serve"])
	c.tsCheckApprove(ctx, cl, byID["approve"], hostID)
	c.tsCheckConnect(byID["connect"])

	state.Checked = true
	state.Summary = immichTailnetSummary(c.host.SetupSecret(types.SetupSecretTailscaleAPIKey) != "", state.Checked, hostTag, hostID)
	if devErr != nil {
		state.Error = "device list: " + devErr.Error()
	}
	return state
}

// immichTailnetSummary is the one-line live status above the step list.
func immichTailnetSummary(hasKey, checked bool, hostTag, hostID string) []string {
	bits := []string{"no API key"}
	if hasKey {
		bits[0] = "API key set"
	}
	if checked {
		bits = append(bits, "live-checked")
	}
	if hostTag != "" {
		bits = append(bits, hostTag)
	}
	if hostID != "" {
		bits = append(bits, "host "+hostID)
	}
	return bits
}

// immichDeviceOptions is the host picker's live choice list.
func immichDeviceOptions(devices []TailscaleDevice) []types.OnboardingOption {
	out := make([]types.OnboardingOption, 0, len(devices))
	for _, d := range devices {
		label := d.Label()
		if len(d.Tags) > 0 {
			label += " — " + strings.Join(d.Tags, ", ")
		}
		out = append(out, types.OnboardingOption{ID: d.ID, Label: label})
	}
	return out
}

// immichConfiguredEndpoint is the Immich API base Aegis currently points at.
func (c *ImmichConnector) immichConfiguredEndpoint() string {
	if svc := c.host.ServiceConfig(ImmichServiceID); svc != nil {
		return svc.Endpoint
	}
	return ""
}

// --- live checks (fail-open: an unreadable surface marks the step unchecked) ---

func (c *ImmichConnector) tsCheckACLTag(ctx context.Context, cl *TailscaleAdminClient, st *types.OnboardingGuideStepState, tag string) {
	if st == nil {
		return
	}
	pol, _, err := cl.LoadPolicy(ctx)
	if err != nil {
		st.Detail = "could not read policy: " + err.Error()
		st.Fixable = true
		return
	}
	st.Checked = true
	st.Fixable = true
	owners := TagOwnersOf(pol, tag)
	if len(owners) > 0 {
		st.OK = true
		st.Detail = fmt.Sprintf("%s declared in tagOwners (%s)", tag, strings.Join(owners, ", "))
		return
	}
	st.OK = false
	st.NeedsFix = true
	st.Detail = fmt.Sprintf("%s missing from tagOwners — create/tag/approve all reject undeclared tags", tag)
}

func (c *ImmichConnector) tsCheckCreateService(ctx context.Context, cl *TailscaleAdminClient, st *types.OnboardingGuideStepState) {
	if st == nil {
		return
	}
	path := "/tailnet/" + cl.TailnetPath() + "/services/" + ImmichVIPServiceName
	var svc TailscaleService
	code, err := cl.Do(ctx, http.MethodGet, path, nil, &svc)
	if err != nil {
		if code == http.StatusNotFound {
			st.Checked = true
			st.OK = false
			st.Fixable = true
			st.NeedsFix = true
			st.Detail = "VIP Service not created yet"
			return
		}
		st.Detail = "could not read service: " + err.Error()
		return
	}
	st.Checked = true
	st.OK = true
	st.Fixable = true
	st.Detail = fmt.Sprintf("service exists (ports %s)", strings.Join(svc.Ports, ", "))
}

func (c *ImmichConnector) tsCheckTagHost(ctx context.Context, cl *TailscaleAdminClient, devices []TailscaleDevice, st *types.OnboardingGuideStepState, hostID, tag string) {
	if st == nil {
		return
	}
	if hostID == "" {
		st.Fixable = true
		st.Detail = "pick the Immich host device, then check again"
		return
	}
	st.Checked = true
	st.Fixable = true
	for _, d := range devices {
		if d.ID != hostID {
			continue
		}
		for _, t := range d.Tags {
			if t == tag {
				st.OK = true
				st.Detail = fmt.Sprintf("device %s already tagged %s", d.Label(), tag)
				return
			}
		}
		st.OK = false
		st.NeedsFix = true
		st.Detail = fmt.Sprintf("device %s is missing %s", d.Label(), tag)
		return
	}
	st.OK = false
	st.NeedsFix = true
	st.Detail = "host device not found in tailnet device list"
}

func (c *ImmichConnector) tsCheckApprove(ctx context.Context, cl *TailscaleAdminClient, st *types.OnboardingGuideStepState, hostID string) {
	if st == nil {
		return
	}
	if hostID == "" {
		st.Fixable = true
		st.Detail = "pick the Immich host device, then check again"
		return
	}
	path := "/tailnet/" + cl.TailnetPath() + "/services/" + ImmichVIPServiceName +
		"/device/" + hostID + "/approved"
	var body struct {
		Approved bool `json:"approved"`
	}
	code, err := cl.Do(ctx, http.MethodGet, path, nil, &body)
	if err != nil {
		if code == http.StatusNotFound {
			st.Checked = true
			st.OK = false
			st.Fixable = true
			st.NeedsFix = true
			st.Detail = "host not registered for the service yet (run serve on the host first, or create the service)"
			return
		}
		st.Detail = "could not read approval: " + err.Error()
		return
	}
	st.Checked = true
	st.Fixable = true
	if body.Approved {
		st.OK = true
		st.Detail = "host approved for " + ImmichVIPServiceName
		return
	}
	st.OK = false
	st.NeedsFix = true
	st.Detail = "host advertised but not approved"
}

// tsCheckServe probes whether the VIP name (or configured endpoint) is
// actually serving Immich. The mapping is host-local, but a live
// /api/server/ping is the acceptance signal that tailscale serve is up.
func (c *ImmichConnector) tsCheckServe(ctx context.Context, st *types.OnboardingGuideStepState) {
	if st == nil {
		return
	}
	st.Checked = true
	st.Fixable = false // still host-local; we only observe the result
	suffix := TailnetDNSFromBaseURL(c.host.BaseURL())
	candidates := []string{}
	if ep := strings.TrimSpace(c.immichConfiguredEndpoint()); ep != "" {
		candidates = append(candidates, ep)
	}
	if suffix != "" {
		candidates = append(candidates, ImmichSuggestedEndpoint(suffix))
	}
	if len(candidates) == 0 {
		st.OK = false
		st.Detail = "no VIP/Immich endpoint to probe — run tailscale serve on the Immich host"
		return
	}
	var lastErr string
	for _, ep := range candidates {
		base, ok := NormalizeImmichEndpoint(ep)
		if !ok || base == "" {
			base = strings.TrimRight(ep, "/")
		}
		ping := strings.TrimRight(base, "/") + "/server/ping"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ping, nil)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		resp, err := immichHTTPClient.Do(req)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK &&
			(strings.Contains(strings.ToLower(string(body)), "pong") ||
				strings.Contains(strings.ToLower(string(body)), "immich") ||
				len(strings.TrimSpace(string(body))) > 0) {
			st.OK = true
			st.Detail = fmt.Sprintf("reachable at %s (HTTP %d)", ping, resp.StatusCode)
			return
		}
		lastErr = fmt.Sprintf("%s: HTTP %d", ping, resp.StatusCode)
	}
	st.OK = false
	st.Detail = "VIP/Immich not answering yet — run tailscale serve on the Immich host. " + lastErr
}

func (c *ImmichConnector) tsCheckConnect(st *types.OnboardingGuideStepState) {
	if st == nil {
		return
	}
	st.Checked = true
	st.Fixable = true
	ep := strings.TrimSpace(c.immichConfiguredEndpoint())
	suffix := TailnetDNSFromBaseURL(c.host.BaseURL())
	want := ImmichSuggestedEndpoint(suffix)
	if ep != "" && strings.Contains(strings.ToLower(ep), "immich") &&
		(strings.HasSuffix(strings.TrimRight(ep, "/"), "/api") || strings.Contains(ep, ".ts.net")) {
		st.OK = true
		st.Detail = "Aegis endpoint: " + ep
		return
	}
	if ep != "" && mustNormalize(ep) != "" && strings.Contains(ep, ":2283") {
		st.OK = false
		st.NeedsFix = true
		st.Detail = "endpoint still uses host:port — switch to the VIP name when ready (" + want + ")"
		return
	}
	if ep == "" {
		st.OK = false
		st.NeedsFix = true
		st.Detail = "no Immich endpoint saved yet — suggested: " + want
		return
	}
	st.OK = true
	st.Detail = "Aegis endpoint: " + ep
}

// --- auto-fixes. Only the tailnet_apply verb reaches these. ---

// tsFixCreateService creates the VIP Service record.
func (c *ImmichConnector) tsFixCreateService(ctx context.Context, cl *TailscaleAdminClient, tag string) error {
	body := TailscaleService{
		Name:        ImmichVIPServiceName,
		DisplayName: "Immich",
		Ports:       []string{"tcp:443"},
		Tags:        []string{tag},
	}
	path := "/tailnet/" + cl.TailnetPath() + "/services/" + ImmichVIPServiceName
	_, err := cl.Do(ctx, http.MethodPut, path, body, nil)
	return err
}

// tsFixTagHost adds the service-host tag to a device, preserving its tags.
func (c *ImmichConnector) tsFixTagHost(ctx context.Context, cl *TailscaleAdminClient, hostID, tag string) error {
	if strings.TrimSpace(hostID) == "" {
		return fmt.Errorf("host device id is required to tag a device")
	}
	// Preserve existing tags; add ours. POST replaces the device tag set.
	devices, err := cl.ListDevices(ctx)
	if err != nil {
		return err
	}
	found := false
	var tags []string
	for _, d := range devices {
		if d.ID != hostID {
			continue
		}
		found = true
		tags = append(tags, d.Tags...)
		break
	}
	if !found {
		return fmt.Errorf("device %s not found", hostID)
	}
	has := false
	for _, t := range tags {
		if t == tag {
			has = true
			break
		}
	}
	if !has {
		tags = append(tags, tag)
	}
	if tags == nil {
		tags = []string{tag}
	}
	_, err = cl.Do(ctx, http.MethodPost, "/device/"+hostID+"/tags",
		map[string]any{"tags": tags}, nil)
	return err
}

// tsFixApprove approves a service host.
func (c *ImmichConnector) tsFixApprove(ctx context.Context, cl *TailscaleAdminClient, hostID string) error {
	if strings.TrimSpace(hostID) == "" {
		return fmt.Errorf("host device id is required to approve a service host")
	}
	path := "/tailnet/" + cl.TailnetPath() + "/services/" + ImmichVIPServiceName +
		"/device/" + hostID + "/approved"
	_, err := cl.Do(ctx, http.MethodPost, path, map[string]any{"approved": true}, nil)
	return err
}

// tsFixConnect is the Aegis-side half of the wizard: it returns the VIP
// endpoint as a connection save so core — never this connector — writes it.
// The existing API key and canonical claim are preserved.
func (c *ImmichConnector) tsFixConnect() (types.ConnectionUpdate, error) {
	suffix := TailnetDNSFromBaseURL(c.host.BaseURL())
	if suffix == "" {
		return types.ConnectionUpdate{}, fmt.Errorf("cannot derive tailnet DNS name from -base-url")
	}
	update := types.ConnectionUpdate{
		Endpoint:      ImmichSuggestedEndpoint(suffix),
		SecretHeaders: map[string]string{},
	}
	// Keep any existing API key / canonical claim.
	if svc := c.host.ServiceConfig(ImmichServiceID); svc != nil {
		for k, v := range svc.Headers {
			if strings.EqualFold(k, "X-API-Key") && strings.TrimSpace(v) != "" {
				update.SecretHeaders["X-API-Key"] = v
			}
		}
		for _, k := range svc.CanonicalFor {
			if k == "photos" {
				update.CanonicalFor = []string{"photos"}
				break
			}
		}
	}
	return update, nil
}

// immichTailnetApply fixes only the requested steps that currently need work.
// Already-OK steps are skipped; unknown IDs fail that step only. Order
// matters: the tag must be declared before create/tag/approve, because
// Tailscale rejects undeclared tags on all three.
func (c *ImmichConnector) immichTailnetApply(ctx context.Context, stepIDs []string, hostDeviceID, hostTag string) (ImmichTailnetApplyResult, []types.StateUpdate) {
	res := ImmichTailnetApplyResult{}
	var updates []types.StateUpdate

	cl := c.immichTailnetClient()
	if cl == nil {
		res.Failed = append(res.Failed, stepIDs...)
		res.Details = append(res.Details, "Tailscale API key not configured")
		return res, nil
	}

	// Persist host selection so subsequent checks/fixes use it.
	tailnet, savedHost, savedTag := c.immichTailnetSettings()
	if hostDeviceID == "" {
		hostDeviceID = savedHost
	}
	if hostTag == "" {
		hostTag = savedTag
	}
	if hostDeviceID != savedHost || hostTag != savedTag {
		updates = append(updates, types.StateUpdate{TailscaleAdmin: &types.TailscaleAdminUpdate{
			HostDeviceID: hostDeviceID,
			HostTag:      hostTag,
			Tailnet:      tailnet,
		}})
	}

	live := c.immichTailnetState(ctx)
	byID := map[string]types.OnboardingGuideStepState{}
	for _, st := range live.Steps {
		byID[st.ID] = st
	}

	want := map[string]bool{}
	for _, id := range stepIDs {
		want[strings.TrimSpace(id)] = true
	}
	if len(want) == 0 {
		res.Details = append(res.Details, "no steps requested")
		return res, updates
	}

	// create/tag/approve all reject undeclared tags — ensure tagOwners first
	// when those steps are requested, even if acl_tag was not named.
	if want["create_service"] || want["tag_host"] || want["approve"] {
		if st, ok := byID["acl_tag"]; ok && st.Checked && !st.OK {
			if err := cl.DeclareTag(ctx, hostTag); err != nil {
				res.Details = append(res.Details, "acl_tag (prerequisite): "+err.Error())
			} else {
				res.Details = append(res.Details, "acl_tag (prerequisite): fixed")
			}
		}
	}

	order := []string{"acl_tag", "create_service", "tag_host", "approve", "connect", "serve"}
	for _, id := range order {
		if !want[id] {
			continue
		}
		st, known := byID[id]
		if !known {
			res.Failed = append(res.Failed, id)
			res.Details = append(res.Details, "unknown step "+id)
			continue
		}
		if st.OK {
			res.Skipped = append(res.Skipped, id)
			res.Details = append(res.Details, id+": already OK")
			continue
		}
		if !st.Fixable {
			res.Skipped = append(res.Skipped, id)
			res.Details = append(res.Details, id+": not auto-fixable — use the copyable command")
			continue
		}
		var err error
		switch id {
		case "acl_tag":
			err = cl.DeclareTag(ctx, hostTag)
		case "create_service":
			err = c.tsFixCreateService(ctx, cl, hostTag)
		case "tag_host":
			err = c.tsFixTagHost(ctx, cl, hostDeviceID, hostTag)
		case "approve":
			err = c.tsFixApprove(ctx, cl, hostDeviceID)
		case "connect":
			var up types.ConnectionUpdate
			up, err = c.tsFixConnect()
			if err == nil {
				updates = append(updates, types.StateUpdate{Connection: &up})
			}
		default:
			err = fmt.Errorf("step %s is not auto-fixable", id)
		}
		if err != nil {
			res.Failed = append(res.Failed, id)
			res.Details = append(res.Details, id+": "+err.Error())
			continue
		}
		res.Applied = append(res.Applied, id)
		res.Details = append(res.Details, id+": fixed")
	}
	return res, updates
}
