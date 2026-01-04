---
schema_version: 1
id: d5dd2a0
status: open
blocked-by: []
created: 2026-01-04T20:44:24Z
type: feature
priority: 3
assignee: Calvin Alkan
---
# Add optional reason for blocked-by relationships

Allow specifying an optional reason why a ticket is blocked by another ticket. This helps explain the dependency relationship.

## Design

## Current Format
```yaml
blocked-by: [abc123, def456]
```

## Proposed Format
Support both simple IDs and objects with reason:
```yaml
blocked-by:
  - abc123
  - id: def456
    reason: Need the API endpoint implemented first
```

## CLI Changes
- `tk block <id> <blocker-id> [--reason "..."]` - add blocker with optional reason
- `tk create --blocked-by <id> [--blocked-reason "..."]` - reason allowed only with single `--blocked-by`
  - Error if `--blocked-reason` provided with multiple `--blocked-by` flags (use `tk block` after)

## Edge Cases

### Re-blocking with reason
If ticket is already blocked by X and you run `tk block <id> X --reason "..."`:
- Update the reason (idempotent behavior)

### Removing reason
- `tk block <id> X --reason ""` - removes reason (reverts to simple ID format)

## Display Changes
- `tk show` displays blocking reasons when present
- `tk ls` keeps compact format (just IDs)

## Implementation
- Update Ticket struct: BlockedBy becomes []BlockedByEntry with ID and optional Reason
- Update parser to handle only the new format, no backward compatibility needed, existing tickets should be updated in place (outside tk)
- Update formatter to write object format only when reason is present
- Update block command to accept --reason flag
- Block command: if blocker already exists, update reason instead of erroring

## Acceptance Criteria

- Can add a blocker with a reason via `tk block <id> <blocker> --reason "..."`
- Can create with single blocker + reason: `tk create --blocked-by X --blocked-reason "..."`
- Error if `--blocked-reason` used with multiple `--blocked-by` in create
- Reason is stored in ticket frontmatter
- Reason is displayed in `tk show` output
- Existing tickets with simple blocked-by arrays continue to work
- Tickets without reasons use compact array format when serialized
- Re-blocking updates the reason (idempotent)
- Empty reason removes existing reason
