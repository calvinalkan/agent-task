# Watcher

mddb provides an optional directory watcher for long-running processes (TUIs, servers) that need to react to external edits.

## API

```go
type Watcher struct { ... }

func (db *DB[I, D]) Watcher() (*Watcher, error)

func (w *Watcher) Events() <-chan WatchEvent
func (w *Watcher) Close() error

type WatchEvent struct {
    Key string // changed doc key, or empty if unknown/multiple
}
```

## Semantics

- The watcher is **opt-in**; mddb does not watch by default.
- The watcher listens for filesystem events affecting `*.mddb.md` in the data-dir.
- On detecting an external change, the watcher SHOULD:
  - acquire the writer lock
  - rebuild or invalidate the cache
  - release the lock
  - emit a `WatchEvent`

The key may be empty if the watcher cannot reliably determine which file changed.

## Notes

- File watching is inherently platform-specific.
- Event coalescing/debouncing is expected.
- CLI tools that run-and-exit typically do not need a watcher.
