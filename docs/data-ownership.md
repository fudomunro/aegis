# Data ownership: per-kind completeness feedback

Status: **phases 1–2 landed** — ownership state, derivation, the admin API,
and connector-declared route actions. Phase 3 (admin UI) and the optional
agent-facing stretch are still open.
Related issues: **#165** (this note), **#247** (connection maturity — the
complementary mark), **#227** (sync job queue; the evidence source),
**#254** (connector-declared metadata), **#286** (mail ingest seam),
**#266** (local import stores), **#162** (library source overview).
Related product rules: fail-open; Aegis does not store media; type-centric
surface; no issue numbers in user-facing copy; coverage floor 80%.

---

## 1. The problem

For every library kind, Aegis's goal is that the user owns all the data of
that kind that matters to them. Messages might be mostly one mail account,
but most users also care about chats, and Aegis should keep prompting them
to bring more messages under management until they confirm the collection
is complete.

Two questions have to stay distinct:

| Mark | Question | Moves when |
|------|----------|------------|
| **Connection maturity** (#247) | How completely does one *connector* cover the types its service offers? | Connector code moves |
| **Data ownership** (#165) | How completely is the *user's* data of one type under management? | The user ingests more, or confirms |

Maturity is a coverage statement in code. Ownership is user state joined
with live evidence.

## 2. What landed in the surrounding work

The approach leans on three seams that did not exist when #165 was filed:

1. **The sync job queue (#227).** Ingestion is a durable, resumable job
   chain with cursors, per-source done-state, import counts, and a per-kind
   copy-freshness rollup. "What is under management" is evidence the system
   already collects — ownership must not invent a parallel tracker.
2. **Connector-declared metadata (#254, #286, #266).** The
   `ConnectionMaturityCatalog()` names every known connector and the types
   it offers, connected or not. `MailSource` / `MailIngest` declare the mail
   ingest sources; `LocalStoreService` declares the import stores. So the
   call-to-action catalog is *derived from declarations*, never from a
   hardcoded per-kind list in core or admin UI.
3. **Local import stores as connector surfaces (#266).** Signal, Quicken,
   and Timeline feed library kinds like any other source, with counts that
   say whether anything has actually been imported.

## 3. Decisions

### 3.1 Confirmation is the only new state

`ownership.json` under the data directory stores one entry per kind —
`confirmed`, `confirmed_at`, optional `note`. Same overlay pattern as
`data_grants.json` and `user_types.json`. Everything else in the ownership
view is derived at read time and is never persisted.

### 3.2 Evidence, not guessing

A kind's ownership status joins three derived inputs:

- **Under management** — sources actually managing data. A live connection
  with credentials (token or static auth) counts; an archive transport
  (Signal, Quicken, Timeline, Takeout) counts only once it holds imported
  rows, so an empty store stays a call to action rather than looking
  complete. Mail-archive rows enrich the messages view and move Takeout
  from route to source (issue #139).
- **Unused routes** — declared ways to get more data of this kind that are
  not yet in use. Built from the maturity catalog (services offering the
  type, connected or not) plus the mail ingest sources. Each route carries
  a call-to-action declared beside its connector (issue #254): the action
  kind (`connect` / `setup` / `import`), its target, and the label the UI
  shows. Core never derives the action from interfaces or transport class.
- **Freshness** — the copy-ledger rollup passed through (issue #227).

Aegis cannot know what data exists upstream that it has never seen. The
model is *user-confirmed* completeness plus visible unused routes — not an
estimate of hidden volume.

### 3.3 Fail-open throughout

Unconfirmed ownership never blocks reads. A store or ledger that cannot be
read drops its evidence from the derivation; the confirmation state still
surfaces.

### 3.4 Confirmed is the only completeness signal

An empty `unused_routes` list is not "complete" — the user may have dismissed
sources we do not know about, or a catalog row may be missing. Only the
user's confirmation marks a kind complete.

## 4. API

| Route | Meaning |
|-------|---------|
| `GET /v1/admin/ownership` | Per-kind status: confirmed state, under-management sources, unused routes, freshness |
| `POST /v1/admin/ownership/{kind}` | Confirm the kind is complete (`{"note": "…"}` optional) |
| `DELETE /v1/admin/ownership/{kind}` | Reopen — return the kind to the call-to-action state (the note is kept) |

Confirm/reopen answer with the refreshed single-kind status so the UI does
not need a second fetch (the onboarding-runner pattern).

## 5. Phases

- **Phase 1** — `ownership.json` store, `OwnershipStatus`
  derivation from the maturity catalog + sync evidence, admin read/write
  routes, tests.
- **Phase 2 (this PR)** — connector-declared route actions: each maturity
  row carries a `Start` (kind, target, label), and ingest-only sources
  declare theirs on `MailSource`. Core reads the declarations and no longer
  derives the action from `Onboarder` or transport class. Tests pin the
  declarations against the seams they name (setup ↔ `Onboarder`, import ↔
  the local-store connectors, connect otherwise).
- **Phase 3** — admin UI: ownership cards in Library datasets with the
  call to action and the confirm/reopen control. Hygiene tests apply.
- **Stretch** — surface unconfirmed kinds in agent instructions or
  `/v1/admin/summary`. Not required for #165's bar.

## 6. Non-goals

- **Per-route dismissals** ("I do not care about this source"). The issue's
  bar is one confirmation per kind; nag-control per route is a refinement.
- **Estimating unseen data.** No probing for what might exist upstream.
- **Blocking reads on unconfirmed kinds.** Fail-open stays.
- **Agent-facing ownership claims.** The issue is user feedback; agent
  instructions are a later stretch.
