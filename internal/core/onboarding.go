package core

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/connectors"
	"github.com/fudomunro/aegis/internal/shared"
)

// Connector-owned onboarding (issue #265): the generic runner over
// connectors.Onboarder. Core hosts the flow — admin routes and rendering,
// token/secret writes, config overlay persistence, the permissions perimeter
// — while the connector owns every provider-specific string, rule, and probe.
//
// The runner applies StateUpdates itself so connectors never write tokens or
// config, and recomputes the status afterwards so responses always reflect
// post-save state. Responses are built from explicit DTOs: a StateUpdate
// carries secret values and must never be serialized back.

// onboardingView is GET /v1/admin/setup/{id}: the declared contract plus the
// live view the panel renders.
type onboardingView struct {
	Spec   shared.OnboardingSpec   `json:"spec"`
	Status shared.OnboardingStatus `json:"status"`
}

// onboardingActionResult is POST /v1/admin/setup/{id}/{action}: the refreshed
// view plus the action's own diagnosis.
type onboardingActionResult struct {
	Spec   shared.OnboardingSpec   `json:"spec"`
	Status shared.OnboardingStatus `json:"status"`
	Probe  *shared.ProbeResult     `json:"probe,omitempty"`
	Detail string                  `json:"detail,omitempty"`
}

// handleAdminOnboarding serves the generic setup routes:
//
//	GET  /v1/admin/setup/{id}          → spec + status
//	POST /v1/admin/setup/{id}/{action} → run one declared verb
//
// Registered under AdminMiddleware: setup writes tokens and config, so it
// stays behind the admin perimeter and out of the agent-facing allowlist.
func (s *Server) handleAdminOnboarding(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/v1/admin/setup/"), "/")
	if rest == "" {
		shared.WriteJSONError(w, http.StatusNotFound, "unknown admin endpoint")
		return
	}
	id, action := rest, ""
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		id, action = rest[:i], strings.Trim(rest[i+1:], "/")
	}

	ob, ok := s.onboarder(id)
	if !ok {
		shared.WriteJSONError(w, http.StatusNotFound, "no setup flow for service "+id)
		return
	}

	switch {
	case action == "" && r.Method == http.MethodGet:
		s.writeOnboardingView(w, r.Context(), ob)
	case action != "" && r.Method == http.MethodPost:
		s.runOnboardingAction(w, r, ob, action)
	default:
		shared.WriteJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// onboarder resolves a registered connector that drives its own setup.
func (s *Server) onboarder(id string) (connectors.Onboarder, bool) {
	conn, ok := s.connectors[id]
	if !ok {
		return nil, false
	}
	ob, ok := conn.(connectors.Onboarder)
	return ob, ok
}

// writeOnboardingView answers with the declared contract and live status.
func (s *Server) writeOnboardingView(w http.ResponseWriter, ctx context.Context, ob connectors.Onboarder) {
	spec := ob.Onboarding()
	shared.WriteJSON(w, onboardingView{
		Spec:   spec,
		Status: s.onboardingStatus(ctx, spec, ob),
	})
}

// runOnboardingAction executes one declared verb, persists whatever state
// changes the connector asked for, then answers with the refreshed view.
func (s *Server) runOnboardingAction(w http.ResponseWriter, r *http.Request, ob connectors.Onboarder, action string) {
	var in shared.OnboardingInput
	if r.Body != nil {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			shared.WriteJSONError(w, http.StatusBadRequest, "invalid body")
			return
		}
		if strings.TrimSpace(string(body)) != "" {
			if err := json.Unmarshal(body, &in); err != nil {
				shared.WriteJSONError(w, http.StatusBadRequest, "invalid JSON body")
				return
			}
		}
	}
	if in.Values == nil {
		in.Values = map[string]string{}
	}

	spec := ob.Onboarding()
	result, err := ob.OnboardingRun(r.Context(), action, in)
	if err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.applyOnboardingUpdates(spec, result.Updates); err != nil {
		shared.WriteJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	shared.WriteJSON(w, onboardingActionResult{
		Spec:   spec,
		Status: s.onboardingStatus(r.Context(), spec, ob),
		Probe:  result.Probe,
		Detail: result.Detail,
	})
}

// applyOnboardingUpdates persists the state changes a connector requested.
// This is the only path from a setup flow to config/token writes (issue #265)
// and it goes through the same SetServiceConnection seam the Immich/YNAB
// admin saves used: secrets into the encrypted store first, overlay second,
// aegis.json never rewritten.
func (s *Server) applyOnboardingUpdates(spec shared.OnboardingSpec, updates []shared.StateUpdate) error {
	if len(updates) == 0 {
		return nil
	}
	touched := false
	for _, u := range updates {
		if u.Connection == nil {
			continue
		}
		entry := serviceConnectionEntry{
			Name:              spec.Connection.Name,
			Action:            spec.Connection.Action,
			Description:       spec.Connection.Description,
			CacheTTL:          spec.Connection.CacheTTL,
			Aliases:           spec.Connection.Aliases,
			Endpoint:          u.Connection.Endpoint,
			Headers:           map[string]string{}, // non-nil: static-credential path
			CanonicalFor:      u.Connection.CanonicalFor,
			ClearCanonicalFor: u.Connection.ClearCanonicalFor,
		}
		for k, v := range u.Connection.SecretHeaders {
			entry.Headers[k] = v
		}
		if err := s.config.SetServiceConnection(spec.ServiceID, entry); err != nil {
			return err
		}
		touched = true
	}
	for _, u := range updates {
		if u.TailscaleAdmin == nil {
			continue
		}
		// The VIP wizard's credential and selections take the same path the
		// admin save used: secret into the encrypted store, settings into the
		// overlay's "tailscale" block (issue #124).
		if err := s.config.SetTailscaleAdmin(tailscaleAdminEntry{
			APIKey:       u.TailscaleAdmin.APIKey,
			Tailnet:      u.TailscaleAdmin.Tailnet,
			HostDeviceID: u.TailscaleAdmin.HostDeviceID,
			HostTag:      u.TailscaleAdmin.HostTag,
		}); err != nil {
			return err
		}
		touched = true
	}
	if touched {
		// Library listings must not serve the old connection's view.
		s.invalidateService(spec.ServiceID)
	}
	return nil
}

// onboardingStatus derives the live view and strips secret values before
// anything is serialized. Connectors are expected to leave Secret fields
// empty; this is the belt to that suspenders.
func (s *Server) onboardingStatus(ctx context.Context, spec shared.OnboardingSpec, ob connectors.Onboarder) shared.OnboardingStatus {
	status := ob.OnboardingStatus(ctx)
	for _, f := range spec.Fields {
		if !f.Secret {
			continue
		}
		if st, ok := status.Fields[f.ID]; ok {
			st.Value = ""
			status.Fields[f.ID] = st
		}
	}
	return status
}
