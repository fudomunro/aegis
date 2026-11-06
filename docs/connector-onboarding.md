# Connector-owned onboarding: the setup seam

Status: **done** — all four phases have landed (#268 seam + YNAB, #269
Immich setup, #270 VIP wizard, and this cleanup). The note is the contract
for the seam; the worked examples are the Immich and YNAB connectors.

Related issues: **#265** (this note), **#254** (the cleanup this completes),
**#39** / **#121** (Immich setup), **#141** (YNAB setup), **#124** (Immich
VIP-Service wizard).
Related product rules: fail-open; Aegis owns OAuth; secrets omitted from
API/UI responses; admin stays in-tailnet; named-service connectors; coverage
floor 80%.

---

## 1. Evaluation

### What #265 asks for

The #254 cleanup moved connector-owned metadata, mirror placement, and
freshness probes into `internal/connectors` (#261–#264). The biggest remaining
provider-owned surface in core is **setup/onboarding**:

| File | Lines | What it is |
|------|-------|------------|
| `internal/core/immich_setup.go` | ~910 | Immich connection wizard: API-key checkbox contract, endpoint repair, probes, acceptance checklist, guided ladder, config save |
| `internal/core/tailscale_admin.go` | ~1025 | Immich VIP-Service wizard: Tailscale Admin API client + step check/fix + a second credential write path |
| `internal/core/ynab_setup.go` | ~290 | YNAB personal-access-token flow (plus a Quicken import block that is not setup) |

Direction: **a connector should be able to declare and drive its own
onboarding** — credential/config inputs, verification probes, external-tool
guidance, status reporting — while **core hosts the flow generically**: admin
routes and rendering, token/secret writes (Aegis owns OAuth), config/overlay
persistence, and the permissions perimeter.

### Why this needs design first

The flows are entangled with four core-owned surfaces at once:

```text
immich_setup.go ──► admin HTTP handlers ──► AdminMiddleware (perimeter)
       │                  │
       ├──► web/admin.html (5.4k lines, bespoke markup + bespoke JS per provider)
       ├──► service_connections.go / tokens.go (writes; secrets at rest)
       └──► connectors (ListUserPhotos, NewYNABConnector) for probes
```

The original #15 note called Immich out as *not* fitting the connectors
structure. A naive `git mv` into `internal/connectors` would drag admin
routing, HTML, and config writes with it — the opposite of the seam.

### The drift this must end

Provider contracts currently have three owners that must be kept in step by
hand (and are pinned by tests that assert the duplication):

| Contract | Go | `web/admin.html` | `docs/` |
|----------|----|------------------|---------|
| Immich API-key checkbox set (one key for Aegis + immich-go) | `immichAPIKeyPermissions` (`immich_setup.go:22`) | hardcoded `<pre>` block (`admin.html:1106-1131`) | table in `immich-live-testing.md`, rules in `connect/immich.md` |
| Starter `.env` for Immich compose | — | copy block (`admin.html:1032-1052`) | `immich-starter.env` + walkthrough |
| Endpoint repair rules | `normalizeImmichEndpoint` | wizard copy | `connect/immich.md:22-27` |
| Probe codes + hints (`unauthorized`, `wrong_path`, …) | `probeImmich` | JS strings | troubleshooting table |
| VIP-Service step table (`acl_tag` … `connect`) | `immichTailnetGuide` + `tsCheck*` | tailnet wizard markup | `connect/immich.md:41-48` |
| YNAB PAT instructions | `ynabSetupStatus` hints | YNAB card copy | `connect/ynab.md` |

Three admin surfaces (Immich, YNAB, and the local import stores) also repeat
the same *shape* — status GET + save + probe/act — as hand-written markup and
hand-written `fetch` calls.

One half of this is already solved. #266 gave the import stores a spec-driven
admin runner (`importStoreAdmin` + `HandleAdminImportStore`,
`internal/core/import_admin.go`) so the HTTP plumbing exists once and each
store declares its status/import callbacks. Onboarding is the same idea one
level richer — forms and state writes instead of status + import — and should
read as its sibling, not as a second invention.

---

## 2. Target architecture

### The seam

Shapes live in `internal/types` (pure data, like `ConnectionMaturity`).
The optional interface lives in `internal/connectors` beside
`ServiceConnector`. Core gets one generic runner.

Shapes live in `internal/types` (pure data, like `ConnectionMaturity`).
The optional interface lives in `internal/connectors` beside
`ServiceConnector`. Core gets one generic runner.

```go
// internal/types/onboarding.go — contract shapes, no I/O.

type OnboardingSpec struct {
	ServiceID   string
	DisplayName string
	// Title is the panel heading (e.g. "YNAB — budget transactions").
	Title      string
	Connection ConnectionMetadata // non-secret half of a service-connections entry
	Fields     []OnboardingField  // form inputs: url, text, password
	Guides     []OnboardingGuide  // copyable walkthroughs, snippets, checkbox lists
	Actions    []OnboardingAction // declared verbs: probe, save, check, …
}

type OnboardingAction struct {
	ID        string       // route segment: "probe", "save", …
	Label     string       // button text
	BusyLabel string       // in-flight text ("Testing YNAB API…")
	Kind      ActionKind   // probe | save | check | apply
	Placement ActionPlacement // header | form
	Fields    []string     // field IDs this action submits
}

// Body is plain text with a minimal inline syntax the renderer escapes
// first, then rewrites: `code` spans and [text](https://…) links. That is
// what the current copy needs (inline code and links) — not an HTML hatch.
type OnboardingGuide struct {
	ID, Title, Body, Command string
}

type OnboardingInput struct{ Values map[string]string }

type OnboardingStatus struct {
	Fields  map[string]OnboardingFieldState // non-secret values, Set flags
	Summary []string                        // one-line status, joined with " · "
	Hints   []string
	Probe   *ProbeResult
}

type ProbeResult struct {
	OK      bool
	Status  int            // upstream HTTP status, when one was reached
	Code    string         // "unauthorized" | "unreachable" | …
	Message string         // what happened, in operator language
	Hint    string
	Metrics map[string]int // budget_count, photo_count, …
}

type OnboardingResult struct {
	Probe   *ProbeResult   // the action's own diagnosis
	Updates []StateUpdate  // what core must persist before answering
}

type StateUpdate struct {
	Connection     *ConnectionUpdate     // → SetServiceConnection
	TailscaleAdmin *TailscaleAdminUpdate // → SetTailscaleAdmin (VIP wizard)
}

type ConnectionUpdate struct {
	Endpoint          string
	SecretHeaders     map[string]string // header name → secret value
	CanonicalFor      []string
	ClearCanonicalFor bool
}
```

```go
// internal/connectors/host.go — beside ServiceConnector.

type Onboarder interface {
	Onboarding() types.OnboardingSpec
	OnboardingStatus(ctx context.Context) types.OnboardingStatus
	OnboardingRun(ctx context.Context, action string, in types.OnboardingInput) (types.OnboardingResult, error)
}
```

**Status is recomputed by the runner, not predicted by the connector.** A save
action cannot see the post-save world (it never writes state), so
`OnboardingResult` carries no status: the runner applies `Updates` first and
then calls `OnboardingStatus`. The response therefore always reflects
post-save state, and the connector stays free of "what will core do" logic.

**Shape phasing.** Phase 1 ships what YNAB needs — fields, guides, summary,
hints, probe, the `save`/`probe` verbs. `Step`/`Check`/`Next` arrive with
Immich (Phase 2); live guide-step state and the `TailscaleAdmin` update kind
arrive with the VIP wizard (Phase 3). Each shape lands with the flow that
uses it rather than as unused contract.

### Decision: core-owned templates, connector-owned content

**A connector never ships HTML or JS.** It ships structured content —
field defs, ladder/check labels, copyable guide bodies and commands, probe
hints. `web/admin.html` renders every setup surface with **one** generic
panel (form + ladder + checklist + copyable guides + action buttons) fed by
the spec and status JSON.

Rejected:

- *Connector-supplied HTML fragments.* Violates the "HTML in embed files"
  rule, opens an XSS surface in the admin UI, and recreates the drift this
  work removes.
- *Keep bespoke panels per provider.* That is the status quo: 5.4k lines of
  admin.html where the Immich card, the 7-page wizard, and the YNAB card each
  duplicate provider copy that also lives in Go and in docs.

Content rules that make the seam hold:

- Every provider-visible string (labels, hints, probe copy, guide bodies) is
  authored **once**, in the connector. The generic renderer adds no wording.
- If a flow needs a visual the generic panel cannot express, the spec grows a
  **new generic capability** (e.g. live `select` options for the VIP host
  picker, guide sub-steps with per-step fix state). There is no custom-HTML
  escape hatch.
- The admin JSON envelope changes with this work. `admin.html` is the only
  consumer and moves with it in the same PR; agent-facing API is untouched.

### Decision: setup state stays in the existing overlays

Setup is a **derived view**, not its own store.

- Connection facts (endpoint, secret headers, `canonical_for` claim) keep
  landing in `service_connections.json` + `tokens.json` via
  `SetServiceConnection` — unchanged ordering, 0600, secrets never in the
  overlay, `aegis.json` never rewritten.
- The Tailscale Admin API key keeps its current home (`tokens.json` via
  `SetTailscaleAdmin`, presence-only in responses).
- Ladder rows, checks, hints, next-step, and probe results are **computed per
  request** — true today (`immichGuidedSteps` derives from status; probes are
  ephemeral) and kept.
- If a flow ever needs an operator *choice* that is not a connection fact
  (e.g. "don't nag me about VIP again"), it becomes a small optional `setup`
  object on that service's connection entry. **No `setup_state.json`.**

Rejected: a dedicated setup-state file. It would dual-write connection facts
that already have one owner, and would drift from them the way admin.html
drifts from Go today.

### What moves, what stays

| | `internal/connectors` | `internal/core` |
|---|---|---|
| **Moves** | checkbox set + rationale, endpoint normalization, probe codes/hints, acceptance checks, ladder/next-step logic, config snippet + aliases, guide bodies (immich-go, starter `.env`, VIP walkthrough), Tailscale Admin API client + VIP step check/fix | |
| **Stays** | | generic runner (`internal/core/onboarding.go`): `/v1/admin/setup/{id}` routes, spec/status JSON, state-update apply, cache clear, secret stripping, fail-open error handling |
| **Stays** | | `SetServiceConnection` / `SetTailscaleAdmin` / token store / `AdminMiddleware` (write seams + perimeter) |
| **Stays** | | `web/admin.html` as the generic renderer (one panel component) |
| **Stays** | | Quicken/Signal/Timeline **import** admin (`import_admin.go`) — related shape, different lifecycle, out of scope |

### Host additions (read seams)

Connectors already read `ServiceConfig`, `GetValidToken`, `BaseURL`. Host
grows one narrow read per phase — only as each flow needs it, never as an
unused method:

```go
// Phase 1 (YNAB readiness bit)
LibraryReadable(kind string) bool    // provider registered AND type policy allows reads

// Phase 2 (Immich canonical claim + conflict copy)
ServiceInfo.CanonicalFor []string    // am I the canonical owner of these kinds?
CanonicalOwner(kind string) string   // who claims it when I do not

// Phase 3 (VIP wizard credential + operator settings)
SetupSecret(name string) string          // e.g. the Tailscale Admin API key
SetupSettings(name string) map[string]string // tailnet, host device, host tag
```

No write methods join `Host`. That is the point of `StateUpdate`.

### Dependency shape

```text
internal/types        onboarding shapes, ConnectionMaturity, models   (pure)
      ▲                       ▲
      └───────────┬───────────┘
                  │
internal/connectors   Onboarder + ServiceConnector + Host (interface)
                  │      provider copy, probes, guides, fix steps
                  │
                  │  core implements connectors.Host (existing seam)
                  │  core calls Onboarder methods; never the reverse
                  ▼
internal/core         onboarding runner + admin routes + state writes
                  │      (SetServiceConnection, SetTailscaleAdmin, tokens,
                  │       AdminMiddleware, cache clear, secret stripping)
                  ▼
web/admin.html        one generic setup panel (no provider copy)
```

Same pattern as the maturity catalog (#247): connectors *declare*, core
*grades and serves*. Same pattern as `localstore.go` (#266): shared machinery
(core runner, admin rendering) separated from per-provider quirks (copy,
probes, fix steps). Same pattern as `importStoreAdmin` (#266): one generic
admin handler fed by a per-surface declaration — onboarding adds fields,
actions, and state writes to that shape.

---

## 3. Phased plan

One concern per branch, worktrees `../aegis-issue-265-<slug>`, tests move with
the code, coverage floor never drops, copy ported verbatim.

### Phase 0 — this note (`docs/issue-265-onboarding-seam`)

Comment on #265 linking this plan. No behavior change.

### Phase 1 — seam + YNAB through it (`refactor/issue-265-onboarding-seam`)

The smaller flow proves the seam before the stress case. **Lands with this
note.**

1. `internal/types/onboarding.go` — the shapes above.
2. `internal/connectors/host.go` — `Onboarder` + `Host.LibraryReadable`.
3. `internal/core/onboarding.go` — generic runner on
   `GET /v1/admin/setup/{id}` and `POST /v1/admin/setup/{id}/{action}`, under
   `AdminMiddleware`. Applies `StateUpdate`s through the existing
   `SetServiceConnection` seam, clears the library cache, and strips secret
   field values before anything is serialized. Responses are explicit DTOs —
   a `StateUpdate` carries secrets and is never marshalled back.
4. YNAB onboarding lives in `internal/connectors/ynab_onboarding.go`.
   The Quicken import admin sharing `ynab_setup.go` was already a thin
   `importStoreAdmin` instance; it moves to `quicken_admin.go` beside the
   other store instances so the file can go. `/v1/admin/ynab/*` is replaced by
   the generic route (`admin.html` is the only consumer).
5. `admin.html` gained one generic setup panel that renders a spec. The YNAB
   card is its first instance; its heading, intro copy, field labels,
   placeholders, button labels and the in-flight "Testing YNAB API…" all move
   into the connector's spec and are rendered from it.
6. Tests split with the code: `internal/connectors/ynab_onboarding_test.go`
   pins the spec copy and probe classification;
   `internal/core/onboarding_test.go` covers the runner (auth, routing, save →
   encrypted store, secrets absent from responses and from
   `service_connections.json`).

**Done when:** YNAB setup behaves as before through the seam; no YNAB setup
copy remains in `admin.html`; `go vet`, race tests, 80% floor, staticcheck
clean.

### Phase 2 — Immich setup core (`refactor/issue-265-immich-setup`)

The stress case: checkbox contract, endpoint repair, 7-row acceptance
checklist, guided ladder. **Lands with this phase.**

1. `internal/connectors/immich_onboarding.go` — the permission set is
   *generated* from grouped declarations (`immichPermissionGroups`), so the
   copyable list an operator ticks in Immich and the set the connector
   enforces are the same data. Beside it: `NormalizeImmichEndpoint`,
   `probeImmich` codes + hints, `runImmichChecks`, `immichGuidedSteps` /
   `immichNextStepFrom`, `immichConfigSnippet`, `immichStampSummary`.
2. `immich_setup.go` disappears. Its handlers become runner actions
   (`probe`, `save`, `check`); unchecking `canonical_for` still clears the
   claim (`ClearCanonicalFor` semantics preserved).
3. The **card** becomes a generic onboarding panel. The generic renderer
   grows the capabilities Immich needs: checkbox fields, the guided ladder,
   the acceptance checklist, the "Next" card, copyable command blocks, and a
   `WalkthroughLabel` that opens the guide pages.
4. **Wizard scope note.** The 7-page wizard modal keeps its own layout for
   now — it is presentation, not the seam. What this phase removes from it is
   the contract: its API-key page renders the checkbox block from the
   connector's declaration, and its Connect/Verify pages submit the same
   `/v1/admin/setup/immich/{probe,save,check}` verbs the card does. Its
   narrative pages and the VIP page are Phase 3/4 debt.
5. Contract pins rehome: the permission-union test moves beside the
   connector; `TestAdminHTML_ImmichSetupPanel` becomes
   `TestImmichAPIKeyContract_SingleOwner`, which asserts the permission names
   appear in **neither** `admin.html` **nor** `docs/`, and that the old
   `/v1/admin/immich/{setup,config,test,check}` routes are gone.
6. `docs/connect/immich.md` and `docs/immich-live-testing.md` stop
   enumerating the checkbox set and point at Admin → Services → Immich.

**Done when:** Immich setup behaves as before through the seam, the checkbox
set has exactly one owner (the connector), and the hygiene test enforces it.

### Phase 3 — VIP-Service wizard (`refactor/issue-265-tailnet-wizard`)

The issue #124 wizard joins the seam. **Lands with this phase.**

1. `internal/connectors/tailscale.go` — the generic Tailscale Admin API
   client (`Do`/`DoRaw`, HuJSON policy parsing, device list, tag
   declaration) as shared machinery, the `localstore.go` analogue for VIP
   flows. Any connector that hosts a VIP Service reuses it.
2. `internal/connectors/immich_tailnet.go` — Immich's `svc:immich` /
   `tag:immich-host` walkthrough copy, the live step checks, and the auto-fix
   rules. `tsFixConnect` returns a `ConnectionUpdate` instead of writing
   config, and the host-selection save becomes a `TailscaleAdminUpdate`.
   `ImmichTailnetLiveStatus` becomes `OnboardingGuideState` (live steps,
   summary, error); the host picker becomes a `select` field with live
   options.
3. `tailscale_admin.go` and `immich_tailnet_guide.go` are deleted; core keeps
   only `SetTailscaleAdmin`, reached through `StateUpdate.TailscaleAdmin`.
   `/v1/admin/immich/*` is gone entirely — the wizard's verbs are
   `tailnet_save` / `tailnet_check` / `tailnet_apply` on the generic runner.
4. Wizard page 5 renders from the spec and guide state: its headings, field
   labels, help text, step titles/details/commands and button labels are all
   declared by the connector. `TestImmichVIPContract_SingleOwner` rejects
   those contract strings in `admin.html` **and** in core.

**Done when:** the wizard's steps, checks, auto-fix rules and copy are
unchanged and owned by the connector, and core holds no `svc:immich`
knowledge.

### Phase 4 — cleanup (`docs/issue-265-onboarding-cleanup`) — **landed**

1. The wizard's narrative pages move into the connector as structured guide
   blocks (prose, headings, lists, copyable commands), so `web/admin.html`
   renders setup through one generic panel and a generic stepped walkthrough
   and contains **no provider copy**. Where the same field or verb is worded
   differently in the compact card and the guided walkthrough (a placeholder
   that names a step, a fuller button label), the spec carries both.
2. `TestAdminHTML_RestatesNoOnboardingCopy` walks every string the
   connectors declare in their specs and rejects each one in `admin.html`.
   A small, documented allowance covers panel vocabulary another surface
   independently owns (the Aperture panel's "Copy JSON", the generic "Saving…"
   busy state).
3. `docs/connect/immich.md`, `docs/immich-live-testing.md` and `docs/api.md`
   stop enumerating the VIP step table, the checkbox set and the per-service
   admin routes; they point at the admin surface and the generic
   `/v1/admin/setup/{id}` routes instead.
4. AGENTS.md documents the `Onboarder` extension point ("declare, don't
   render"; connectors never ship HTML/JS or write Aegis state) and gains a
   pitfalls row for provider copy in `admin.html` / `docs/`.
5. Dead routes and bespoke wizard JS removed; the design note's definition
   of done is now all true.

## 4. Risks and mitigations

| Risk | Mitigation |
|------|------------|
| Copy regression while relocating (the done-when demands identical user-visible text) | Port strings verbatim into spec fields; golden-text assertions on the spec + status output per flow; no "improve the wording" in migration PRs |
| Spec too rigid for the VIP wizard | That is the design's stress case (Phase 3 must fit); extend the spec generically (live select options, guide sub-step state) — never custom HTML |
| Secret leakage through spec/status/results | Presence flags only; runner strips anything the spec marks `Secret`/`SecretHeaders`; extend the existing secret-hygiene tests to every onboarding response shape |
| Fail-open regression | Runner treats connector errors as failed checks/hints, never 500s the admin page; pre-request hook untouched |
| Admin JSON change breaks the UI | `admin.html` migrates in the same PR as each flow; it is the only consumer (admin is in-tailnet, not agent API) |
| Coverage floor noise from moved files | Tests move with the source; runner + connector suites together must hold the 80% floor each PR |
| Scope creep into import-store admin | Explicitly out of scope (§2); revisit only if the generic panel is asked for there |
| OAuth path confusion | Onboarding never authenticates with providers. A future OAuth-needing flow declares a `save` action that *links* to the existing connections flow; tokens stay core-owned |

---

## 5. Definition of done

- [ ] Immich and YNAB setup run through the seam with behavior unchanged —
      same steps, checks, and user-visible copy.
- [ ] Their provider logic (contract, probes, hints, guides, VIP step rules)
      lives in `internal/connectors`; core holds no `immich`- or
      `ynab`-specific setup strings.
- [ ] `web/admin.html` renders setup through one generic panel and contains
      no provider copy; a hygiene test enforces it.
- [ ] `docs/` no longer duplicates provider contracts (checkbox set, step
      tables); it points at the admin surface that renders the live contract.
- [ ] Fail-open, "Aegis owns OAuth", secrets-omitted, and in-tailnet admin all
      hold — covered by tests, not by convention.
- [ ] `go vet ./...`, `go test ./... -count=1 -race`, coverage ≥ 80%,
      staticcheck clean, `go build -o aegis .`
- [ ] AGENTS.md documents the `Onboarder` extension point.

After that, a new named service can ship an advanced setup wizard by
implementing `Onboarder` in its connector file — no core changes, no admin
HTML, no doc updates beyond its `docs/connect/` page.
