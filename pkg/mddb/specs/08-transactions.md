# Transactions and locking

mddb provides a single-writer transaction model with roll-forward crash recovery.

## Locking model

- Multiple readers are allowed concurrently.
- There is at most one writer across processes.

mddb uses a file lock (e.g. `flock`) to enforce this:

- `Begin()` acquires an **exclusive** lock.
- `Commit()` / `Abort()` releases it.
- `Open()` also acquires the lock when WAL replay is required.

Reads do not acquire locks.

## Transaction lifecycle

```
Begin()  → acquire exclusive lock
         → replay WAL if exists
         ↓
Create/Update/Delete → buffer writes in memory
         ↓
Commit() → write .wal (intent)
         → apply changes to .mddb.md files
         → update .cache
         → truncate .wal
         → release lock

Abort()  → discard buffer
         → release lock
```

## Operations

### Create(key, doc)

- Fails with `ErrExists` if the key already exists.
- Validates `key` (`ErrInvalidKey`).
- Validates frontmatter against schema immediately (`ErrFieldValue`).
- `doc.Content` MUST be non-nil.

### Update(key, patch)

- Fails with `ErrNotFound` if key does not exist.
- Shallow-merges frontmatter; `nil` values delete keys.
- If `patch.Content == nil`, content is preserved.
- Validates the merged frontmatter immediately (`ErrFieldValue`).

### Delete(key)

- Fails with `ErrNotFound` if key does not exist.

## Read-your-writes

Inside a transaction:

- `Get(key)` MUST reflect buffered creates/updates/deletes.
- `Filter(...)` MUST reflect buffered creates/updates/deletes.

Outside the process, uncommitted writes are not visible.

## Commit file update strategy (atomicity)

When applying changes to `*.mddb.md` files, mddb SHOULD use an atomic write pattern:

1. write new contents to a temp file in the same directory
2. `fsync` the temp file (depending on SyncMode)
3. `rename` temp → final path (atomic within a directory)
4. `fsync` the directory (depending on SyncMode)

Deletes SHOULD be followed by directory `fsync` under the strongest durability mode.

## Crash behavior

- If a crash occurs before `.wal` is fully written and checksummed, WAL replay will ignore it.
- If a crash occurs after `.wal` is valid but before all file updates are applied, replay re-applies it.
- If `.wal` exists at `Open()`, mddb MUST acquire the exclusive lock and replay it before serving reads.

See [WAL](wal.md) for details.
