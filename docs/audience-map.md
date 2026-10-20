# Audience map — where Aegis speaks to whom

Living map of every communication surface (UI, docs, API language, agent
injection, logs, config) to the audiences it serves, plus a reverse index by
audience. Update this when a surface is added or its register changes.

Companion: [`core-concepts.md`](core-concepts.md).

**Audience labels used below**

| Label | Who |
|-------|-----|
| **Runtime LLM agent** | Model/tool-loop that consumes pre-request injection and `/v1/library/*` |
| **End user (admin)** | Tailnet owner / homelab operator running Aegis |
| **Human ops** | Deployer / troubleshooter (Aperture, Docker, logs, flags) |
| **Developer integrator** | Builds tools/apps against Aegis APIs |
| **Security auditor** | Reviews trust model, redaction, token handling |
| **Coding agent / maintainer** | Edits this repo (`AGENTS.md` audience) |
| **Prospective adopter** | Evaluates Aegis before installing |
| **Third-party app user** | Consents to a scoped app token |

Note: *runtime LLM agent* and *coding agent* are different audiences despite
the shared word “agent”.

---

## 1. Surfaces by location

### Front-end UI

| Surface | Path | Audiences | Notes |
|---------|------|-----------|-------|
| Admin SPA | `internal/core/web/admin.html` (served `/`, `/library`, `/services`, `/advanced`) | End user (admin), Human ops | Checklist-driven; never shows OAuth secrets. Issue numbers appear in copy (`#76`, `#39`, …). |
| App consent | `internal/core/web/app_authorize.html` (`/v1/apps/authorize`) | End user (admin), Third-party app user | Cautious consent; scoped app credential ≠ provider OAuth token. |
| Connect demo | `internal/core/web/third_party_demo.html` (`/demo/third-party`) | End user, Developer integrator | Free/busy story; “minimal consent”. |
| Marketing site | `site/index.html` (+ `style.css`, `site.js`) | Prospective adopter | Metaphor-heavy (jellyfish). Not served by the binary; claims must match README/SECURITY. |
| Code walkthrough | `experiments/index.html` (+ generator) | Developer integrator, Coding agent | Stamped with commit hash. |
| Brand assets | `internal/core/web/` embeds (`logo.svg`, favicon) | Any browser | Jellyfish mark (#87). |

### Documentation

| Surface | Path | Audiences | Notes |
|---------|------|-----------|-------|
| Product manual | `README.md` | End user, Developer integrator, Human ops, (Security auditor) | Huge mixed contract + how-to + API reference. |
| Trust model | `SECURITY.md` | Security auditor, Human ops | Tailnet boundary, auth modes, redaction. |
| Coding-agent handbook | `AGENTS.md` | Coding agent, Maintainer | Non-negotiable product rules. |
| Extension guide | `docs/adding-data-types.md` | Developer integrator, Coding agent | New library types checklist. |
| Aperture ops guide | `docs/configuring-aperture.md` | Human ops | Hooks, grants, Guardian migration. |
| Immich runbook | `docs/immich-live-testing.md` | Human ops, QA | Live checklist; product contract callouts. |
| CalDAV/CardDAV guide | `docs/caldav-carddav.md` | Human ops, End user | Nextcloud / iCloud app-specific passwords; mirror + freebusy. |
| UDT design spike | `docs/user-defined-types.md`, `examples/user-types/` | Developer integrator, Pack author | Schema pack format. |
| Architecture plan | `docs/restructure-core-types-connectors.md` | Coding agent | Historical; phases marked done. |
| Example config | `aegis.example.json` | End user, Developer integrator | Self-describing `description` fields. |
| Site README | `site/README.md` | Maintainer of marketing copy | Claim-sync guardrails. |
| Walkthrough brief | `learn.md` | Coding agent | Regenerate walkthrough with commit stamp. |
| CI workflow | `.github/workflows/ci.yml` | Developer, Coding agent, CI | 80% floor, staticcheck, fuzz. |

### API language

| Surface | Primary audience | Secondary | Language character |
|---------|------------------|-----------|--------------------|
| `POST /v1/pre-request` | Aperture (machine) | Runtime LLM agent (via injected text), Human ops (hook-health) | `allow`/`block`/`modify` + `message`. Fail-open on parse errors. |
| `/v1/library/{kind}` | Runtime LLM agent | Third-party apps (scoped), admin Library tab | Neutral fields (`sources[]`, `canonical`, `truncated`, `entity_id`, `match`). Actionable `EmptyMsg`/`DenyMsg`/`FailMsg`. |
| `/v1/services/{id}/…` | Runtime LLM agent (content/ingest) | Human ops (imports) | Boundary errors: “content/ingest only”. |
| `/v1/mail/*` | Runtime LLM agent (search) | Human ops (sync, Takeout) | Archive disabled / not found / sync messages. |
| `/v1/auth/*`, `/v1/apps/*` | End user (browser), Third-party app | Human ops | Short OAuth machine strings; human success pages. |
| `/v1/admin/*` | Admin UI | Human ops curl, Security auditor | Diagnostic + prescriptive `fix` strings (hook-health is best-in-class). |
| `/healthz`, `/readyz`, `/metrics` | Supervisors, Ops/Prometheus | — | Deliberately free of config detail; metrics never label agent identities. |
| Audit log | Security auditor, Human ops | — | `agent`, `surface`, `target`, `data_type`, `decision`, `reason`. |

**Shared schema lexicon** (strongest cross-audience vocabulary):
`sources[]`, `canonical`, `copy`, `entity_id`, `match`, `truncated`,
`by_service`, `freebusy`, `blob_url`, `claim.sha256`.

### Agent-facing injection language

Built in `HandlePreRequest` and delivered as system/developer instructions.

| Block | Source | Audience | Register notes |
|-------|--------|----------|----------------|
| LibraryKind instructions | `internal/types/kind.go`, `librarykind.go` | Runtime LLM agent | Imperative tool-use guide; `truncated`, `sources[].canonical`. |
| Connector SystemInstructions | each connector (`spotify.go`, `immich.go`, …) | Runtime LLM agent | Negative constraints (“Do not call X”, “never ask for credentials”). |
| Generic fallback | `handler.go` | Runtime LLM agent | Library-first, not a services API. |
| Permission notices | `core/permissions.go` | Runtime LLM agent | Denied types; freebusy-only “never request titles…”. |
| Write policy | `core/canonical.go` | Runtime LLM agent | Canonical home first; no auto-mirror. |
| Mail archive hints | `core/mail_archive_http.go` | Runtime LLM agent | Endpoint cookbook + sort recipes. |
| Photo source discipline | `internal/types/builders.go` | Runtime LLM agent (as security policy) | No Photos.app / Immich env / raw CDN. |
| UDT hints | `core/udt_handlers.go` | Runtime LLM agent + pack author | Schema field list. |
| Capabilities / Operation | `internal/types/capabilities.go` + `Capabilities()` | Admin metadata (not agent discovery) | Provider-shaped English; **dialect differs** from SystemInstructions. |

### Logs and config

| Surface | Audience | Notes |
|---------|----------|-------|
| `slog` (JSON default) | Human ops, log pipeline, Security auditor | Fields: `service`, `login`, `identity_source`, `path`, `error`, `request_id`, … Fail-open encoded in messages. |
| CLI flag help (`run.go`) | Human ops, Coding agent | `-data-dir`, `-import-quicken`, `-auth-mode`, `-admin-api-key`, `-admin-logins`, empty-path disables. |
| `aegis.json` / `data_grants.json` / `user_types.json` | End user (via admin), Human ops | Data, not comments. Admin saves never rewrite `aegis.json`. |
| Encrypted secrets (`tokens.json`, static service credentials) | Human ops | AES-256-GCM under `-token-pass`; presence flags only in admin/API. |
| UDT `schema.json` packs | Pack author | Field descriptions; “Always opt-in”. |

---

## 2. Reverse index — audience → surfaces

### A. Runtime LLM agent
**Surfaces:** pre-request injection suite (library instructions, SystemInstructions, permission / write-policy / mail / photo notices), `/v1/library/*` + content + mail APIs (response shapes and error strings), guardrail `block` message.

**Strengths:** richest product voice; neutral schema; actionable errors; injection strings stay free of issue numbers and CLI flags.

**Gaps:** no single in-runtime agent API doc (OpenAPI is step-4 follow-up).

### B. End user (tailnet admin)
**Surfaces:** admin.html (Home/Services/Library/Advanced + Immich wizard), app authorize, OAuth login pages, README Quick Start + Connecting Services, `aegis.example.json`, marketing `site/`.

**Strengths:** setup path is real; secrets never displayed.

**Gaps:** no non-admin household UI; admin often ops-heavy; UDT product surface still design-level (#15) while the site markets it.

### C. Human ops / troubleshooter
**Surfaces:** README ops sections (flags, cache, data dir), `docs/configuring-aperture.md`, `docs/caldav-carddav.md`, hook-health `fix` strings, slog, `/healthz` `/readyz` `/metrics`, admin Advanced, Immich runbook, import CLI flags, admin credentials (`-admin-api-key`, `-admin-logins`).

**Strengths:** hook-health fix text and Immich wizard are strong; SECURITY covers admin vs agent credentials, CSRF/Origin, and encrypted static secrets.

**Gaps:** Guardian→Aegis migration only in hook-health + Aperture doc.

### D. Developer integrator
**Surfaces:** README API Reference, `docs/adding-data-types.md`, walkthrough `experiments/index.html`, app token docs, `third_party_demo.html`, example schema packs.

**Strengths:** prose is complete enough to integrate.

**Gaps:** no machine-readable API schema (OpenAPI); app-developer path thinner than agent path.

### E. Security auditor
**Surfaces:** SECURITY.md (auth tiers, CSRF/Origin, encrypted secrets), README auth/permissions/audit/metrics, audit log schema, admin redaction guarantees, PhotoSourceDiscipline, site claims.

**Strengths:** token handling, admin vs agent credentials, and tailnet model are explicit.

**Gaps:** no formal threat-model / data-flow doc; fail-open rationale scattered (AGENTS, kind docs) rather than in SECURITY.

### F. Coding agent / repo maintainer
**Surfaces:** AGENTS.md, `docs/*`, `learn.md` + walkthrough generator, CI workflow, sparse “why” comments.

**Gaps:** naming collision with runtime “agents”; historical restructure plan can misread as current state.

### G. Prospective adopter
**Surfaces:** `site/index.html` only.

**Gaps:** disconnected from the binary; claim-sync is manual (`site/README.md`).

### H. Third-party app user
**Surfaces:** `app_authorize.html`, demo page, freebusy stories on site.

**Gaps:** revoke path points at “Aegis admin”; little post-grant UX.

---

## 3. Mixed-audience hotspots

| Surface | Mixed how | Risk |
|---------|-----------|------|
| SystemInstructions | LLM agent + sometimes operator + admin tab names | Wrong register for pure model consumption |
| Library/content error strings | Agent + ops + auditor | Generally good; product-rule prose in 403/404 can confuse |
| README | Setup + API contract + ops + security | Different readers must hunt |
| admin.html | Homelab wizard + security claims + ops diagnostics + issue numbers | Cognitive load; Immich install is almost a product |
| Capabilities/Operations | Admin metadata vs provider-shaped docs vs library-first agent story | Drift vs type-centric rule |
| Hook-health `fix` text | Ops English + Aperture snippets | Excellent; not linked from agent docs |
| slog | Ops + privacy-conscious auditors | Good field discipline |

---

## 4. Audience coverage matrix

| Audience | Well served by | Weak / missing |
|----------|----------------|----------------|
| Runtime LLM agent | Injection suite, library errors, neutral schema | No dedicated agent API doc |
| End user (admin) | admin.html, README setup, example config | Non-admin users; wizard/ops blend |
| Human ops | hook-health, logs, metrics, runbooks, flags | Import ops live under Advanced / operator CLI |
| Developer integrator | README API, walkthrough, adding-data-types | No OpenAPI; thin app-dev docs |
| Security auditor | SECURITY.md, audit, redaction, admin Map | No formal threat model |
| Coding agent | AGENTS.md + docs + CI | Name collision with runtime “agents” |
| Third-party app user | consent HTML + freebusy demos | Lifecycle / revoke UX |
| Prospective adopter | site/ | Not linked from product; manual claim-sync |

---

## 5. Consistency notes for cleanup

1. **Shared lexicon is healthy:** `/v1/library/{kind}`, `sources[]`, `canonical`, `truncated`, `freebusy`, “Aegis owns OAuth”, “does not store media”, “fail-open”, “content/ingest only”.
2. **Agent injection is the richest product voice** and needs register discipline (keep ops tickets, CLI flags, and issue numbers out).
3. **Ops diagnosis quality** lives in hook-health `fix` strings and the Immich wizard, not in slog alone.
4. **Capabilities vs SystemInstructions** are two product dialects (provider-shaped vs library-first). Product rules say capabilities are admin metadata — keep that boundary explicit.
5. **Audiences with no dedicated surface:** non-admin end users, formal security-auditor threat model, machine-readable API schema for integrators.

Concept-name overlaps (service/connector/connection, permissions cluster, canonical vocabulary, type/kind naming) are tracked in [`core-concepts.md`](core-concepts.md) §2 and follow-up issue [#174](https://github.com/fudomunro/aegis/issues/174). Canonical English is in [`glossary.md`](glossary.md) — product term for user allow-rules is **data grant**.
