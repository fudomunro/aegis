# Aegis public demo — deploy runbook

Stands up a **read-only synthetic** Aegis (`-demo`) for alpha testers and the
marketing agent kit. Not the product deployment model: the real Aegis stays on
your tailnet (see `SECURITY.md`).

```
Internet → Caddy (TLS) → aegis-demo (:8090, -demo, sample:// only)
                └→ static site (GitHub Pages) ← agent kit + demo-config.js
```

## 0. Prerequisites

| Item | Notes |
|------|--------|
| VM | 1 vCPU / 1 GB is enough (e.g. DigitalOcean droplet, Ubuntu 22.04+) |
| DNS | `demo.example.com` → droplet IPv4 |
| Go | Build on the droplet or ship a linux/arm64-or-amd64 binary |
| Ports | 80/443 public; 8090 **localhost only** |

Do **not** open 8090 publicly and do **not** use Tailscale Funnel.

## 1. One-time on the VM

```bash
sudo useradd --system --home /var/lib/aegis-demo --shell /usr/sbin/nologin aegis-demo
sudo mkdir -p /var/lib/aegis-demo /opt/aegis-demo /etc/aegis-demo
sudo chown -R aegis-demo:aegis-demo /var/lib/aegis-demo
```

Install the binary (from a release build or copy):

```bash
sudo install -m 755 aegis /opt/aegis-demo/aegis
```

Or build on the VM:

```bash
sudo apt-get install -y golang git
git clone https://github.com/fudomunro/aegis /opt/aegis-demo/src
cd /opt/aegis-demo/src && go build -o /opt/aegis-demo/aegis .
```

## 2. Config

Copy the examples and generate keys:

```bash
sudo install -m 640 -o aegis-demo -g aegis-demo \
  deploy/demo/aegis.demo.json /etc/aegis-demo/aegis.json
sudo install -m 640 -o aegis-demo -g aegis-demo \
  deploy/demo/data_scopes.example.json /etc/aegis-demo/data_scopes.json
sudo install -m 640 -o aegis-demo -g aegis-demo \
  deploy/demo/data_grants.json /var/lib/aegis-demo/data_grants.json
```

Edit `data_scopes.json`:

- Set `api_key` for each persona. For **alpha only**, use long random values
  (`openssl rand -hex 24`) and send them to the tester out of band.
- Leave `read_only: true` (demo mode forces this anyway).

Token passphrase (not for secrets you care about; demo has no OAuth tokens):

```bash
echo 'AEGIS_TOKEN_PASS=change-me-demo' | sudo tee /etc/aegis-demo/env
sudo chmod 600 /etc/aegis-demo/env
```

## 3. Seed LotR data

```bash
sudo -u aegis-demo /opt/aegis-demo/aegis \
  -data-dir /var/lib/aegis-demo \
  -seed-lotr
```

## 4. systemd

```bash
sudo cp deploy/demo/aegis-demo.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now aegis-demo
sudo systemctl status aegis-demo
curl -sS http://127.0.0.1:8090/healthz
```

## 5. Caddy (TLS)

```bash
sudo apt-get install -y caddy
sudo cp deploy/demo/Caddyfile /etc/caddy/Caddyfile
# edit demo.example.com
sudo systemctl reload caddy
curl -sS https://demo.example.com/healthz
```

Smoke test with a persona key:

```bash
curl -sS -H "X-Api-Key: $SHIRE_KEY" \
  "https://demo.example.com/v1/mail/search?sort=asc&limit=1"
```

Expect *The party will be on the twenty-second* (2023-04-12).

## 6. Agent kit URL

On the marketing site (`site/demo-config.js` on `main`):

- `baseUrl`: `https://demo.example.com`
- `shire` / `gondor` keys: same as `data_scopes.json` **only if** you are
  ready for the keys to be public. For alpha, leave placeholders and email
  the kit + keys to the tester (they can paste into the page fields).

Deploy the site via GitHub Pages (`site/` on `main`).

## 7. Reset job

Synthetic data only; a cron job re-seeds so experiments stay clean:

```bash
sudo cp deploy/demo/reset-demo.sh /usr/local/sbin/aegis-demo-reset
sudo chmod 755 /usr/local/sbin/aegis-demo-reset
# nightly at 04:17 local
echo '17 4 * * * root /usr/local/sbin/aegis-demo-reset' | sudo tee /etc/cron.d/aegis-demo-reset
```

`reset-demo.sh` stops the unit, wipes the data dir, re-seeds, starts the unit.

## 8. Alpha tester handoff

Send:

1. Agent kit URL (`https://fudomunro.github.io/aegis/demo.html` or your fork).
2. Base URL (`https://demo.example.com`).
3. One persona key (shire **or** gondor first — isolation is a second exercise).
4. The one-liner smoke test.
5. Note: needs an agent with **Bash/`curl`** (Claude Code / Cursor / Codex).

Suggested first prompt (already in the kit): *What is the earliest message in
my mail?*

## 9. Ops notes

| Topic | Practice |
|-------|----------|
| Logs | `journalctl -u aegis-demo -f` |
| Restart | `systemctl restart aegis-demo` |
| Rotate a key | edit `data_scopes.json`, `systemctl restart` |
| Telemetry | `/metrics` needs the **admin** key; do not put that in the kit |
| Backups | none — demo state is disposable |
| Abuse | Caddy rate limits optional; keys are read-only + synthetic |

## 10. What this is not

- Not a multi-tenant SaaS threat model (`docs/threat-model.md`).
- Not a place for real mail, music tokens, or personal photos.
- Not Funnel / public-tunnel product advice — the product stays tailnet-private.
