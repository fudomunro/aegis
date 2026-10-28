# Aegis marketing site (issue #129, refreshed in #196)

A static, dependency-free page that explains what Aegis is and why you'd want it.
It plays up the jellyfish mark from `web/logo.svg` (issue #87).

```
site/
  index.html      page markup + inline jellyfish SVG + F.A.Q.
  demo.html       agent kit: persona picker, pasteable brief, golden queries
  demo-config.js  public demo base URL + persona keys (read-only synthetic only)
  demo.js         brief builder / copy buttons for demo.html
  demo.css        agent kit layout on top of style.css
  style.css       deep-sea theme, animations (respects prefers-reduced-motion)
  site.js         marine snow, scroll reveals, cursor-tracking jellyfish eyes
  favicon.svg     copy of web/favicon.svg
```

Live site: <https://fudomunro.github.io/aegis/> (GitHub Pages from this
directory, deployed by [`.github/workflows/pages.yml`](../.github/workflows/pages.yml)
on every `main` change under `site/`).

The **agent kit** (`demo.html`) is for tool-enabled agents (Bash/`curl`).
Canonical brief text and sync rules: [`docs/agent-kit.md`](../docs/agent-kit.md).
Credentials in `demo-config.js` must only point at a `-demo` read-only
synthetic instance.

Preview locally:

```bash
python3 -m http.server -d site 8000   # then open http://localhost:8000
```

## Product language

Prefer the glossary terms from [`docs/glossary.md`](../docs/glossary.md):
**data grant**, **access level**, **app data grant**, **service limits**,
**data type**, **user-defined type**. Do not put older “permissions” product
English back into visible copy. Issue numbers are fine on this page only where
they point at open design work (for example #15 for user-defined types).

## The four use cases

The page is limited to four scenarios. They are ordered as a "dive" from shallow
to deep:

| Zone | Use case | Status | Product surface |
|------|----------|--------|-----------------|
| Sunlight | Anniversary "Wrapped" with photos from the same weeks | Works today | `/v1/library/tracks`, `/v1/library/photos`, player data grant `none` |
| Twilight | Free/busy calendar for a scheduling app | Works today | app data grant `events:freebusy` (#128, #131), `/v1/library/freebusy` |
| Midnight | Timeline across inboxes and calendar | Works today | `/v1/mail/search` archive, `/v1/library/events` |
| Abyss | Spreadsheet / database opened column by column | Exploring (#15) | Concept only, labelled as such on the page |

The **F.A.Q.** section carries the candid product Q&A (from issue #177):
trust, storage, Tailscale/Aperture, OAuth tokens, data grants, writes, fail-open,
supported services, audit visibility, and backup/wipe.

All names, dates, and data in the demo panels are made up. Keep it that way:
don't paste real research output (mail, calendar, travel) into the site.

If you change a product claim on the page (endpoints, data grants, access levels,
fail-open, tailnet-only), check it against `README.md`, `docs/configuration.md`,
and `SECURITY.md`. The site is not served by the Aegis binary and is not part of
`go:embed`.
