package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// Tailscale Admin API client for Immich VIP Service setup (issue #124).
// Aegis holds an operator API key in service_connections.json and uses it to
// check each walkthrough step and fix only the ones that still need work.
// Never mutates tailnet topology from the pre-request hook — only from
// explicit admin apply calls.

// tailscaleAdminAPIBase is the public Admin API root. Tests override this
// to point at httptest.Server.
var tailscaleAdminAPIBase = "https://api.tailscale.com/api/v2"

// Default service-host tag for Immich VIP hosts.
const immichDefaultHostTag = "tag:immich-host"

// Immich VIP service name (must match ImmichTailnetGuide).
const immichVIPServiceName = "svc:immich"

// tailscaleAdminClient talks to api.tailscale.com with an operator access
// token. Auth is HTTP Basic (key as username, empty password) for tskey-api
// tokens; Bearer is used for OAuth-style access tokens.
type tailscaleAdminClient struct {
	base    string
	apiKey  string
	tailnet string
	http    *http.Client
}

func newTailscaleAdminClient(apiKey, tailnet string) *tailscaleAdminClient {
	t := strings.TrimSpace(tailnet)
	if t == "" {
		t = "-"
	}
	return &tailscaleAdminClient{
		base:    tailscaleAdminAPIBase,
		apiKey:  strings.TrimSpace(apiKey),
		tailnet: t,
		http:    aegisHTTPClient,
	}
}

func (c *tailscaleAdminClient) do(ctx context.Context, method, path string, body any, out any) (int, error) {
	code, data, err := c.doRaw(ctx, method, path, body)
	if err != nil {
		return code, err
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return code, err
		}
	}
	return code, nil
}

// doRaw performs the request and returns the raw body. The policy endpoint
// serves HuJSON (comments + trailing commas), so callers parse that themselves.
func (c *tailscaleAdminClient) doRaw(ctx context.Context, method, path string, body any) (int, []byte, error) {
	if c == nil || c.apiKey == "" {
		return 0, nil, fmt.Errorf("tailscale api key not configured")
	}
	var rdr io.Reader
	if body != nil {
		var raw []byte
		var err error
		switch b := body.(type) {
		case []byte:
			raw = b
		case string:
			raw = []byte(b)
		default:
			raw, err = json.Marshal(body)
			if err != nil {
				return 0, nil, err
			}
		}
		rdr = bytes.NewReader(raw)
	}
	url := c.base + path
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// tskey-api tokens use HTTP Basic with an empty password. OAuth-style
	// access tokens (no tskey- prefix) use Bearer.
	if strings.HasPrefix(c.apiKey, "tskey-") {
		req.SetBasicAuth(c.apiKey, "")
	} else {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	if resp.StatusCode >= 400 {
		return resp.StatusCode, data, fmt.Errorf("tailscale api %s %s: %s: %s",
			method, path, resp.Status, strings.TrimSpace(string(data)))
	}
	return resp.StatusCode, data, nil
}

// hujsonToJSON strips // and /* */ comments and trailing commas so the
// policy file (HuJSON) can be decoded with encoding/json. Enough for
// Tailscale ACL text; not a general HuJSON implementation.
func hujsonToJSON(src []byte) []byte {
	var out []byte
	out = make([]byte, 0, len(src))
	inStr := false
	esc := false
	for i := 0; i < len(src); i++ {
		ch := src[i]
		if inStr {
			out = append(out, ch)
			if esc {
				esc = false
				continue
			}
			if ch == '\\' {
				esc = true
				continue
			}
			if ch == '"' {
				inStr = false
			}
			continue
		}
		if ch == '"' {
			inStr = true
			out = append(out, ch)
			continue
		}
		// line comment
		if ch == '/' && i+1 < len(src) && src[i+1] == '/' {
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
			continue
		}
		// block comment
		if ch == '/' && i+1 < len(src) && src[i+1] == '*' {
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // skip '/'
			continue
		}
		// trailing comma before } or ]
		if ch == ',' {
			j := i + 1
			for j < len(src) {
				c := src[j]
				if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
					j++
					continue
				}
				// skip comments after the comma
				if c == '/' && j+1 < len(src) && src[j+1] == '/' {
					for j < len(src) && src[j] != '\n' {
						j++
					}
					continue
				}
				if c == '/' && j+1 < len(src) && src[j+1] == '*' {
					j += 2
					for j+1 < len(src) && !(src[j] == '*' && src[j+1] == '/') {
						j++
					}
					j++
					continue
				}
				break
			}
			if j < len(src) && (src[j] == '}' || src[j] == ']') {
				continue // drop trailing comma
			}
		}
		out = append(out, ch)
	}
	return out
}

func (c *tailscaleAdminClient) tailnetPath() string {
	return c.tailnet
}

// --- API payload shapes (subset of api.tailscale.com /api/v2) ---

type tailscaleService struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName,omitempty"`
	Ports       []string `json:"ports,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

type tailscaleDevice struct {
	ID       string   `json:"id"`
	Name     string   `json:"name,omitempty"`
	Hostname string   `json:"hostname,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// Policy is a partial view of the tailnet policy file (tagOwners only).
type tailscalePolicy struct {
	TagOwners map[string][]string `json:"tagOwners"`
}

// --- step status / apply (wired from Immich setup) ---

// ImmichTailnetStepStatus is one live-checked VIP setup step (issue #124).
type ImmichTailnetStepStatus struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	OK      bool   `json:"ok"`
	Checked bool   `json:"checked"`
	// Fixable is true when Aegis can repair this step via the Admin API.
	Fixable bool `json:"fixable"`
	// NeedsFix is true when checked && !ok && fixable.
	NeedsFix bool `json:"needs_fix"`
	Detail   string `json:"detail,omitempty"`
	// Command remains the copyable fallback (also used when !fixable).
	Command string `json:"command,omitempty"`
}

// ImmichTailnetLiveStatus is the live check result embedded in setup status.
type ImmichTailnetLiveStatus struct {
	HasAPIKey bool                       `json:"has_api_key"`
	Checked   bool                       `json:"checked"`
	Error     string                     `json:"error,omitempty"`
	HostTag   string                     `json:"host_tag,omitempty"`
	HostDeviceID string                  `json:"host_device_id,omitempty"`
	Steps     []ImmichTailnetStepStatus  `json:"steps,omitempty"`
	Devices   []ImmichTailnetDevice      `json:"devices,omitempty"`
}

// ImmichTailnetDevice is a host-candidate device for the admin UI picker.
type ImmichTailnetDevice struct {
	ID       string   `json:"id"`
	Hostname string   `json:"hostname,omitempty"`
	Name     string   `json:"name,omitempty"`
	Tags     []string `json:"tags,omitempty"`
}

// ImmichTailnetConfigRequest is POST /v1/admin/immich/tailnet/config.
type ImmichTailnetConfigRequest struct {
	APIKey       string `json:"api_key,omitempty"`
	Tailnet      string `json:"tailnet,omitempty"`
	HostDeviceID string `json:"host_device_id,omitempty"`
	HostTag      string `json:"host_tag,omitempty"`
}

// ImmichTailnetApplyRequest is POST /v1/admin/immich/tailnet/apply.
// Only named steps that currently need fixing are attempted.
type ImmichTailnetApplyRequest struct {
	Steps        []string `json:"steps"`
	HostDeviceID string   `json:"host_device_id,omitempty"`
	HostTag      string   `json:"host_tag,omitempty"`
}

// ImmichTailnetApplyResult reports what was fixed vs skipped.
type ImmichTailnetApplyResult struct {
	Applied []string `json:"applied,omitempty"`
	Skipped []string `json:"skipped,omitempty"`
	Failed  []string `json:"failed,omitempty"`
	Details []string `json:"details,omitempty"`
	Live    *ImmichTailnetLiveStatus `json:"live,omitempty"`
}

func (s *Server) tailscaleAdminClient() *tailscaleAdminClient {
	snap := s.config.TailscaleAdminSnapshot()
	if !snap.HasAPIKey {
		return nil
	}
	return newTailscaleAdminClient(s.config.TailscaleAdminAPIKey(), snap.Tailnet)
}

func (s *Server) immichHostTag() string {
	snap := s.config.TailscaleAdminSnapshot()
	tag := strings.TrimSpace(snap.HostTag)
	if tag == "" {
		return immichDefaultHostTag
	}
	return tag
}

func (s *Server) immichHostDeviceID() string {
	return strings.TrimSpace(s.config.TailscaleAdminSnapshot().HostDeviceID)
}

// checkImmichTailnetSteps probes the Admin API for each walkthrough step.
// Fail-open: API errors surface as Error and un-checked steps, never panic.
func (s *Server) checkImmichTailnetSteps(ctx context.Context) *ImmichTailnetLiveStatus {
	snap := s.config.TailscaleAdminSnapshot()
	live := &ImmichTailnetLiveStatus{
		HasAPIKey:    snap.HasAPIKey,
		HostTag:      s.immichHostTag(),
		HostDeviceID: s.immichHostDeviceID(),
	}
	guide := immichTailnetGuide(s.baseURL, s.immichConfiguredEndpoint())
	if guide != nil {
		for _, st := range guide.Steps {
			live.Steps = append(live.Steps, ImmichTailnetStepStatus{
				ID:      st.ID,
				Title:   st.Title,
				Command: st.Command,
			})
		}
	}
	if !snap.HasAPIKey {
		live.Error = "Tailscale API key not configured — paste one to enable live checks and one-click fixes."
		return live
	}
	c := s.tailscaleAdminClient()
	if c == nil {
		live.Error = "Tailscale API key not configured"
		return live
	}

	// Discover host candidates once; used by tag_host / approve checks.
	devices, devErr := s.tsListDevices(ctx, c)
	if devErr == nil {
		for _, d := range devices {
			live.Devices = append(live.Devices, ImmichTailnetDevice{
				ID:       d.ID,
				Hostname: d.Hostname,
				Name:     d.Name,
				Tags:     d.Tags,
			})
		}
	}

	hostID := live.HostDeviceID
	if hostID == "" {
		hostID = pickImmichHostDevice(devices)
		if hostID != "" {
			live.HostDeviceID = hostID
		}
	}

	byID := map[string]*ImmichTailnetStepStatus{}
	for i := range live.Steps {
		byID[live.Steps[i].ID] = &live.Steps[i]
	}

	// 1) acl_tag — tag present in policy tagOwners. Must be first: create
	// and tag both reject undeclared tags.
	s.tsCheckACLTag(ctx, c, byID["acl_tag"], live.HostTag)
	// 2) create_service
	s.tsCheckCreateService(ctx, c, byID["create_service"], live.HostTag)
	// 3) tag_host
	s.tsCheckTagHost(ctx, c, devices, byID["tag_host"], hostID, live.HostTag)
	// 4) serve — probe the VIP name (or configured Immich endpoint) so a
	// working host-local tailscale serve turns this green without SSH.
	s.tsCheckServe(ctx, byID["serve"])
	// 5) approve
	s.tsCheckApprove(ctx, c, byID["approve"], hostID)
	// 6) connect — Aegis-side endpoint.
	s.tsCheckConnect(byID["connect"])

	live.Checked = true
	if devErr != nil {
		live.Error = "device list: " + devErr.Error()
	}
	return live
}

func (s *Server) immichConfiguredEndpoint() string {
	if svc := s.config.GetService("immich"); svc != nil {
		return svc.Endpoint
	}
	return ""
}

func pickImmichHostDevice(devices []tailscaleDevice) string {
	// Prefer an already-tagged service host; otherwise leave empty so the
	// operator can pick from the device list.
	for _, d := range devices {
		for _, t := range d.Tags {
			if t == immichDefaultHostTag || strings.HasPrefix(t, "tag:immich") {
				return d.ID
			}
		}
	}
	return ""
}

// loadTailscalePolicy reads the tailnet policy as a generic JSON object.
// GET /acl returns HuJSON; we strip comments and decode with encoding/json.
func (c *tailscaleAdminClient) loadPolicy(ctx context.Context) (map[string]any, []byte, error) {
	path := "/tailnet/" + c.tailnetPath() + "/acl"
	_, raw, err := c.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, nil, err
	}
	normalized := hujsonToJSON(raw)
	var pol map[string]any
	if err := json.Unmarshal(normalized, &pol); err != nil {
		return nil, nil, fmt.Errorf("parse policy: %w", err)
	}
	return pol, normalized, nil
}

func (s *Server) tsCheckACLTag(ctx context.Context, c *tailscaleAdminClient, st *ImmichTailnetStepStatus, tag string) {
	if st == nil {
		return
	}
	pol, _, err := c.loadPolicy(ctx)
	if err != nil {
		st.Checked = false
		st.Fixable = true
		st.Detail = "could not read policy: " + err.Error()
		return
	}
	st.Checked = true
	st.Fixable = true
	owners := tagOwnersOf(pol, tag)
	if len(owners) > 0 {
		st.OK = true
		st.Detail = fmt.Sprintf("%s declared in tagOwners (%s)", tag, strings.Join(owners, ", "))
		return
	}
	st.OK = false
	st.NeedsFix = true
	st.Detail = fmt.Sprintf("%s missing from tagOwners — create/tag/approve all reject undeclared tags", tag)
}

func tagOwnersOf(pol map[string]any, tag string) []string {
	raw, ok := pol["tagOwners"]
	if !ok {
		return nil
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return nil
	}
	list, ok := m[tag]
	if !ok {
		return nil
	}
	var out []string
	switch v := list.(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case []string:
		out = append(out, v...)
	}
	return out
}

// tsFixACLTag adds tag → [autogroup:admin] to tagOwners (preserves the rest
// of the policy). This unblocks create_service / tag_host / approve.
func (s *Server) tsFixACLTag(ctx context.Context, c *tailscaleAdminClient, tag string) error {
	pol, _, err := c.loadPolicy(ctx)
	if err != nil {
		return err
	}
	if len(tagOwnersOf(pol, tag)) > 0 {
		return nil
	}
	owners, _ := pol["tagOwners"].(map[string]any)
	if owners == nil {
		owners = map[string]any{}
	}
	owners[tag] = []string{"autogroup:admin"}
	pol["tagOwners"] = owners
	body, err := json.Marshal(pol)
	if err != nil {
		return err
	}
	path := "/tailnet/" + c.tailnetPath() + "/acl"
	_, _, err = c.doRaw(ctx, http.MethodPost, path, body)
	return err
}

func (s *Server) tsCheckCreateService(ctx context.Context, c *tailscaleAdminClient, st *ImmichTailnetStepStatus, tag string) {
	if st == nil {
		return
	}
	path := "/tailnet/" + c.tailnetPath() + "/services/" + immichVIPServiceName
	var svc tailscaleService
	code, err := c.do(ctx, http.MethodGet, path, nil, &svc)
	if err != nil {
		if code == http.StatusNotFound {
			st.Checked = true
			st.OK = false
			st.Fixable = true
			st.NeedsFix = true
			st.Detail = "VIP Service not created yet"
			return
		}
		st.Checked = false
		st.Detail = "could not read service: " + err.Error()
		return
	}
	st.Checked = true
	st.OK = true
	st.Fixable = true
	st.Detail = fmt.Sprintf("service exists (ports %s)", strings.Join(svc.Ports, ", "))
	_ = tag
}

func (s *Server) tsCheckTagHost(ctx context.Context, c *tailscaleAdminClient, devices []tailscaleDevice, st *ImmichTailnetStepStatus, hostID, tag string) {
	if st == nil {
		return
	}
	if hostID == "" {
		st.Checked = false
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
				st.Detail = fmt.Sprintf("device %s already tagged %s", hostLabel(d), tag)
				return
			}
		}
		st.OK = false
		st.NeedsFix = true
		st.Detail = fmt.Sprintf("device %s is missing %s", hostLabel(d), tag)
		return
	}
	st.OK = false
	st.NeedsFix = true
	st.Detail = "host device not found in tailnet device list"
}

func (s *Server) tsCheckApprove(ctx context.Context, c *tailscaleAdminClient, st *ImmichTailnetStepStatus, hostID string) {
	if st == nil {
		return
	}
	if hostID == "" {
		st.Checked = false
		st.Fixable = true
		st.Detail = "pick the Immich host device, then check again"
		return
	}
	path := "/tailnet/" + c.tailnetPath() + "/services/" + immichVIPServiceName +
		"/device/" + hostID + "/approved"
	var body struct {
		Approved bool `json:"approved"`
	}
	code, err := c.do(ctx, http.MethodGet, path, nil, &body)
	if err != nil {
		if code == http.StatusNotFound {
			st.Checked = true
			st.OK = false
			st.Fixable = true
			st.NeedsFix = true
			st.Detail = "host not registered for the service yet (run serve on the host first, or create the service)"
			return
		}
		st.Checked = false
		st.Detail = "could not read approval: " + err.Error()
		return
	}
	st.Checked = true
	st.Fixable = true
	if body.Approved {
		st.OK = true
		st.Detail = "host approved for " + immichVIPServiceName
		return
	}
	st.OK = false
	st.NeedsFix = true
	st.Detail = "host advertised but not approved"
}

// tsCheckServe probes whether the VIP name (or configured endpoint) is
// actually serving Immich. The mapping is host-local, but a live /api/server/ping
// is the acceptance signal that tailscale serve is up and approved.
func (s *Server) tsCheckServe(ctx context.Context, st *ImmichTailnetStepStatus) {
	if st == nil {
		return
	}
	st.Checked = true
	st.Fixable = false // still host-local; we only observe the result
	suffix := tailnetDNSFromBaseURL(s.baseURL)
	candidates := []string{}
	if ep := strings.TrimSpace(s.immichConfiguredEndpoint()); ep != "" {
		candidates = append(candidates, ep)
	}
	if suffix != "" {
		candidates = append(candidates, immichSuggestedEndpoint(suffix))
	}
	if len(candidates) == 0 {
		st.OK = false
		st.Detail = "no VIP/Immich endpoint to probe — run tailscale serve on the Immich host"
		return
	}
	var lastErr string
	for _, ep := range candidates {
		base, ok := normalizeImmichEndpoint(ep)
		if !ok || base == "" {
			base = strings.TrimRight(ep, "/")
		}
		ping := strings.TrimRight(base, "/") + "/server/ping"
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ping, nil)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		resp, err := aegisHTTPClient.Do(req)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK &&
			(strings.Contains(strings.ToLower(string(body)), "pong") ||
				strings.Contains(strings.ToLower(string(body)), "immich") ||
				len(bytes.TrimSpace(body)) > 0) {
			st.OK = true
			st.Detail = fmt.Sprintf("reachable at %s (HTTP %d)", ping, resp.StatusCode)
			return
		}
		lastErr = fmt.Sprintf("%s: HTTP %d", ping, resp.StatusCode)
	}
	st.OK = false
	st.Detail = "VIP/Immich not answering yet — run tailscale serve on the Immich host. " + lastErr
}

func (s *Server) tsCheckConnect(st *ImmichTailnetStepStatus) {
	if st == nil {
		return
	}
	st.Checked = true
	st.Fixable = true
	ep := strings.TrimSpace(s.immichConfiguredEndpoint())
	suffix := tailnetDNSFromBaseURL(s.baseURL)
	want := immichSuggestedEndpoint(suffix)
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

func hostLabel(d tailscaleDevice) string {
	if d.Hostname != "" {
		return d.Hostname
	}
	if d.Name != "" {
		return d.Name
	}
	return d.ID
}

func (s *Server) tsListDevices(ctx context.Context, c *tailscaleAdminClient) ([]tailscaleDevice, error) {
	path := "/tailnet/" + c.tailnetPath() + "/devices"
	var body struct {
		Devices []tailscaleDevice `json:"devices"`
	}
	if _, err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return nil, err
	}
	return body.Devices, nil
}

// applyImmichTailnetSteps fixes only the requested steps that currently need
// work. Already-OK steps are skipped; unknown IDs fail that step only.
func (s *Server) applyImmichTailnetSteps(ctx context.Context, req ImmichTailnetApplyRequest) ImmichTailnetApplyResult {
	res := ImmichTailnetApplyResult{}
	c := s.tailscaleAdminClient()
	if c == nil {
		res.Failed = req.Steps
		res.Details = append(res.Details, "Tailscale API key not configured")
		live := s.checkImmichTailnetSteps(ctx)
		res.Live = live
		return res
	}

	// Persist host selection so subsequent checks/fixes use it.
	cfgReq := tailscaleAdminEntry{
		HostDeviceID: strings.TrimSpace(req.HostDeviceID),
		HostTag:      strings.TrimSpace(req.HostTag),
	}
	if cfgReq.HostDeviceID != "" || cfgReq.HostTag != "" {
		if err := s.config.SetTailscaleAdmin(cfgReq); err != nil {
			slog.Warn("tailscale admin host selection not saved", "error", err)
		}
	}

	live := s.checkImmichTailnetSteps(ctx)
	byID := map[string]ImmichTailnetStepStatus{}
	for _, st := range live.Steps {
		byID[st.ID] = st
	}

	tag := live.HostTag
	if tag == "" {
		tag = immichDefaultHostTag
	}
	hostID := strings.TrimSpace(req.HostDeviceID)
	if hostID == "" {
		hostID = live.HostDeviceID
	}

	want := map[string]bool{}
	for _, id := range req.Steps {
		want[strings.TrimSpace(id)] = true
	}
	if len(want) == 0 {
		res.Details = append(res.Details, "no steps requested")
		res.Live = live
		return res
	}

	// create/tag/approve all reject undeclared tags — ensure tagOwners first
	// when those steps are requested, even if acl_tag was not named.
	if want["create_service"] || want["tag_host"] || want["approve"] {
		if st, ok := byID["acl_tag"]; ok && st.Checked && !st.OK {
			if err := s.tsFixACLTag(ctx, c, tag); err != nil {
				res.Details = append(res.Details, "acl_tag (prerequisite): "+err.Error())
			} else {
				res.Details = append(res.Details, "acl_tag (prerequisite): fixed")
				if _, ok := want["acl_tag"]; !ok {
					// Not an explicit request; don't count it as applied.
					delete(want, "acl_tag")
				}
			}
		}
	}

	// Order matters: declare the tag before create/tag/approve (Tailscale
	// rejects undeclared tags on all three).
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
			err = s.tsFixACLTag(ctx, c, tag)
		case "create_service":
			err = s.tsFixCreateService(ctx, c, tag)
		case "tag_host":
			err = s.tsFixTagHost(ctx, c, hostID, tag)
		case "approve":
			err = s.tsFixApprove(ctx, c, hostID)
		case "connect":
			err = s.tsFixConnect(tag)
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

	res.Live = s.checkImmichTailnetSteps(ctx)
	return res
}

func (s *Server) tsFixCreateService(ctx context.Context, c *tailscaleAdminClient, tag string) error {
	body := tailscaleService{
		Name:        immichVIPServiceName,
		DisplayName: "Immich",
		Ports:       []string{"tcp:443"},
		Tags:        []string{tag},
	}
	path := "/tailnet/" + c.tailnetPath() + "/services/" + immichVIPServiceName
	_, err := c.do(ctx, http.MethodPut, path, body, nil)
	return err
}

func (s *Server) tsFixTagHost(ctx context.Context, c *tailscaleAdminClient, hostID, tag string) error {
	if strings.TrimSpace(hostID) == "" {
		return fmt.Errorf("host device id is required to tag a device")
	}
	// Preserve existing tags; add ours.
	path := "/tailnet/" + c.tailnetPath() + "/devices"
	var body struct {
		Devices []tailscaleDevice `json:"devices"`
	}
	if _, err := c.do(ctx, http.MethodGet, path, nil, &body); err != nil {
		return err
	}
	found := false
	var tags []string
	for _, d := range body.Devices {
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
	// POST replaces the device tag set.
	_, err := c.do(ctx, http.MethodPost, "/device/"+hostID+"/tags",
		map[string]any{"tags": tags}, nil)
	return err
}

func (s *Server) tsFixApprove(ctx context.Context, c *tailscaleAdminClient, hostID string) error {
	if strings.TrimSpace(hostID) == "" {
		return fmt.Errorf("host device id is required to approve a service host")
	}
	path := "/tailnet/" + c.tailnetPath() + "/services/" + immichVIPServiceName +
		"/device/" + hostID + "/approved"
	_, err := c.do(ctx, http.MethodPost, path, map[string]any{"approved": true}, nil)
	return err
}

// tsFixConnect saves the suggested VIP endpoint into the Immich connection
// without clobbering an existing API key.
func (s *Server) tsFixConnect(tag string) error {
	suffix := tailnetDNSFromBaseURL(s.baseURL)
	ep := immichSuggestedEndpoint(suffix)
	if suffix == "" {
		return fmt.Errorf("cannot derive tailnet DNS name from -base-url")
	}
	entry := serviceConnectionEntry{
		Name:        "Immich",
		Action:      "inject",
		Endpoint:    ep,
		CacheTTL:    "5m",
		Description: "Self-hosted Immich photo library. Photo bytes stay on Immich (foreign/local canonical).",
		Aliases: []string{
			"immich photos", "self-hosted photos", "immich",
			"photo", "photos", "picture", "pictures", "photo library",
		},
	}
	// Keep any existing API key / canonical claim.
	if svc := s.config.GetService("immich"); svc != nil {
		if key := headerValue(svc.Headers, "X-API-Key"); key != "" {
			entry.Headers = map[string]string{"X-API-Key": key}
		}
		for _, k := range svc.CanonicalFor {
			if k == "photos" {
				entry.CanonicalFor = []string{"photos"}
				break
			}
		}
	}
	_ = tag
	if err := s.config.SetServiceConnection("immich", entry); err != nil {
		return err
	}
	if s.cache != nil {
		s.cache.Clear()
	}
	return nil
}

// handleImmichTailnetConfig stores operator Tailscale Admin settings.
// POST /v1/admin/immich/tailnet/config
func (s *Server) handleImmichTailnetConfig(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req ImmichTailnetConfigRequest
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	entry := tailscaleAdminEntry{
		APIKey:       strings.TrimSpace(req.APIKey),
		Tailnet:      strings.TrimSpace(req.Tailnet),
		HostDeviceID: strings.TrimSpace(req.HostDeviceID),
		HostTag:      strings.TrimSpace(req.HostTag),
	}
	if entry.APIKey == "" && entry.Tailnet == "" && entry.HostDeviceID == "" && entry.HostTag == "" {
		writeJSONError(w, http.StatusBadRequest, "api_key, tailnet, host_device_id, or host_tag is required")
		return
	}
	if err := s.config.SetTailscaleAdmin(entry); err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	slogImmich().Info("tailscale admin settings saved",
		"key_set", entry.APIKey != "", "host_device", entry.HostDeviceID != "")

	// Return setup status including live checks when a key is present.
	s.writeImmichSetupStatus(w, r, true)
}

// handleImmichTailnetCheck re-runs live step checks.
// POST /v1/admin/immich/tailnet/check
func (s *Server) handleImmichTailnetCheck(w http.ResponseWriter, r *http.Request) {
	live := s.checkImmichTailnetSteps(r.Context())
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(live)
}

// handleImmichTailnetApply fixes only steps that need work.
// POST /v1/admin/immich/tailnet/apply
func (s *Server) handleImmichTailnetApply(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid body")
		return
	}
	var req ImmichTailnetApplyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(req.Steps) == 0 {
		writeJSONError(w, http.StatusBadRequest, "steps is required — e.g. [\"create_service\",\"tag_host\",\"approve\"]")
		return
	}
	res := s.applyImmichTailnetSteps(r.Context(), req)
	slogImmich().Info("tailscale vip apply",
		"applied", res.Applied, "skipped", res.Skipped, "failed", res.Failed,
		"details", res.Details)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// ensureImmichTailnet routes are registered under /v1/admin/immich/.
func dispatchImmichTailnetSubroute(s *Server, w http.ResponseWriter, r *http.Request, path string) bool {
	switch path {
	case "tailnet/config":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return true
		}
		s.handleImmichTailnetConfig(w, r)
	case "tailnet/check":
		if r.Method != http.MethodPost && r.Method != http.MethodGet {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return true
		}
		s.handleImmichTailnetCheck(w, r)
	case "tailnet/apply":
		if r.Method != http.MethodPost {
			writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
			return true
		}
		s.handleImmichTailnetApply(w, r)
	default:
		return false
	}
	return true
}
