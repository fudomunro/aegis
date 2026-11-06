package connectors

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/fudomunro/aegis/internal/types"
)

// Immich onboarding (issues #39, #121, #265): the self-hosted photo
// connection wizard — API-key checkbox contract, endpoint repair, live
// probes, the acceptance checklist, and the guided ladder — declared and
// driven here while core hosts it generically. Every string below is
// operator-visible copy: the admin panel renders it and must not restate it.
//
// The Tailscale VIP-Service wizard (issue #124) is a separate surface owned
// by core's tailscale_admin.go for now; it joins this seam with Phase 3.

// immichPermissionGroup is one comment-headed run of the single-key checkbox
// set. Groups exist so the copyable list the operator ticks in Immich is
// generated from the same declaration the connector enforces — the set has
// exactly one owner.
type immichPermissionGroup struct {
	Comment string
	Perms   []string
}

// immichPermissionGroups is the single Immich v3 checkbox set for one key
// used by both Aegis (list/thumbnail/download) and immich-go Takeout import.
// Nothing else may enumerate these strings: admin.html renders this and
// docs/ point at the admin surface (issue #265).
var immichPermissionGroups = []immichPermissionGroup{
	{
		Comment: "# Aegis",
		Perms:   []string{"asset.read", "asset.view", "asset.download", "album.read", "user.read"},
	},
	{
		Comment: "# immich-go upload from-google-photos",
		Perms: []string{
			"asset.upload", "asset.update", "asset.copy", "asset.delete", "asset.statistics",
			"album.create", "albumAsset.create",
			"stack.create", "tag.create", "tag.asset",
			"server.about",
		},
	},
	{
		Comment: "# immich-go pauses jobs during import (admin-linked key)",
		Perms:   []string{"job.create", "job.read"},
	},
}

// ImmichAPIKeyPermissions flattens the checkbox set for probes and hints.
func ImmichAPIKeyPermissions() []string {
	out := make([]string, 0, 24)
	for _, g := range immichPermissionGroups {
		out = append(out, g.Perms...)
	}
	return out
}

// ImmichAPIKeyPermissionsSummary is the one-line union for hints and
// next-step copy.
func ImmichAPIKeyPermissionsSummary() string {
	return strings.Join(ImmichAPIKeyPermissions(), ", ")
}

// immichAPIKeyPermissionsBlock is the copyable list operators tick in Immich,
// generated from the same groups the connector enforces.
func immichAPIKeyPermissionsBlock() string {
	var b strings.Builder
	b.WriteString("# Single Immich key — Aegis library/proxy AND immich-go Takeout import\n")
	for _, g := range immichPermissionGroups {
		b.WriteString("\n" + g.Comment + "\n")
		for _, p := range g.Perms {
			b.WriteString(p + "\n")
		}
	}
	return b.String()
}

// Immich probe / diagnosis codes (issue #121). Stable strings: the admin UI
// and tests key off them.
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

// Field / step / check / guide ids. The admin routes and tests key off them.
const (
	immichFieldEndpoint = "endpoint"
	immichFieldAPIKey   = "api_key"
	immichFieldClaim    = "claim_canonical_for_photos"

	immichStepInstall = "install"
	immichStepAPIKey  = "api_key"
	immichStepConnect = "connect"
	immichStepVerify  = "verify"

	immichCheckConfigured = "service_configured"
	immichCheckAPIKey     = "api_key"
	immichCheckCanonical  = "canonical_owner"
	immichCheckReachable  = "endpoint_reachable"
	immichCheckListing    = "library_listing"
	immichCheckStamp      = "canonical_stamp"
	immichCheckBytes      = "bytes_stay_upstream"

	immichGuideHelp  = "help"
	immichGuidePerms = "api_key_permissions"
	immichGuideSnip  = "snippet"

	// VIP-Service wizard (issue #124) — walkthrough-scoped.
	immichFieldTSAPIKey  = "ts_api_key"
	immichFieldTSTailnet = "ts_tailnet"
	immichFieldHostDev   = "host_device"

	immichActionTSSave  = "tailnet_save"
	immichActionTSCheck = "tailnet_check"
	immichActionTSApply = "tailnet_apply"

	immichGuideTSAdmin    = "tailnet_admin"
	immichGuideTSEndpoint = "tailnet_endpoint"
)

// Onboarding returns Immich's declared setup contract.
func (c *ImmichConnector) Onboarding() types.OnboardingSpec {
	return types.OnboardingSpec{
		ServiceID:   ImmichServiceID,
		DisplayName: ImmichServiceName,
		Title:       "Immich — local canonical photos",
		// WalkthroughLabel opens the guided setup pages. The panel owns the
		// modal chrome; this connector owns every page's copy.
		WalkthroughLabel: "Setup guide",
		WalkthroughTitle: "Immich setup guide",
		ChecksEmpty:      "Run checks after saving to verify the checklist.",
		Connection: types.ConnectionMetadata{
			Name:        "Immich",
			Action:      "inject",
			CacheTTL:    "5m",
			Description: "Self-hosted Immich photo library. Photo bytes stay on Immich (foreign/local canonical).",
			Aliases: []string{
				"immich photos", "self-hosted photos", "immich",
				"photo", "photos", "picture", "pictures", "photo library",
			},
			SecretHeaders: []string{"X-API-Key"},
		},
		Fields: append([]types.OnboardingField{
			{
				ID:                     immichFieldEndpoint,
				Label:                  "Immich endpoint",
				Kind:                   "url",
				Placeholder:            "http://photos.home.ts.net:2283/api",
				WalkthroughPlaceholder: "http://localhost:2283/api",
				Help:                   "Endpoint must end in `/api` (we repair common pastes). Same machine: `http://localhost:2283/api`. VIP Service: `https://immich.<tailnet>.ts.net/api`.",
			},
			{
				ID:                     immichFieldAPIKey,
				Label:                  "Immich API key",
				Kind:                   "password",
				Placeholder:            "Account → API Keys",
				WalkthroughPlaceholder: "Paste the key from step 4",
				Secret:                 true,
			},
			{
				ID:    immichFieldClaim,
				Label: "Claim canonical for photos",
				Kind:  "checkbox",
				Help:  "Saved to `service_connections.json` (not `aegis.json`). API key is stored and never shown again after save.",
			},
		}, immichTailnetFields()...),
		Steps: []types.OnboardingStepDef{
			{ID: immichStepInstall, Label: "Immich installed and reachable"},
			{ID: immichStepAPIKey, Label: "Immich API key created"},
			{ID: immichStepConnect, Label: "Saved into Aegis"},
			{ID: immichStepVerify, Label: "Verified end-to-end"},
		},
		Checks: []types.OnboardingCheckDef{
			{ID: immichCheckConfigured, Label: "Immich service configured"},
			{ID: immichCheckAPIKey, Label: "API key present (X-API-Key)"},
			{ID: immichCheckCanonical, Label: "Immich is canonical_for photos"},
			{ID: immichCheckReachable, Label: "Immich endpoint reachable"},
			{ID: immichCheckListing, Label: "Library lists Immich photos"},
			{ID: immichCheckStamp, Label: "Sources stamped canonical + proxy blob_url"},
			{ID: immichCheckBytes, Label: "Photo bytes stay on Immich"},
		},
		Guides: append([]types.OnboardingGuide{
			{
				ID: immichGuideHelp,
				Body: "Self-hosted photo library as the canonical home for photos. Bytes stay on Immich. " +
					"[Immich docs](https://docs.immich.app/overview/quick-start/) · `docs/immich-live-testing.md`",
			},
			{
				ID:          immichGuidePerms,
				Title:       "One API key for Aegis + immich-go",
				Short:       "API key",
				Button:      "Copy list",
				Walkthrough: true,
				Body:        "Copy the secret once when Immich shows it — use it in the next step and in `immich-go --api-key=`.",
				// Command comes from the status: the block is generated from
				// the groups above so the set has one owner.
			},
			{
				ID:          immichGuideSnip,
				Title:       "Prefer aegis.json? Show bootstrap snippet",
				Label:       "services[] entry",
				Button:      "Copy JSON",
				Collapsible: true,
			},
		}, immichTailnetGuides()...),
		Actions: append([]types.OnboardingAction{
			{
				ID:               "probe",
				Label:            "Test",
				WalkthroughLabel: "Test connection",
				BusyLabel:        "Testing…",
				Kind:             types.ActionProbe,
				Placement:        types.ActionInHeader,
				Fields:           []string{immichFieldEndpoint, immichFieldAPIKey},
			},
			{
				ID:        "save",
				Label:     "Save to Aegis",
				BusyLabel: "Saving…",
				Kind:      types.ActionSave,
				Placement: types.ActionInForm,
				Fields:    []string{immichFieldEndpoint, immichFieldAPIKey, immichFieldClaim},
			},
			{
				ID:        "check",
				Label:     "Run checks",
				BusyLabel: "Checking…",
				Kind:      types.ActionCheck,
				Placement: types.ActionInHeader,
			},
		}, immichTailnetActions()...),
	}
}

// immichTailnetGuides are the VIP-Service wizard's pages (issue #124): the
// copyable walkthrough, the optional Tailscale Admin API settings, and the
// endpoint to point Aegis at afterwards. Copy is generated beside the checks
// that enforce it.
func immichTailnetGuides() []types.OnboardingGuide {
	return []types.OnboardingGuide{
		immichTailnetGuide("", ""),
		{
			ID:          immichGuideTSAdmin,
			Title:       "Tailscale Admin API (optional — live checks & one-click fixes)",
			Walkthrough: true,
			Terminal:    "settings",
		},
		{
			ID:          immichGuideTSEndpoint,
			Title:       "Suggested Aegis endpoint after hosting",
			Label:       "Suggested Aegis endpoint after hosting",
			Button:      "Copy endpoint",
			Walkthrough: true,
			Body: "Confirm the exact host from `tailscale serve status` on the Immich machine. Endpoint must end in `/api`.\n\n" +
				"Skip this step if you already have a reachable endpoint (localhost, LAN, or MagicDNS + `:2283`). Optional upgrade later.",
		},
	}
}

// immichTailnetFields are the wizard's Tailscale Admin settings.
func immichTailnetFields() []types.OnboardingField {
	return []types.OnboardingField{
		{
			ID:          immichFieldTSAPIKey,
			Label:       "Tailscale API key",
			Kind:        "password",
			Placeholder: "tskey-api-…",
			Secret:      true,
			Walkthrough: true,
			Help: "Paste an access token from [Keys](https://console.tailscale.com/admin/settings/keys) " +
				"(scopes: `services`, `devices:core`). Stored in `service_connections.json` (0600) — never echoed back. " +
				"Aegis only offers to fix steps that still need work.",
		},
		{
			ID:          immichFieldTSTailnet,
			Label:       "Tailnet",
			Kind:        "text",
			Placeholder: "- (default)",
			Walkthrough: true,
		},
		{
			ID:          immichFieldHostDev,
			Label:       "Immich host device",
			Kind:        "select",
			Placeholder: "(select after checking)",
			Walkthrough: true,
		},
	}
}

// immichTailnetActions are the wizard's verbs.
func immichTailnetActions() []types.OnboardingAction {
	return []types.OnboardingAction{
		{
			ID: immichActionTSSave, Label: "Save key", BusyLabel: "Saving…",
			Kind: types.ActionSave, Placement: types.ActionInForm, Walkthrough: true,
			Fields: []string{immichFieldTSAPIKey, immichFieldTSTailnet, immichFieldHostDev},
		},
		{
			ID: immichActionTSCheck, Label: "Check steps", BusyLabel: "Checking…",
			Kind: types.ActionCheck, Placement: types.ActionInForm, Walkthrough: true,
			Fields: []string{immichFieldHostDev},
		},
		{
			ID: immichActionTSApply, Label: "Fix needed steps", BusyLabel: "Applying…",
			Kind: types.ActionApply, Placement: types.ActionInForm, Walkthrough: true,
			Fields: []string{immichFieldHostDev},
		},
	}
}

// compile-time check: Immich drives its own setup (issue #265).
var _ Onboarder = (*ImmichConnector)(nil)

// NormalizeImmichEndpoint repairs the mistakes first-timers make: missing
// scheme, bare host:port, web-UI URL without /api. Returns the normalized
// endpoint and whether it changed.
func NormalizeImmichEndpoint(raw string) (string, bool) {
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

// TailnetDNSFromBaseURL extracts the MagicDNS suffix from Aegis's public URL
// (https://aegis.your-tailnet.ts.net → your-tailnet.ts.net). Empty when the
// base URL is not a ts.net name.
func TailnetDNSFromBaseURL(baseURL string) string {
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

// ImmichSuggestedEndpoint is the Immich API base operators should paste.
// Prefer the VIP Service HTTPS name when the tailnet suffix is known;
// otherwise the portable host:port form from the live-testing guide.
func ImmichSuggestedEndpoint(tailnetDNS string) string {
	if tailnetDNS != "" {
		return "https://immich." + tailnetDNS + "/api"
	}
	return "http://photos.home.ts.net:2283/api"
}

// ImmichConfigSnippet is the display-only bootstrap snippet for operators
// who prefer aegis.json.
func ImmichConfigSnippet(endpoint, baseURL string) string {
	if strings.TrimSpace(endpoint) == "" {
		endpoint = ImmichSuggestedEndpoint(TailnetDNSFromBaseURL(baseURL))
	}
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

// firstNonEmpty returns the first non-blank value.
func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// mustNormalize returns the normalized endpoint, ignoring the changed flag.
func mustNormalize(raw string) string {
	out, _ := NormalizeImmichEndpoint(raw)
	return out
}

// immichHTTPClient bounds setup probes so a hung Immich cannot stall the
// admin panel.
var immichHTTPClient = &http.Client{Timeout: 8 * time.Second}

// OnboardingStatus derives Immich's live view: the acceptance checklist, the
// guided ladder, the one-line summary, and the single next action. Fail-open
// — a broken Immich reports through failed rows and hints, never an error
// status (issue #39).
func (c *ImmichConnector) OnboardingStatus(ctx context.Context) types.OnboardingStatus {
	svc := c.host.ServiceConfig(ImmichServiceID)
	status := types.OnboardingStatus{
		Fields:  map[string]types.OnboardingFieldState{},
		Guides:  map[string]types.OnboardingGuideState{},
		Metrics: map[string]int{},
	}
	endpoint := ""
	apiKey := ""
	configured := svc != nil
	if configured {
		endpoint = svc.Endpoint
		for k, v := range svc.Headers {
			if strings.EqualFold(k, "X-API-Key") {
				apiKey = v
			}
		}
	}
	claim := configured && immichClaimsPhotos(svc.CanonicalFor)
	status.Fields[immichFieldEndpoint] = types.OnboardingFieldState{ID: immichFieldEndpoint, Value: endpoint, Valid: true}
	status.Fields[immichFieldAPIKey] = types.OnboardingFieldState{ID: immichFieldAPIKey, Set: strings.TrimSpace(apiKey) != "", Valid: true}
	status.Fields[immichFieldClaim] = types.OnboardingFieldState{ID: immichFieldClaim, Checked: claim, Valid: true}

	// Derived copyable blocks: the checkbox set and the bootstrap snippet are
	// generated, never hand-written.
	status.Guides[immichGuidePerms] = types.OnboardingGuideState{Command: immichAPIKeyPermissionsBlock()}
	status.Guides[immichGuideSnip] = types.OnboardingGuideState{
		Command: ImmichConfigSnippet(endpoint, c.host.BaseURL()),
	}

	// VIP-Service wizard state (issue #124): live per-step view and the host
	// picker's candidate devices.
	if tsKey := strings.TrimSpace(c.host.SetupSecret(types.SetupSecretTailscaleAPIKey)); tsKey != "" {
		tsState := c.immichTailnetState(ctx)
		tsState.Command = immichTailnetGuide(c.host.BaseURL(), endpoint).Command
		status.Guides[immichGuideIDTailnet] = tsState
		status.Guides[immichGuideTSEndpoint] = types.OnboardingGuideState{
			Command: ImmichSuggestedEndpoint(TailnetDNSFromBaseURL(c.host.BaseURL())),
		}
		if opts := c.immichTailnetDeviceOptions(ctx); len(opts) > 0 {
			st := status.Fields[immichFieldHostDev]
			st.ID = immichFieldHostDev
			st.Options = opts
			st.Valid = true
			status.Fields[immichFieldHostDev] = st
		}
	} else {
		status.Guides[immichGuideIDTailnet] = types.OnboardingGuideState{
			Command: immichTailnetGuide(c.host.BaseURL(), endpoint).Command,
			Summary: immichTailnetSummary(false, false, "", ""),
			Error:   "Tailscale API key not configured — paste one to enable live checks and one-click fixes.",
		}
		status.Guides[immichGuideTSEndpoint] = types.OnboardingGuideState{
			Command: ImmichSuggestedEndpoint(TailnetDNSFromBaseURL(c.host.BaseURL())),
		}
	}

	status.Checks = c.runImmichChecks(ctx, &status, configured, apiKey, endpoint)
	status.Steps = c.immichGuidedSteps(&status, configured, endpoint)
	status.Next = immichNextStepFrom(status.Steps, status.Probe, status.Checks)
	status.Summary = immichSummary(&status, configured, claim)

	// Placeholder hint when nothing is configured yet.
	if !configured {
		status.Hints = append(status.Hints, "Add Immich with the form above (or aegis.json)")
	}
	return status
}

// immichSummary builds the panel's one-line status: configured · credential ·
// canonical · library.
func immichSummary(status *types.OnboardingStatus, configured, claim bool) []string {
	first, second := "not configured", "no API key"
	if configured {
		first = "configured"
	}
	if status.Fields[immichFieldAPIKey].Set {
		second = "API key set"
	}

	owner := "canonical: —"
	switch {
	case claim:
		owner = "canonical: photos"
	default:
		for _, chk := range status.Checks {
			if chk.ID != immichCheckCanonical || !strings.HasPrefix(chk.Detail, "Currently claimed by ") {
				continue
			}
			name := strings.TrimPrefix(chk.Detail, "Currently claimed by ")
			name = strings.TrimSuffix(name, " — enable claim below to move it")
			owner = "photos home: " + name
			break
		}
	}

	lib := "library: —"
	for _, chk := range status.Checks {
		if chk.ID != immichCheckListing {
			continue
		}
		if chk.OK {
			lib = fmt.Sprintf("library: ok (%d)", status.Metrics["photo_count"])
		} else {
			lib = "library: error"
		}
		break
	}
	return []string{first, second, owner, lib}
}

// OnboardingRun executes Immich's declared verbs. Save returns a
// ConnectionUpdate for core to persist — this connector never writes tokens
// or config itself. "check" is a no-op here: the runner recomputes the
// status, which runs the acceptance checklist.
func (c *ImmichConnector) OnboardingRun(ctx context.Context, action string, in types.OnboardingInput) (types.OnboardingResult, error) {
	kind := ""
	for _, a := range c.Onboarding().Actions {
		if a.ID == action {
			kind = string(a.Kind)
			break
		}
	}

	switch kind {
	case string(types.ActionProbe):
		endpoint, apiKey := c.candidateCredentials(in)
		res := c.probeImmich(ctx, endpoint, apiKey)
		return types.OnboardingResult{Probe: &res}, nil

	case string(types.ActionSave):
		if action == immichActionTSSave {
			return types.OnboardingResult{Updates: []types.StateUpdate{{TailscaleAdmin: &types.TailscaleAdminUpdate{
				APIKey:       strings.TrimSpace(in.Values[immichFieldTSAPIKey]),
				Tailnet:      strings.TrimSpace(in.Values[immichFieldTSTailnet]),
				HostDeviceID: strings.TrimSpace(in.Values[immichFieldHostDev]),
				HostTag:      c.immichHostTag(),
			}}}}, nil
		}
		update, err := c.immichConnectionUpdate(in)
		if err != nil {
			return types.OnboardingResult{}, err
		}
		return types.OnboardingResult{Updates: []types.StateUpdate{{Connection: &update}}}, nil

	case string(types.ActionCheck):
		// Read-only: the runner recomputes status, which carries the live
		// checklist and the VIP wizard's per-step state.
		return types.OnboardingResult{}, nil

	case string(types.ActionApply):
		if action != immichActionTSApply {
			return types.OnboardingResult{}, fmt.Errorf("unknown onboarding action %q", action)
		}
		res, updates := c.immichTailnetApply(ctx, in.Selected,
			strings.TrimSpace(in.Values[immichFieldHostDev]), c.immichHostTag())
		return types.OnboardingResult{Detail: res.Summary(), Updates: updates}, nil

	default:
		return types.OnboardingResult{}, fmt.Errorf("unknown onboarding action %q", action)
	}
}

// candidateCredentials resolves submitted values, falling back to the saved
// connection so an empty form still tests what is configured.
func (c *ImmichConnector) candidateCredentials(in types.OnboardingInput) (endpoint, apiKey string) {
	endpoint = strings.TrimSpace(in.Values[immichFieldEndpoint])
	apiKey = strings.TrimSpace(in.Values[immichFieldAPIKey])
	if svc := c.host.ServiceConfig(ImmichServiceID); svc != nil {
		if endpoint == "" {
			endpoint = svc.Endpoint
		}
		if apiKey == "" {
			for k, v := range svc.Headers {
				if strings.EqualFold(k, "X-API-Key") {
					apiKey = v
				}
			}
		}
	}
	return endpoint, apiKey
}

// immichConnectionUpdate maps submitted form values to a connection save.
// A blank API key leaves any stored credential untouched (core merges
// headers key-wise). Unchecking the claim clears it — the single-owner rule
// must not leave a stale claim behind.
func (c *ImmichConnector) immichConnectionUpdate(in types.OnboardingInput) (types.ConnectionUpdate, error) {
	endpoint := strings.TrimSpace(in.Values[immichFieldEndpoint])
	if endpoint == "" {
		return types.ConnectionUpdate{}, fmt.Errorf("endpoint is required — e.g. https://immich.your-tailnet.ts.net/api")
	}
	// Repair common first-timer URLs (scheme, /api, localhost port).
	endpoint, _ = NormalizeImmichEndpoint(endpoint)
	if endpoint == "" {
		return types.ConnectionUpdate{}, fmt.Errorf("endpoint is required — e.g. https://immich.your-tailnet.ts.net/api")
	}
	update := types.ConnectionUpdate{
		Endpoint:      endpoint,
		SecretHeaders: map[string]string{},
	}
	if key := strings.TrimSpace(in.Values[immichFieldAPIKey]); key != "" {
		update.SecretHeaders["X-API-Key"] = key
	}
	if in.Flags[immichFieldClaim] {
		update.CanonicalFor = []string{"photos"}
	} else {
		update.ClearCanonicalFor = true
	}
	return update, nil
}

// immichClaimsPhotos reports whether a canonical claim list owns photos.
func immichClaimsPhotos(claims []string) bool {
	for _, k := range claims {
		if k == "photos" {
			return true
		}
	}
	return false
}

// runImmichChecks executes the issue #39 acceptance checklist against the
// live configuration. Fail-open: a broken Immich marks checks failed but
// never errors the admin request.
func (c *ImmichConnector) runImmichChecks(ctx context.Context, status *types.OnboardingStatus, configured bool, apiKey, endpoint string) []types.OnboardingCheckState {
	checks := map[string]*types.OnboardingCheckState{}
	order := []string{
		immichCheckConfigured, immichCheckAPIKey, immichCheckCanonical,
		immichCheckReachable, immichCheckListing, immichCheckStamp, immichCheckBytes,
	}
	for _, id := range order {
		checks[id] = &types.OnboardingCheckState{ID: id}
	}
	mark := func(id, detail string, ok bool) {
		if c, exists := checks[id]; exists {
			c.OK = ok
			c.Detail = detail
		}
	}
	fail := func() []types.OnboardingCheckState {
		mark(immichCheckBytes, "Product rule: Aegis never stores photo bytes", true)
		out := make([]types.OnboardingCheckState, 0, len(order))
		for _, id := range order {
			out = append(out, *checks[id])
		}
		return out
	}

	if !configured {
		mark(immichCheckConfigured, "Add Immich with the form above (or aegis.json)", false)
		mark(immichCheckAPIKey, "Not configured", false)
		mark(immichCheckCanonical, "Not configured", false)
		mark(immichCheckReachable, "Not configured", false)
		mark(immichCheckListing, "Not configured", false)
		mark(immichCheckStamp, "Not configured", false)
		return fail()
	}
	mark(immichCheckConfigured, "id=immich", true)

	if strings.TrimSpace(apiKey) != "" {
		mark(immichCheckAPIKey, "Header set (value not shown)", true)
	} else {
		mark(immichCheckAPIKey, "Set X-API-Key via this form or aegis.json headers", false)
	}

	svc := c.host.ServiceConfig(ImmichServiceID)
	claim := immichClaimsPhotos(svc.CanonicalFor)
	conflict := ""
	if !claim {
		if owner := c.host.CanonicalOwner("photos"); owner != "" && owner != ImmichServiceID {
			conflict = owner
		}
	}
	switch {
	case claim:
		mark(immichCheckCanonical, "canonical_for includes photos", true)
	case conflict != "":
		mark(immichCheckCanonical, "Currently claimed by "+conflict+" — enable claim below to move it", false)
	default:
		mark(immichCheckCanonical, "No service claims canonical_for photos (optional)", true)
	}

	// Probe reachability + auth using the configured connector path.
	probe := c.probeImmich(ctx, endpoint, apiKey)
	status.Probe = &probe
	if probe.OK {
		mark(immichCheckReachable, probe.Message, true)
	} else {
		mark(immichCheckReachable, probe.Message, false)
		mark(immichCheckListing, "Skipped — endpoint/auth failed", false)
		mark(immichCheckStamp, "Skipped — endpoint/auth failed", false)
		return fail()
	}

	photos, _, err := c.ListUserPhotos(ctx)
	if err != nil {
		mark(immichCheckListing, err.Error(), false)
		mark(immichCheckStamp, "Skipped — listing failed", false)
		return fail()
	}
	status.Metrics["photo_count"] = len(photos)
	if len(photos) == 0 {
		mark(immichCheckListing, "0 photos — Immich is empty. Add photos via web upload, phone backup, or Google Takeout + immich-go", false)
	} else {
		mark(immichCheckListing, fmt.Sprintf("%d photos", len(photos)), true)
	}

	stampOK, stampDetail := immichStampSummary(photos, c.host.BaseURL())
	mark(immichCheckStamp, stampDetail, stampOK)

	// Product invariant, not a live probe.
	mark(immichCheckBytes, "Metadata + proxy only; downloads stream from Immich", true)

	out := make([]types.OnboardingCheckState, 0, len(order))
	for _, id := range order {
		out = append(out, *checks[id])
	}
	return out
}

// immichGuidedSteps builds the first-run ladder (issue #121). Steps are
// ordered: install → key → connect → verify, fully derived per request.
func (c *ImmichConnector) immichGuidedSteps(status *types.OnboardingStatus, configured bool, endpoint string) []types.OnboardingStepState {
	reachable := status.Probe != nil && status.Probe.OK

	installDetail := "Install with the starter .env (docs/immich-starter.env) + docker compose up -d, wait for healthy. Web UI: http://localhost:2283"
	if endpoint != "" {
		installDetail = "API base: " + endpoint
	}
	if reachable {
		installDetail = "Immich answered on " + firstNonEmpty(endpoint, "the configured endpoint")
	}

	keyDetail := "Immich → Account → API Keys → New key. One key for Aegis + immich-go: " + ImmichAPIKeyPermissionsSummary() + ". Copy it once."
	if status.Fields[immichFieldAPIKey].Set {
		keyDetail = "API key is saved in Aegis (value not shown)."
	}

	connectDetail := "Use Test connection + Save to Aegis below."
	if configured {
		connectDetail = "Connected in Aegis (service_connections.json)."
	}

	verifyDetail := "Run checks after saving."
	verifyOK := false
	if configured && status.Fields[immichFieldAPIKey].Set && len(status.Checks) > 0 {
		failed := false
		for _, chk := range status.Checks {
			if !chk.OK && chk.ID != immichCheckCanonical {
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
	} else if configured && status.Fields[immichFieldAPIKey].Set {
		verifyDetail = "Run checks to finish verification."
	}

	steps := []types.OnboardingStepState{
		{ID: immichStepInstall, OK: reachable, Detail: installDetail},
		{ID: immichStepAPIKey, OK: status.Fields[immichFieldAPIKey].Set, Detail: keyDetail},
		{ID: immichStepConnect, OK: configured, Detail: connectDetail},
		{ID: immichStepVerify, OK: verifyOK, Detail: verifyDetail},
	}
	for i := range steps {
		if !steps[i].OK {
			steps[i].Current = true
			break
		}
	}
	return steps
}

// immichNextStepFrom names the single thing to do now.
func immichNextStepFrom(steps []types.OnboardingStepState, probe *types.ProbeResult, checks []types.OnboardingCheckState) string {
	emptyLibrary := false
	for _, chk := range checks {
		if chk.ID == immichCheckListing && strings.HasPrefix(chk.Detail, "0 photos") {
			emptyLibrary = true
		}
	}
	for _, st := range steps {
		if st.OK {
			continue
		}
		switch st.ID {
		case immichStepInstall:
			if probe != nil && probe.Hint != "" {
				return probe.Hint
			}
			return "Install Immich with the starter .env (Admin → Services copy button), then Test connection."
		case immichStepAPIKey:
			return "Create one Immich API key (Aegis + immich-go: " + ImmichAPIKeyPermissionsSummary() + ") and paste it above."
		case immichStepConnect:
			return "Click Save to Aegis to store the connection."
		case immichStepVerify:
			if emptyLibrary {
				return "Immich has no photos yet — upload a few in the web UI, phone-backup an album, or import Google Takeout with immich-go (Services → New to Immich?), then Run checks again."
			}
			return "Click Run checks and fix anything still red."
		}
	}
	return "Immich setup is complete. Agents can list photos via /v1/library/photos."
}

// immichStampSummary inspects one listing for the canonical + proxy stamp.
func immichStampSummary(photos []types.Photo, baseURL string) (bool, string) {
	if len(photos) == 0 {
		return false, "no photos to inspect"
	}
	proxyPrefix := strings.TrimRight(baseURL, "/") + "/v1/services/" + ImmichServiceID + "/"
	for _, p := range photos {
		for _, src := range p.Sources {
			if src.Service != ImmichServiceID {
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

// probeImmich checks endpoint reachability and API key auth with actionable
// diagnostics (issue #121). Tries GET /users/me (auth) then GET /server/ping.
func (c *ImmichConnector) probeImmich(ctx context.Context, endpoint, apiKey string) types.ProbeResult {
	raw := strings.TrimSpace(endpoint)
	base, changed := NormalizeImmichEndpoint(raw)
	if base == "" {
		return types.ProbeResult{
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
		return types.ProbeResult{
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
		resp, err := immichHTTPClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 512))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return types.ProbeResult{
					OK: true, Status: resp.StatusCode, Code: immichCodeOK,
					Message:   "Immich accepted the API key",
					Hint:      "Save the connection, then run checks.",
					Suggested: suggested,
				}
			}
			if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
				return types.ProbeResult{
					OK: false, Status: resp.StatusCode, Code: immichCodeUnauthorized,
					Message:   "Immich rejected the API key",
					Hint:      "In Immich: Account → API Keys → New key. Tick the single-key set (Aegis + immich-go): " + ImmichAPIKeyPermissionsSummary() + ". Copy the secret once, paste it here.",
					Suggested: suggested,
				}
			}
		}
	}

	req2, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/server/ping", nil)
	if err != nil {
		return types.ProbeResult{OK: false, Code: immichCodeBadURL, Message: err.Error(), Hint: "Check the endpoint spelling."}
	}
	if apiKey != "" {
		req2.Header.Set("X-API-Key", apiKey)
	}
	resp2, err := immichHTTPClient.Do(req2)
	if err != nil {
		code := immichCodeUnreachable
		hint := "Start Immich with Docker: save our starter as .env (before the first up), docker compose up -d, then wait for immich_server to be healthy in docker compose ps. If localhost:2283 is refused, check server logs for database \"immich\" does not exist. Use a hostname this Aegis host can reach (LAN IP or tailnet name)."
		if ctx.Err() != nil || strings.Contains(err.Error(), "timeout") || strings.Contains(err.Error(), "deadline") {
			code = immichCodeTimeout
			hint = "The request timed out. Immich may be slow to start, firewalled, or on another network. Try the tailnet hostname if this Aegis runs on a different machine."
		}
		if suggested == "" {
			if norm, ch := NormalizeImmichEndpoint(raw); ch {
				suggested = norm
			}
		}
		return types.ProbeResult{
			OK: false, Code: code,
			Message:   "Could not reach Immich: " + err.Error(),
			Hint:      hint,
			Suggested: suggested,
		}
	}
	defer resp2.Body.Close()
	sniff := make([]byte, 64)
	n, _ := io.ReadFull(resp2.Body, sniff)
	bodyHint := strings.ToLower(string(sniff[:n]))

	if resp2.StatusCode == http.StatusOK {
		if apiKey == "" {
			return types.ProbeResult{
				OK: true, Status: resp2.StatusCode, Code: immichCodeOKNoKey,
				Message:   "Immich is reachable (no API key set yet)",
				Hint:      "Create one Immich API key for Aegis + immich-go (permissions: " + ImmichAPIKeyPermissionsSummary() + ") and paste it above so agents can list photos.",
				Suggested: suggested,
			}
		}
		return types.ProbeResult{
			OK: true, Status: resp2.StatusCode, Code: immichCodeOK,
			Message:   "Immich is reachable",
			Hint:      "If you have not already, paste the API key and save.",
			Suggested: suggested,
		}
	}

	// Classify non-200 responses for first-timers.
	switch {
	case resp2.StatusCode == http.StatusUnauthorized || resp2.StatusCode == http.StatusForbidden:
		return types.ProbeResult{
			OK: false, Status: resp2.StatusCode, Code: immichCodeUnauthorized,
			Message:   fmt.Sprintf("Immich returned HTTP %d", resp2.StatusCode),
			Hint:      "Check the API key (Account → API Keys). If you just created Immich, finish the web-UI signup first.",
			Suggested: suggested,
		}
	case resp2.StatusCode == http.StatusNotFound && strings.Contains(bodyHint, "<html"):
		return types.ProbeResult{
			OK: false, Status: resp2.StatusCode, Code: immichCodeWrongPath,
			Message:   "Got a web page, not the Immich API",
			Hint:      "The endpoint must be the API base and end in /api — e.g. http://host:2283/api (not the album /photos page).",
			Suggested: firstNonEmpty(suggested, mustNormalize(base+"/api")),
		}
	case strings.Contains(bodyHint, "<html") || strings.Contains(bodyHint, "<!doctype"):
		return types.ProbeResult{
			OK: false, Status: resp2.StatusCode, Code: immichCodeNotImmich,
			Message:   "That host answered with a web page, not Immich",
			Hint:      "Double-check the host/port. Immich’s API answers JSON from /server/ping on port 2283 by default.",
			Suggested: suggested,
		}
	default:
		return types.ProbeResult{
			OK: false, Status: resp2.StatusCode, Code: immichCodeHTTPError,
			Message:   fmt.Sprintf("Immich returned HTTP %d", resp2.StatusCode),
			Hint:      "Open the same URL in a browser or curl. If it is not Immich, fix the endpoint; if Immich is healthy, check reverse-proxy / TLS.",
			Suggested: suggested,
		}
	}
}

// immichHostTag is the service-host tag the wizard tags devices with.
func (c *ImmichConnector) immichHostTag() string {
	_, _, tag := c.immichTailnetSettings()
	return tag
}

// immichTailnetDeviceOptions lists host candidates for the wizard's picker.
func (c *ImmichConnector) immichTailnetDeviceOptions(ctx context.Context) []types.OnboardingOption {
	cl := c.immichTailnetClient()
	if cl == nil {
		return nil
	}
	devices, err := cl.ListDevices(ctx)
	if err != nil {
		return nil
	}
	return immichDeviceOptions(devices)
}
