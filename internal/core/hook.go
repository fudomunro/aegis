package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/fudomunro/aegis/internal/core/hookhealth"
	"github.com/fudomunro/aegis/internal/core/inject"
	"github.com/fudomunro/aegis/internal/kinds"
	"github.com/fudomunro/aegis/internal/library"
	"github.com/fudomunro/aegis/internal/shared"
)

// Aperture request payload for pre_request hooks.
type PreRequestPayload struct {
	Metadata    RequestMetadata `json:"metadata"`
	RequestBody json.RawMessage `json:"request_body,omitempty"`
	UserMessage string          `json:"user_message,omitempty"`
}

type RequestMetadata struct {
	LoginName string `json:"login_name"`
	UserAgent string `json:"user_agent"`
	URL       string `json:"url"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	Tailnet   string `json:"tailnet_name"`
	NodeID    string `json:"stable_node_id"`
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
}

// Guardrail response actions.
const (
	ActionAllow  = "allow"
	ActionBlock  = "block"
	ActionModify = "modify"
	// ActionSkip is a hook-health observation only (not sent to Aperture):
	// matching was skipped because the message looked like development work
	// or hit a configured skip pattern (issue #205). Aperture always sees allow.
	ActionSkip = "skip"
)

type GuardrailResponse struct {
	Action string `json:"action"`
	// RequestBody is the replacement request body for ActionModify. Aperture
	// requires this field name; a `body` key is treated as allow.
	RequestBody json.RawMessage `json:"request_body,omitempty"`
	Message     string          `json:"message,omitempty"` // human-readable reason
}

// recordHook records a pre-request hook outcome in both the admin ring and
// the Prometheus counters (issue #105).
func (s *Server) recordHook(obs hookhealth.HookObservation) {
	if s == nil {
		return
	}
	if s.hookHealth != nil {
		s.hookHealth.Record(obs)
	}
	if s.prom != nil {
		s.prom.ObserveHook(obs.Action)
	}
}

// requestMethod returns the HTTP method or empty.
// warnIdentityMismatch logs when Aperture's claimed user differs from the
// Tailscale peer that actually called Aegis (shared tailnet / bridge risk).
func warnIdentityMismatch(logger *slog.Logger, apertureLogin string, r *http.Request) {
	id := PeerIdentityFrom(r)
	if id == nil || apertureLogin == "" || id.LoginName == "" {
		return
	}
	if !strings.EqualFold(id.LoginName, apertureLogin) {
		logger.Warn("identity mismatch: aperture login does not match tailnet peer",
			"aperture_login", apertureLogin,
			"tailnet_login", id.LoginName,
			"identity_source", id.Source,
			"path", r.URL.Path,
		)
	}
}
func (s *Server) HandlePreRequest(w http.ResponseWriter, r *http.Request) {
	// Cap the hook body so a huge conversation cannot exhaust memory (issue #97).
	r.Body = http.MaxBytesReader(w, r.Body, maxHookBodyBytes)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			slog.Error("pre-request body too large", "limit", maxErr.Limit)
			s.recordHook(hookhealth.HookObservation{Action: "parse_fail", Host: requestHost(r)})
			shared.WriteJSONError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit))
			return
		}
		slog.Error("failed to read request body", "error", err)
		s.recordHook(hookhealth.HookObservation{Action: "parse_fail", Host: requestHost(r)})
		writeGuardrailResponse(w, GuardrailResponse{
			Action:  ActionAllow,
			Message: "failed to read request body",
		})
		return
	}

	var payload PreRequestPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		slog.Error("failed to parse payload", "error", err)
		s.recordHook(hookhealth.HookObservation{Action: "parse_fail", Host: requestHost(r)})
		writeGuardrailResponse(w, GuardrailResponse{
			Action:  ActionAllow,
			Message: "failed to parse payload",
		})
		return
	}
	obs := hookhealth.HookObservation{
		Host:           requestHost(r),
		Action:         ActionAllow,
		HasUserMessage: strings.TrimSpace(payload.UserMessage) != "",
		HasRequestBody: len(payload.RequestBody) > 0,
		Model:          payload.Metadata.Model,
		Provider:       payload.Metadata.Provider,
		Login:          payload.Metadata.LoginName,
	}
	defer func() { s.recordHook(obs) }()

	logger := slog.With(
		"request_id", payload.Metadata.RequestID,
		"user", payload.Metadata.LoginName,
		"model", payload.Metadata.Model,
	)
	logger = withPeerLog(logger, r)
	warnIdentityMismatch(logger, payload.Metadata.LoginName, r)

	// Skip matching for development work (issue #205). Coding agents that
	// mention service names as identifiers must not be redirected to the
	// library surface. Fail-open: the request passes through unchanged.
	if reason := s.config.shouldSkipMatching(payload.UserMessage); reason != "" {
		logger.Debug("skipping pre-request matching", "reason", reason)
		obs.Action = ActionSkip
		obs.SkipReason = reason
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	matched := s.config.FindMatchingServices(payload.UserMessage)
	// Data-scope isolation: never match (or block on) services outside this
	// principal's dataset partition (docs/data-scopes.md).
	if scope := shared.DataScopeFrom(r); scope != nil {
		visible := make([]Service, 0, len(matched))
		for i := range matched {
			if shared.ServiceVisible(matched[i].Dataset, scope) {
				visible = append(visible, matched[i])
			}
		}
		matched = visible
	}

	// Surface matching unified library endpoints alongside service proxy hints.
	// Issue #125: photo questions that named a service still need /v1/library/photos
	// (and the no-bypass policy); do not gate library hints on zero service matches.
	libraryInstructions := s.library().LibraryHintsForMessage(r, payload.UserMessage)
	// Local mail archive search is a separate surface from live provider listing.
	libraryInstructions = append(libraryInstructions, s.library().MailArchiveHints(r, payload.UserMessage)...)

	if len(matched) == 0 && len(libraryInstructions) == 0 {
		logger.Debug("no services matched, allowing")
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	serviceIDs := make([]string, len(matched))
	for i, svc := range matched {
		serviceIDs[i] = svc.ID
	}
	if len(matched) > 0 {
		logger.Info("services matched", "services", serviceIDs)
	}

	// If any matched service is blocked, reject the request.
	for _, svc := range matched {
		if svc.Action == "block" {
			logger.Warn("request blocked by service policy", "service", svc.ID, "reason", svc.Description)
			obs.Action = ActionBlock
			writeGuardrailResponse(w, GuardrailResponse{
				Action:  ActionBlock,
				Message: fmt.Sprintf("blocked: access to %s is not allowed. %s", svc.Name, svc.Description),
			})
			return
		}
	}

	// Build instructions pointing agents at Aegis's data surface (issue #119).
	// Library is primary; connector instructions only add content/ingest gaps.
	var instructions []string
	for _, svc := range matched {
		if svc.Action != "inject" {
			continue
		}
		// Skip services that deny every kind — nothing useful to inject.
		if library.AllDataDenied(s.requestGrants(r)) {
			logger.Info("skipping inject: all kinds denied by permission", "service", svc.ID)
			continue
		}
		// Use connector-specific instructions if available.
		if connector, ok := s.connectors[svc.ID]; ok {
			instructions = append(instructions, connector.SystemInstructions(s.baseURLFor(r)))
		} else {
			instruction := fmt.Sprintf(
				"When working with %s, use Aegis's unified library endpoints "+
					"(/v1/library/{kind}) for the user's data. Do not call %s directly. "+
					"Service provenance lives in sources[] for sync — not a separate services API.",
				svc.Name, svc.Name,
			)
			instructions = append(instructions, instruction)
		}
		if notice := library.PermissionNotice(s.requestGrants(r)); notice != "" {
			instructions = append(instructions, notice)
		}
	}

	views := make([]kinds.ServiceView, 0, len(matched))
	for _, svc := range matched {
		views = append(views, serviceViewOf(svc))
	}
	instructions = append(instructions, s.library().WritePolicyNotices(views)...)
	instructions = append(instructions, libraryInstructions...)

	if len(instructions) == 0 {
		logger.Debug("no injectable services, allowing")
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	// Inject instructions into the request body.
	systemContent := strings.Join(instructions, "\n\n")
	result, err := inject.RequestInstructions(payload.RequestBody, systemContent,
		payload.Metadata.Provider, payload.Metadata.Model)
	obs.BodyShape = result.Shape
	if err != nil {
		if errors.Is(err, inject.ErrUnrecognizedBodyShape) {
			logger.Warn("unrecognized request body shape, allowing without injection",
				"provider", payload.Metadata.Provider, "model", payload.Metadata.Model)
			obs.Action = ActionAllow
			writeGuardrailResponse(w, GuardrailResponse{
				Action:  ActionAllow,
				Message: "unrecognized request body shape; instructions not injected",
			})
			return
		}
		logger.Error("failed to inject system message", "error", err)
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}
	if result.AlreadyPresent {
		logger.Debug("instructions already present, skipping injection")
		obs.Action = ActionAllow
		writeGuardrailResponse(w, GuardrailResponse{Action: ActionAllow})
		return
	}

	logger.Info("request modified with proxy instructions",
		"services", len(instructions), "body_shape", result.Shape)
	obs.Action = ActionModify
	writeGuardrailResponse(w, GuardrailResponse{
		Action:      ActionModify,
		RequestBody: result.Body,
	})
}

func writeGuardrailResponse(w http.ResponseWriter, resp GuardrailResponse) {
	w.Header().Set("Content-Type", "application/json")
	if resp.Action == ActionBlock {
		w.WriteHeader(http.StatusForbidden)
	}
	json.NewEncoder(w).Encode(resp)
}
