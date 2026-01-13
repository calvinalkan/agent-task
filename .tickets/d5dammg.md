---
schema_version: 1
id: d5dammg
status: closed
closed: 2026-01-10T03:42:37Z
blocked-by: [d5dae6r]
created: 2026-01-04T17:58:42Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# tk close: require children closed before parent

## Design

## Summary

Update `tk close` to error if any direct children are still open.

## Current behavior

`tk close` only checks:
- (nothing, just sets status to closed)

## New behavior

`tk close` checks:
- All direct children in `children: []` are closed

Error message:
```
error: cannot close d5abc: children still open: [d5abc.1, d5abc.2]
```

## Rationale

Parent represents a logical unit of work. If children are open, the work isn't done.

Only need to check direct children because closing a child already requires its children to be closed (recursive guarantee).

## Implementation

```go
func openChildren(ticketDir string, children []string) []string {
    var open []string
    for _, childID := range children {
        status := getTicketStatus(ticketDir, childID)
        if status != StatusClosed {
            open = append(open, childID)
        }
    }
    return open
}
```

## Acceptance Criteria

- [ ] Error if any direct child is open
- [ ] Error message lists which children are open
- [ ] Works if `children: []` is empty (no error)
- [ ] Works if `children:` field missing (no error)
- [ ] Still closes tickets without children
