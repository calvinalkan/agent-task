---
schema_version: 1
id: d5jhgc8
status: open
blocked-by: []
created: 2026-01-12T15:50:09Z
type: feature
priority: 2
---
# Add delete command with referential integrity checks

Add `tk delete <id>` command that safely removes a ticket while maintaining referential integrity.

## Validation
Before deleting, check:
1. No other ticket has this ID in `blocked-by`
2. No other ticket has this ID as `parent`

If violations exist, error with details listing the blocking tickets.

## Flags
- `--force`: Override checks and remove references automatically (clears blocked-by refs, orphans children)

## Implementation
- Scan cache for blocked-by references to target ID
- Scan cache for parent references to target ID
- If found and no --force, error with list
- If --force, update those tickets to remove references
- Delete the ticket file
- Update cache via DeleteCacheEntry()
