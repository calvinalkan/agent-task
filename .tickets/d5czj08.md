---
schema_version: 1
id: d5czj08
status: closed
closed: 2026-01-04T05:36:50Z
blocked-by: []
created: 2026-01-04T05:22:09Z
type: feature
priority: 1
assignee: Calvin Alkan
---
# Add centralized ticket file access with locking

Create a single access point for all ticket file operations with file locking to prevent race conditions.

Problem: Multiple agents can read-check-write the same ticket concurrently, causing lost updates.

Solution: All ticket modifications go through a single function that locks the file for the duration of read→check→write.

## Design

## New function in ticket.go

```go
func WithTicketLock(path string, fn func(content []byte) ([]byte, error)) error {
    err := lock(path)
    if err != nil {
        return fmt.Errorf("acquiring lock: %w", err)
    }
    defer unlock(path)
    
    content, err := os.ReadFile(path)
    if err != nil {
        return err
    }
    
    newContent, err := fn(content)
    if err != nil {
        return err  // check failed, no write
    }
    
    if newContent == nil {
        return nil  // read-only operation
    }
    
    return atomic.WriteFile(path, newContent)
}
```

## Locking implementation

Use `flock` (github.com/gofrs/flock or similar):
- Lock file: `<path>.lock`
- Timeout: configurable, default 5s
- On timeout: return error

## Refactor existing operations

Update to use WithTicketLock:
- `UpdateTicketStatus()`
- `AddTicketField()`
- `RemoveTicketField()`
- `UpdateTicketBlockedBy()`

## Commands affected

- start.go
- close.go  
- reopen.go
- block.go
- unblock.go
- repair.go (future)

## Acceptance Criteria

## Functionality

- [ ] All ticket writes go through WithTicketLock
- [ ] Lock acquired before read
- [ ] Lock released after write (or error)
- [ ] Concurrent access serialized correctly

## Locking

- [ ] Lock uses separate .lock file
- [ ] Lock has configurable timeout
- [ ] Timeout returns clear error
- [ ] Lock failure returns clear error

## Error handling

- [ ] Lock acquisition failure is an error
- [ ] fn() returning error skips write, releases lock
- [ ] fn() returning nil content skips write (read-only)

## Invariants

- [ ] No writes without holding lock
- [ ] Lock always released (defer)
- [ ] Atomic write still used within lock

## Tests

- [ ] Test concurrent access serialization
- [ ] Test lock timeout
- [ ] Test lock acquisition failure
- [ ] Test error in fn() releases lock
