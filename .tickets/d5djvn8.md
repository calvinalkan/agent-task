---
schema_version: 1
id: d5djvn8
status: open
blocked-by: []
created: 2026-01-05T03:19:49Z
type: chore
priority: 2
assignee: Calvin Alkan
---
# Integrate cache update into ticket write function

Currently updateCacheAfterTicketWrite is a separate function that must be called after every ticket write. This is error-prone - callers can forget to update the cache.

Consider integrating cache update into the ticket writing mechanism itself, e.g.:

```go
func WriteTicketWithCache(path string, handler func(content []byte) ([]byte, *TicketSummary, error)) error
```

The handler would return both the new content AND the summary. The function would:
1. Acquire lock
2. Call handler
3. Write file
4. Update cache
5. Release lock

This makes it impossible to forget the cache update.

Affected files: create.go, close.go, start.go, reopen.go, block.go, unblock.go, repair.go
