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
- **Canonical path**: `.tickets/YYYY/MM-DD/<short_id>.md`.
- **Frontmatter**:
  - YAML subset defined below (scalars + flat string lists + flat objects).
  - `id` first, `schema_version` second, remaining keys alphabetical.
  - Keep existing field names (`status`, `type`, `priority`, `blocked-by`, `parent`, etc.).
- **WAL format**: JSONL body + fixed 32-byte footer with CRC (defined below).
- **Filesystem IO**: use `pkg/fs` with `fs.Real` for atomic writes + locking (replaces `github.com/natefinch/atomic`).
- **Locking**: `pkg/fs.Locker` (flock) on `.tickets/.tk/wal`:
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
.tickets/
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
Readers take a **shared** lock on `.tickets/.tk/wal` to avoid mid-commit state.

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
Writers take **exclusive** lock on `.tickets/.tk/wal`.

---

## 4. Storage Format Details

### 4.1 IDs and short_id
- Generate **UUIDv7** for `id`.
- Derive **short_id** (base32 Crockford, first 12 chars) from UUIDv7 random bits.
- If path collision occurs (for creating tickets), regenerate new UUIDv7.

### 4.2 Path Derivation
```
Path(id) = .tickets/YYYY/MM-DD/<short_id>.md
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
- Writers MUST NOT write inside `.tickets/.tk/`.

### 4.7 Rebuild Rules

- Rebuild acquires an **exclusive** lock on `.tickets/.tk/wal` for the full run.
- Rebuild walks `.tickets/` recursively and ignores `.tickets/.tk/`.
- Rebuild uses `github.com/calvinalkan/fileproc` (recursive `ProcessStat` with `Suffix: ".md"`) as the file walker; the callback skips any path under `.tk/`.
- `fileproc` callback paths are borrowed and callbacks may run concurrently; rebuild must copy paths it retains and avoid unsynchronized shared state.
- Only regular `.md` files are considered candidates.
- Orphans (file path does not match canonical path for its `id`) are reported and skipped.
- Symlinked directories are not followed.
- Rebuild executes in a **single SQLite transaction** (DROP/CREATE/INSERT), and sets `PRAGMA user_version` last; no `VACUUM` or `PRAGMA journal_mode` inside the txn.
- Rebuild handles WAL state under the exclusive lock: committed WAL is replayed to files (idempotent) before rebuild; uncommitted WAL is truncated. WAL is truncated only after a successful rebuild.
- WAL replay during rebuild uses the same atomic write/remove helpers as Commit (same fsync semantics).
- Rebuild is a standalone entrypoint and does **not** require `Open()` to succeed; it opens the WAL/SQLite directly under lock.

### 4.8 WAL Format

**File:** `.tickets/.tk/wal` (also the lock target). The file MUST be stable (no delete+replace); modify in place via `ftruncate` + writes.

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
- If WAL is empty: nothing to do.
- If footer missing/invalid: truncate WAL and rebuild index.
- If footer valid but CRC mismatch: return `ErrWALCorrupt` (do not modify documents).
- If footer valid + CRC ok: replay WAL to files (idempotent), attempt to update SQLite; if that fails, return `ErrIndexUpdate` and leave WAL (operator should run `tk rebuild`).

**Force recovery (optional):**
- Operator may copy `.tickets/.tk/wal` for inspection, then `ftruncate` it to 0 and rebuild index.
- Only allowed while holding the exclusive WAL lock.

### 4.9 SQLite Configuration (PRAGMAs)

```go
db.Exec(`
    PRAGMA journal_mode = WAL;        -- concurrent readers
    PRAGMA synchronous = FULL;        -- durable writes before WAL removal
    PRAGMA mmap_size = 268435456;     -- 256MB mmap
    PRAGMA cache_size = -20000;       -- 20MB page cache
    PRAGMA temp_store = MEMORY;       -- faster temp files
`)
```

Apply on `Open()` after connecting.

### 4.10 SQLite Schema (Derived Index)
**Tables** (proposed):
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

### 4.11 Schema Versioning

```go
const schemaVersion = 1

func Open(dir string) (*Store, error) {
    s, err := openStore(dir) // initializes fs/locker/atomic/wal; applies pragmas
    if err != nil {
        return nil, err
    }

    version := getUserVersion(s.sql)
    if version != schemaVersion {
        lock, err := s.locker.Lock(walPath(dir))
        if err != nil {
            return nil, err
        }
        rebuildIndexInTxn(s.sql, dir)
        setUserVersion(s.sql, schemaVersion)
        _ = lock.Close()
    }

    return s, nil
}
```

No migrations. Schema changes → bump version → rebuild.

---

## 5. Operational Pseudocode

### 5.1 Open

```go
func Open(dir string) (*Store, error) {
    fsys := fs.NewReal()
    locker := fs.NewLocker(fsys)
    atomicWriter := fs.NewAtomicWriter(fsys)

    tkDir := dir + "/.tk"
    walPath := tkDir + "/wal"

    db := openSQLite(tkDir + "/index.sqlite")
    ensureDirIfErr(tkDir)
    wal := openWalFile(fsys, walPath)
    applyPragmas(db)

    if schemaVersion(db) != currentSchemaVersion {
        lock, err := locker.Lock(walPath)
        if err != nil {
            return nil, err
        }
        rebuildIndexInTxn(db, dir) // uses fileproc
        setSchemaVersion(db, currentSchemaVersion)
        _ = lock.Close()
    }

    if walHasData(wal) {
        lock, err := locker.Lock(walPath)
        if err != nil {
            return nil, err
        }
        defer lock.Close()

        switch walState := validateWal(wal); walState {
        case walUncommitted: // footer missing/invalid
            truncateWal(wal)
            rebuildIndexInTxn(db, dir)
        case walCommitted:
            ops := readWal(wal)
            replayToFiles(ops)   // temp+rename, idempotent
            if err := updateSQLite(db, ops); err != nil {
                return nil, ErrIndexUpdate(err)
            }
            truncateWal(wal)
        case walCorrupt:
            return nil, ErrWALCorrupt
        }
    }

    return &Store{
        dir: dir,
        sql: db,
        fs: fsys,
        locker: locker,
        atomic: atomicWriter,
        wal: wal,
    }, nil
}
```

### 5.2 Commit

```go
func (tx *Tx) Commit() error {
    walPath := tx.store.dir + "/.tk/wal"
    lock, err := tx.store.locker.Lock(walPath)
    if err != nil {
        return err
    }
    defer lock.Close()

    wal := tx.store.wal
    if walHasData(wal) {
        if err := recoverWal(tx.store); err != nil {
            return err // do not truncate on corrupt WAL
        }
    }
    ftruncate(wal, 0) // clear before writing new body+footer

    writeWalBody(wal, tx.ops)
    writeWalFooter(wal) // includes length + CRC
    fsync(wal)          // commit point

    for _, op := range tx.ops {
        switch op.Type {
        case Put:
            tx.store.atomic.WriteWithDefaults(op.Path, strings.NewReader(op.Content)) // pkg/fs.AtomicWriter
        case Delete:
            tx.store.fs.Remove(op.Path)
            fsyncDir(tx.store.fs, parent(op.Path))
        }
    }

    if err := updateSQLite(tx.store.sql, tx.ops); err != nil { // single txn
        return ErrIndexUpdate(err) // leave WAL; run `tk rebuild`
    }
    truncateWal(wal)

    return nil
}
```

### 5.3 Read Path (Get / Query)

```go
func (s *Store) Query(opts QueryOpts) ([]Summary, error) {
    walPath := s.dir + "/.tk/wal"
    readLock, err := s.locker.RLock(walPath)
    if err != nil {
        return nil, err
    }
    defer lock.Close()

    if walHasData(s.wal) {
        _ = readLock.Close()
        writeLock, err = s.locker.Lock(walPath)
        if err != nil {
            return nil, err
        }
        if err := recoverWal(s); err != nil {
            return nil, err
        }
        _ = writeLock.Close()

        readLock, err = s.locker.RLock(walPath)
        if err != nil {
            return nil, err
        }
        defer readLock.Close()
    }

    return querySQLite(s.sql, opts)
}

func (s *Store) Get(id string) (*Ticket, error) {
    walPath := s.dir + "/.tk/wal"
    readLock, err := s.locker.RLock(walPath)
    if err != nil {
        return nil, err
    }
    defer lock.Close()

    if walHasData(s.wal) {
        _ = lock.Close()
        readLock, err = s.locker.Lock(walPath)
        if err != nil {
            return nil, err
        }
        if err := recoverWal(s); err != nil {
            return nil, err
        }
        _ = lock.Close()

        readLock, err = s.locker.RLock(walPath)
        if err != nil {
            return nil, err
        }
        defer lock.Close()
    }

    path := pathFor(id)
    return readTicket(s.fs, path)
}
```

---

## 6. Codebase Structure + Public API

### 6.1 Package Tree

```
internal/
  cli/
    create.go
    ls.go
    ready.go
    show.go
    start.go
    close.go
    reopen.go
    block.go
    unblock.go
    edit.go
    rebuild.go
    ...

  store/
    store.go        # Open/Close, lock handling, recovery
    fs.go           # filesystem wiring (pkg/fs Real, AtomicWriter, Locker)
    tx.go           # Tx buffer + Commit/Rollback
    wal.go          # WAL encode/decode + footer CRC
    index_sqlite.go # PRAGMAs, schema init, queries
    rebuild.go      # rebuild index from files (uses fileproc.ProcessStat)
    path.go         # UUIDv7 + short_id + path derivation
    file.go         # read/parse/format ticket files
    types.go        # Ticket, Summary, QueryOpts
    errors.go       # ErrNotFound, ErrWALCorrupt, ErrIndexUpdate, ...
```

### 6.2 Public API (CLI uses only store)

```go
type Store struct {
  dir string
  sql *sql.DB
  fs  fs.FS
  locker *fs.Locker
  atomic *fs.AtomicWriter
  wal fs.File
}

// fs/locker/atomic are created once in Open and reused across operations.
func Open(dir string) (*Store, error)
func (s *Store) Close() error

func (s *Store) Get(id string) (*Ticket, error)
func (s *Store) Query(opts QueryOpts) ([]Summary, error)

func (s *Store) Begin() (*Tx, error)
func (tx *Tx) Put(t *Ticket) error
func (tx *Tx) Delete(id string) error
func (tx *Tx) Commit() error
func (tx *Tx) Rollback() error

func Rebuild(dir string) error
```

Rules ownership:
- **Business rules live in CLI** (parent/child, blockers, status transitions).
- **Store enforces format correctness** (frontmatter parsing, required fields, schema_version).

---

## 7. CLI Integration

### 7.1 Command → Store mapping

| Command | Current behavior | New behavior |
|--------|------------------|--------------|
| `create` | write file + update cache | store.Begin → Put → Commit |
| `ls` | ListTickets (cache scan) | store.Query |
| `ready` | scan summaries + Go logic | SQL query |
| `show` | read file | store.Get |
| `start/close/reopen` | per-file lock + edit | store.Update via Tx |
| `block/unblock` | per-file lock + edit | store.Update via Tx |
| `edit --apply` | direct file write | store.Update via Tx |
| `rebuild` (was `repair --rebuild-cache`) | rebuild binary cache | store.Rebuild(dir) |

### 7.2 Dataflow Diagram

```
CLI Command
    │
    │ (business rules / checks)
    ▼
internal/store
    │
    ├─ Begin() → Tx
    │    ├─ Put/Delete (buffered)
    │    └─ Commit() → WAL → Files → SQLite
    │
    ├─ Get(id)      → read file
    ├─ Query()      → SQLite only
    └─ Rebuild(dir) → scan files → SQLite
```

---

## 8. Transaction / Read Dataflow

```
Tx.Commit()
   │
   ├─ LOCK_EX on .tickets/.tk/wal
   ├─ recover WAL if needed
   ├─ write WAL body
   ├─ write WAL footer + fsync (commit point)
   ├─ write files (temp + rename + fsync file/dir)
   ├─ update SQLite index (single transaction)
   ├─ truncate WAL
   └─ unlock
```

```
Query()
   │
   ├─ LOCK_SH on .tickets/.tk/wal
   ├─ recover WAL if needed
   └─ query SQLite
```

```
Get(id)
   │
   ├─ LOCK_SH on .tickets/.tk/wal
   ├─ recover WAL if needed
   └─ read file directly
```

---

## 9. External Edits / Staleness

- `Get(id)` always reads the file and is always fresh.
- `Query()` reads SQLite and may be stale if files are edited outside tk.
- SQLite stores `mtime_ns` for each ticket to help detect staleness.
- The primary remediation is an explicit `tk rebuild` (full reindex).
- Treat externally edited frontmatter that introduces invalid references (e.g., missing blockers/parents) as errors (warn + non-ready).
- Optional future: file watcher to trigger rebuild/refresh.
- Optional future: per-result mtime verification for queries.

---

## 10. Migration Phases (Checkbox Plan)

### Phase 1 — Core primitives
- [ ] Create `internal/store/` package skeleton
- [ ] Implement UUIDv7 generator (internal; no new dependency)
- [ ] Implement short_id derivation (base32, length 12)
- [ ] Implement path derivation (`YYYY/MM-DD/<short_id>.md`)
- [ ] Implement YAML subset parsing (frontmatter only)
- [x] Implement deterministic frontmatter serialization

### Phase 2 — SQLite index
- [ ] Define schema + version (`PRAGMA user_version`)
- [ ] Implement `Open()` SQLite initialization
- [ ] Implement `Rebuild(dir)` using `fileproc.ProcessStat` (recursive, suffix `.md`)
- [ ] Add `github.com/calvinalkan/fileproc` dependency (go.mod; go.work for local dev)
- [ ] Add `github.com/mattn/go-sqlite3` dependency (CGO)
- [ ] Rebuild runs in a single SQLite transaction (DROP/CREATE/INSERT, set `user_version` last)
- [ ] Insert/update `ticket_blockers` table
- [ ] Implement `Query()` with filters + ordering by `id`

### Phase 3 — WAL + locking
- [ ] Implement WAL format (JSONL + footer/CRC)
- [ ] Implement WAL recovery on Open
- [ ] Implement lock file using `pkg/fs.Locker` on `.tk/wal`
- [ ] Add shared lock for reads, exclusive for writes
- [ ] Add atomic write helper using `pkg/fs.AtomicWriter` (temp + rename + fsync)

### Phase 4 — Store API
- [ ] Implement `Get(id)` (strict)
- [ ] Implement Tx buffer + `Put`/`Delete`
- [ ] Implement `Commit()` sequence: WAL → files → SQLite → truncate WAL
- [ ] Implement `Rollback()`
- [ ] Add typed query options (status/type/priority/parent/short-id)
- [ ] Expose `Rebuild(dir)` as a standalone entrypoint (no `Open()` required)

### Phase 5 — CLI migration
- [ ] Replace `internal/ticket.ListTickets` usage with store.Query
- [ ] Replace per-command file edits with store Tx updates
- [ ] Update `ready` to use SQL query
- [ ] Update `edit --apply` to go through store
- [ ] Rename `repair --rebuild-cache` to `rebuild` (no flags)
- [ ] Keep CLI output unchanged where possible

### Phase 6 — Tests
- [ ] Keep CLI/e2e tests as the primary safety net
- [ ] Update CLI tests for new IDs + paths
- [ ] Remove old cache/lock tests under `internal/ticket`

### Phase 7 — Cleanup / docs
- [ ] Move `internal/ticket/config.go` → `internal/cli/config.go`
- [ ] Split `internal/ticket/errors.go` into CLI/store errors
- [ ] Move ticket structs + frontmatter parsing → `internal/store/types.go` + `internal/store/file.go`
- [ ] Remove binary cache code (`internal/ticket/cache*`)
- [ ] Remove per-ticket locking (`internal/ticket/lock.go`)
- [ ] Remove `github.com/natefinch/atomic` dependency (use `pkg/fs`)
- [ ] Remove `internal/ticket` tests (cache-specific, no longer relevant)
- [ ] Update README + CLI docs to new layout
- [ ] Add `tk rebuild` doc

---

## 11. Open Questions (Deferred / Optional)

- File watcher for external edits (optional)
- Optional `tk migrate` helper for rewriting old tickets
- Performance tuning for SQLite indices

---

## 12. Acceptance Checklist
- [ ] All CLI commands operate through store
- [ ] Reads never see mid-commit state
- [ ] WAL recovery passes tests
- [ ] Rebuild works on corrupted/missing SQLite
- [ ] No direct cache code remains
- [ ] E2E tests pass (`make check`)
- [ ] Update AGENTS.md to reflect the new architecture layout
