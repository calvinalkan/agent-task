# Write-ahead log (WAL)

mddb uses a single-file write-ahead log for crash-safe, roll-forward commits.

The WAL exists only:

- during `Commit()` (while the exclusive lock is held), or
- after a crash, until it is replayed by the next writer.

## File format

The WAL is:

- **JSONL** (one operation per line)
- followed by a **CRC32-C** checksum of the JSONL bytes (4 bytes, little-endian)

```
[JSONL bytes][CRC32-C (4 bytes LE)]
```

The checksum uses the Castagnoli polynomial (Go: `crc32.MakeTable(crc32.Castagnoli)`).

### JSONL operations

Each line is a JSON object with:

- `op`: "create" | "update" | "delete"
- `key`: document key
- `frontmatter`: object (create = full; update = patch)
- `content`: string or null (update only) / required for create

Example:

```jsonl
{"op":"create","key":"0042","frontmatter":{"status":"open","priority":1},"content":"# Add User Avatars\n\nDescription."}
{"op":"update","key":"0001","frontmatter":{"status":"closed"},"content":null}
{"op":"delete","key":"0007"}
```

## Writing the WAL

On `Commit()`:

1. Serialize buffered operations to JSONL.
2. Compute CRC32-C over the JSONL bytes.
3. Write JSONL bytes + checksum to `.wal`.
4. Durability depends on `SyncMode` (see below).

Implementations SHOULD write `.wal` via temp+rename to avoid exposing partial JSONL with a valid length but invalid contents.

## Reading and verification

On WAL replay:

1. Read the WAL bytes.
2. Split: `jsonl = wal[:-4]`, `checksum = wal[-4:]`.
3. Compute CRC32-C over `jsonl`.
4. If checksum matches: parse and replay.
5. If checksum does not match: discard the WAL (treat as crash during WAL write).

## Replay

Replay is done under the exclusive writer lock.

For each operation (in order):

- create: write `<key>.mddb.md`
- update: read existing doc, merge patch, write `<key>.mddb.md`
- delete: remove `<key>.mddb.md`

After applying all operations:

- update/rebuild `.cache` to reflect the new state
- truncate/remove `.wal`

Replay MUST be idempotent: replaying the same WAL twice yields the same final state.

## Durability (SyncMode)

The DB-level durability mode controls `fsync` behavior.

| Mode | Behavior | Notes |
|---|---|---|
| `SyncNone` | no `fsync` | fastest, weakest durability |
| `Sync` | `fsync` files (`.wal`, temp doc files, `.cache`) | directory entries may still be lost on power loss |
| `SyncFull` (default) | `fsync` files + directory | strongest durability on POSIX local FS |

Directory `fsync` is required to make renames/unlinks durable across power loss on many filesystems.
