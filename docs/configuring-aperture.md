# Configuring Aperture for Aegis

This guide is for operators who already run [Tailscale Aperture](https://tailscale.com/docs/aperture) and want to attach **Aegis** as a `pre_request` hook. It assumes you can edit Aperture policy/grants and know how the dashboard **Administration → Configuration** editor works.

Aegis also ships this guide in **Admin → Aperture** (live Aperture link, copyable JSON, hook health).

If you only need the JSON snippet, jump to [Minimal working config](#minimal-working-config). If you are migrating from the old **Guardian** names, jump to [Migrating from Guardian](#migrating-from-guardian).

## What Aperture should do

Aperture calls Aegis **synchronously** before the LLM provider sees the request. Aegis may:

| Aegis response | Meaning |
|----------------|---------|
| `{"action":"allow"}` | No named service / library match — pass the request through |
| `{"action":"modify","request_body":{...}}` | Inject proxy/library instructions into the LLM request |
| `{"action":"block","message":"..."}` | Reject the chat (HTTP 403) |

Aegis needs two payload fields from Aperture:

- **`user_message`** — to match named services (Spotify, Gmail, …) and library questions
- **`request_body`** — to rewrite the provider request when injecting instructions

If either is missing, the hook still runs but loses that capability (see [hook-health](#verify-with-hook-health)).

## Minimal working config

Add a hook endpoint and a grant that fires `pre_request` for the traffic you care about. Names matter: use **`aegis`**, not `guardian`.

```jsonc
{
  "hooks": {
    "aegis": {
      // Public URL of your Aegis instance + the hook path
      "url": "https://aegis.heron-census.ts.net/v1/pre-request",
      // Required when Aegis -auth-mode is auto or api-key.
      // Omit only if Aegis runs with -auth-mode tailnet and Aperture
      // reaches it with Tailscale identity (Serve / WhoIs).
      "apikey": "<AEGIS_API_KEY>",
      // Keep short — this is on the chat latency path.
      "timeout": "5s",
      // Fail-open matches Aegis itself: never brick chat if Aegis is down.
      "fail_policy": "fail_open"
    }
  },
  "grants": [
    {
      // Who gets the hook. Start narrow if you like — "*" is fine on a
      // personal tailnet.
      "src": ["*"],
      "app": {
        "tailscale.com/cap/aperture": [
          {
            "models": "**",
            "send_hooks": [
              {
                "name": "aegis",
                "events": ["pre_request"],
                "send": ["user_message", "request_body"]
              }
            ]
          }
        ]
      }
    }
  ]
}
```

Field notes:

| Field | Guidance |
|-------|----------|
| `hooks.aegis.url` | Must be the **public** Aegis base URL + `/v1/pre-request`. Same host Aegis uses for `-base-url`. |
| `hooks.aegis.apikey` | Shared secret. Aegis reads `AEGIS_API_KEY` (or `-api-key`). Leave empty only for pure `tailnet` auth. |
| `hooks.aegis.timeout` | Aperture default is `5s`. Keep it small; Aegis is designed to fail-open quickly. |
| `hooks.aegis.fail_policy` | `fail_open` (default) if Aegis is unreachable. Use `fail_closed` only if you want chat blocked when Aegis is down. |
| `send_hooks[].name` | Must match the `hooks` key (`aegis`). |
| `send_hooks[].events` | Must include `pre_request`. |
| `send_hooks[].send` | Must include `user_message` and `request_body` for full matching + injection. |

You can put the `hooks` object and the `grants` entry into your existing Aperture configuration (dashboard **Administration → Configuration**, or the visual **Providers** / **Grants** editors). Do not paste this as a full document over a live config — merge the two sections.

## Programmatic apply (Aperture config API)

Aperture stores one HUJSON document. To edit it from a script (or from Aegis tooling):

```bash
APERTURE=https://aperture.heron-census.ts.net   # your gateway

# 1. Fetch current config + hash
curl -sS "$APERTURE/api/config" > /tmp/aperture-config.json

# 2. Edit the "config" string in that JSON (or write a merged HUJSON file)
#    Keep every other section: providers, existing grants, quotas, …

# 3. PUT the full document back with the original hash (optimistic lock)
curl -sS -X PUT "$APERTURE/api/config" \
  -H "Content-Type: application/json" \
  -d @/tmp/aperture-config-put.json
```

The PUT body is:

```json
{
  "config": "<complete HUJSON configuration string>",
  "hash": "<hash from the GET response>"
}
```

Rules:

1. **GET → modify → PUT** with the **same hash**. A changed hash means someone else saved first — GET again and re-merge.
2. **PUT replaces the whole configuration.** Incomplete merge deletes providers/grants. Always start from the GET document.
3. You need Aperture **admin** grant access.

## Migrating from Guardian

Aegis used to be called Guardian. After the rename, leftover Aperture config looks healthy but is wrong. Checklist:

| Old (broken after rename) | New |
|---------------------------|-----|
| Hook key `guardian` | `aegis` |
| `url`: `https://guardian.…/v1/pre-request` | `https://aegis.…/v1/pre-request` (your real host) |
| `send_hooks[].name`: `"guardian"` | `"aegis"` |
| Comments / dashboard labels saying Guardian | Aegis |

Do **not** leave both hooks registered unless you intend two callouts. One `pre_request` hook named `aegis` is enough.

Quick static check (paste your Aperture policy/hooks JSON):

```bash
curl -sS -X POST \
  -H "Authorization: Bearer $AEGIS_API_KEY" \
  -H "Content-Type: application/json" \
  --data-binary @aperture-policy.json \
  "$AEGIS/v1/admin/hook-health/check"
```

It flags `legacy_hook_name`, `legacy_hook_url`, `stale_hook_host`, `hook_url_mismatch`, missing `send` fields, and more.

## Verify with hook-health

After Aperture is pointed at Aegis:

```bash
# Live traffic + expected snippet
curl -sS -H "Authorization: Bearer $AEGIS_API_KEY" \
  "$AEGIS/v1/admin/hook-health"
```

Healthy looks like:

- `status`: `ok` (or `warn` only for informational items)
- `last_hook_at` present (send any chat through Aperture first)
- `stale_hook_host` / `legacy_*` / `hook_auth_failures` **absent**
- Recent observations have `has_user_message` and `has_request_body` true

Send a trivial chat through Aperture (any model) so there is at least one `pre_request` observation, then re-run the check.

### Interpreting failures

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `no_hook_traffic` | Aperture not calling Aegis | Check hook `url`, network path, and that `send_hooks` is attached to a matching `src` |
| `hook_auth_failures` | Wrong `apikey` or Aegis `auth-mode` | Align hook `apikey` with Aegis `-api-key` / `AEGIS_API_KEY`, or use tailnet identity |
| `stale_hook_host` / `legacy_hook_url` | Still pointing at `guardian.*` | Update hook URL to the Aegis base URL |
| `legacy_hook_name` | Hook key still `guardian` | Rename key and `send_hooks[].name` to `aegis` |
| `missing_user_message` | `send` omits `user_message` | Add it to `send_hooks[].send` |
| `missing_request_body` | `send` omits `request_body` | Add it so Aegis can inject instructions |
| `hook_url_mismatch` | URL host ≠ Aegis `-base-url` | Fix URL or `-base-url` so they match |
| Modify never happens, only `allow` | Message has no service/library keywords | Expected for unrelated chats; try “what playlists do I have?” |

## Operational notes

- **Latency.** `pre_request` is synchronous. Keep Aegis local to the tailnet and the hook `timeout` short.
- **Fail-open.** Both Aegis and (by default) Aperture should allow chat if the hook is down. Do not set `fail_policy: fail_closed` unless blocking chat is intentional.
- **Secrets.** The hook `apikey` lives in Aperture’s config and Aegis’s env/flags. Never put service OAuth tokens in the Aperture hook — Aegis owns those.
- **Multiple hooks.** Aperture can chain `pre_request` hooks. If you also run other guardrails, order them deliberately; Aegis only needs one slot.
- **Events.** Only `pre_request` is required for Aegis today. Post-response events are unused.

## Related

- Aegis product/API surface: [`README.md`](../README.md)
- Aperture configuration reference: <https://tailscale.com/docs/aperture/configuration>
- Aegis tailnet trust model: [`SECURITY.md`](../SECURITY.md)
