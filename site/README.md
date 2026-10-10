# Aegis marketing site (issue #129)

A static, dependency-free page that explains what Aegis is and why you'd want it.
It plays up the jellyfish mark from `web/logo.svg` (issue #87).

```
site/
  index.html   page markup + inline jellyfish SVG
  style.css    deep-sea theme, animations (respects prefers-reduced-motion)
  site.js      marine snow, scroll reveals, cursor-tracking jellyfish eyes
  favicon.svg  copy of web/favicon.svg
```

Preview locally:

```bash
python3 -m http.server -d site 8000   # then open http://localhost:8000
```

## The four use cases

The page is limited to four scenarios. They are ordered as a "dive" from shallow
to deep:

| Zone | Use case | Status | Product surface |
|------|----------|--------|-----------------|
| Sunlight | Anniversary "Wrapped" with photos from the same weeks | Works today | `/v1/library/tracks`, `/v1/library/photos`, `player: none` |
| Twilight | Free/busy calendar for a scheduling app | Works today | `freebusy` permission (#128), `/v1/library/freebusy`, app tokens (#131) |
| Midnight | Timeline across inboxes and calendar | Works today | `/v1/mail/search` archive, `/v1/library/events` |
| Abyss | Spreadsheet / database opened column by column | Exploring (#15) | Concept only, labelled as such on the page |

All names, dates, and data in the demo panels are made up. Keep it that way:
don't paste real research output (mail, calendar, travel) into the site.

If you change a product claim on the page (endpoints, permission levels,
fail-open, tailnet-only), check it against `README.md` and `SECURITY.md`.
The site is not served by the Aegis binary and is not part of `go:embed`.
