# Aegis public demo — deploy runbook

Stands up a **read-only synthetic** Aegis (`-demo`) for alpha testers and the
marketing agent kit. Not the product deployment model: the real Aegis stays on
your tailnet (see `SECURITY.md`).

```
Internet → nginx :8443 (self-signed IP TLS) → aegis-demo 127.0.0.1:8090
                └→ static agent kit (GitHub Pages) ← paste base URL + key
```

This profile matches the shared Ubuntu droplet that already hosts the
experiments / unified-designs sites (nginx + systemd). Do **not** open 8090
publicly and do **not** use Tailscale Funnel.

## Layout on the VM

| Path | Purpose |
|------|---------|
| `/opt/aegis-demo/aegis` | linux/amd64 binary (`-demo` capable) |
| `/etc/aegis-demo/aegis.json` | sample:// library services + datasets |
| `/etc/aegis-demo/env` | `AEGIS_TOKEN_PASS`, `AEGIS_ADMIN_API_KEY` (0600) |
| `/var/lib/aegis-demo/data_scopes.json` | persona API keys (0640) — **under the data dir** |
| `/var/lib/aegis-demo/data_grants.json` | places/documents/messages read |
| `/var/lib/aegis-demo/mail-archive.db` | seeded LotR mail |
| `/etc/systemd/system/aegis-demo.service` | unit |
| `/etc/nginx/sites-available/aegis-demo` | :8443 reverse proxy |
| `/etc/ssl/aegis-demo/` | self-signed cert for the droplet IP |
| `/usr/local/sbin/aegis-demo-reset` | re-seed synthetic data |
| `/etc/cron.d/aegis-demo-reset` | nightly reset (04:17) |

## One-time (already done on 138.197.8.215)

```bash
# build from a workstation
GOOS=linux GOARCH=amd64 go build -ldflags='-s -w' -o aegis .
scp aegis root@<host>:/root/aegis-demo-stage/

# on the VM
useradd --system --home /var/lib/aegis-demo --shell /usr/sbin/nologin aegis-demo
install -m 755 aegis /opt/aegis-demo/aegis
install -m 640 -o aegis-demo -g aegis-demo aegis.json /etc/aegis-demo/aegis.json
install -m 600 -o root -g root env /etc/aegis-demo/env
install -m 640 -o aegis-demo -g aegis-demo data_scopes.json /var/lib/aegis-demo/data_scopes.json
install -m 640 -o aegis-demo -g aegis-demo data_grants.json /var/lib/aegis-demo/data_grants.json

openssl req -x509 -nodes -newkey rsa:2048 -days 825 \
  -keyout /etc/ssl/aegis-demo/privkey.pem \
  -out /etc/ssl/aegis-demo/fullchain.pem \
  -subj "/CN=<droplet-ip>" -addext "subjectAltName=IP:<droplet-ip>"

sudo -u aegis-demo /opt/aegis-demo/aegis -data-dir /var/lib/aegis-demo -seed-lotr
install -m 644 aegis-demo.service /etc/systemd/system/ && systemctl daemon-reload
systemctl enable --now aegis-demo
install -m 644 aegis-demo.nginx /etc/nginx/sites-available/aegis-demo
ln -sfn /etc/nginx/sites-available/aegis-demo /etc/nginx/sites-enabled/
nginx -t && systemctl reload nginx
```

## Smoke test

```bash
curl -sk https://<droplet-ip>:8443/healthz
curl -sk -H "X-Api-Key: <shire key>" \
  "https://<droplet-ip>:8443/v1/mail/search?sort=asc&limit=1"
# expect: The party will be on the twenty-second (2023-04-12)
```

Clients must accept a self-signed cert (`curl -k`, or browser proceed).
That is expected for the IP-only alpha profile.

## Reset

```bash
/usr/local/sbin/aegis-demo-reset
# cron: /etc/cron.d/aegis-demo-reset → 17 4 * * *
```

## Alpha tester handoff

Send **privately** (do not commit keys):

1. Agent kit: https://fudomunro.github.io/aegis/demo.html
2. Base URL: `https://138.197.8.215:8443` (self-signed — use `curl -k` / accept cert)
3. One persona key (start with shire)
4. Smoke-test curl
5. Needs an agent with **Bash/`curl`** (Claude Code / Cursor / Codex)

Leave `site/demo-config.js` placeholders until you are ready for a public
launch; the kit page has fields the tester can paste into.

## When a hostname exists

1. Point DNS A → droplet.
2. Certbot for that name on 443 (same proxy_pass).
3. Update `-base-url` in the unit and `site/demo-config.js`.
4. Drop the self-signed :8443 vhost.

## Ops

| Topic | Practice |
|-------|----------|
| Logs | `journalctl -u aegis-demo -f` or `/var/lib/aegis-demo/logs/aegis.log` |
| Restart | `systemctl restart aegis-demo` |
| Rotate a key | edit `/var/lib/aegis-demo/data_scopes.json`, restart |
| Admin API | key in `/etc/aegis-demo/env` — never in the kit |
| Backups | none — disposable synthetic state |

## What this is not

- Not a multi-tenant SaaS threat model (`docs/threat-model.md`).
- Not a place for real mail, music tokens, or personal photos.
- Not Funnel / public-tunnel product advice — the product stays tailnet-private.
