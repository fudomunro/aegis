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

Counters are process-lifetime and **reset on restart**. That is reset-safe for
Prometheus: `rate()` / `increase()` treat a drop as a counter reset. Agent
identities are deliberately **not** used as label values (cardinality and
privacy); per-agent detail stays in the admin Map and the audit log.

Scrape example:

```bash
curl -H "Authorization: Bearer $ADMIN_KEY" "$AEGIS/metrics"
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

