---
schema_version: 1
id: d5d7msg
status: closed
closed: 2026-01-04T14:51:24Z
blocked-by: []
created: 2026-01-04T14:34:14Z
type: task
priority: 2
assignee: Calvin Alkan
---
# Append letter suffix on ID collision instead of retry/sleep

Current ID generation retries with 1ms sleep when collision occurs (same second). Instead, append letter suffix (a-z) to handle multiple tickets per second.

Example:
```
d5czj08     (first ticket at second X)
d5czj08a    (second)
d5czj08b    (third)
...
d5czj08z    (27th)
d5czj08za   (28th)
```

Lexicographic sorting preserved because shorter string < longer, and a < b < c.

## Design

### Changes to ticket.go

**GenerateUniqueID(ticketDir string) (string, error)**

Replace retry/sleep loop with suffix approach:

```go
func GenerateUniqueID(ticketDir string) (string, error) {
    base := GenerateID()
    
    // Try base ID first
    if !ticketExistsWithID(ticketDir, base) {
        return base, nil
    }
    
    // Append letter suffixes: a, b, ..., z, za, zb, ..., zz, zza, ...
    suffix := ""
    for {
        suffix = nextSuffix(suffix)
        candidate := base + suffix
        if !ticketExistsWithID(ticketDir, candidate) {
            return candidate, nil
        }
        // Safety limit
        if len(suffix) > 4 {
            return "", errIDGenerationFailed
        }
    }
}

func nextSuffix(s string) string {
    if s == "" {
        return "a"
    }
    // Increment like base-26: a->b, z->za, zz->zza
    // ...
}
```

### Remove

- Remove `time.Sleep(time.Millisecond)` logic
- Remove `lastID` tracking

## Acceptance Criteria

### Functionality

- [ ] First ticket in a second gets base ID (e.g., `d5czj08`)
- [ ] Second ticket same second gets suffix `a` (e.g., `d5czj08a`)
- [ ] Third gets `b`, etc.
- [ ] After `z`, continues with `za`, `zb`, ...
- [ ] No sleep/retry delays

### Sorting

- [ ] IDs sort lexicographically in creation order
- [ ] `d5czj08` < `d5czj08a` < `d5czj08b` < ... < `d5czj08z` < `d5czj08za`

### Invariants

- [ ] All generated IDs are unique
- [ ] Suffix only added when base ID already exists
- [ ] Safety limit prevents infinite loop (max suffix length)

### Tests

- [ ] Concurrent test: 5 goroutines each run `tk create`, all succeed
- [ ] All created tickets retrievable via `tk show <id>`
- [ ] IDs sort in expected order
- [ ] Update existing ID generation tests if needed
