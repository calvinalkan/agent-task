# tk Storage Migration Plan (SQLite + Markdown)

## 0. Big Picture / Goals

We are replacing the current binary mmap cache and scattered ticket IO with a **single, coherent storage layer** that uses:

- **Markdown files as the sole source of truth** (authoritative).
- **SQLite as a derived, read-only query index** (fast queries, safe to rebuild).
- **A WAL file + global lock** for atomic multi-file commits and crash recovery.

This is a clean-slate design that lives **directly inside tk** (no external storage abstraction). We are intentionally **not backward compatible** with the current storage layout or cache format.

Key outcomes:
- Simple, predictable data flow.
- One place to implement IO, locking, WAL, index updates.
- CLI commands become thin wrappers around a single store API.
- External edits are handled via `rebuild` (optional watcher later).

Importat: We do **not** support windows. We assume a POSIX-like filesystem and linux/unix. Dont add conditionals for windows in tests nor in the codebase.

---

## 1. Decisions (Locked In)

- **Markdown is authoritative**. SQLite is derived only.
- **Writes never consult SQLite** (filesystem is the source of truth).
- **SQLite driver**: `github.com/mattn/go-sqlite3` (CGO).
- **IDs are UUIDv7** (`github.com/google/uuid` v1.6+).
- **short_id**: base32 (Crockford) derived from UUIDv7 random bits; length **12**.
- **Canonical path**: `ticketDir/YYYY/MM-DD/<short_id>.md` (relative path: `YYYY/MM-DD/<short_id>.md`).
- **Frontmatter**:
  - YAML subset defined below (scalars + flat string lists + flat objects).
  - `id` first, `schema_version` second, remaining keys alphabetical.
  - Keep existing field names (`status`, `type`, `priority`, `blocked-by`, `parent`, etc.).
- **WAL format**: JSONL body + fixed 32-byte footer with CRC (defined below).
- **Filesystem IO**: use `pkg/fs` with `fs.Real` for atomic writes + locking (replaces `github.com/natefinch/atomic`).
- **Locking**: `pkg/fs.Locker` (flock) on `ticketDir/.tk/wal`:
  - shared lock for reads
  - exclusive lock for writes and recovery
- **Get(id)** is strict (no filesystem scan).
- **Ready query** is computed in SQLite (not Go-scanned summaries).
- **Short-id resolution**: CLI accepts UUID prefix or short_id prefix; ambiguous matches list candidates and require more input.
- **No backward compatibility** with current cache/layout.

---

## 2. Core Principles

- **Files are authoritative**; the index is disposable and rebuildable.
- **Deterministic serialization** for stable diffs.
- **Restricted YAML subset** for fast parsing.
- **Single-writer, multi-reader** with a stable lock file.
- **Atomic commits via WAL** (roll-forward recovery only).
- **Rebuild is always possible** from documents.
- **Strict validation** of required fields and schema_version.

---

## 3. Target Architecture

### 3.1 Directory Layout
```
ticketDir/
  YYYY/
    MM-DD/
      <short_id>.md
  .tk/
    wal            # WAL + lock file (stable inode)
    index.sqlite   # derived index
    tmp/           # optional temp files
```

### 3.2 Data Flow

**Reads**
```
Query(...)  -> SQLite (index only)
Get(id)    -> read markdown file directly
```
Readers take a **shared** lock on `ticketDir/.tk/wal` to avoid mid-commit state.

**Writes**
```
Begin -> buffer ops
Commit:
  1) write WAL body
  2) publish WAL footer (commit point)
  3) write files (temp + rename + fsync file/dir)
  4) update SQLite index in a single transaction (on failure keep WAL)
  5) truncate WAL
```
Writers take **exclusive** lock on `ticketDir/.tk/wal`.

---

## 4. Storage Format Details

### 4.1 IDs and short_id
- Generate **UUIDv7** for `id`.
- Derive **short_id** (base32 Crockford, first 12 chars) from UUIDv7 random bits.
- If path collision occurs (for creating tickets), regenerate new UUIDv7.

### 4.2 Path Derivation
```
Path(id) = YYYY/MM-DD/<short_id>.md   // relative to ticketDir
```
- `YYYY/MM-DD` derived from UUIDv7 timestamp **in UTC** (no local-time ambiguity).
- `short_id` from UUIDv7 random bits.
- `id` stored in frontmatter, not in filename.

### 4.3 YAML Subset

Supported YAML within frontmatter:
- **Scalars:** strings, integers, booleans
- **Flat string lists:** sequences of strings
- **Flat objects:** single-level map of string → scalar

Not supported:
- Nested objects
- Lists of objects
- Lists of integers/booleans
- Anchors, aliases, tags

### 4.4 Document Format
```
---
id: <uuidv7>
schema_version: 1
assignee: ...
blocked-by:
  - <id>
  - <id>
closed: 2026-01-27T15:23:10Z
created: 2026-01-27T15:23:10Z
external-ref: ...
parent: ...
priority: 2
status: open
type: task
---

# Title
...
```

Body is raw markdown after frontmatter.

Ordering:
1) `id`
2) `schema_version`
3) remaining keys sorted alphabetically

### 4.5 Document Validity + ID Rules

- File MUST start with a line exactly `---` and contain a closing `---` fence.
- Implementations SHOULD enforce a maximum frontmatter scan length (e.g., 100 lines) before treating as invalid.
- Frontmatter MUST parse to a mapping within the YAML subset (custom simple parser, no yaml package)
- `id` and `schema_version` are required.
- `created`/`closed` timestamps MUST be RFC3339 UTC; writers normalize to UTC on write.
- `id` in frontmatter MUST match the canonical path-derived identity.
- `Get(id)` returns an error if the canonical file exists but its frontmatter `id` differs.
- Only **regular files** are considered tickets (no symlinks, devices, sockets, fifos).

**ID validation (UUIDv7):**
- Non-empty canonical UUIDv7 string.
- Must not contain path separators or whitespace.
- Must not contain NUL bytes.

### 4.6 Write Rules

- Writers MUST create missing parent directories.
- Writers MUST use temp-file + rename for atomic writes.
- Writers MUST use `pkg/fs.AtomicWriter` (with `fs.Real`) for atomic writes (temp file + fsync + dir sync).
- Writers MUST fsync temp files before rename.
- Writers MUST fsync parent directories after rename/delete to persist metadata (deduplicate them based on file layout to avoid unnecessary fsyncs).
- Temp files MUST NOT end with `.md` (to avoid accidental indexing).
- Writers MUST NOT write inside `ticketDir/.tk/`.

### 4.7 Rebuild Rules

- Rebuild acquires an **exclusive** lock on `ticketDir/.tk/wal` for the full run.
- Rebuild walks `ticketDir/` recursively and ignores `ticketDir/.tk/`.
- Rebuild uses `github.com/calvinalkan/fileproc` (recursive `ProcessStat` with `Suffix: ".md"`) as the file walker; the callback skips any path under `.tk/`.
- `fileproc` callback paths are borrowed and callbacks may run concurrently; rebuild must copy paths it retains and avoid unsynchronized shared state.
- Only regular `.md` files are considered candidates.
- Orphans (file path does not match canonical path for its `id`) are reported as scan errors; rebuild does not update SQLite.
- Symlinked directories are not followed.
- Rebuild returns scan errors without updating SQLite (no partial index); fix files and rerun.
- Rebuild executes in a **single SQLite transaction** (DROP/CREATE/INSERT), and sets `PRAGMA user_version` last; no `VACUUM` or `PRAGMA journal_mode` inside the txn.
- Rebuild handles WAL state under the exclusive lock: committed WAL is replayed to files (idempotent) before rebuild; uncommitted WAL is truncated. WAL is truncated only after a successful rebuild.
- WAL replay during rebuild uses the same atomic write/remove helpers as Commit (same fsync semantics).
- Rebuild is a standalone entrypoint and does **not** require `Open()` to succeed; it opens the WAL/SQLite directly under lock.

### 4.8 WAL Format

**File:** `ticketDir/.tk/wal` (also the lock target). The file MUST be stable (no delete+replace); modify in place via `ftruncate` + writes.

**Body:** UTF-8 JSON Lines, one record per line. Records are the **net ops** for a commit.

`put` record:
```json
{"op":"put","id":"<uuid>","path":"<relpath>","frontmatter":{...},"content":"..."}
```

`delete` record:
```json
{"op":"delete","id":"<uuid>","path":"<relpath>"}
```

**Replay path validation:**
- `path` must be relative, contain no `..`, and end with `.md`.
- `path` must match the canonical path derived from `id`.
- If validation fails, recovery returns `ErrWALReplay`.

**Replay semantics (idempotent):**
- `put` always overwrites the target using the normal atomic write helper.
- `delete` ignores `ENOENT` (already deleted).
- Replay uses the same atomic write/remove helpers as Commit (`pkg/fs.AtomicWriter` + `fs.Remove`, same fsync semantics).
- Any other filesystem error aborts recovery with `ErrWALReplay`.

**Footer:** fixed 32 bytes, little-endian:
```
struct WalFooterV1 {
  u8  magic[8];        // "TKWAL001"
  u64 body_len;        // bytes in JSONL body
  u64 body_len_inv;    // bitwise NOT of body_len
  u32 crc32c;          // CRC32-C of JSONL body
  u32 crc32c_inv;      // bitwise NOT of crc32c
}
```

**Commit point:** footer is appended and `fsync(wal)` completes. If the footer is missing/invalid, the WAL is treated as **uncommitted** and truncated.

**Recovery (on Open):**
- If WAL is empty (0 bytes): nothing to do, return nil immediately.
- If footer missing/invalid (uncommitted): truncate WAL, return nil.
- If footer valid but CRC mismatch: return `ErrWALCorrupt` (do not modify documents).
- If footer valid + CRC ok (committed): `recoverWalLocked` does all work in one call:
  1. Replay WAL ops to filesystem (idempotent)
  2. Update SQLite index from ops (single transaction with `BEGIN IMMEDIATE`)
  3. Truncate WAL
  On any error, return immediately (caller should treat as fatal).

**Implementation note:** `recoverWalLocked(ctx)` returns `error` only (no intermediate state struct). Callers hold exclusive lock before calling.

**Force recovery (optional):**
- Operator may copy `ticketDir/.tk/wal` for inspection, then `ftruncate` it to 0 and rebuild index.
- Only allowed while holding the exclusive WAL lock.

### 4.9 SQLite Configuration

**PRAGMAs** (applied on connect):
| Pragma | Value | Purpose |
|--------|-------|---------|
| `busy_timeout` | 10000 | 10s wait on locked DB |
| `journal_mode` | WAL | Concurrent readers |
| `synchronous` | FULL | Durable writes |
| `mmap_size` | 268435456 | 256MB mmap |
| `cache_size` | -20000 | 20MB page cache |
| `temp_store` | MEMORY | Faster temp files |

**Transactions:** All `BeginTx` calls use `sql.LevelSerializable` → `BEGIN IMMEDIATE` in SQLite.

→ Implemented in `sql.go:applyPragmas()`

### 4.10 SQLite Schema

```sql
CREATE TABLE tickets (
  id TEXT PRIMARY KEY,
  short_id TEXT NOT NULL,
  path TEXT NOT NULL,
  mtime_ns INTEGER NOT NULL,
  status TEXT NOT NULL,
  type TEXT NOT NULL,
  priority INTEGER NOT NULL,
  assignee TEXT,
  parent TEXT,
  created_at INTEGER NOT NULL,
  closed_at INTEGER,
  external_ref TEXT,
  title TEXT NOT NULL
) WITHOUT ROWID;

CREATE TABLE ticket_blockers (
  ticket_id TEXT NOT NULL,
  blocker_id TEXT NOT NULL,
  PRIMARY KEY (ticket_id, blocker_id)
) WITHOUT ROWID;

CREATE INDEX idx_status_priority ON tickets(status, priority);
CREATE INDEX idx_status_type ON tickets(status, type);
CREATE INDEX idx_parent ON tickets(parent);
CREATE INDEX idx_short_id ON tickets(short_id);
CREATE INDEX idx_blocker ON ticket_blockers(blocker_id);
```

→ Implemented in `sql.go:dropAndRecreateSchema()`

### 4.11 Schema Versioning

- `currentSchemaVersion = 1` constant in `sql.go`
- Stored via `PRAGMA user_version`
- On `Open()`: if version mismatch, trigger full reindex
- No migrations - schema changes bump version and rebuild

→ Implemented in `sql.go:storedSchemaVersion()`, `store.go:Open()`

---

## 5. Implementation Reference

### 5.1 Open

1. Create `pkg/fs` instances (Real, Locker, AtomicWriter)
2. Ensure `.tk/` directory exists
3. Open WAL file and SQLite database (applies pragmas)
4. If no work needed (WAL empty + schema current): return immediately
5. Acquire exclusive lock with 10s timeout
6. If schema mismatch: run `reindexLocked()` (recovers WAL + scans files + rebuilds SQLite)
7. Else: run `recoverWalLocked()` (replays WAL to FS + SQLite, truncates)
8. Release lock

→ Implemented in `store.go:Open()` (lines 40-95)

### 5.2 Query

1. Acquire shared lock with 10s timeout
2. If WAL has data: upgrade to exclusive lock, run `recoverWalLocked()`, release, reacquire shared
3. Execute query via `queryTickets()`:
   - Uses LEFT JOIN to fetch tickets + blockers in single query
   - When LIMIT/OFFSET specified: subquery paginates tickets first, then joins blockers
4. Release lock

→ Implemented in `query.go:Query()`, `sql.go:queryTickets()`, `sql.go:buildTicketQuery()`

### 5.3 Recovery (recoverWalLocked)

Called under exclusive lock. Returns `error` only.

1. If WAL empty (0 bytes): return nil
2. Read and validate footer; if missing/invalid: truncate WAL, return nil
3. If CRC mismatch: return `ErrWALCorrupt`
4. Parse JSONL ops from body
5. Replay ops to filesystem (put = atomic write, delete = remove)
6. Update SQLite in single `BEGIN IMMEDIATE` transaction via `ticketInserter`
7. Truncate WAL

→ Implemented in `wal.go:recoverWalLocked()`

### 5.4 Reindex

1. Acquire exclusive lock with 10s timeout
2. Run `recoverWalLocked()` first (replay any committed WAL)
3. Walk ticketDir recursively via `fileproc.ProcessStat` (suffix `.md`, skip `.tk/`)
4. Parse each file, validate frontmatter, check path matches canonical
5. If any scan errors: return errors without updating SQLite
6. In single transaction: DROP tables, CREATE schema, INSERT all tickets + blockers, set `user_version`
7. Truncate WAL
8. Release lock

→ Implemented in `reindex.go:Reindex()`, `reindex.go:reindexLocked()`

### 5.5 Shared SQL Helpers

**ticketInserter:** Prepared statement wrapper for inserting tickets + blockers. Used by both WAL recovery and reindex to avoid code duplication.

→ Implemented in `sql.go:ticketInserter`, `sql.go:prepareTicketInserter()`

### 5.6 Commit (TODO)

Not yet implemented. Sequence:
1. Acquire exclusive lock
2. Recover any existing WAL
3. Truncate WAL, write body + footer, fsync (commit point)
4. Write files via AtomicWriter
5. Update SQLite
6. Truncate WAL

### 5.7 Get (TODO)

Not yet implemented. Will:
1. Acquire shared lock
2. Recover WAL if needed
3. Read file directly from canonical path

---

## 6. Package Structure

### 6.1 File Layout (`internal/store/`)

| File | Purpose |
|------|---------|
| `store.go` | `Open()`, `Close()`, `Store` struct, `lockTimeout` constant |
| `wal.go` | WAL encode/decode, footer CRC, `recoverWalLocked()` |
| `sql.go` | SQLite pragmas, schema, `queryTickets()`, `ticketInserter` |
| `reindex.go` | `Reindex()`, `Rebuild()`, file scanning via fileproc |
| `query.go` | `Query()` method with lock handling |
| `frontmatter.go` | YAML subset parsing + deterministic serialization |
| `ids.go` | UUIDv7 + short_id derivation |
| `path.go` | Path derivation from ID |

### 6.2 Public API

**Implemented:**
- `Open(ctx, dir) (*Store, error)` — `store.go`
- `(*Store) Close() error` — `store.go`
- `(*Store) Query(ctx, opts) ([]Ticket, error)` — `query.go`
- `(*Store) Reindex(ctx) (int, error)` — `reindex.go`
- `Rebuild(ctx, ticketDir) (int, error)` — `reindex.go` (standalone, no Open required)

**TODO:**
- `(*Store) Get(id) (*Ticket, error)`
- `(*Store) Begin() (*Tx, error)`
- `(*Tx) Put(t *Ticket) error`
- `(*Tx) Delete(id string) error`
- `(*Tx) Commit() error`
- `(*Tx) Rollback() error`

### 6.3 Ownership

- **CLI owns business rules:** parent/child relationships, blockers, status transitions
- **Store owns format correctness:** frontmatter parsing, required fields, schema_version

---

## 7. CLI Integration

| Command | Store API |
|--------|-----------|
| `create` | `Begin()` → `Put()` → `Commit()` |
| `ls` | `Query()` |
| `ready` | `Query()` with status filter |
| `show` | `Get()` |
| `start/close/reopen` | `Begin()` → `Put()` → `Commit()` |
| `block/unblock` | `Begin()` → `Put()` → `Commit()` |
| `edit --apply` | `Begin()` → `Put()` → `Commit()` |
| `rebuild` | `Rebuild()` |

---

## 8. External Edits / Staleness

- `Get(id)` always reads the file → always fresh
- `Query()` reads SQLite → may be stale if files edited outside tk
- SQLite stores `mtime_ns` per ticket (for future staleness detection)
- Primary remediation: explicit `tk rebuild`
- Invalid references from external edits: warn + mark non-ready
- Future: file watcher, per-result mtime verification

---

## 9. Migration Phases

### Phase 1 — Core primitives
- [x] Create `internal/store/` package skeleton
- [x] Implement UUIDv7 generator (with google uuid package)
- [x] Implement short_id derivation (base32, length 12)
- [x] Implement path derivation (`YYYY/MM-DD/<short_id>.md`)
- [x] Implement YAML subset parsing (frontmatter only)
- [x] Implement deterministic frontmatter serialization

### Phase 2 — SQLite index
- [x] Define schema + version (`PRAGMA user_version`)
- [x] Implement `Open()` SQLite initialization
- [x] Implement `Rebuild(ctx, ticketDir)` using `fileproc.ProcessStat` (recursive, suffix `.md`)
- [x] Add `github.com/calvinalkan/fileproc` dependency (go.mod; go.work for local dev)
- [x] Add `github.com/mattn/go-sqlite3` dependency (CGO)
- [x] Rebuild runs in a single SQLite transaction (DROP/CREATE/INSERT, set `user_version` last)
- [x] Insert/update `ticket_blockers` table
- [x] Implement `Query()` with filters + ordering by `id`

### Phase 3 — WAL + locking
- [x] Implement WAL format (JSONL + footer/CRC)
- [x] Implement WAL recovery on Open
- [x] Implement lock file using `pkg/fs.Locker` on `.tk/wal`
- [x] Add shared lock for reads, exclusive for writes
- [x] Add atomic write helper using `pkg/fs.AtomicWriter` (temp + rename + fsync)
- [x] Add lock acquisition timeouts (10s) via `LockWithTimeout`/`RLockWithTimeout`
- [x] Add SQLite busy_timeout pragma (10s)

### Phase 4 — Store API
- [x] Implement `Get(id)` 
- [x] Implement Tx buffer + `Put`/`Delete` (tx.go)
- [x] Implement `Commit()` sequence: WAL → files → SQLite → truncate WAL (tx.go)
- [x] Implement `Rollback()` (tx.go)
- [x] Add typed query options (status/type/priority/parent/short-id)
- [x] Expose `Rebuild(ctx, ticketDir)` as a standalone entrypoint (no `Open()` required)
- [x] Query uses LEFT JOIN for tickets + blockers (single query)
- [x] Query handles LIMIT/OFFSET with subquery for correct pagination

### Phase 5 — CLI migration
- [ ] Replace `internal/ticket.ListTickets` usage with store.Query
- [ ] Replace per-command file edits with store Tx updates
- [ ] Update `ready` to use SQL query
- [ ] Update `edit --apply` to go through store
- [ ] Rename `repair --rebuild-cache` to `rebuild` (no flags)
- [ ] Keep CLI output unchanged where possible

### Phase 6 — Tests
- [ ] Keep CLI/e2e tests as the primary safety net
- [ ] Update CLI tests for new IDs + paths, make spec tests in testutil use uuidv7
- [ ] Remove old cache/lock tests under `internal/ticket`

### Phase 7 — Cleanup / docs
- [ ] Move `internal/ticket/config.go` → `internal/cli/config.go`
- [ ] Split `internal/ticket/errors.go` into cli files as inline erorrs, not sentital, unless cli programmatically handles them.
- [ ] Remove binary cache code (`internal/ticket/cache*`)
- [ ] Remove per-ticket locking (`internal/ticket/lock.go`)
- [ ] Remove `github.com/natefinch/atomic` dependency (use `pkg/fs`)
- [ ] Remove `internal/ticket` (no longer relevant)
- [ ] All CLI commands use the correct architecture and reference the new APIs

---

## 10. Open Questions

- File watcher for external edits
- `tk migrate` helper for rewriting old tickets
- SQLite index performance tuning

---

## 11. Acceptance Checklist
- [ ] All CLI commands operate through store
- [ ] Reads never see mid-commit state
- [ ] WAL recovery passes tests
- [ ] Rebuild works on corrupted/missing SQLite
- [ ] No direct cache code remains
- [ ] E2E tests pass (`make check`)
- [ ] Update AGENTS.md to reflect the new architecture layout
