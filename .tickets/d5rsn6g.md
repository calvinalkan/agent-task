---
schema_version: 1
id: d5rsn6g
status: open
blocked-by: []
created: 2026-01-22T03:33:46Z
type: feature
priority: 2
---
# slotcache: Commit() should not close writer, add Abort() method

## Problem

Currently `Writer.Commit()` closes the writer after flushing changes. This means:
- Each batch of writes requires a new `Writer()` call
- Writer acquisition costs ~3.4µs (5 syscalls for locking)
- Can't do multiple commits in a row without re-acquiring

## Proposed Change

| Method | Flushes | Clears Buffer | Releases Lock |
|--------|---------|---------------|---------------|
| `Commit()` | ✅ | ✅ | ❌ |
| `Abort()` | ❌ | ✅ | ❌ |
| `Close()` | ❌ | ✅ | ✅ |

## New Usage Pattern

```go
w, _ := cache.Writer()
defer w.Close()

// Batch 1
for i := 0; i < 1000; i++ {
    w.Put(key, rev, idx)
}
w.Commit()  // flushes, writer stays open

// Batch 2 - no syscall overhead!
for i := 0; i < 1000; i++ {
    w.Put(key, rev, idx)
}
w.Commit()

// Batch 3 - validation failed, discard
w.Put(badKey, rev, idx)
w.Abort()   // discard changes, keep lock

// Close() releases the lock
```

## Benefits

- Amortize 3.4µs writer acquire across many commits
- Natural fit for streaming writes (commit every N ops)
- Caller controls when to release the lock
- Abort() allows discarding a batch without releasing lock

## Spec Changes

- Update `003-semantics.md` § Write session lifecycle
- `Commit()` clears buffered ops but keeps writer open
- `Commit()` can be called multiple times
- Add `Abort()` that clears buffer without flushing
- `Close()` without `Commit()` discards uncommitted changes (same as now)
- Operations after `Close()` return `ErrClosed` (same as now)

## Acceptance Criteria

- [ ] Update spec docs
- [ ] Implement `Abort()` method
- [ ] Change `Commit()` to not close writer
- [ ] Update tests
- [ ] Benchmark multi-commit pattern
