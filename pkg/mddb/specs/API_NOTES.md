# API notes and examples (non-normative)

This document captures a **historical API sketch** and usage examples. It is **non-normative**; the specs define required behavior, not API shape.

> **Terminology note:** some sketches use `Key` where the specs use `id`. Treat `Key` as `id`.

## Index schema API shapes

mddb commonly exposes two equivalent ways to define an index schema:

1. **Field-based DSL** (`Index`) — field helpers + matcher DSL
2. **Struct-based** (`IndexFor[T]`) — raw access via tagged struct

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

Access values via field helpers:

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
    fmt.Println(m.Index.Status)   // TicketStatus (uint8)
    fmt.Println(m.Index.Priority) // uint8
}
```

### Field helper types (illustrative)

| Helper | YAML Value | Encoded | Notes |
|--------|------------|---------|-------|
| `Enum(name, values...)` | string | `uint8` | index into values |
| `Bool(name)` | bool | `uint8` | 0 or 1 |
| `Int8/Uint8` | integer | 1 byte | range-checked |
| `Int16/Uint16` | integer | 2 bytes | |
| `Int32/Uint32` | integer | 4 bytes | |
| `Int64/Uint64` | integer | 8 bytes | |
| `Timestamp(name)` | RFC3339 string | `int64` | Unix nanos |
| `String(name, maxLen)` | string | `[maxLen]byte` | NUL-padded |
| `Bitset(name, values...)` | list | `uint64` | bit per value |
| `StringList(name, count, len)` | list | `[count][len]byte` | fixed slots |

## Public API sketch

### Types (illustrative)

```go
// DB with index type I and optional data type D
type DB[I, D any] struct { /* ... */ }

type Doc struct {
    Frontmatter map[string]any  // required for Create; merged for Update
    Content     *string         // nil = keep existing (Update only)
}

type Entry[I, D any] struct {
    ID          string
    Frontmatter map[string]any
    Content     string
    Index       I
    Data        D
}

type Match[I, D any] struct {
    ID    string
    Index I
    Data  D
}

type Tx[I, D any] struct { /* ... */ }

type Options struct {
    SyncMode    SyncMode      // default is implementation-defined
    LockTimeout time.Duration // example: 2s
}

type NoCachedData struct{}
```

### Opening / closing (illustrative)

```go
// Field-based index
func Open[D any](path string, index IndexSchema, data DataSchema[D], opts ...Options) (*DB[any, D], error)

// Struct-based index
func OpenFor[I, D any](path string, index IndexSchema[I], data DataSchema[D], opts ...Options) (*DB[I, D], error)

// Shorthand without data caching
func Open(path string, index IndexSchema, opts ...Options) (*DB[any, NoCachedData], error)
func OpenFor[I any](path string, index IndexSchema[I], opts ...Options) (*DB[I, NoCachedData], error)

func (db *DB[I, D]) Close() error
```

Actual APIs may additionally accept `PathOf`, `IdFromPath`, `IDSpec`, and `LayoutID` parameters.

`Open` typically:
- replays the WAL under the exclusive WAL lock (`flock` on `.mddb/wal`, per spec)
- opens or rebuilds the cache
- returns an error if replay fails or the directory is missing

`Close` typically releases resources and returns `ErrTxActive` if a transaction is still open.

## Reading

```go
func (db *DB[I, D]) Len() (int, error)
func (db *DB[I, D]) Get(id string) (Entry[I, D], bool, error)
func (db *DB[I, D]) Filter(opts FilterOpts, matcher any) ([]Match[I, D], error)
```

- `Len()` returns the number of cached documents (O(1)).
- `Get()` reads the source-of-truth file directly and is always fresh.
- `Filter()` scans the cache and may be stale unless verified; see spec.

Many historical implementations returned `ErrOffsetOutOfBounds` if `Filter`’s `Offset` exceeded result count. Current spec leaves this behavior to the API.

### Filter options (illustrative)

```go
type FilterOpts struct {
    Reverse bool
    Offset  int
    Limit   int
    VerifyRevisions bool // if supported
}
```

### Matcher examples (field-based DSL)

```go
// Single matcher
Status.Eq("open")
Priority.Gt(2)
Blocked.Eq(false)

// Combining matchers
Status.Eq("open").And(Priority.Gte(2))
Status.Eq("open").Or(Status.Eq("in_progress"))

// Callback for complex logic
db.Filter(opts, func(idx TicketIndex) bool {
    return idx.Status == StatusOpen && idx.Priority >= 2
})
```

## Writing (transactions)

```go
func (db *DB[I, D]) Begin() (*Tx[I, D], error)
func (tx *Tx[I, D]) Create(id string, doc Doc) error
func (tx *Tx[I, D]) Update(id string, doc Doc) error
func (tx *Tx[I, D]) Delete(id string) error
func (tx *Tx[I, D]) Commit() error
func (tx *Tx[I, D]) Abort() error
```

Typical semantics:

- `Create` fails if the document exists.
- `Update` merges frontmatter and optionally replaces content.
- `Delete` removes the document.
- `Commit` writes WAL → updates docs → updates cache.
- Optional optimistic concurrency: `UpdateIfRevision` / `DeleteIfRevision`.

### Operations within the same transaction

```go
tx.Create("0001", doc)
tx.Update("0001", patch)  // modifies buffered create

tx.Create("0002", doc)
tx.Delete("0002")         // net effect: nothing
```

## Cache management

Typical APIs include:

```go
func (db *DB[I, D]) Rebuild() error
func (db *DB[I, D]) Refresh() error      // optional incremental refresh
func (db *DB[I, D]) InvalidateCache() error
```

## Watcher (optional)

```go
type Watcher struct { /* ... */ }
func (db *DB[I, D]) Watcher() (*Watcher, error)
func (w *Watcher) Events() <-chan WatchEvent
func (w *Watcher) Close() error

type WatchEvent struct {
    ID string // document id (or empty if unknown)
}
```

Watchers are useful for long-running processes (servers/TUIs) to react to external edits. CLI tools that run-and-exit typically do not need them.

## Example usage

```go
// Define fields
var (
    Status   = mddb.Enum("status", "open", "in_progress", "closed")
    Priority = mddb.Uint8("priority")
    Blocked  = mddb.Bool("blocked").Default(false)
)

// Create index schema
var indexSchema = mddb.Index(Status, Priority, Blocked)

// Optional: data caching (see DATA_CACHE.md)
var dataSchema = mddb.DataSchema(func(fm map[string]any, content string) TicketData {
    return TicketData{
        Title:   strings.SplitN(content, "\n", 2)[0],
        Preview: content[:min(200, len(content))],
    }
})

// Open
db, _ := mddb.Open(".tickets", indexSchema, dataSchema)

defer db.Close()

// Filter
matches, _ := db.Filter(mddb.FilterOpts{}, Status.Eq("open").And(Priority.Gte(2)))

// Get
entry, ok, _ := db.Get("0001")

// Write
tx, _ := db.Begin()
content := "# Add User Avatars\n\nAllow users to upload profile pictures."
tx.Create("0042", mddb.Doc{
    Frontmatter: map[string]any{"status": "open", "priority": 1},
    Content:     &content,
})
tx.Commit()
```
