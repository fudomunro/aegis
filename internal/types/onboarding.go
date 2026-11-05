package types

// Connector-owned onboarding (issue #265): the contract shapes for advanced
// setup flows — credential/config inputs, verification probes, copyable
// guidance, status reporting. Connectors declare and drive these; core hosts
// them generically (admin routes and rendering, token/secret writes, config
// overlay persistence, the permissions perimeter).
//
// This is the same split as the maturity catalog (#247): connectors declare,
// core serves. Everything here is pure data — no I/O, no secrets in flight
// except inside OnboardingInput (submitted form values) and ConnectionUpdate
// (values core alone persists).
//
// Shape phasing: Field/Guide/Summary land with the YNAB flow (Phase 1);
// Step/Check/Next arrive with Immich (Phase 2); live guide-step state and a
// TailscaleAdmin state-update kind arrive with the VIP wizard (Phase 3).

// ActionKind classifies a declared onboarding verb for the core runner.
type ActionKind string

const (
	// ActionProbe diagnoses a candidate or saved connection. Read-only.
	ActionProbe ActionKind = "probe"
	// ActionSave returns a StateUpdate for core to persist. The connector
	// never writes tokens or config itself.
	ActionSave ActionKind = "save"
	// ActionCheck re-runs a flow's acceptance checklist. Read-only.
	ActionCheck ActionKind = "check"
	// ActionApply mutates an external system (probes, provider-side fixes)
	// and may also return StateUpdates.
	ActionApply ActionKind = "apply"
)

// ActionPlacement is where the admin panel renders an action's button.
type ActionPlacement string

const (
	// ActionInHeader places the button beside the panel title.
	ActionInHeader ActionPlacement = "header"
	// ActionInForm places the button under the form fields.
	ActionInForm ActionPlacement = "form"
)

// OnboardingSpec is a connector's declared setup contract. The admin panel
// renders it generically; every provider-visible string lives here or in the
// status the connector computes — never in web/admin.html.
type OnboardingSpec struct {
	ServiceID   string
	DisplayName string
	// Title is the panel heading (e.g. "YNAB — budget transactions").
	Title string
	// Connection is the non-secret half of a service-connections entry. Core
	// assembles and persists the entry on save; secrets never appear here.
	Connection ConnectionMetadata
	Fields     []OnboardingField
	// Guides are static copyable blocks: walkthroughs, snippets, checkboxes.
	Guides  []OnboardingGuide
	Actions []OnboardingAction
}

// ConnectionMetadata is the non-secret half of a service-connections entry
// (mirrors core's serviceConnectionEntry without the header values).
type ConnectionMetadata struct {
	Name        string
	Action      string
	Description string
	CacheTTL    string
	Aliases     []string
	// SecretHeaders names the header fields a save may set (e.g. X-API-Key,
	// Authorization). Values arrive in OnboardingInput and are persisted by
	// core; they are never echoed back.
	SecretHeaders []string
}

// OnboardingField is one form input the admin panel collects.
type OnboardingField struct {
	ID          string
	Label       string
	Kind        string // url | text | password
	Placeholder string
	Help        string
	Secret      bool
	Required    bool
}

// OnboardingGuide is a static copy block. Body is plain text with a minimal
// inline syntax the renderer escapes first, then rewrites: `code` spans and
// [text](https://…) links. Newlines are preserved.
type OnboardingGuide struct {
	ID string
	// Title is optional; empty renders the body alone.
	Title string
	Body  string
	// Command, when set, renders as a copyable command block.
	Command string
}

// OnboardingAction is one declared verb and its button.
type OnboardingAction struct {
	ID    string // route segment: "probe", "save", …
	Label string // button text
	// BusyLabel is the in-flight text (e.g. "Testing YNAB API…"). The panel
	// falls back to "Working…" when empty.
	BusyLabel string
	Kind      ActionKind
	Placement ActionPlacement
	// Fields lists the field IDs this action submits.
	Fields []string
}

// OnboardingInput carries the submitted form values for one action. Secret
// values are present here and must never be echoed back.
type OnboardingInput struct {
	Values map[string]string // field id → submitted value
}

// OnboardingStatus is the live view the panel renders. Computed per request —
// ladder rows, hints, and probe results are derived state, never persisted.
type OnboardingStatus struct {
	// Fields carries current values (secrets as Set flags only).
	Fields map[string]OnboardingFieldState
	// Summary is a one-line status the panel joins with " · ".
	Summary []string
	// Hints are short imperative prompts for what to do next.
	Hints []string
	// Probe is the most recent diagnosis when the flow keeps one.
	Probe *ProbeResult
}

// OnboardingFieldState is the derived state of one spec field.
type OnboardingFieldState struct {
	ID string
	// Value is the non-secret current value. Empty for Secret fields.
	Value string
	// Set reports whether a secret field holds a value.
	Set     bool
	Valid   bool
	Error   string
}

// ProbeResult is a connection diagnosis. Code classifies the outcome for the
// panel; Message and Hint are actionable copy. Never carries secrets.
type ProbeResult struct {
	OK bool
	// Status is the upstream HTTP status when one was reached.
	Status int
	// Code classifies the outcome (e.g. "unauthorized", "unreachable").
	Code string
	// Message is what happened, in operator language.
	Message string
	// Hint is the actionable next step, when there is one.
	Hint string
	// Metrics are small named counts (e.g. budget_count, photo_count).
	Metrics map[string]int
}

// OnboardingResult is what an action returns. State changes are requests to
// core — connectors never write tokens or config themselves. The runner
// applies Updates first and then recomputes the status, so the response
// always reflects post-save state; a connector cannot and need not predict it.
type OnboardingResult struct {
	// Probe is the action's own diagnosis (probe / apply verbs).
	Probe *ProbeResult
	// Updates are what core must persist before answering.
	Updates []StateUpdate
}

// StateUpdate is a request for core to write state a connector cannot. The
// runner applies each kind; new kinds join with a core apply hook (a
// TailscaleAdmin kind arrives with the VIP wizard, Phase 3 of #265).
type StateUpdate struct {
	// Connection persists the entry core assembles from OnboardingSpec
	// (metadata) plus these values. ClearCanonicalFor mirrors the uncheck
	// semantics of admin saves: dropping the claim must strip it.
	Connection *ConnectionUpdate
}

// ConnectionUpdate carries the secret-bearing values of a connection save.
type ConnectionUpdate struct {
	Endpoint          string
	SecretHeaders     map[string]string // header name → secret value
	CanonicalFor      []string
	ClearCanonicalFor bool
}
