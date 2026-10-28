# Aegis public demo — deploy runbook

Stands up a **read-only synthetic** Aegis (`-demo`) for alpha testers and the
marketing agent kit. Not the product deployment model: the real Aegis stays on
your tailnet (see `SECURITY.md`).

```
Internet → nginx (aegis.unified-designs.com) → aegis-demo 127.0.0.1:8090
                └→ static agent kit (GitHub Pages) ← paste base URL + key
```

Live profile on the shared Ubuntu droplet (nginx + systemd, same host as the
unified-designs / experiments sites). Do **not** open 8090 publicly and do
**not** use Tailscale Funnel.

| | |
|--|--|
| **Base URL** | `https://aegis.unified-designs.com` |
| **TLS** | Let’s Encrypt (certbot nginx plugin, auto-renew) |
| **IP fallback** | `https://138.197.8.215:8443` (self-signed) — temporary |

## Layout on the VM

| Path | Purpose |
|------|---------|
| `/opt/aegis-demo/aegis` | linux/amd64 binary (`-demo` capable) |
| `/etc/aegis-demo/aegis.json` | sample:// library services + datasets |
| `/etc/aegis-demo/env` | `AEGIS_TOKEN_PASS`, `AEGIS_ADMIN_API_KEY` (0600) |
| `/var/lib/aegis-demo/data_scopes.json` | persona API keys (0640) — **under the data dir** |
| `/var/lib/aegis-demo/data_grants.json` | places/documents/messages read |
| `/var/lib/aegis-demo/mail-archive.db` | seeded LotR mail |
| `/etc/systemd/system/aegis-demo.service` | unit (`-base-url https://aegis.unified-designs.com`) |
| `/etc/nginx/sites-available/aegis-demo` | hostname reverse proxy (80/443) |
| `/etc/nginx/sites-available/aegis-demo-ip` | optional self-signed :8443 fallback |
| `/etc/letsencrypt/live/aegis.unified-designs.com/` | certificate |
| `/usr/local/sbin/aegis-demo-reset` | re-seed synthetic data |
| `/etc/cron.d/aegis-demo-reset` | nightly reset (04:17) |

## DNS

Add an **A** record for `aegis.unified-designs.com` → `138.197.8.215`.
Cloudflare DNS-only (grey cloud) is the simplest path for HTTP-01; proxied
works if port 80 reaches the origin.

```bash
dig +short aegis.unified-designs.com   # → 138.197.8.215
```

## One-time install

```bash
# build from a workstation
GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o aegis .
scp aegis root@138.197.8.215:/root/aegis-demo-stage/

# on the VM
useradd --system --home /var/lib/aegis-demo --shell /usr/sbin/nologin aegis-demo
mkdir -p /opt/aegis-demo /etc/aegis-demo /var/lib/aegis-demo
install -m 755 aegis /opt/aegis-demo/aegis
install -m 640 -o aegis-demo -g aegis-demo aegis.json /etc/aegis-demo/aegis.json
install -m 600 -o root -g root env /etc/aegis-demo/env
install -m 640 -o aegis-demo -g aegis-demo data_scopes.json /var/lib/aegis-demo/data_scopes.json
install -m 640 -o aegis-demo -g aegis-demo data_grants.json /var/lib/aegis-demo/data_grants.json

sudo -u aegis-demo /opt/aegis-demo/aegis -data-dir /var/lib/aegis-demo -seed-lotr
install -m 644 aegis-demo.service /etc/systemd/system/ && systemctl daemon-reload
systemctl enable --now aegis-demo
```

### nginx + TLS

Hostname vhost (certbot will attach certificates):

```bash
install -m 644 aegis-demo.nginx /etc/nginx/sites-available/aegis-demo
ln -sfn /etc/nginx/sites-available/aegis-demo /etc/nginx/sites-enabled/
nginx -t && systemctl reload nginx

certbot --nginx -d aegis.unified-designs.com \
  --non-interactive --agree-tos --register-unsafely-without-email --redirect
```

Optional IP fallback while DNS or certificates settle
(`aegis-demo-ip.nginx`, self-signed under `/etc/ssl/aegis-demo/`). Remove
once the hostname is stable.

## Smoke test

```bash
curl -sS https://aegis.unified-designs.com/healthz
curl -sS -H "X-Api-Key: <shire key>" \
  "https://aegis.unified-designs.com/v1/mail/search?sort=asc&limit=1"
# expect: The party will be on the twenty-second (2023-04-12)
```

No `curl -k` — the certificate should verify.

## Reset

```bash
/usr/local/sbin/aegis-demo-reset
# cron: /etc/cron.d/aegis-demo-reset → 17 4 * * *
```

## Alpha tester handoff

Send **privately** (do not commit keys):

1. Agent kit: https://fudomunro.github.io/aegis/demo.html
2. Base URL: `https://aegis.unified-designs.com`
3. One persona key (shire first; gondor for an isolation follow-up)
4. Smoke-test curl
5. Needs an agent with **Bash/`curl`** (Claude Code / Cursor / Codex)

Leave `site/demo-config.js` placeholders until a public launch; the kit page
has fields the tester can paste into.

## Ops

| Topic | Practice |
|-------|----------|
| Logs | `journalctl -u aegis-demo -f` or `/var/lib/aegis-demo/logs/aegis.log` |
| Restart | `systemctl restart aegis-demo` |
| Rotate a key | edit `/var/lib/aegis-demo/data_scopes.json`, restart |
| Renew TLS | certbot timer (automatic); `certbot renew --dry-run` to check |
| Admin API | key in `/etc/aegis-demo/env` — never in the kit |
| Backups | none — disposable synthetic state |

## What this is not

- Not a multi-tenant SaaS threat model (`docs/threat-model.md`).
- Not a place for real mail, music tokens, or personal photos.
- Not Funnel / public-tunnel product advice — the product stays tailnet-private.
