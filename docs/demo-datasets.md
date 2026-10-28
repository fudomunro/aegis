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
./aegis -seed-demo-datasets -data-dir /tmp/aegis-demo
# or -seed-lotr (same); writes <data-dir>/sample/datasets/{shire,gondor,lincoln}-library.json
# and loads each partition into <data-dir>/mail-archive.db
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

## Abraham Lincoln pack

One partition (`lincoln`) for a historical desk, 1861–1865. Event dates follow
the record (inaugurations, Antietam, Emancipation, Gettysburg, Appomattox,
Ford’s Theatre). Correspondence is mostly **original text** in a
mid-19th-century register; a few short public-domain closing lines (Gettysburg,
Second Inaugural) appear where they are the point of the query. Mail uses
`lincoln.example`.

| Kind | Highlights |
|------|------------|
| Contacts | Lincoln, Mary Todd, Seward, Stanton, Grant, Sherman, Douglass, Nicolay/Hay (+ Grant merge pair) |
| Events | Full second-term timeline including City Point and Richmond |
| Places | Springfield→Washington journey, Gettysburg, City Point, Richmond, Executive Mansion |
| Documents | Reconstruction memorandum; Gettysburg / Second Inaugural closings |
| Mail | Seward on diplomacy, Stanton telegraphs, Grant appointments, family note, Appomattox news |

**Lincoln key prompts**

| Prompt | Surfaces |
|--------|----------|
| What did I tell Grant after Vicksburg? | mail 1863-07-13 |
| What is on my calendar in November 1863? | Gettysburg event |
| Where did I travel in March 1865? | places City Point |
| Find the closing line of the Gettysburg remarks. | documents text |
| Who wrote to me about Antietam? | mail from Stanton |

**Isolation:** shire/gondor keys must return nothing for `Appomattox` or
`Grant named general-in-chief`.

## Content rules

- No copyrighted prose, lyrics, or illustration files in git.
- Photo entries are **metadata only** (captions + `example` blob URLs) until
  the demo host supplies licensed art.
- Keep invented emails on `example.com` / `middle-earth.example` / `lincoln.example`.
- Merge demos (Strider ↔ Aragorn phone; Grant ↔ Captain Grant) stay inside one partition.
- Historical packs: factual event dates; letters original unless clearly
  public-domain fragments (US works published before 1929).

## Related

- [`docs/data-scopes.md`](data-scopes.md) — principal → dataset isolation
- [`docs/development.md`](development.md) — generic sample fixtures for CI
