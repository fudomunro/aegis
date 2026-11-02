# Observability

Aegis exposes cheap probes, Prometheus metrics, and a durable access audit
trail so you can see who reached which data — including denials — after a
restart.

### Health probes

| Endpoint | Auth | Meaning |
|----------|------|---------|
| `GET /healthz` | none | Liveness: process and HTTP stack are up |
| `GET /readyz` | none | Readiness: config and token store are loaded (503 otherwise) |

Both return a tiny JSON body (`{"status":"ok"}` / `{"status":"ready"}`) and
never include config, tokens, or service inventory. Use them from a process
supervisor or local health check.

### Metrics (`GET /metrics`)

Prometheus text exposition format. **Auth required** (same credentials as
other protected APIs) because series include service ids and denial reasons.

| Series | Type | Labels |
|--------|------|--------|
| `aegis_process_start_time_seconds` / `aegis_process_uptime_seconds` | gauge | — |
| `aegis_hook_outcomes_total` | counter | `action` (`allow`, `modify`, `block`, `auth_fail`, `parse_fail`) |
| `aegis_access_total` | counter | `surface`, `target`, `decision` |
| `aegis_cache_events_total` | counter | `result` (`hit`/`miss`) |
| `aegis_permission_denials_total` | counter | `surface`, `target` |
| `aegis_audit_events_total` | counter | `decision` |
| `aegis_upstream_duration_seconds` | histogram | `service` |
| `aegis_sync_jobs_total` | counter | `job_type`, `state` |
| `aegis_sync_retries_total` | counter | `job_type`, `error_class` |
| `aegis_sync_job_duration_seconds` | histogram | `job_type`, `service` |
| `aegis_sync_queue_depth` | gauge | `state` |
| `aegis_sync_last_success_timestamp_seconds` | gauge | `kind`, `service` |

Counters are process-lifetime and **reset on restart**. That is reset-safe for
Prometheus: `rate()` / `increase()` treat a drop as a counter reset. Agent
identities are deliberately **not** used as label values (cardinality and
privacy); per-agent detail stays in the admin Map and the audit log.

Scrape example:

```bash
curl -H "Authorization: Bearer $ADMIN_KEY" "$AEGIS/metrics"
```

### Maintenance signals

Sync-maintenance observation bundles (`docs/sync-maintenance.md`). Same admin
scope as `/metrics` — the bundle names services and failure modes and must
not reach agent credentials.

| Endpoint | Method | Meaning |
|----------|--------|---------|
| `GET /v1/admin/maintenance/signals` | GET | Current signal bundle (JSON) |
| `POST /v1/admin/maintenance/signals` | POST | Capture: write the bundle under `<data-dir>/maintenance/`, return its path |
| `POST /v1/admin/maintenance/scan` | POST | Scan captured bundles; open/update structured tickets under `<data-dir>/maintenance/tickets/` |
| `GET /v1/admin/maintenance/tickets` | GET | List sync-failure tickets (`repro_state`, observations, triage fields) |

The bundle is self-contained: build stamp (`version`, `commit`, hostname),
sync-queue rollups (`counts`, `by_kind`, `by_job_type`), up to 20 recent
failed jobs with scrubbed error text, and the per-kind copy freshness
rollup. Failure text is redacted (emails, credentials) and truncated before
it leaves the process; bundles never include request/response bodies,
library contents, tokens, or agent identities. Files land as `signals-<UTC>.json`
(0600); the scheduled scanner that reads them may rotate old files.

```bash
# Capture a bundle to <data-dir>/maintenance/
curl -X POST -H "Authorization: Bearer $ADMIN_KEY" "$AEGIS/v1/admin/maintenance/signals"
```

The scanner (phase 2 of `docs/sync-maintenance.md`) clusters bundle failures
by normalized signature and tickets only recurrent evidence — two or more
distinct bundles — while freshness findings (stale/missing copies) ticket
from the latest bundle alone. Tickets are structured JSON files, not prose;
rescans never clobber triage fields. A local scheduled job is the intended
trigger for now:

```cron
0 * * * * curl -sS -X POST -H "Authorization: Bearer $ADMIN_KEY" "$AEGIS/v1/admin/maintenance/signals" >/dev/null
7 * * * * curl -sS -X POST -H "Authorization: Bearer $ADMIN_KEY" "$AEGIS/v1/admin/maintenance/scan" >/dev/null
```

### Access audit log

Durable, append-only record of allowed and denied data access. Stored in
SQLite at `<data-dir>/audit_log.db`.

Each row is redacted like the in-memory access ring: agent login
or `app:<id>`, surface (`library` / `service` / `auth` / …), target (service
id or library kind), data type, decision, and a short reason
(`permission`, `app_scope`, `service_blocked`, `auth`). **No tokens, request
bodies, query strings, or full paths.**

- **Retention:** 30 days by default (`-audit-retention-days`). Older rows are
 pruned on startup and periodically on write. A hard row cap (500k) drops the
 oldest rows first so a noisy agent cannot fill the disk.
- **Fail-open:** audit write errors never block agent requests.
- **Disable:** pass an empty `-audit-log` (explicitly set) to turn persistence off.

```bash
# Example: 90-day retention
./aegis... -audit-retention-days 90
```

Admin Library previews (`/v1/admin/library/{kind}`) are **not** audited as
agent access, matching Map counting rules.

