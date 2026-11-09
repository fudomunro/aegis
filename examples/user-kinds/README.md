# examples/user-kinds

Portable **schema packs** for Aegis user-defined kinds (issue #15).

Each subdirectory is one pack:

```text
<pack>/schema.json      # kind definition (fields, identity, sources)
<pack>/sample/…         # demo data for file_json sources
<pack>/README.md        # what the pack proves
```

Install by copying a pack into Aegis's user-kinds directory:

```bash
cp -R examples/user-kinds/reading-list <data-dir>/user-kinds/
# or
export AEGIS_USER_KINDS_DIR=$PWD/examples/user-kinds
```

Restart Aegis and call `GET /v1/library/<pack-id>`.

## Own-repo note

This tree is shaped to move to a standalone content repo
(`aegis-user-kinds`) once schema v1 stabilizes. See
`docs/user-defined-kinds.md` §6 for the tradeoff. The service repo will keep a
minimal inline fixture so runtime tests do not depend on the content repo.
