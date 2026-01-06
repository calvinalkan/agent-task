---
schema_version: 1
id: d5e8hz8
status: open
blocked-by: []
created: 2026-01-06T04:01:01Z
type: feature
priority: 2
---
# Design SetParent operation for spec

Explore adding a SetParent/Reparent operation to the spec that allows changing a ticket's parent after creation.

## Context
Currently ParentID is immutable (set only at create time). This makes parent cycles structurally impossible. Adding SetParent would require explicit cycle detection.

## Key Questions

1. Status constraints on the child being reparented:
   - Option A: Only Open tickets can be reparented (simplest)
   - Option B: InProgress allowed if new parent is InProgress/Closed
   - Option C: Allow any, Start constraint only applies at start time

2. Removing parent (orphaning):
   - Set ParentID to empty - should this be allowed?
   - Same status constraints as reparenting?

3. Cycle detection:
   - Can not set parent to self
   - Can not set parent to any descendant
   - Would give ErrParentCycle a use (currently dead code)

## Proposed Constraints
- Ticket must exist
- Ticket must not be Closed (immutable)
- Ticket must be Open (Option A - simplest)
- New parent must exist (if non-empty)
- New parent must not be Closed
- No self-parent
- No cycles (new parent can not be a descendant)
