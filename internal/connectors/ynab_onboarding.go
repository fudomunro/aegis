package connectors

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/fudomunro/aegis/internal/types"
)

// YNAB onboarding (issues #141, #265): the personal-access-token setup flow,
// declared and driven here while core hosts it generically. Every string
// below is operator-visible copy — the admin panel renders it and must not
// restate it.

// ynabDefaultEndpoint is the API base used when the operator leaves the
// endpoint field blank.
const ynabDefaultEndpoint = "https://api.ynab.com/v1"

// ynabGuideID is the intro block on the setup panel.
const ynabGuideID = "intro"

// Onboarding returns YNAB's declared setup contract.
func (c *YNABConnector) Onboarding() types.OnboardingSpec {
	return types.OnboardingSpec{
		ServiceID:   YNABServiceID,
		DisplayName: YNABServiceName,
		Title:       "YNAB — budget transactions",
		Connection: types.ConnectionMetadata{
			Name:        YNABServiceName,
			Action:      "inject",
			CacheTTL:    "5m",
			Description: "Live YNAB budget transactions for /v1/library/transactions. Amounts map from milliunits to signed decimal strings.",
			Aliases:     []string{"ynab", "you need a budget", "budget"},
			// One personal access token, injected as Authorization. YNAB
			// also accepts X-YNAB-Token; the save path normalizes to Bearer.
			SecretHeaders: []string{"Authorization"},
		},
		Fields: []types.OnboardingField{
			{
				ID:          "endpoint",
				Label:       "API endpoint",
				Kind:        "url",
				Placeholder: ynabDefaultEndpoint,
			},
			{
				ID:          "token",
				Label:       "Personal Access Token",
				Kind:        "password",
				Placeholder: "YNAB → Developer Settings",
				Secret:      true,
			},
		},
		Guides: []types.OnboardingGuide{
			{
				ID: ynabGuideID,
				Body: "Live budget line items for `GET /v1/library/transactions`. " +
					"Create a Personal Access Token in YNAB → Account Settings → Developer Settings. " +
					"Stored in `service_connections.json` — never shown again after save. " +
					"[YNAB API docs](https://api.ynab.com/)",
			},
		},
		Actions: []types.OnboardingAction{
			{
				ID:        "probe",
				Label:     "Test",
				BusyLabel: "Testing YNAB API…",
				Kind:      types.ActionProbe,
				Placement: types.ActionInHeader,
			},
			{
				ID:        "save",
				Label:     "Save connection",
				BusyLabel: "Saving…",
				Kind:      types.ActionSave,
				Placement: types.ActionInHeader,
				Fields:    []string{"endpoint", "token"},
			},
			{
				ID:        "save_test",
				Label:     "Save & test",
				BusyLabel: "Saving…",
				Kind:      types.ActionSave,
				Placement: types.ActionInForm,
				Fields:    []string{"endpoint", "token"},
			},
		},
	}
}

// OnboardingStatus derives YNAB's live view: credential presence, a one-line
// summary, next-step hints, and the connection probe when a token exists.
// Fail-open — a broken YNAB reports through hints and the probe, never an
// error status.
func (c *YNABConnector) OnboardingStatus(ctx context.Context) types.OnboardingStatus {
	svc := c.host.ServiceConfig(YNABServiceID)
	configured := svc != nil
	has, source := c.ynabTokenSource()

	status := types.OnboardingStatus{
		Fields: map[string]types.OnboardingFieldState{
			"endpoint": {ID: "endpoint", Value: c.YnabUpstreamBase(), Valid: true},
			"token":    {ID: "token", Set: has, Valid: true},
		},
	}
	status.Summary = append(status.Summary, ynabTokenSummary(has, source))

	if !configured {
		status.Hints = append(status.Hints, "Add a ynab service (this page can save it for you).")
	}
	if !has {
		status.Hints = append(status.Hints,
			"Create a Personal Access Token in YNAB → Account Settings → Developer Settings, then paste it below.")
	}

	if !has {
		// Nothing to probe yet; the summary already says "API: unchecked".
		status.Summary = append(status.Summary, "API: unchecked")
		status.Summary = append(status.Summary, ynabLibrarySummary(false))
		return status
	}

	res := c.ynabProbe(ctx)
	status.Probe = &res
	status.Summary = append(status.Summary, ynabProbeSummary(&res))
	status.Summary = append(status.Summary, ynabLibrarySummary(c.host.LibraryReadable("transactions")))
	if res.OK {
		status.Hints = append(status.Hints,
			fmt.Sprintf("Connected — %d budget(s) visible. Try GET /v1/library/transactions.", res.Metrics["budget_count"]))
	}
	return status
}

// OnboardingRun executes YNAB's declared verbs. Save returns a ConnectionUpdate
// for core to persist — this connector never writes tokens or config itself.
// Core recomputes the status after applying the update.
func (c *YNABConnector) OnboardingRun(ctx context.Context, action string, in types.OnboardingInput) (types.OnboardingResult, error) {
	spec := c.Onboarding()
	var kind types.ActionKind
	for _, a := range spec.Actions {
		if a.ID == action {
			kind = a.Kind
			break
		}
	}

	switch kind {
	case types.ActionSave:
		update, err := ynabConnectionUpdate(in)
		if err != nil {
			return types.OnboardingResult{}, err
		}
		return types.OnboardingResult{
			Updates: []types.StateUpdate{{Connection: &update}},
		}, nil

	case types.ActionProbe:
		// Distinct diagnosis from a rejected token: nothing was configured.
		if has, _ := c.ynabTokenSource(); !has {
			return types.OnboardingResult{Probe: &types.ProbeResult{
				OK:      false,
				Code:    "unauthorized",
				Message: "no YNAB token configured",
			}}, nil
		}
		res := c.ynabProbe(ctx)
		return types.OnboardingResult{Probe: &res}, nil

	default:
		return types.OnboardingResult{}, fmt.Errorf("unknown onboarding action %q", action)
	}
}

// ynabTokenSource reports whether a credential is present and where it came
// from: a static header on the service, or a stored OAuth token.
func (c *YNABConnector) ynabTokenSource() (bool, string) {
	if svc := c.host.ServiceConfig(YNABServiceID); svc != nil {
		for k, v := range svc.Headers {
			lower := strings.ToLower(k)
			if lower == "authorization" || lower == "x-ynab-token" {
				if strings.TrimSpace(v) != "" {
					return true, "header"
				}
			}
		}
	}
	if c.host.GetValidToken(YNABServiceID, slog.Default()) != "" {
		return true, "oauth"
	}
	return false, ""
}

// ynabConnectionUpdate maps submitted form values to a connection save.
// An empty endpoint falls back to the public API base; a blank token leaves
// any stored credential untouched (core merges headers key-wise).
func ynabConnectionUpdate(in types.OnboardingInput) (types.ConnectionUpdate, error) {
	endpoint := strings.TrimSpace(in.Values["endpoint"])
	if endpoint == "" {
		endpoint = ynabDefaultEndpoint
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		return types.ConnectionUpdate{}, fmt.Errorf("endpoint must be an http(s) URL")
	}
	update := types.ConnectionUpdate{
		Endpoint: strings.TrimRight(endpoint, "/"),
		// Non-nil so core takes the static-credential path even when no new
		// secret is submitted (matches the admin save semantics).
		SecretHeaders: map[string]string{},
	}
	if tok := strings.TrimSpace(in.Values["token"]); tok != "" {
		// Personal Access Token. Agents never see it; Aegis injects Bearer.
		if strings.HasPrefix(strings.ToLower(tok), "bearer ") {
			tok = strings.TrimSpace(tok[len("bearer "):])
		}
		update.SecretHeaders["Authorization"] = "Bearer " + tok
	}
	return update, nil
}

// ynabProbe checks reachability and auth with the budgets listing.
func (c *YNABConnector) ynabProbe(ctx context.Context) types.ProbeResult {
	var out BudgetsResponse
	err := c.APIGet(ctx, slog.With("handler", "admin_ynab"), "/budgets", nil, &out)
	switch {
	case err == types.ErrUnauthorized:
		return types.ProbeResult{
			OK:      false,
			Code:    "unauthorized",
			Message: "YNAB rejected the token — check the Personal Access Token",
		}
	case err != nil:
		return types.ProbeResult{OK: false, Code: "error", Message: err.Error()}
	}
	n := len(out.Data.Budgets)
	return types.ProbeResult{
		OK:      true,
		Code:    "ok",
		Message: fmt.Sprintf("YNAB API reachable — %d budget(s)", n),
		Metrics: map[string]int{"budget_count": n},
	}
}

// ynabTokenSummary is the credential half of the panel's status line.
func ynabTokenSummary(has bool, source string) string {
	if !has {
		return "token: not set"
	}
	if source == "" {
		source = "saved"
	}
	return "token: configured (" + source + ")"
}

// ynabProbeSummary is the API half of the panel's status line.
func ynabProbeSummary(res *types.ProbeResult) string {
	if res == nil {
		return ""
	}
	if res.OK {
		return fmt.Sprintf("API: ok · %d budget(s)", res.Metrics["budget_count"])
	}
	return "API: " + res.Code
}

// ynabLibrarySummary is the library half of the panel's status line.
func ynabLibrarySummary(ready bool) string {
	if ready {
		return "library: ready"
	}
	return "library: not ready"
}

// compile-time check: YNAB drives its own setup (issue #265).
var _ Onboarder = (*YNABConnector)(nil)
