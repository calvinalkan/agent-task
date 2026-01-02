---
id: d5czbmr
status: closed
closed: 2026-01-04T05:10:02Z
blocked-by: []
created: 2026-01-04T05:08:35Z
type: chore
priority: 2
assignee: Calvin Alkan
---
# Update ls output format for blocked-by display

Change the blocked-by indicator in `tk ls` output from `<-` to `<- blocked-by:` for clarity.

Current:
```
d5cz5s0 [open] - Add ready command <- [d5cz9br]
```

New:
```
d5cz5s0 [open] - Add ready command <- blocked-by: [d5cz9br]
```

## Design

## Changes to ls.go

Update `formatTicketLine()`:

```go
// Current
builder.WriteString(" <- [")

// New  
builder.WriteString(" <- blocked-by: [")
```

## Acceptance Criteria

- [ ] `tk ls` shows `<- blocked-by: [ids]` for blocked tickets
- [ ] Tickets without blockers unchanged
- [ ] Tests updated
