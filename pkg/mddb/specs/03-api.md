# Public API (conceptual)

This is a conceptual Go API surface for mddb.

It is meant to document behavior and types; exact package names and signatures may vary.

## Core types

```go
// DB with index type I and optional cached data type D.
// (See open question about data caching in open-questions.md.)
type DB[I, D any] struct { /* ... */ }

type Doc struct {
    Frontmatter map[string]any // required for Create; merged for Update
    Content     *string        // nil = keep existing (Update only)
}

type Entry[I, D any] struct {
    Key         string
    Frontmatter map[string]any
    Content     string
    Index       I
    Data        D
}

type Match[I, D any] struct {
    Key   string
    Index I
    Data  D
}

type Tx[I, D any] struct { /* ... */ }
```

## Options

```go
type SyncMode int

const (
    SyncNone SyncMode = iota
    Sync
    SyncFull // default
)

type Options struct {
    SyncMode    SyncMode
    LockTimeout time.Duration // default 2s
}

type FilterOpts struct {
    Reverse bool
    Offset  int
    Limit   int
}
```

## Opening and closing

```go
func Open(path string, schema IndexSchema, opts ...Options) (*DB[any, NoData], error)
func OpenFor[I any](path string, schema IndexSchema[I], opts ...Options) (*DB[I, NoData], error)

func (db *DB[I, D]) Close() error
```

Open behavior:

- If `.wal` exists, the writer lock is acquired and the WAL is replayed.
- The cache is opened; if missing/corrupt/incompatible, it is rebuilt.
- The data directory must exist (mddb creates files, not directories).

## Reading

```go
func (db *DB[I, D]) Len() (int, error)
func (db *DB[I, D]) Get(key string) (Entry[I, D], bool, error)
func (db *DB[I, D]) Filter(opts FilterOpts, matcher any) ([]Match[I, D], error)
```

- `Get` reads the `*.mddb.md` file directly.
- `Filter` uses the cache only.

## Writing

```go
func (db *DB[I, D]) Begin() (*Tx[I, D], error)

func (tx *Tx[I, D]) Create(key string, doc Doc) error
func (tx *Tx[I, D]) Update(key string, patch Doc) error
func (tx *Tx[I, D]) Delete(key string) error

func (tx *Tx[I, D]) Commit() error
func (tx *Tx[I, D]) Abort() error
```

## Cache management

```go
func (db *DB[I, D]) Rebuild() error
func (db *DB[I, D]) InvalidateCache() error
```

## Watcher

See [watcher](watcher.md).
