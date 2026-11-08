---
name: Design decision / feature
about: Answer the ownership questions before implementation starts
title: ""
labels: ""
---

## Decision note (fill in before the first PR)

These three questions are the churn guardrails. Episodes in this repo's
history where they were answered *after* implementation cost full PRs of
rework — permissions ownership (three pivots), the mirror seam ("maybe we
made a mistake treating mirrors like data types?"), and the local ticket
store ("when I refer to tickets, I'm expecting GitHub issues").

**1. What is this a property of?**
Pick one (or say why it is none of them):

- a connected **service** (provider adapter, connection state)
- a **library kind** (neutral model, merge/identity, kind metadata)
- a **user data grant** (what an agent/app may use; `internal/core/policy`)
- a **reconciliation policy over kinds** (mirror, copy, sync — not a kind)
- **Aegis-internal metadata** (index, store, plumbing — `core/` sub-package or `shared`)

**2. What is the canonical name?**
Add it to `docs/glossary.md` in the same PR that introduces it. The glossary
is authoritative — identifiers, docs, and issue titles use the same word.

**3. What is explicitly out of scope?**
Name the thing this is *not*, especially when the noun is ambiguous
(ticket / mirror / grant / type / pick / copy).

## Problem

## Proposed shape

## Out of scope
