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
// Step/Check/Next/Suggested arrive with Immich (Phase 2); live guide-step
// state and a TailscaleAdmin state-update kind arrive with the VIP wizard
// (Phase 3). Each shape lands with the flow that uses it.

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
	// WalkthroughLabel, when set, renders a button that opens the spec's
	// guides as a stepped walkthrough. The label is provider copy ("Setup
	// guide"); the modal chrome is the panel's.
	WalkthroughLabel string
	// DetailsLabel, when set, wraps the ladder / form / checklist in a
	// disclosure with this summary (e.g. "Details: guided ladder,
	// connection form, checks").
	DetailsLabel string
	// Connection is the non-secret half of a service-connections entry. Core
	// assembles and persists the entry on save; secrets never appear here.
	Connection ConnectionMetadata
	Fields     []OnboardingField
	// Steps is the guided ladder's declared rows: labels only, state is
	// derived. Rendered in order.
	Steps []OnboardingStepDef
	// Checks is the acceptance checklist's declared rows.
	Checks []OnboardingCheckDef
	// Guides are static copyable blocks: walkthroughs, snippets, checkboxes.
	Guides  []OnboardingGuide
	Actions []OnboardingAction
}

// OnboardingStepDef declares one guided-ladder row. The label is contract
// copy; whether the row is done and its detail are derived per request.
type OnboardingStepDef struct {
	ID    string
	Label string
}

// OnboardingCheckDef declares one acceptance-checklist row.
type OnboardingCheckDef struct {
	ID    string
	Label string
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

// OnboardingOption is one choice in a select or checkbox field. Options on a
// spec are static; options on a field state are the live set.
type OnboardingOption struct {
	ID    string
	Label string
}

// OnboardingField is one form input the admin panel collects.
type OnboardingField struct {
	ID          string
	Label       string
	Kind        string // url | text | password | checkbox | select
	Placeholder string
	Help        string
	Secret   bool
	Required bool
	// Walkthrough marks a field the stepped walkthrough renders on its own
	// page. The card skips it.
	Walkthrough bool
}

// OnboardingGuide is a static copy block. Body is plain text with a minimal
// inline syntax the renderer escapes first, then rewrites: `code` spans and
// [text](https://…) links. Newlines are preserved.
type OnboardingGuide struct {
	ID string
	// Title is optional; empty renders the body alone.
	Title string
	// Short is the walkthrough chip label when it differs from Title.
	Short string
	Body  string
	// Command, when set, renders as a copyable block. Label is its caption
	// and Button the copy-button text (the panel supplies neutral fallbacks).
	Command string
	Label   string
	Button  string
	// Collapsible wraps the guide in a disclosure on the card.
	Collapsible bool
	// Walkthrough marks a guide the stepped walkthrough renders as its own
	// page. The card skips it — it is narrative copy, not panel chrome.
	Walkthrough bool
	// Steps are the guide's declared operator actions: titles and copyable
	// commands the connector owns. Live per-step state arrives in
	// OnboardingStatus.Guides[id].Steps.
	Steps []OnboardingGuideStep
	// LiveLabel names the live per-step status block the panel renders
	// beside the copyable walkthrough.
	LiveLabel string
	// Requirements are the guide's hard preconditions, shown before the steps.
	Requirements []string
}

// OnboardingGuideStep is one declared operator action inside a guide. The
// connector owns the copy; whether the step is done is derived.
type OnboardingGuideStep struct {
	ID      string
	Title   string
	Detail  string
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
	// Walkthrough marks an action the stepped walkthrough renders. The card
	// skips it.
	Walkthrough bool
	// Fields lists the field IDs this action submits.
	Fields []string
}

// OnboardingInput carries the submitted form values for one action. Secret
// values are present here and must never be echoed back.
type OnboardingInput struct {
	Values map[string]string // field id → submitted value (url/text/password)
	Flags  map[string]bool   // field id → checked state (checkbox)
	// Selected names the declared rows an action applies to (e.g. guide
	// step ids to fix). Empty means "every row that needs it".
	Selected []string
}

// OnboardingStatus is the live view the panel renders. Computed per request —
// ladder rows, hints, and probe results are derived state, never persisted.
type OnboardingStatus struct {
	// Fields carries current values (secrets as Set flags only).
	Fields map[string]OnboardingFieldState
	// Steps and Checks are state keyed to the spec's declared rows. Labels
	// come from the spec so provider copy has one owner.
	Steps  []OnboardingStepState
	Checks []OnboardingCheckState
	// Summary is a one-line status the panel joins with " · ".
	Summary []string
	// Hints are short imperative prompts for what to do next.
	Hints []string
	// Next is the single imperative next action (the first undone step).
	Next string
	// Guides carries derived content for spec guides whose copyable block
	// depends on live state (e.g. a bootstrap snippet for the current
	// endpoint). Keyed by OnboardingGuide.ID.
	Guides map[string]OnboardingGuideState
	// Metrics are small named counts derived during status (photo_count,
	// budget_count) for the summary and panel to show.
	Metrics map[string]int
	// Probe is the most recent diagnosis when the flow keeps one.
	Probe *ProbeResult
}

// OnboardingStepState is the derived state of one spec step.
type OnboardingStepState struct {
	ID string
	// OK reports the step as satisfied. Current marks the first undone step.
	OK      bool
	Current bool
	Detail  string
}

// OnboardingCheckState is the derived state of one spec check.
type OnboardingCheckState struct {
	ID     string
	OK     bool
	Detail string
}

// OnboardingGuideState carries derived content for one spec guide. Prose is
// static contract copy; the copyable block and per-step state are derived.
type OnboardingGuideState struct {
	// Command overrides the guide's copyable block when it is generated.
	Command string
	// Steps is live state keyed to the guide's declared steps.
	Steps []OnboardingGuideStepState
	// Summary is the one-line live status the panel joins with " · ".
	Summary []string
	// Checked reports that live verification ran.
	Checked bool
	// Error is a flow-level diagnostic (e.g. no API key configured).
	Error string
}

// OnboardingGuideStepState is the live state of one declared guide step.
type OnboardingGuideStepState struct {
	ID string
	OK bool
	// Checked reports that this step was verified.
	Checked bool
	// Fixable reports that the flow can repair this step automatically.
	Fixable bool
	// NeedsFix is Checked && !OK && Fixable.
	NeedsFix bool
	// Detail is the live diagnosis for this step.
	Detail string
	// Command is the copyable fallback, carried so the panel can render a
	// step without the spec in hand.
	Command string
	// Title is the step's copy, carried for the same reason.
	Title string
}

// OnboardingFieldState is the derived state of one spec field.
type OnboardingFieldState struct {
	ID string
	// Value is the non-secret current value. Empty for Secret fields.
	Value string
	// Set reports whether a secret field holds a value.
	Set bool
	// Checked is the state of a checkbox field.
	Checked bool
	// Options are the live choices for a select field (e.g. host candidates).
	Options []OnboardingOption
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
	// Suggested is a normalized value when the submitted input was
	// ambiguous (e.g. an endpoint missing its scheme or /api suffix).
	Suggested string
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
	// Detail is what the action did, in operator language (e.g. an apply
	// verb's fixed/skipped/failed report).
	Detail string
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
	// TailscaleAdmin persists the VIP-wizard credential and the operator's
	// tailnet/host selections (issue #124).
	TailscaleAdmin *TailscaleAdminUpdate
}

// TailscaleAdminUpdate is the VIP-wizard state a save persists: the Admin
// API credential (a secret, stored encrypted) and the operator's tailnet /
// host selections (stored in the service-connections overlay).
type TailscaleAdminUpdate struct {
	APIKey       string
	Tailnet      string
	HostDeviceID string
	HostTag      string
}

// Named setup secrets and settings namespaces. Core resolves these; the
// strings are the seam's vocabulary, not provider copy.
const (
	// SetupSecretTailscaleAPIKey is the operator's Tailscale Admin API token.
	SetupSecretTailscaleAPIKey = "tailscale.api_key"
	// SetupFlowTailscale is the settings namespace for the VIP wizard.
	SetupFlowTailscale = "tailscale"
)

// ConnectionUpdate carries the secret-bearing values of a connection save.
type ConnectionUpdate struct {
	Endpoint          string
	SecretHeaders     map[string]string // header name → secret value
	CanonicalFor      []string
	ClearCanonicalFor bool
}
