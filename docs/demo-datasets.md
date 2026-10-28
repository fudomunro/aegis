# Demo datasets

Synthetic datasets for a public demo of Aegis (issue #217). All correspondence
is **original invented text** in a general epic-fantasy register — not quotes
or translations of any copyrighted work. Emails use the reserved
`middle-earth.example` domain.

## Lord of the Rings pack

Two data-scope partitions so visitors can try cross-principal isolation with
familiar names:

| Dataset | Principal view | Sample leads |
|---------|----------------|--------------|
| `shire` | Frodo’s circle (hobbits, Bilbo, early road) | Frodo, Sam, Merry, Pippin, Bilbo, Gandalf, Rosie, Strider/Aragorn |
| `gondor` | The wider war | Gandalf, Aragorn, Elrond, Galadriel, Boromir, Faramir, Éowyn, Théoden |

Timeline: **April–July 2023** (departure → council → Hornburg → Pelennor →
coronation). Place visits cover Hobbiton→Bywater, Bree, Rivendell, Helm’s
Deep, Minas Tirith, Isengard.

### Seed locally

```bash
./aegis -seed-lotr -data-dir /tmp/aegis-demo
# writes <data-dir>/sample/lotr/{shire,gondor}-library.json
# and loads both partitions into <data-dir>/mail-archive.db
```

Wire the partitions in `aegis.json`:

```json
{
  "services": [
    {
      "id": "sample-library-shire",
      "name": "Sample Library (shire)",
      "action": "inject",
      "endpoint": "sample://library",
      "dataset": "shire"
    },
    {
      "id": "sample-library-gondor",
      "name": "Sample Library (gondor)",
      "action": "inject",
      "endpoint": "sample://library",
      "dataset": "gondor"
    }
  ]
}
```

And `data_scopes.json` (never commit; 0600):

```json
{
  "version": 1,
  "data_scopes": [
    { "id": "shire", "name": "Frodo’s circle", "dataset": "shire", "api_key": "YOUR_SHIRE_KEY", "read_only": true },
    { "id": "gondor", "name": "South-kingdom", "dataset": "gondor", "api_key": "YOUR_GONDOR_KEY", "read_only": true }
  ]
}
```

Use `YOUR_*` placeholder keys in docs and tests (`.gitleaks.toml`).

### Golden queries

Paste these into an agent that has Aegis base URL + one scope key. Each
question is answerable from the pack alone.

**Shire key**

| Prompt | Surfaces |
|--------|----------|
| What is the earliest message in my mail? | `/v1/mail/search?sort=asc` |
| Who is coming with me on the journey? | `/v1/library/contacts` (Sam, Merry, Pippin) |
| Summarize Gandalf’s instructions before Bree. | mail search + body |
| Where did I travel between leaving Bag End and Rivendell? | `/v1/library/places` |
| What photos do I have from the road? | `/v1/library/photos` |
| What is on my packing list? | `/v1/library/documents` |
| What did I spend before leaving the Shire? | `/v1/library/transactions` |

**Gondor key**

| Prompt | Surfaces |
|--------|----------|
| What happened around the Hornburg? | events + places + mail |
| Who wrote to me about the council? | mail from Elrond / Boromir |
| List songs in my war playlist. | `/v1/library/playlists` + tracks |
| When was the coronation? | `/v1/library/events` |
| What did the proclamation say? | `/v1/library/documents` text |

**Isolation checks** (run with either key)

| Prompt | Expected |
|--------|----------|
| Find mail about Boromir’s dream. | Answer only with the matching key; the other key finds nothing. |
| Show photos from Minas Tirith. | Shire key: empty / 404. |
| Call `/v1/services/sample-library-{other}/…` | 404 (invisible partition). |

## Content rules

- No copyrighted prose, lyrics, or illustration files in git.
- Photo entries are **metadata only** (captions + `example` blob URLs) until
  the demo host supplies licensed art.
- Keep invented emails on `example.com` / `middle-earth.example`.
- Merge demos (Strider ↔ Aragorn phone) stay inside one partition.

## Related

- [`docs/data-scopes.md`](data-scopes.md) — principal → dataset isolation
- [`docs/development.md`](development.md) — generic sample fixtures for CI
