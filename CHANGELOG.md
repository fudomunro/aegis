# Changelog

History and design decisions for Aegis. User-facing docs do not cite GitHub
issue numbers; this file is where that context lives.

## Unreleased

### Fixed

- Pre-request hook no longer intercepts software-development messages that
  mention service names as identifiers (issue #205). Built-in development
  detection (code fences, source file paths, Aegis repo markers) plus optional
  `hook.skip_patterns` regexes skip matching and allow the request unchanged.

### Security

- CI secret scan (gitleaks) over the full tree and history, with an allowlist
  for operator snippets and test fakes. `public_hygiene_test.go` fails the
  Test job if private host/path/email patterns reappear in the worktree.

### Documentation

- Refresh the marketing site (`site/`) for current product language (data
  grants, access levels, service limits) and add the F.A.Q. from issue #177
  (#196).
- Split the monolithic README into an entry point plus reference docs
  (`docs/api.md`, `docs/configuration.md`, `docs/connect/*`, `docs/observability.md`,
  `docs/development.md`).
- CI checks local markdown links/fragments and keeps README headings and admin
  UI visible copy free of issue numbers and glossary-avoid terms.

## Earlier milestones (issue references)

These landed before the docs split; see the linked issues for design notes.

| Area | Issues | Notes |
|------|--------|-------|
| Named-service connectors + type-centric library | #119, #15 | `/v1/services` is content/ingest only; library kinds live in `internal/types` |
| Entity resolution / merge | #4 | Prefer ISRC/UPC/MBID over name |
| Canonical sources / mirror | #16 | `canonical_for` marks the non-Aegis home; mirror is explicit copy |
| Data grants + service limits | #148, #160 | Type-owned policy; service maps only tighten |
| User-defined types (schema packs) | #15 | Packs load into the same aggregate pipeline |
| Token encryption of static credentials | #101, #184 | AES-256-GCM + PBKDF2; rekey via `-token-new-pass` |
| Pagination contract | #185 | Shared `next_cursor` / `total` / `truncated` |
| Admin auth (admin vs agent) | #181 | Agent credentials never admin |
| CSRF / origin protection | #182 | Writes check Origin |
| Places / location redaction | #142 | Default `none`; city / precise ladder |
| Documents / Paperless | #143 | Metadata vs read ladder |
| Mail archive + Signal / Takeout import | #168 | Local stores; no cloud mail storage |
| Copy ledger | #76 | Durable known-copies map |
| Hook body shapes (OpenAI / Anthropic / Responses) | #103 | Fail-open on unknown shapes |
| Library source overview | #162 | Admin map of sources → types |
| Photos ↔ places join | — | Timeline + OwnTracks feeds |
| CalDAV / CardDAV | #146 | Nextcloud, iCloud, Fastmail |
| App data grants (third-party tokens) | #131 | Scoped `aeg_app_…` tokens |
| Terminology pass (glossary) | #174 | Prefer *data grant*, *canonical home*, *content transport* |

## Maintainer docs

Process notes (worktrees, PR shape, coverage floor) live in [`AGENTS.md`](AGENTS.md),
not here.
