# Filesystem layout

## Database directory

An mddb database lives under a single **data directory** (chosen by the caller):

1. **Document files**: `*.mddb.md` files under caller-defined relative paths.
2. **Internal directory**: `.mddb/` for WAL/lock and cache.

## Internal directory

mddb reserves the path `<data-dir>/.mddb/`.

- mddb MUST NOT treat any file under `.mddb/` as a document.
- mddb MUST create `.mddb/` if it does not exist.

Recommended permissions: `0700` for the directory, `0600` for internal files. Document file permissions are controlled by the caller.

### Locking

mddb coordinates concurrent access using advisory file locks (`flock(2)`) on:

- `<data-dir>/.mddb/wal`

Lock modes:

- **Exclusive lock**: writers (`Begin()`/`Commit()`) and crash recovery
- **Shared lock**: read transactions (`BeginReadTx()`)

Lock-free reads (`Get`, `Query`) do not acquire any lock.

**Stability requirement:** `flock` locks an inode, not a pathname. Therefore `.mddb/wal` MUST be treated as a stable lock file and MUST NOT be replaced (renamed over, unlinked, delete+recreate) while the database may be in use. Implementations MUST only modify `.mddb/wal` in place (for example using `ftruncate` and writes).

### Reserved paths

| Path | Purpose |
|------|---------|
| `.mddb/wal` | Write-ahead log (WAL) and lock target (`flock`) |
| `.mddb/cache.slc` | slotcache index file |
| `.mddb/tmp/` | Temporary files (optional) |

Implementations MAY also create lock files required by slotcache (e.g., `cache.slc.lock`).

## Document files

### Extension

mddb-managed documents MUST have the extension `.mddb.md`.

This distinguishes mddb documents from arbitrary markdown and makes them easy to target via `find`/`rg`.

### Regular files only

A canonical document MUST be a **regular file**.

Symlinks, device nodes, sockets, and fifos MUST NOT be treated as documents. `Get(id)` MUST return an error if the canonical path exists but is not a regular file.

### Nested directories

Documents MAY be stored in nested subdirectories. The directory structure is controlled by a caller-provided `PathOf(id)` mapping (see [Path mapping](005-path-mapping.md)).

### Temporary files during atomic writes

Writers SHOULD use temp-file + rename to ensure readers never observe partial documents.

Temporary files (e.g., `<doc>.mddb.md.tmp.<random>`):

- MUST NOT end with `.mddb.md` (so they are never indexed)
- SHOULD be removed on startup or during rebuild if discovered

## Rebuild enumeration

Rebuild enumeration rules (including symlink handling) are defined in [Rebuild](011-rebuild.md).
