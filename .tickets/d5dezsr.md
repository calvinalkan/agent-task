---
schema_version: 1
id: d5dezsr
status: open
blocked-by: []
created: 2026-01-04T22:55:35Z
type: bug
priority: 2
assignee: Calvin Alkan
---
# Lock files not cleaned up after release

The release() method in lock.go does not delete the .lock file from disk after releasing the flock and closing the file handle. This causes stale .lock files to accumulate in the .tickets/ directory over time.

The issue is in lock.go:

```go
func (l *fileLock) release() {
    if l.file != nil {
        _ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
        _ = l.file.Close()
        // Missing: os.Remove(l.path)
    }
}
```

The fix is to add `os.Remove(l.path)` after closing the file.

## Acceptance Criteria

1. The release() method removes the .lock file after releasing the lock
2. New tests verify that lock files are cleaned up after:
   - Normal lock/release cycle
   - WithLock() completes successfully
   - WithTicketLock() completes successfully
3. Existing tests continue to pass
4. make check passes
