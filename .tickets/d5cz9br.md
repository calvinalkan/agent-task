---
schema_version: 1
id: d5cz9br
status: closed
closed: 2026-01-04T05:09:22Z
blocked-by: []
created: 2026-01-04T05:03:43Z
type: chore
priority: 1
assignee: Calvin Alkan
---
# Change priority range from 0-4 to 1-4 with 1=highest

Change priority range from 0-4 to 1-4. Priority 1 is highest, 4 is lowest. Default becomes 2.

Current: 0-4, 0=highest, default=2
New: 1-4, 1=highest, default=2

## Design

## Changes

**ticket.go**
- MinPriority: 0 → 1
- MaxPriority: 4 (unchanged)
- DefaultPriority: 2 (unchanged)

**create.go**
- Update help text: "Priority 1-4, 1=highest"
- Update error message

**Tests**
- Update validation tests for new range
- Update any hardcoded priority values

## Migration

Existing tickets with priority 0 need manual fix or migration script.

## Acceptance Criteria

- [ ] Priority range is 1-4
- [ ] Priority 1 is highest
- [ ] Default priority is 2
- [ ] Priority 0 rejected with error
- [ ] Priority 5 rejected with error
- [ ] Help text shows 1-4 range
- [ ] All tests pass with new range
