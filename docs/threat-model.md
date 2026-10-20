# Threat model

Auditor-facing model of what Aegis protects, from whom, and which controls
hold the line. Operational setup and flags live in [`SECURITY.md`](../SECURITY.md);
product vocabulary is in [`glossary.md`](glossary.md). This document is the
structured view; when code changes the trust boundary, update both.

Aegis is a **private tailnet service**. It is not a multi-tenant SaaS and is
not designed for a hostile public network.

---

## 1. System overview

```
LLM agent ──► Aperture ──► Aegis pre-request hook ──► LLM provider
     │                        │
     │                        ▼
     └──────────────────► Aegis library / content transport
                                   │
                                   ▼
                          provider APIs (Spotify, Google, Immich, …)
                                   │
                                   ▼
                          local state (data directory)
```

| Boundary | What crosses it |
|----------|-----------------|
| Tailnet edge | Tailscale identity (Serve headers or WhoIs); optional agent API key |
| Aegis ↔ LLM request | Injected system instructions (never tokens) |
| Aegis ↔ providers | OAuth / static credentials from the encrypted token store |
| Aegis ↔ disk | `tokens.json`, `data_grants.json`, `apps.json`, `audit_log.db`, … |
| Browser ↔ admin / consent | Ambient tailnet identity + CSRF/Origin rules |

---

## 2. Assets

| Asset | Why it matters | Where |
|-------|----------------|-------|
| Provider OAuth tokens | Full read (and sometimes write) of music, mail, photos, calendar, finance | Encrypted `tokens.json` |
| Static service credentials | Immich/YNAB API keys, Tailscale Admin API key | Same store, same master key |
| User library data | Playlists, contacts, events, messages, transactions, places, documents | Provider-side; Aegis returns metadata + content transport |
| Data grants / app scopes | Policy that constrains agents and third-party apps | `data_grants.json`, `apps.json` |
| App tokens | Scoped third-party credentials | SHA-256 hashes in `apps.json` |
| Audit log | Who accessed which data type, allow/deny | `audit_log.db` (retention capped) |
| Agent API key / admin API key | Shared secrets when not pure-tailnet | Operator-supplied flags/env |

**Non-assets (by design):** media bytes (stay on the source service), LLM
conversation content (Aegis only injects instructions), and long-lived copies
of full message bodies outside the optional local mail archive.

---

## 3. Actors and trust levels

| Actor | Trust | Credential | Max reach |
|-------|-------|------------|-----------|
| Tailnet owner / admin principal | Highest on the service | Admin API key or user login (`-admin-logins`) | Policy writes, OAuth, app minting, `/v1/admin/*`, `/metrics` |
| LLM agent (runtime) | Constrained by data grants | Agent API key, tagged device, allowed tailnet login | Library reads, hook, content transport, mail search |
| Third-party app | Least privilege | App token `aeg_app_…` | Library **read** only, AND of app scopes × data grants |
| Human operator (SSH/host) | Host root | OS account | Full data directory — out of Aegis’s auth |
| Public internet | Untrusted | — | Must have **no** path (no Funnel) |

Authentication answers *who may call*. Data grants answer *what an authorized
caller may do with each data type*. Admin is never the agent key.

---

## 4. Trust assumptions

1. **Tailnet admission is the perimeter.** Device approval + ACLs/grants decide
   who can open a TCP connection to Aegis.
2. **Tailscale identity headers are trustworthy only behind Serve (or an
   equivalent proxy that strips inbound identity headers).**
3. **Host filesystem is trusted** for the data directory (mode 0700). Disk
   encryption of `tokens.json` protects stolen media/backups, not a live root
   attacker.
4. **Provider APIs are honest** about item contents; Aegis does not cryptographically
   verify provider payloads.
5. **The LLM is hostile-ish.** Prompt injection can make the agent call any
   API its credential allows. Policy must hold even when the model misbehaves.
6. **Single-user token store.** Every authorized caller uses the same provider
   tokens; tailnet identity does not isolate “whose” library is returned.

---

## 5. Threats and mitigations

### 5.1 Credential theft

| Threat | Mitigation |
|--------|------------|
| Steal `tokens.json` / static secrets from disk or backup | AES-256-GCM under PBKDF2 master key (`-token-pass`); wrong passphrase refuses start and never overwrites (issue #100) |
| Leak OAuth client secrets in shared config | `"$ENV:NAME"` / `"${NAME}"` indirection in `aegis.json` (issue #102) |
| Agent transcripts contain tokens | Agents never see tokens; Aegis owns OAuth; admin/API expose presence/scopes/expiry only |
| Reuse agent key as admin | Separate `-admin-api-key`; shared `-api-key` is never admin (#181) |
| App token database stolen | Only SHA-256 hashes in `apps.json` (0600); plaintext shown once |
| Schema pack smuggles secrets | `http_json` credential headers rejected at UDT load |

### 5.2 Unauthorized access

| Threat | Mitigation |
|--------|------------|
| Guest laptop / tagged CI on shared tailnet reads library | `-allowed-logins` allowlist; tag service host; deny-by-default grants |
| Agent rewrites policy that constrains it | Admin tier separate; agent credential gets **403** on `/v1/admin/*` writes |
| Tagged device treated as admin | Tagged identities are **not** admin unless listed in `-admin-logins` |
| Public exposure (Funnel / LAN bind) | **No Funnel**; bind localhost/Tailscale IP or Serve; tag `tag:aegis` |
| Unapproved device joins with stolen auth key | Tailscale device approval |
| `/metrics` leaks inventory to the world | Admin-scoped (#181); series include service ids / denial reasons |

### 5.3 CSRF / ambient browser identity

| Threat | Mitigation |
|--------|------------|
| Hostile page auto-submits a write using Serve identity headers | Same-origin `Origin` / `Sec-Fetch-Site` required for ambient identity (#182) |
| CORS “simple request” form/text POST | JSON write surfaces require `Content-Type: application/json` |
| App consent form cross-site POST | Single-use CSRF token on `GET /v1/apps/authorize` |
| Non-browser clients blocked by Origin rules | API key and app token writes **skip** Origin (not CSRF-ambient) |

### 5.4 Over-broad agent / app access

| Threat | Mitigation |
|--------|------------|
| Agent reads data types the user meant to hide | Type-owned **data grants** + tighten-only **service grant limits**; 403 on deny |
| Redacted views leak raw content | Type-scoped levels: `freebusy`, `summary`, `city`/`precise`, `metadata` |
| Places exposed by default | Places default **none**; opt-in |
| Third-party app exceeds intent | App scopes AND data grants; library read only |
| Capability listing becomes an attack map | Capabilities are admin/connector metadata, not agent discovery (#119) |
| Prompt injection widens access | Injection never grants tokens or admin; write policy points at canonical home only |

### 5.5 Prompt injection / instruction channel

| Threat | Mitigation |
|--------|------------|
| Injected text steers the agent to dump secrets or call providers directly | Photo/source discipline, “Aegis owns OAuth”, negative constraints in SystemInstructions |
| Injected text teaches CLI flags / issue trackers | Agent-string hygiene guard (`issue #`, CLI flags, glossary-avoid terms) |
| Untrusted page content becomes instructions | Aegis does not fetch remote HTML into the system prompt; library content is data |

### 5.6 Host / ops compromise

| Threat | Mitigation / residual |
|--------|----------------------|
| Root on the Aegis host | **Out of scope** — full token store and flags are readable |
| World-readable data directory | Documented 0700; audit log and `tokens.json` share that boundary |
| Log leakage of PII/tokens | slog fields avoid bodies/tokens; denials log route family not full paths; metrics never label agent identities |
| UDT browse lists host paths | Admin-equivalent (#181); selection is the dangerous step |

### 5.7 Availability / integrity (lower emphasis)

| Threat | Mitigation |
|--------|------------|
| Cache poisoning | Cache **GET + HTTP 200** only; no error-body cache |
| Unbounded memory from huge hook bodies | Hook body cap |
| OAuth state / page loops | Caps, eviction, `libraryPageCap`, singleflight refresh |
| Fail-closed lockout on provider errors | **Fail-open**: parse/fetch errors allow the request and log |

---

## 6. Residual risks (accepted)

1. **Single-user token blast radius** — any authorized agent/app shares one
   provider identity. Per-user libraries are future work.
2. **Fail-open** — a malicious payload that breaks the hook still reaches the
   LLM *without* Aegis instructions (and without Aegis data). Prefer that over
   blocking the user’s chat.
3. **Provider trust** — Aegis cannot prove a photo’s provenance beyond content
   hashes and copy-ledger breadcrumbs (ownership-claim PoC is not a
   cryptographic proof).
4. **Host compromise** — disk encryption does not protect a live root attacker
   or a cold-boot dump of the running passphrase.
5. **LLM non-compliance** — instructions and 403s constrain tools; the model
   may still narrate or request denied data. Enforcement is at the API, not
   the prose.
6. **Audit log is local** — useful for review, not a remote SIEM unless the
   operator ships it.

---

## 7. Control map (quick index)

| Control area | Primary reference |
|--------------|-------------------|
| Trust model, auth modes, identity resolution | [`SECURITY.md`](../SECURITY.md) |
| Admin vs agent credentials | SECURITY.md “Admin vs agent credentials” |
| CSRF / Origin | SECURITY.md “CSRF / origin protection on writes” |
| Encryption at rest | SECURITY.md “Best practices” §8; README “Token encryption” |
| Data grants / redaction | SECURITY.md “What authorized means”; [`glossary.md`](glossary.md) |
| Audit log | SECURITY.md “Best practices” §12 |
| Deployment checklist | SECURITY.md “Deployment checklist” |
| Product rules (fail-open, no media store, type-centric) | [`../AGENTS.md`](../AGENTS.md) |

---

## 8. Out of scope

- Multi-tenant / SaaS deployment
- Tailscale Funnel or any public exposure
- Cryptographic proof of media ownership (PoC only)
- Malicious provider backends
- Side-channel attacks on the LLM host
- Formal verification of OAuth redirect handling beyond the documented public login/callback surfaces

When adding a surface that stores secrets, expands admin power, or creates a
new ambient-auth path, extend this model in the same PR.
