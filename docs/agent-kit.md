# Agent kit (no Aperture)

How an LLM agent talks to a demo Aegis without the Aperture pre-request hook.
The user-facing page is [`site/demo.html`](../site/demo.html); this file is the
canonical brief so docs and the site can stay aligned.

## Requirements

| Client | Works? |
|--------|--------|
| Claude Code / Cursor / Codex / tool agents with Bash | **Yes** — paste the brief |
| Chat UI with web browse only | **No** — cannot reach a private or even public API reliably |
| MCP-enabled clients | Not yet — future wrapper (agent discovery) |

The brief must include a **literal curl block**. Tool agents execute that
instead of inventing answers from the story.

## Canonical brief

```text
You are helping with a personal-data API named Aegis.

BASE_URL = <demo base URL>
API_KEY  = <persona key>

You MUST call the API with Bash (curl). Do not answer from your own
knowledge of the story — only from what the API returns.

First command to run now (copy exactly):

  curl -sS -H "X-Api-Key: <persona key>" \
    "<demo base URL>/v1/mail/search?sort=asc&limit=3"

Account: <persona label>. <blurb>
Synthetic demo data. Read-only.

Endpoints (send X-Api-Key on every request):
  GET /v1/mail/search?q=&from=&to=&after=&before=&sort=asc|desc&limit=
  GET /v1/library/messages
  GET /v1/library/contacts
  GET /v1/library/events
  GET /v1/library/places
  GET /v1/library/photos
  GET /v1/library/tracks, /playlists, /albums, /artists
  GET /v1/library/transactions
  GET /v1/library/documents
  GET /v1/library/documents/{service}/{id}/text

Rules:
- Only call this Aegis API — no other data sources.
- Never invent messages or people; if the API returns empty, say so.
- Read-only: do not POST/PUT/DELETE.
- Cite subjects and dates when you answer.
- If Bash or network is unavailable, say that plainly and stop.
- If curl fails, report the status code and body exactly.
```

## Endpoints vs library kinds

The list above matches the registered library routes (`librarykind.go`).
When a kind is added, update:

1. `site/demo.js` (`briefText`)
2. This file
3. `docs/demo-datasets.md` golden queries (when the pack covers the kind)

Do not invent agent-facing endpoints that are not registered.

## Personas

From the LotR pack (`docs/demo-datasets.md`):

| Key | Label | Blurb |
|-----|-------|-------|
| `shire` | Frodo’s circle | Hobbits, Bilbo, first days on the road |
| `gondor` | South-kingdom | Council, Hornburg, Pelennor, coronation |

## Credentials

`site/demo-config.js` holds the public demo base URL and keys. Those keys must
only authorize a **read-only synthetic** instance (`-demo`). Never point them
at a personal Aegis.

## Local preview

```bash
python3 -m http.server -d site 8000
# open http://127.0.0.1:8000/demo.html
```
