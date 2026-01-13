# mddb: Markdown Database

mddb is a document database for markdown files with YAML frontmatter, optimized for agentic CLI tools operating on local files.

This document specifies:
- Goals, use cases, and design rationale
- File layout and document format
- Schema and typed index
- Public API contract
- Transaction and locking model
- WAL semantics
- Cache invalidation protocol
- Error model
- Limitations and tradeoffs

## Goals

- **Human-readable storage**: Plain markdown files, readable with cat/grep/vim
- **Git-native**: Files work with git for replication, history, branching, merge conflict resolution
- **Fast indexed reads**: Filter without scanning all files
- **Typed schema**: Compile-time safety via generics
- **Crash-safe writes**: WAL-based durability
- **Zero server**: Embedded library, no daemon process
- **External change friendly**: Simple protocol for cache invalidation

## Use Case

**Primary:** Agentic CLI tools operating on local files (task managers, notes, todos, configs).

### Why Markdown for Agents?

Agents (LLMs operating autonomously) work exceptionally well with markdown:

- **Native search**: Agents use `grep`, `ripgrep`, `find` to search files. Markdown just works. SQLite requires SQL queries.
- **Native read/write**: Agents know markdown intimately. Every LLM has seen millions of markdown files in training. They can read, write, and modify it reliably.
- **Partial access**: Agent can read/edit one file without loading entire database. Crucial for large contexts.
- **Debuggable**: When agent makes a mistake, human can `cat` the file and see exactly what happened.
- **Recoverable**: Agent corrupts a file? `git checkout` that one file. SQLite corruption requires full restore.

### The Stale Data Problem

A common pattern is SQLite-index-on-top-of-markdown: store files as markdown, index with SQLite for fast queries. This seems like "best of both worlds" but has a critical flaw:

**The index drifts.**

When files are edited externally (by agents, editors, git operations), the SQLite index becomes stale. The system returns wrong results. For agents, this is catastrophic:

- Agent queries "show me open tasks" → gets stale results
- Agent makes decisions based on wrong data
- Agent goes down wrong path, wastes tokens, produces wrong output
- Debugging is hard: "why did the agent do that?" → stale index, not visible

**mddb's approach:** The cache is explicitly throwaway. Simple invalidation protocol (delete `.cache`). Source of truth is always the `.md` files. Stale cache? Delete it, rebuild. No complex sync logic, no drift.

### Why Not SQLite?

| Aspect | SQLite | mddb | Winner for use case |
|--------|--------|------|---------------------|
| Human readability | Binary, need queries | Plain markdown | mddb |
| Agent access | SQL queries | grep/cat/direct read | mddb |
| Agent writes | INSERT/UPDATE statements | Just write markdown | mddb |
| Git integration | Binary blob, no diff | Native diffs, merge, blame | mddb |
| Merge conflicts | Binary conflict, pick one | Human-readable 3-way merge | mddb |
| Branching | One DB, complex sync | Files follow branch | mddb |
| Direct edits | Need sqlite3 CLI | Any text editor | mddb |
| Partial operations | All or nothing | Work on single file | mddb |
| Recovery | Restore whole DB | Restore single file from git | mddb |
| Schema changes | ALTER TABLE, migrations | Just add fields | mddb |
| External changes | Index drifts, complex sync | Simple invalidation protocol | mddb |
| Concurrent writes | MVCC, excellent | Single writer | SQLite |
| Complex queries | Full SQL | Filter on indexed fields | SQLite |
| Rollback | Full ACID | Roll-forward only | SQLite |
| Large datasets | Millions of rows | Thousands of docs | SQLite |

**TLDR:** mddb trades concurrent writes and query power for human-readable, git-native storage with guaranteed consistency. Right choice when files are the API and agents are the users.

## Non-Goals (v1)

- Multi-writer concurrency (single writer only)
- MVCC / snapshot isolation
- Cross-collection transactions
- Nested directories
- Non-YAML frontmatter formats (TOML, JSON)
- Rollback (only roll-forward from WAL)
- Automatic file watching (opt-in `Watcher()` available, not started by default)
- Incremental cache updates (full rebuild on schema change)

## Architecture

```
┌─────────────────────────────────────────┐
│  Application (tk, notes, etc.)          │
├─────────────────────────────────────────┤
│  mddb: schema + transactions + WAL      │  ← this spec
├─────────────────────────────────────────┤
│  fmcache: binary index cache            │  ← fmcache/spec.md
├─────────────────────────────────────────┤
│  Filesystem + Git                       │
└─────────────────────────────────────────┘
```

## File Layout

Single flat directory containing all files:

```
<data-dir>/
  .wal              # Write-ahead log (exists only during transaction)
  .cache            # Binary index cache (fmcache format)
  .locks/           # Lock files subdirectory
  <key>.mddb.md     # Document files
  <key>.mddb.md
  ...
```

Documents use `.mddb.md` extension (not plain `.md`). This:
- Clearly identifies mddb-managed files
- Avoids accidentally indexing stray markdown files (README.md, etc.)
- Enables targeted search: `find . -name "*.mddb.md"`

All files in one directory enables single fsync for durability.

**Directory creation:** mddb does NOT create the data directory. It must exist. mddb creates `.wal`, `.cache`, and `.locks/` as needed inside an existing directory. This matches SQLite behavior (creates files, not directories).

## Document Format

Documents are markdown files with YAML frontmatter:

```markdown
---
status: open
priority: 1
blocked: false
tags: [bug, urgent]
custom_field: anything
---
# Title

Content here...
```

### Frontmatter

- Delimited by `---` fences (YAML only, v1)
- Parsed via `gopkg.in/yaml.v3`
- Stored as `map[string]any`
- Supports scalars, arrays, nested objects
- Not all fields need to be indexed
- Extra fields preserved on read/write

### Content

- Everything after closing `---`
- Opaque bytes (markdown by convention)
- Stored in `.md` file, not cached

### Key

- Derived from filename: `<key>.mddb.md`
- Must not be empty
- Must not exceed 64 bytes
- Must not contain path separators or NUL
- Stored in cache with fixed-size, NUL-padded

### IDs and Ordering

**mddb does not generate IDs.** Keys are always user-provided.

`Filter` returns results in **lexicographic key order**. `Offset` and `Limit` paginate in that order. This has important implications:

| Key Strategy | Example | Pagination Behavior |
|--------------|---------|---------------------|
| ULID / KSUID | `01ARZ3NDEKTSV4RRFFQ69G5FAV` | Chronological ✓ |
| Timestamp prefix | `2024-01-15-fix-bug` | Chronological ✓ |
| Zero-padded sequential | `0001`, `0002`, `0010` | Creation order ✓ |
| Slugs | `fix-login-bug` | Alphabetical (not temporal) |
| UUID v4 | `550e8400-e29b-41d4-...` | Arbitrary (but stable) |
| Unpadded numbers | `1`, `2`, `10` | Wrong order: `1`, `10`, `2` ✗ |

**If you need chronological ordering:** Use lexicographically-sortable IDs (ULID, KSUID, timestamp prefix, or zero-padded counters).

**If you use arbitrary slugs or UUIDs:**
- Offset/Limit gives stable but not semantically meaningful pages
- For custom ordering: fetch all matches, sort in application code
- Or: add a sortable field to your index schema and sort post-fetch

## Schema and Typed Index

mddb offers two ways to define an index schema:

1. **Field-based** (`Index`): Define field helpers, get type-safe matchers
2. **Struct-based** (`IndexFor`): Define a struct with tags, use raw access

Both produce the same internal representation. Choose based on preference.

### API A: Field-based (recommended)

```go
// 1. Define fields
var (
    Status   = mddb.Enum("status", "open", "in_progress", "closed")
    Priority = mddb.Uint8("priority")
    Blocked  = mddb.Bool("blocked").Default(false)
)

// 2. Create index schema
var schema = mddb.Index(Status, Priority, Blocked)
```

No struct needed. Access values via field helpers:

```go
matches, _ := db.Filter(opts, Status.Eq("open"))
for _, m := range matches {
    fmt.Println(Status.Get(m))    // "open" (string)
    fmt.Println(Priority.Get(m))  // uint8
}
```

### API B: Struct-based (raw access)

```go
// 1. Define constants
type TicketStatus uint8

const (
    StatusOpen       TicketStatus = 0
    StatusInProgress TicketStatus = 1
    StatusClosed     TicketStatus = 2
)

// 2. Define struct with tags
type TicketIndex struct {
    Status   TicketStatus `mddb:"status,enum=open|in_progress|closed"`
    Priority uint8        `mddb:"priority"`
    Blocked  bool         `mddb:"blocked,default=false"`
}

// 3. Create index schema from struct
var schema = mddb.IndexFor[TicketIndex]()
```

Access values directly on struct:

```go
matches, _ := db.Filter(opts, func(idx TicketIndex) bool {
    return idx.Status == StatusOpen && idx.Priority >= 2
})
for _, m := range matches {
    fmt.Println(m.Index.Status)    // TicketStatus (uint8)
    fmt.Println(m.Index.Priority)  // uint8
}
```

### Comparison

| | API A: `Index(...)` | API B: `IndexFor[T]()` |
|--|---------------------|------------------------|
| Matchers | `Status.Eq("open")` ✓ | Callbacks only |
| Access | `Status.Get(m)` → `"open"` | `m.Index.Status` → `0` |
| Type safety | Via field helpers | Via constants |
| Setup | Define fields | Define struct + tags |

### Field Types

Field helpers handle YAML ↔ binary conversion:

| Field Type | YAML Value | Binary | Notes |
|------------|------------|--------|-------|
| `Enum(name, values...)` | `"open"` | `uint8` | Index in values list |
| `Bool(name)` | `true` | `uint8` | 0 or 1 |
| `Int8(name)` | `-5` | `int8` | -128 to 127 |
| `Uint8(name)` | `1` | `uint8` | 0 to 255 |
| `Int16(name)` | `-1000` | `int16` | |
| `Uint16(name)` | `1000` | `uint16` | |
| `Int32(name)` | `-100000` | `int32` | |
| `Uint32(name)` | `100000` | `uint32` | |
| `Int64(name)` | `-1e12` | `int64` | |
| `Uint64(name)` | `1e12` | `uint64` | |
| `Timestamp(name)` | `"2024-01-15T10:30:00Z"` | `int64` | Unix nanos, parses ISO 8601 |
| `String(name, maxLen)` | `"foo"` | `[maxLen]byte` | NUL-padded |
| `Bitset(name, values...)` | `["a", "b"]` | `uint64` | Bit per value, max 64 |
| `StringList(name, count, len)` | `["x", "y"]` | `[count][len]byte` | Fixed slots, NUL-padded |

For struct-based API (API B), schema validates at `Open` that struct tags match field definitions.

### Schema Versioning (Automatic)

mddb automatically detects schema changes via hashing. A hash is computed from:
- Field names, types, and order
- Enum values (in order)
- String/StringList max lengths
- Bitset values
- Default values

This hash is passed to fmcache as `SchemaVersion`. If the schema changes, the hash changes and the cache is automatically rebuilt on next `Open()`.

**No manual version bumping required.**

### Schema Evolution

Some schema changes are safe (auto-handled), others require fixing docs first:

**Safe changes (auto-handled):**

| Change | Behavior |
|--------|----------|
| New optional field (has `.Default()`) | Use default for existing docs |
| Removed field | Don't index it (frontmatter preserved in .md) |
| New enum value (appended) | Existing indices unchanged |
| Bitset: removed value | Don't set that bit, others still work |

**Breaking changes (rebuild errors with details):**

| Change | Example Error |
|--------|---------------|
| New required field | `doc "0005": field "assignee" required but missing` |
| Enum: removed value | `doc "0005": field "status" unknown value "archived"` |
| String: shorter maxLen | `doc "0005": field "parent" value (50 bytes) exceeds maxLen 32` |
| StringList: fewer slots | `doc "0005": field "tags" has 5 items, max 4` |
| StringList: shorter item len | `doc "0005": field "tags[2]" value (24 bytes) exceeds maxLen 16` |
| Field type changed | `doc "0005": field "priority" type mismatch` |

Breaking changes require fixing affected docs (or reverting schema) before rebuild succeeds. Use `Get()` to read docs and fix them - it always works regardless of schema.

### Required vs Optional Fields

**All fields are required by default.** If a field is missing from frontmatter, indexing fails with `ErrFieldValue`.

Use `.Default(x)` to make a field optional:

```go
// Required - must be present in frontmatter
Status   = mddb.Enum("status", "open", "in_progress", "closed")
Priority = mddb.Uint8("priority")

// Optional - uses default when missing
Parent   = mddb.String("parent", 32).Default("")
Tags     = mddb.Bitset("tags", "bug", "feature").Default()  // empty bitset
Blocked  = mddb.Bool("blocked").Default(false)
```

Default values are validated at schema construction:

```go
// These panic at startup (invalid defaults):
mddb.Enum("status", "open", "closed").Default("invalid")  // not in enum
mddb.Uint8("priority").Default(300)                        // exceeds uint8
mddb.String("parent", 32).Default(strings.Repeat("x", 64)) // exceeds maxLen
```

### Field Validation (Strict)

Indexing fails with descriptive errors if YAML doesn't fit schema:

| Field Type | Error Condition | Example Error |
|------------|-----------------|---------------|
| Any | Missing required field | `field "status": required but missing` |
| `Enum` | Unknown value | `field "status": unknown value "pending", valid: [open, in_progress, closed]` |
| `Uint8` | Out of range | `field "priority": value 300 exceeds uint8 range` |
| `String` | Too long | `field "parent": value "..." (42 bytes) exceeds max 32 bytes` |
| `Bitset` | Unknown value | `field "tags": unknown value "oops", valid: [bug, feature, urgent]` |
| `StringList` | Too many items | `field "blocked_by": 5 items exceeds max 4` |
| `StringList` | Item too long | `field "blocked_by[2]": value "..." (24 bytes) exceeds max 16 bytes` |

Fewer items than slots is OK (remaining slots are empty/zero). Validation errors surface during `Create`, `Update`, or `Rebuild`.

### Data Caching (Optional)

In addition to the index (for filtering), mddb can cache arbitrary data per document. This avoids file I/O when you need more than just the index fields.

```go
// Define data struct
type TicketData struct {
    Title   string
    Preview string
    Author  string
}

// Create data schema with extraction function
var dataSchema = mddb.DataSchema(func(frontmatter map[string]any, content string) TicketData {
    title := ""
    if i := strings.Index(content, "\n"); i > 0 {
        title = strings.TrimPrefix(content[:i], "# ")
    }
    author, _ := frontmatter["author"].(string)
    
    return TicketData{
        Title:   title,
        Preview: content[:min(200, len(content))],
        Author:  author,
    }
})

// Open with data schema
db, _ := mddb.Open(".tickets", indexSchema, dataSchema)

// Filter returns cached data - no file I/O
matches, _ := db.Filter(opts, Status.Eq("open"))
for _, m := range matches {
    fmt.Println(m.Data.Title)    // cached
    fmt.Println(m.Data.Preview)  // cached
}

// Get() still reads full file
entry, _, _ := db.Get("0001")
fmt.Println(entry.Content)  // full content from disk
```

Data is serialized via `encoding/gob` internally. The data struct can contain any gob-encodable types (strings, slices, maps, nested structs).

**Without data caching:** Pass `nil` as data schema, or omit it:

```go
db, _ := mddb.Open(".tickets", indexSchema, nil)
// or
db, _ := mddb.Open(".tickets", indexSchema)
```

## Public API

### Types

```go
// DB with index type I and optional data type D
type DB[I, D any] struct { ... }

type Doc struct {
    Frontmatter map[string]any  // required for Create; merged for Update
    Content     *string         // nil = keep existing (Update only)
}

type Entry[I, D any] struct {
    Key         string
    Frontmatter map[string]any
    Content     string
    Index       I      // typed index (for IndexFor[T] path)
    Data        D      // cached data (if data schema provided)
}

// Match returned by Filter - index and cached data, no file I/O
type Match[I, D any] struct {
    Key   string
    Index I      // typed index (for IndexFor[T]) or use Status.Get(m) (for Index)
    Data  D      // cached data
}

type Tx[I, D any] struct { ... }

type Options struct {
    SyncMode    SyncMode      // default SyncFull
    LockTimeout time.Duration // default 2s
}

// NoData is used when data caching is not needed
type NoData struct{}
```

### Opening

```go
// Field-based index (API A)
func Open[D any](path string, index IndexSchema, data DataSchema[D], opts ...Options) (*DB[any, D], error)

// Struct-based index (API B)
func OpenFor[I, D any](path string, index IndexSchema[I], data DataSchema[D], opts ...Options) (*DB[I, D], error)

// Shorthand without data caching
func Open(path string, index IndexSchema, opts ...Options) (*DB[any, NoData], error)
func OpenFor[I any](path string, index IndexSchema[I], opts ...Options) (*DB[I, NoData], error)

func (db *DB[I, D]) Close() error
```

`Open`:
- Checks for `.wal`, replays if exists (acquires exclusive lock during replay)
- May block up to `LockTimeout` if lock is held and WAL replay needed
- Opens or rebuilds `.cache`
- Returns error if directory doesn't exist
- Returns error if WAL replay fails or lock timeout

`Close`:
- Releases resources
- Returns `ErrTxActive` if there's an uncommitted transaction (call `Commit` or `Abort` first)
- Thread-safe: multiple goroutines may call `Get`/`Filter` concurrently

### Reading

```go
func (db *DB[I, D]) Len() (int, error)

func (db *DB[I, D]) Get(key string) (Entry[I, D], bool, error)

func (db *DB[I, D]) Filter(opts FilterOpts, matcher any) ([]Match[I, D], error)
```

`Len`:
- Returns total number of documents in cache
- O(1) operation
- Returns `ErrOffsetOutOfBounds` if Filter offset exceeds this count

`Get`:
- Reads `.mddb.md` file directly (always fresh)
- During transaction: returns buffered writes if key was modified (read-your-writes)
- Parses frontmatter, returns full Entry with typed Index and cached Data
- Returns `false` if key not found

`Filter`:
- Scans cache index only (no file I/O)
- Returns matches with Index and cached Data
- During transaction: sees buffered writes (fmcache overlays uncommitted writes until Commit)
- May return stale data if external changes occurred (see Cache Invalidation Protocol)
- Returns `ErrOffsetOutOfBounds` if offset exceeds result count
- `matcher` can be:
  - Field matchers (API A): `Status.Eq("open")`, `Priority.Gt(2)`, etc.
  - Callback (API A): `func(m Match[I, D]) bool`
  - Callback (API B): `func(idx I) bool`
  - `nil` - match all

```go
type FilterOpts struct {
    Reverse bool // key order: false=asc, true=desc
    Offset  int  // skip first N matches
    Limit   int  // 0 = no limit
}
```

Filter helpers use typed field references:

```go
// Single matcher
db.Filter(opts, Status.Eq("open"))
db.Filter(opts, Status.In("open", "in_progress"))
db.Filter(opts, Priority.Gt(2))
db.Filter(opts, Blocked.Eq(false))

// Combining matchers with And/Or (chainable)
db.Filter(opts, Status.Eq("open").And(Priority.Gte(2)))
db.Filter(opts, Status.Eq("open").Or(Status.Eq("in_progress")))

// Callback for complex logic
db.Filter(opts, func(key string, idx TicketIndex) bool {
    return idx.Status == Status.Val("open") && idx.Priority >= 2
})
```

Matcher chaining resolves left-to-right. Nesting controls precedence:

```go
// A.And(B).Or(C) → (A AND B) OR C
Status.Eq("open").And(Priority.Gte(2)).Or(Blocked.Eq(true))

// A.And(B.Or(C)) → A AND (B OR C)
Status.Eq("open").And(Priority.Gte(2).Or(Blocked.Eq(true)))

// A.Or(B).And(C) → (A OR B) AND C
Status.Eq("open").Or(Status.Eq("in_progress")).And(Blocked.Eq(false))
```

### Writing (Transactions)

```go
func (db *DB[I, D]) Begin() (*Tx[I, D], error)

func (tx *Tx[I, D]) Create(key string, doc Doc) error
func (tx *Tx[I, D]) Update(key string, doc Doc) error
func (tx *Tx[I, D]) Delete(key string) error

func (tx *Tx[I, D]) Commit() error
func (tx *Tx[I, D]) Abort() error
```

`Begin`:
- Acquires exclusive lock (blocks if held by another process/tx, times out per `LockTimeout`)
- Multiple `Begin()` calls block until previous transaction completes
- Replays WAL if exists (crash recovery)
- Returns `Tx` for buffering writes

`Create`:
- Creates new document
- Error if key already exists (`ErrExists`)
- Error if key invalid (`ErrInvalidKey`)
- Error if frontmatter fails schema validation (`ErrFieldValue`) - validated immediately
- `doc.Content` must not be nil
- Buffers in memory

`Update`:
- Updates existing document
- Error if key doesn't exist (`ErrNotFound`)
- Merges `doc.Frontmatter` into existing frontmatter
- To remove a field: set its value to `nil`
- Error if merged frontmatter fails schema validation (`ErrFieldValue`) - validated immediately
- Replaces content if `doc.Content` non-nil, keeps existing if nil
- Buffers in memory

`Delete`:
- Removes document
- Error if key doesn't exist (`ErrNotFound`)
- Buffers in memory

**Operations within same transaction:**

```go
tx.Create("0001", doc)
tx.Update("0001", patch)  // ✓ modifies buffered create

tx.Create("0002", doc)
tx.Delete("0002")         // ✓ net effect: nothing (doc never written)

tx.Delete("0003")         // existing doc
db.Get("0003")            // ✓ returns not found (sees buffered delete)
```

The transaction buffer is always checked first. Read-your-writes semantics apply.

`Commit`:
- Writes buffer to `.wal`
- Applies changes to `.md` files
- Updates `.cache`
- Truncates `.wal`
- Releases lock

`Abort`:
- Discards buffered changes
- Releases lock
- Nothing written

### Cache Management

```go
func (db *DB[I, D]) Rebuild() error
func (db *DB[I, D]) InvalidateCache() error
```

`Rebuild`:
- Acquires exclusive lock
- Scans all `.md` files, parses frontmatter, builds index
- Writes new `.cache` atomically (temp file + rename)
- Releases lock
- Returns `ErrFieldValue` if any document fails validation (cache unchanged)

`InvalidateCache`:
- Acquires exclusive lock
- Closes mmap
- Deletes `.cache` file
- Releases lock
- Next `Filter()` call will trigger automatic rebuild

**Important:** Do not delete `.cache` directly while mddb is open. The mmap remains valid after file deletion (Unix behavior), so mddb would continue reading stale data. Use `InvalidateCache()` or `Rebuild()` instead.

### Watcher (Optional)

```go
type Watcher struct { ... }

func (db *DB[I, D]) Watcher() (*Watcher, error)

func (w *Watcher) Events() <-chan WatchEvent
func (w *Watcher) Close() error

type WatchEvent struct {
    Key string  // which document changed, or empty if multiple/unknown
}
```

`Watcher`:
- Creates a file watcher for the database directory
- User must explicitly call this; mddb does not start watching automatically
- Uses fsnotify (or platform equivalent) under the hood

`Events`:
- Returns channel that emits when external .md changes detected
- Automatically calls `Rebuild()` before sending event (properly closes mmap, rebuilds)
- Only detects changes when mddb is not holding the lock (external changes)

`Close`:
- Stops watching, closes event channel
- Safe to call multiple times

**Usage:**
```go
db, _ := mddb.Open(".tickets", indexSchema)

// Optional: start watcher if you want live updates
watcher, _ := db.Watcher()
defer watcher.Close()

go func() {
    for event := range watcher.Events() {
        log.Printf("external change: %s", event.Key)
        // refresh UI, re-run query, etc.
    }
}()
```

The watcher is useful for long-running processes (servers, TUIs) that need to react to external edits. CLI tools that run-and-exit typically don't need it.

### Example Usage

**API A: Field-based (recommended)**

```go
// 1. Define fields
var (
    Status   = mddb.Enum("status", "open", "in_progress", "closed")
    Priority = mddb.Uint8("priority")
    Blocked  = mddb.Bool("blocked").Default(false)
)

// 2. Create index schema
var indexSchema = mddb.Index(Status, Priority, Blocked)

// 3. Optional: data caching
type TicketData struct {
    Title   string
    Preview string
}

var dataSchema = mddb.DataSchema(func(fm map[string]any, content string) TicketData {
    return TicketData{
        Title:   strings.SplitN(content, "\n", 2)[0],
        Preview: content[:min(200, len(content))],
    }
})

// 4. Open
db, _ := mddb.Open(".tickets", indexSchema, dataSchema)
defer db.Close()

// 5. Filter with matchers
matches, _ := db.Filter(mddb.FilterOpts{}, Status.Eq("open").And(Priority.Gte(2)))

for _, m := range matches {
    fmt.Printf("%s: %s\n", m.Key, m.Data.Title)
    fmt.Printf("  Status: %s, Priority: %d\n", Status.Get(m), Priority.Get(m))
}

// 6. Get full document
entry, ok, _ := db.Get("0001")
if ok {
    fmt.Println(entry.Content)
}

// 7. Write (transaction)
tx, _ := db.Begin()

content := "# Add User Avatars\n\nAllow users to upload profile pictures."
tx.Create("0042", mddb.Doc{
    Frontmatter: map[string]any{
        "status":   "open",
        "priority": 1,
    },
    Content: &content,
})

tx.Update("0001", mddb.Doc{
    Frontmatter: map[string]any{"status": "closed"},
})

tx.Delete("0007")
tx.Commit()
```

**API B: Struct-based (raw access)**

```go
// 1. Define constants and struct
type TicketStatus uint8

const (
    StatusOpen       TicketStatus = 0
    StatusInProgress TicketStatus = 1
    StatusClosed     TicketStatus = 2
)

type TicketIndex struct {
    Status   TicketStatus `mddb:"status,enum=open|in_progress|closed"`
    Priority uint8        `mddb:"priority"`
    Blocked  bool         `mddb:"blocked,default=false"`
}

// 2. Create index schema from struct
var indexSchema = mddb.IndexFor[TicketIndex]()

// 3. Open with struct type
db, _ := mddb.OpenFor[TicketIndex](".tickets", indexSchema)
defer db.Close()

// 4. Filter with callback
matches, _ := db.Filter(mddb.FilterOpts{}, func(idx TicketIndex) bool {
    return idx.Status == StatusOpen && idx.Priority >= 2
})

for _, m := range matches {
    fmt.Printf("%s: status=%d, priority=%d\n", m.Key, m.Index.Status, m.Index.Priority)
}

// 5. Write (same as API A)
tx, _ := db.Begin()
// ... Create, Update, Delete ...
tx.Commit()
```

## Transaction and Locking Model

### Locking

mddb uses file-based locking (via `flock`):

- **Shared lock (read)**: Not required. Reads access cache file directly.
- **Exclusive lock (write)**: Acquired by `Begin()`, released by `Commit()`/`Abort()`

| Scenario | Behavior |
|----------|----------|
| Multiple readers | OK, read from cache |
| Reader + writer | Both proceed (reader sees pre-commit state) |
| Multiple writers | Second blocks until first commits/aborts or times out |

Lock files stored in `.locks/` subdirectory to avoid modifying parent directory mtime.

### Transaction Flow

```
Begin()  → acquire exclusive lock
         → replay WAL if exists
         ↓
Create() → buffer in memory
Update() → buffer in memory
Delete() → buffer in memory
         ↓
Commit() → write .wal (intent)
         → apply to .md files
         → update .cache
         → truncate .wal
         → release lock

Abort()  → discard buffer
         → release lock
```

### No Rollback

mddb only supports roll-forward:

- `Commit()` writes intent to WAL, then applies
- If crash during apply, WAL is replayed on next `Open()`
- No "before" state stored, so no rollback
- `Abort()` only works before `Commit()` (nothing persisted yet)

## WAL Semantics

### Format

JSONL (one operation per line) followed by CRC32-C checksum (4 bytes, little-endian). Uses Castagnoli polynomial (`crc32.Castagnoli` in Go) for hardware acceleration.

```
[JSONL bytes][CRC32-C - 4 bytes]
```

JSONL structure (one JSON object per line):

```jsonl
{"op":"create","key":"0042","frontmatter":{"status":"open","priority":1},"content":"# Add User Avatars\n\nDescription."}
{"op":"update","key":"0001","frontmatter":{"status":"closed"},"content":null}
{"op":"delete","key":"0007"}
```

- `op`: Operation type - `"create"`, `"update"`, or `"delete"`
- `key`: Document key (filename without `.md`)
- `frontmatter`: For create: full frontmatter. For update: fields to merge (nil value = delete field)
- `content`: For create: required. For update: null = keep existing, string = replace. Omitted for delete.

### Checksum Verification

On WAL read:
1. Read file contents
2. Split into JSONL (all but last 4 bytes) and checksum (last 4 bytes)
3. Compute CRC32-C of JSONL bytes
4. If checksum matches → parse lines and replay
5. If checksum doesn't match → discard WAL (crash during write)

This catches partial writes from crashes. All-or-nothing: either entire WAL is valid or it's discarded.

### Replay

On `Open()`, if `.wal` exists:

1. Acquire exclusive lock
2. Verify checksum, parse JSONL
3. For each line (in order):
   - `create`: Write `.md` file with frontmatter + content
   - `update`: Read existing `.md`, merge frontmatter (nil removes keys), replace content if provided, write `.md`
   - `delete`: Remove `.md` file
4. Update `.cache`
5. Truncate `.wal`
6. Release lock

Replay is **idempotent**: re-applying the same WAL produces the same result. Safe to replay multiple times after crash.

### Durability

Controlled by `SyncMode` (default `SyncFull`):

| Mode | Behavior | Durability |
|------|----------|------------|
| `SyncNone` | No fsync | WAL may be lost on crash |
| `Sync` | fsync files only | WAL likely survives |
| `SyncFull` | fsync files + directory | WAL survives power loss |

## Cache Invalidation Protocol

mddb has a simple protocol for detecting and handling external changes (edits made outside mddb by agents, editors, git, etc.).

### The Problem

Other SQLite-over-markdown systems suffer from index drift: the SQLite index becomes stale when files are edited externally, with no easy way to detect or fix.

### The Protocol

```
Files:
  .cache       - Binary index cache (throwaway, also serves as write marker)
  *.mddb.md    - Source of truth (always authoritative)

Invariant:
  After mddb commit: .cache mtime >= mtime of all .mddb.md files written by mddb

Rule:
  If any .mddb.md file has mtime > .cache mtime → external change → cache is stale

Recovery:
  Call InvalidateCache() or Rebuild() on open db instance
  Or delete .cache when no mddb instance is open → next Open() rebuilds
```

### mddb Commit Sequence

```
1. Write .wal
2. Apply to .md files     ← sets mtime = T1
3. Update .cache          ← sets mtime = T2 (where T2 >= T1)
4. Truncate .wal
```

The `.cache` file mtime serves as the "high water mark" of mddb's writes.

### External Watcher

mddb provides an optional `Watcher()` API for detecting external changes (see Public API). This is the recommended approach for long-running processes.

For external tools that need to signal cache invalidation:
- **If mddb is open:** Signal the application to call `InvalidateCache()` or `Rebuild()` (e.g., via IPC, signal handler, or file-based trigger)
- **If mddb is closed:** Delete `.cache` directly; next `Open()` will rebuild

**Do not delete `.cache` while mddb has it open** - the mmap remains valid after deletion (Unix behavior), causing mddb to read stale data.

The mtime comparison can detect external changes:
```bash
# Only valid when mddb is NOT running
if [ "$file" -nt "<data-dir>/.cache" ]; then
    rm -f "<data-dir>/.cache"
fi
```

### Behavior Summary

| Scenario | `Get()` | `Filter()` |
|----------|---------|------------|
| No external changes | Fresh (reads file) | Fresh (cache valid) |
| External edit | Fresh (reads file) | Stale (cache outdated) |
| Cache deleted | Fresh (reads file) | Fresh (cache rebuilt) |

- `Get()` always reads the `.mddb.md` file directly → always fresh
- `Filter()` uses cache → may be stale if external changes
- `InvalidateCache()` or `Rebuild()` → forces cache refresh while mddb is open
- Delete `.cache` (when mddb closed) → forces rebuild on next `Open()`

### Why This Works

- `.md` files are **always** the source of truth
- `.cache` is derived, throwaway, can be deleted anytime
- Simple protocol: delete one file → guaranteed consistency
- No IPC needed, works across processes
- No complex sync logic

### Known Limitation

On filesystems with coarse mtime resolution (e.g., 1-second on HFS+), an external edit in the same time quantum as a `.cache` write may not be detected. This is rare and recoverable via manual `Rebuild()`.

## Error Model

### Sentinel Errors

```go
var (
    // WAL
    ErrWALCorrupt     // .wal exists but can't parse/replay

    // Transaction  
    ErrTxClosed       // Commit/Abort already called
    ErrTxActive       // Close called with uncommitted transaction
    ErrLockTimeout    // couldn't acquire lock

    // Validation
    ErrNotFound       // key doesn't exist (Update, Delete)
    ErrExists         // key already exists (Create)
    ErrInvalidKey     // empty, >64 bytes, path separators, NUL
    ErrFieldValue     // YAML value doesn't fit schema (see Field Validation)

    // Filter
    ErrOffsetOutOfBounds  // Filter offset exceeds result count
)
```

### Cache Errors

Cache errors trigger automatic rebuild, not user-facing errors:

- Cache missing → rebuild from `.md` files
- Cache corrupt → rebuild from `.md` files  
- Schema mismatch → rebuild from `.md` files

### Wrapped Errors

Filesystem and fmcache errors are wrapped and passed through:
- `os.ErrNotExist` - directory doesn't exist
- `os.ErrPermission` - permission denied
- fmcache errors (when not auto-recoverable)

## Limitations and Tradeoffs

### Concurrency

| Limitation | Consequence | Tradeoff |
|------------|-------------|----------|
| Single writer | Writers block each other | Simplicity over concurrency |
| Global lock | No concurrent writes to different docs | Predictable, no deadlocks |
| No MVCC | Readers may see stale data during write | Simpler implementation |

### Storage

| Limitation | Consequence | Tradeoff |
|------------|-------------|----------|
| Flat directory | All docs in one dir, no hierarchy | Simple key model |
| YAML only | No TOML/JSON frontmatter | One parser, no ambiguity |
| 4GiB cache limit | From fmcache v1 | Sufficient for thousands of docs |

### Transactions

| Limitation | Consequence | Tradeoff |
|------------|-------------|----------|
| No rollback | Must complete or replay on crash | Simpler WAL, no "before" state |
| Full cache rebuild on schema change | Slow migration for large collections | Simple, always correct |

### External Changes

| Limitation | Consequence | Tradeoff |
|------------|-------------|----------|
| Cache stale on external edits | `Filter()` may return wrong results | Simple protocol to invalidate |
| No automatic detection | Need external watcher or manual rebuild | mddb stays simple, optional watcher |

## Future Considerations

Potential enhancements for future versions:

| Area | Current (v1) | Possible Future |
|------|--------------|-----------------|
| Concurrency | Global lock | Per-document locking |
| Cache rebuild | Full rebuild on schema change | Incremental migration |
| Stale detection | External watcher | mtime check per entry on read |
| Rollback | Roll-forward only | Store "before" state in WAL |
| Directory structure | Flat | Nested with path-based keys |
| Frontmatter format | YAML only | Pluggable parsers |
| WAL | Global, single file | Partitioned for concurrency |
| Cache size | 4GiB (fmcache v1) | 64-bit offsets in fmcache v2 |
