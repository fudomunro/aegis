# examples/user-types

Portable **schema packs** for Aegis user-defined types (issue #15).

Each subdirectory is one pack:

```text
<pack>/schema.json      # kind definition (fields, identity, sources)
<pack>/sample/…         # demo data for file_json sources
<pack>/README.md        # what the pack proves
```

Install by copying a pack into Aegis's user-types directory:

```bash
cp -R examples/user-types/reading-list <data-dir>/user-types/
# or
export AEGIS_USER_TYPES_DIR=$PWD/examples/user-types
```

Restart Aegis and call `GET /v1/library/<pack-id>`.

## Own-repo note

This tree is shaped to move to a standalone content repo
(`aegis-user-types`) once schema v1 stabilizes. See
`docs/user-defined-types.md` §6 for the tradeoff. The service repo will keep a
minimal inline fixture so runtime tests do not depend on the content repo.
