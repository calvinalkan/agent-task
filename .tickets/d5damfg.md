---
id: d5damfg
status: open
blocked-by: [d5dae6r]
created: 2026-01-04T17:58:22Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# tk ready: check ancestor blocking

## Design

## Summary

Update `tk ready` to check ancestor blocking. A ticket is not ready if any ancestor is blocked.

## Current behavior

`tk ready` shows tickets where:
- Status is `open`
- All `blocked-by` tickets are closed

## New behavior

`tk ready` shows tickets where:
- Status is `open`
- All own `blocked-by` tickets are closed
- All ancestors' `blocked-by` tickets are closed (walk up parent chain)

## Implementation

```go
func isAncestorBlocked(ticketDir, id string, statusMap map[string]string) bool {
    parentID := getParentID(id)  // strip last .<n>
    if parentID == "" {
        return false  // no parent, not blocked by ancestry
    }
    
    // Check parent's blocked-by
    // Then recurse to grandparent
}
```

## Example

```
d5abc (blocked-by: [d5xyz] where d5xyz is open)
└── d5abc.1 (no blocked-by)
    └── d5abc.1.1 (no blocked-by)
```

None of these appear in `tk ready` because root is blocked.

## Acceptance Criteria

- [ ] Ticket with blocked ancestor does not appear in `tk ready`
- [ ] Ticket with unblocked ancestors appears normally
- [ ] Works with multiple levels of nesting
- [ ] Performance acceptable (parent chain is short)
