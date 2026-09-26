package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// HookHealth keeps a small in-memory record of Aperture pre-request traffic so
// admin can tell whether the hook is actually wired, and whether that wiring
// still points at the Aegis-era names/URLs (issue: leftover "Guardian" config).
type HookHealth struct {
	mu sync.Mutex
	// observations is a ring of recent hook outcomes (no bodies, no secrets).
	observations []HookObservation
	start        int
	count        int
	capacity     int

	authFailures       int
	parseFailures      int
	staleHostHits      int
	unrecognizedShapes int
	lastAt             time.Time
	lastHost           string
	lastAction         string
}

// HookObservation is one redacted pre-request hook outcome.
type HookObservation struct {
	At             time.Time `json:"at"`
	Host           string    `json:"host,omitempty"`
	Action         string    `json:"action"` // allow, modify, block, auth_fail, parse_fail
	HasUserMessage bool      `json:"has_user_message"`
	HasRequestBody bool      `json:"has_request_body"`
	Model          string    `json:"model,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Login          string    `json:"login,omitempty"`
	// BodyShape is the LLM request wire shape used for instruction injection
	// (chat, anthropic, responses, unknown). Empty when no request_body.
	BodyShape string `json:"body_shape,omitempty"`
}

// HookIssue is one actionable hook-config problem.
type HookIssue struct {
	Severity string `json:"severity"` // error, warn, info
	Code     string `json:"code"`
	Message  string `json:"message"`
	Fix      string `json:"fix,omitempty"`
}

// HookHealthReport is the admin payload for GET /v1/admin/hook-health.
type HookHealthReport struct {
	Status             string            `json:"status"` // ok, warn, error, unknown
	ExpectedURL        string            `json:"expected_url"`
	ExpectedHookName   string            `json:"expected_hook_name"`
	HookPath           string            `json:"hook_path"`
	LastHookAt         *time.Time        `json:"last_hook_at,omitempty"`
	LastHost           string            `json:"last_host,omitempty"`
	LastAction         string            `json:"last_action,omitempty"`
	AuthFailures       int               `json:"auth_failures"`
	ParseFailures      int               `json:"parse_failures"`
	StaleHostHits      int               `json:"stale_host_hits"`
	UnrecognizedShapes int               `json:"unrecognized_shapes"`
	Recent             []HookObservation `json:"recent,omitempty"`
	Issues             []HookIssue       `json:"issues"`
	ExpectedHookConfig json.RawMessage   `json:"expected_hook_config,omitempty"`
}

// defaultHookObservationCapacity bounds the recent-hook ring buffer.
const defaultHookObservationCapacity = 64

// hookPath is the Aperture pre-request hook route.
const hookPath = "/v1/pre-request"

// expectedHookName is the hook key Aperture should use after the Aegis rename.
const expectedHookName = "aegis"

func NewHookHealth(capacity int) *HookHealth {
	if capacity <= 0 {
		capacity = defaultHookObservationCapacity
	}
	return &HookHealth{
		observations: make([]HookObservation, capacity),
		capacity:     capacity,
	}
}

// Record appends one hook outcome. Safe for concurrent use.
func (h *HookHealth) Record(obs HookObservation) {
	if h == nil {
		return
	}
	if obs.At.IsZero() {
		obs.At = time.Now()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.count < h.capacity {
		idx := (h.start + h.count) % h.capacity
		h.observations[idx] = obs
		h.count++
	} else {
		h.observations[h.start] = obs
		h.start = (h.start + 1) % h.capacity
	}
	h.lastAt = obs.At
	h.lastHost = obs.Host
	h.lastAction = obs.Action
	switch obs.Action {
	case "auth_fail":
		h.authFailures++
	case "parse_fail":
		h.parseFailures++
	}
	if obs.BodyShape == bodyShapeUnknown {
		h.unrecognizedShapes++
	}
	if obs.Host != "" && isStaleHookHost(obs.Host) {
		h.staleHostHits++
	}
}

// Snapshot builds the admin report, including config issues.
func (h *HookHealth) Snapshot(baseURL string) HookHealthReport {
	expected := expectedHookURL(baseURL)
	rep := HookHealthReport{
		Status:           "unknown",
		ExpectedURL:      expected,
		ExpectedHookName: expectedHookName,
		HookPath:         hookPath,
		Issues:           []HookIssue{},
	}
	if h == nil {
		rep.Issues = append(rep.Issues, hookIssueError("hook_health_missing",
			"hook health tracker is not installed",
			"upgrade/restart Aegis"))
		rep.Status = "error"
		return rep
	}

	h.mu.Lock()
	rep.AuthFailures = h.authFailures
	rep.ParseFailures = h.parseFailures
	rep.StaleHostHits = h.staleHostHits
	rep.UnrecognizedShapes = h.unrecognizedShapes
	rep.LastHost = h.lastHost
	rep.LastAction = h.lastAction
	if !h.lastAt.IsZero() {
		t := h.lastAt
		rep.LastHookAt = &t
	}
	n := h.count
	if n > 8 {
		n = 8
	}
	for i := 0; i < n; i++ {
		idx := (h.start + h.count - 1 - i + h.capacity*2) % h.capacity
		rep.Recent = append(rep.Recent, h.observations[idx])
	}
	h.mu.Unlock()

	// Live-traffic issues.
	if rep.LastHookAt == nil {
		rep.Issues = append(rep.Issues, hookIssueWarn("no_hook_traffic",
			"no Aperture pre-request calls have been observed since startup",
			"confirm the Aperture hooks entry url is "+expected+" and grants send_hooks includes name \""+expectedHookName+"\""))
	} else {
		if rep.StaleHostHits > 0 {
			rep.Issues = append(rep.Issues, hookIssueError("stale_hook_host",
				fmt.Sprintf("%d hook call(s) arrived on a legacy/stale Host (last %q)", rep.StaleHostHits, rep.LastHost),
				"update the Aperture hook url to "+expected+" (do not keep guardian.* hosts)"))
		}
		if !hostMatchesBase(rep.LastHost, baseURL) && rep.LastHost != "" {
			rep.Issues = append(rep.Issues, hookIssueWarn("host_mismatch",
				fmt.Sprintf("last hook Host %q does not match configured base URL %q", rep.LastHost, baseURL),
				"point Aperture at "+expected+" or fix -base-url"))
		}
		if rep.AuthFailures > 0 {
			rep.Issues = append(rep.Issues, hookIssueError("hook_auth_failures",
				fmt.Sprintf("%d pre-request call(s) failed auth", rep.AuthFailures),
				"check the Aperture hook apikey (and auth-mode / tailnet identity)"))
		}
		if rep.ParseFailures > 0 {
			rep.Issues = append(rep.Issues, hookIssueWarn("hook_parse_failures",
				fmt.Sprintf("%d pre-request payload(s) failed to parse", rep.ParseFailures),
				"confirm Aperture is posting JSON to "+expected))
		}
		if rep.UnrecognizedShapes > 0 {
			rep.Issues = append(rep.Issues, hookIssueWarn("unrecognized_body_shape",
				fmt.Sprintf("%d request body(s) used an unrecognized LLM wire shape; instructions were not injected", rep.UnrecognizedShapes),
				"Aegis can augment OpenAI chat completions (messages), Anthropic Messages (system+messages), and OpenAI Responses (input)"))
		}
		// Payload shape: modify needs request_body; matching needs user_message.
		missingBody, missingUser := 0, 0
		for _, o := range rep.Recent {
			if !o.HasRequestBody {
				missingBody++
			}
			if !o.HasUserMessage {
				missingUser++
			}
		}
		if len(rep.Recent) > 0 && missingBody == len(rep.Recent) {
			rep.Issues = append(rep.Issues, hookIssueWarn("missing_request_body",
				"recent hooks did not include request_body",
				`add "request_body" to send_hooks[].send so Aegis can inject system instructions`))
		}
		if len(rep.Recent) > 0 && missingUser == len(rep.Recent) {
			rep.Issues = append(rep.Issues, hookIssueWarn("missing_user_message",
				"recent hooks did not include user_message",
				`add "user_message" to send_hooks[].send so Aegis can match named services`))
		}
	}

	rep.Status = hookStatusFromIssues(rep.Issues)
	rep.ExpectedHookConfig = expectedApertureHookConfig(baseURL)
	return rep
}

// CheckApertureConfig validates a pasted Aperture policy/hooks JSON document
// against the live Aegis base URL. Unknown shapes still get name/URL checks.
func (s *Server) CheckApertureConfig(raw []byte) HookHealthReport {
	rep := s.hookHealth.Snapshot(s.baseURL)
	issues := analyzeApertureConfig(raw, s.baseURL)
	// Merge config issues ahead of live ones; status recomputed.
	rep.Issues = append(issues, rep.Issues...)
	rep.Status = hookStatusFromIssues(rep.Issues)
	rep.ExpectedHookConfig = expectedApertureHookConfig(s.baseURL)
	return rep
}

func analyzeApertureConfig(raw []byte, baseURL string) []HookIssue {
	var issues []HookIssue
	expected := expectedHookURL(baseURL)

	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		return append(issues, hookIssueError("config_not_json",
			"body is not a JSON object", "paste the Aperture policy/hooks JSON"))
	}

	// Accept either a full policy {"hooks":{...},"grants":[...]} or a bare hooks map.
	var hooks map[string]json.RawMessage
	if hRaw, ok := doc["hooks"]; ok {
		if err := json.Unmarshal(hRaw, &hooks); err != nil {
			issues = append(issues, hookIssueError("hooks_not_object", "hooks is not an object", ""))
			return issues
		}
	} else {
		// Bare map of hook name → def (values should be objects with url).
		bare := true
		for _, v := range doc {
			var probe map[string]any
			if err := json.Unmarshal(v, &probe); err != nil {
				bare = false
				break
			}
			if _, hasURL := probe["url"]; !hasURL {
				bare = false
				break
			}
		}
		if bare && len(doc) > 0 {
			hooks = doc
		}
	}

	if len(hooks) == 0 {
		issues = append(issues, hookIssueError("no_hooks",
			"no hooks found in the config document",
			`add a hooks entry named "`+expectedHookName+`" with url `+expected))
		return issues
	}

	foundPreRequest := false
	for name, rawDef := range hooks {
		var def struct {
			URL     string `json:"url"`
			APIKey  string `json:"apikey"`
			Timeout string `json:"timeout"`
		}
		if err := json.Unmarshal(rawDef, &def); err != nil {
			issues = append(issues, hookIssueWarn("hook_not_object",
				fmt.Sprintf("hook %q is not an object", name), ""))
			continue
		}
		isPR := strings.Contains(def.URL, hookPath) || strings.HasSuffix(strings.TrimRight(def.URL, "/"), hookPath)
		if isPR {
			foundPreRequest = true
		}
		if strings.EqualFold(name, "guardian") || strings.Contains(strings.ToLower(name), "guardian") {
			issues = append(issues, hookIssueWarn("legacy_hook_name",
				fmt.Sprintf("hook %q still uses the old Guardian name", name),
				fmt.Sprintf("rename it to %q", expectedHookName)))
		}
		if strings.Contains(strings.ToLower(def.URL), "guardian") {
			issues = append(issues, hookIssueError("legacy_hook_url",
				fmt.Sprintf("hook %q url still references Guardian: %s", name, def.URL),
				"set url to "+expected))
		}
		if isPR && def.URL != "" && normalizeURL(def.URL) != normalizeURL(expected) {
			issues = append(issues, hookIssueError("hook_url_mismatch",
				fmt.Sprintf("hook %q url %s does not match Aegis base URL", name, def.URL),
				"set url to "+expected))
		}
		if isPR && def.APIKey == "" {
			// Only a problem when auth-mode needs a key; still worth noting.
			issues = append(issues, hookIssueInfo("hook_missing_apikey",
				fmt.Sprintf("hook %q has no apikey field", name),
				"add apikey if Aegis auth-mode is auto or api-key"))
		}
	}

	if !foundPreRequest {
		issues = append(issues, hookIssueError("no_pre_request_hook",
			"no hook url points at "+hookPath,
			"set hooks."+expectedHookName+".url to "+expected))
	}

	// grants → send_hooks wiring
	if gRaw, ok := doc["grants"]; ok {
		var grants []json.RawMessage
		if err := json.Unmarshal(gRaw, &grants); err == nil {
			issues = append(issues, analyzeSendHooks(grants)...)
		}
	}

	return issues
}

func analyzeSendHooks(grants []json.RawMessage) []HookIssue {
	var issues []HookIssue
	sawPreRequest := false
	sawName := false
	missingSend := map[string]bool{}

	for _, g := range grants {
		var grant map[string]json.RawMessage
		if err := json.Unmarshal(g, &grant); err != nil {
			continue
		}
		appRaw, ok := grant["app"]
		if !ok {
			continue
		}
		var app map[string]json.RawMessage
		if err := json.Unmarshal(appRaw, &app); err != nil {
			continue
		}
		capRaw, ok := app["tailscale.com/cap/aperture"]
		if !ok {
			continue
		}
		var caps []map[string]json.RawMessage
		if err := json.Unmarshal(capRaw, &caps); err != nil {
			continue
		}
		for _, cap := range caps {
			shRaw, ok := cap["send_hooks"]
			if !ok {
				continue
			}
			var sendHooks []struct {
				Name   string   `json:"name"`
				Events []string `json:"events"`
				Send   []string `json:"send"`
			}
			if err := json.Unmarshal(shRaw, &sendHooks); err != nil {
				continue
			}
			for _, sh := range sendHooks {
				if strings.Contains(strings.ToLower(sh.Name), "guardian") {
					issues = append(issues, hookIssueWarn("legacy_send_hook_name",
						fmt.Sprintf("send_hooks name %q still uses Guardian", sh.Name),
						fmt.Sprintf("use %q", expectedHookName)))
				}
				if sh.Name == expectedHookName {
					sawName = true
				}
				for _, ev := range sh.Events {
					if ev == "pre_request" {
						sawPreRequest = true
					}
				}
				if len(sh.Events) > 0 && !containsFold(sh.Events, "pre_request") {
					issues = append(issues, hookIssueError("missing_pre_request_event",
						fmt.Sprintf("send_hooks %q events %v omit pre_request", sh.Name, sh.Events),
						`add "pre_request" to events`))
				}
				for _, want := range []string{"user_message", "request_body"} {
					if len(sh.Send) > 0 && !containsFold(sh.Send, want) {
						missingSend[want] = true
					}
				}
			}
		}
	}

	if !sawPreRequest {
		issues = append(issues, hookIssueError("grants_missing_pre_request",
			"no grant send_hooks entry enables pre_request",
			`add send_hooks with name "`+expectedHookName+`" and events ["pre_request"]`))
	}
	if !sawName && sawPreRequest {
		issues = append(issues, hookIssueInfo("send_hook_name_unverified",
			fmt.Sprintf("pre_request is enabled but no send_hooks name matches %q", expectedHookName),
			fmt.Sprintf("prefer naming the hook %q", expectedHookName)))
	}
	for _, want := range []string{"user_message", "request_body"} {
		if missingSend[want] {
			issues = append(issues, hookIssueWarn("send_missing_"+want,
				fmt.Sprintf("send_hooks[].send does not include %q", want),
				fmt.Sprintf(`add "%s" to send`, want)))
		}
	}
	return issues
}

func expectedApertureHookConfig(baseURL string) json.RawMessage {
	cfg := map[string]any{
		"hooks": map[string]any{
			expectedHookName: map[string]any{
				"url":     expectedHookURL(baseURL),
				"apikey":  "<AEGIS_API_KEY if auth-mode is auto/api-key>",
				"timeout": "5s",
			},
		},
		"grants": []any{
			map[string]any{
				"src": []string{"*"},
				"app": map[string]any{
					"tailscale.com/cap/aperture": []any{
						map[string]any{
							"models": "**",
							"send_hooks": []any{
								map[string]any{
									"name":   expectedHookName,
									"events": []string{"pre_request"},
									"send":   []string{"user_message", "request_body"},
								},
							},
						},
					},
				},
			},
		},
	}
	raw, _ := json.Marshal(cfg)
	return raw
}

func expectedHookURL(baseURL string) string {
	return strings.TrimRight(baseURL, "/") + hookPath
}

// isStaleHookHost reports Hosts that still use the pre-rename Guardian name.
func isStaleHookHost(host string) bool {
	h := strings.ToLower(host)
	return strings.Contains(h, "guardian")
}

func hostMatchesBase(host, baseURL string) bool {
	if host == "" || baseURL == "" {
		return true
	}
	h := strings.ToLower(strings.TrimSpace(host))
	// Strip port if present.
	if i := strings.LastIndex(h, ":"); i > 0 && !strings.Contains(h[i:], "]") {
		h = h[:i]
	}
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return strings.Contains(strings.ToLower(baseURL), h)
	}
	b := strings.ToLower(u.Host)
	if i := strings.LastIndex(b, ":"); i > 0 && !strings.Contains(b[i:], "]") {
		b = b[:i]
	}
	return h == b || strings.HasSuffix(h, "."+b) || strings.HasSuffix(b, "."+h)
}

func normalizeURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return strings.TrimRight(strings.TrimSpace(raw), "/")
	}
	u.Fragment = ""
	u.RawQuery = ""
	return strings.TrimRight(u.String(), "/")
}

func hookIssueError(code, msg, fix string) HookIssue {
	return HookIssue{Severity: "error", Code: code, Message: msg, Fix: fix}
}
func hookIssueWarn(code, msg, fix string) HookIssue {
	return HookIssue{Severity: "warn", Code: code, Message: msg, Fix: fix}
}
func hookIssueInfo(code, msg, fix string) HookIssue {
	return HookIssue{Severity: "info", Code: code, Message: msg, Fix: fix}
}

func hookStatusFromIssues(issues []HookIssue) string {
	status := "ok"
	for _, is := range issues {
		switch is.Severity {
		case "error":
			return "error"
		case "warn":
			status = "warn"
		}
	}
	return status
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(v, want) {
			return true
		}
	}
	return false
}

// sortHookIssues keeps admin output stable (severity, then code).
func sortHookIssues(issues []HookIssue) {
	rank := map[string]int{"error": 0, "warn": 1, "info": 2}
	sort.SliceStable(issues, func(i, j int) bool {
		if rank[issues[i].Severity] != rank[issues[j].Severity] {
			return rank[issues[i].Severity] < rank[issues[j].Severity]
		}
		return issues[i].Code < issues[j].Code
	})
}
