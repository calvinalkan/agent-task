---
id: d5dae6r
status: open
blocked-by: []
created: 2026-01-04T17:44:59Z
type: feature
priority: 1
assignee: Calvin Alkan
---
# Add --parent flag for hierarchical tickets

## Design

## ID Format

`<base>.<n>.<n>...`

Examples:
- `d5abc123` - root ticket
- `d5abc123.1` - first child
- `d5abc123.1.1` - grandchild

Child numbers start at 1, increment sequentially (max+1). Unlimited depth.

## New CLI Flag

```
tk create "Subtask title" --parent d5abc123
```

Creates ticket with ID `d5abc123.1` (or next available).

Error if parent ID doesn't exist.

## New Field: children

```yaml
children: [d5abc123.1, d5abc123.2]
```

Stored on parent. Only direct children.

If `children:` field missing from ticket, treat as empty array internally (no error).

## Deriving Parent from ID

No `parent` field stored. Computed by stripping last `.<n>`:

- `d5abc123.1.1` → parent is `d5abc123.1`
- `d5abc123` → no parent

## Create Child Flow

1. Lock parent file
2. Read parent content
3. Determine next child number (max of existing + 1)
4. Add child ID to parent's `children` array
5. Write parent file
6. Create child file
7. If step 6 fails: restore original parent content
8. Unlock

## New Helpers Needed

```go
// Parse parent ID from child ID
func parentID(id string) string

// Get next child number from parent's children array
func nextChildID(parentContent []byte) string

// Add child ID to parent's children array
func addChildToParent(content []byte, childID string) []byte
```

## Future: Affected Commands (separate tickets)

- `tk ready`: Check ancestor blocking
- `tk close`: Require children closed before parent
- `tk show`: Display children
- `tk ls`: Maybe indent children
- `tk repair`: Validate children arrays

## Acceptance Criteria

## Flag parsing
- [ ] `--parent` shows in help output
- [ ] `--parent <id>` accepted
- [ ] `--parent` without value errors

## Basic creation
- [ ] Creates child with correct ID format (`parent.1`)
- [ ] Child file contains correct ID in frontmatter
- [ ] Parent's `children: []` updated with new child ID
- [ ] Second child gets `.2`, third gets `.3`, etc.
- [ ] Grandchild works: `parent.1.1`

## Parent validation
- [ ] Error if parent doesn't exist
- [ ] Error if parent ID is malformed

## Field in output
- [ ] `children: []` appears in new tickets (empty by default)
- [ ] `children: [x.1, x.2]` appears after adding children
- [ ] Child ticket has `children: []`

## Numbering
- [ ] Uses max+1: if `x.1` and `x.3` exist, next is `x.4`
- [ ] First child is always `.1`

## Graceful handling
- [ ] Missing `children:` field in parent → treated as empty array
- [ ] Malformed `children:` field → treated as empty array
- [ ] Child in `children:` but file missing → create still works

## Repair integration
- [ ] `tk repair` removes non-existent children from `children: []`
- [ ] `tk repair` adds orphan children to parent's list
- [ ] `tk repair` adds missing `children:` field to tickets
