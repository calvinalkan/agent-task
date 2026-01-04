---
schema_version: 1
id: d5cz1ng
status: closed
closed: 2026-01-04T04:53:10Z
blocked-by: []
created: 2026-01-04T04:47:18Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add block/unblock commands for managing ticket blockers

Add two new commands to manage ticket blockers after creation:
- `tk block <id> <blocker-id>` - Add a blocker to a ticket
- `tk unblock <id> <blocker-id>` - Remove a blocker from a ticket

Currently blockers can only be set at creation time via `--blocked-by`. These commands enable dynamic blocker management.

## Design

## New Files

**block.go**
- `cmdBlock(out, errOut, cfg, workDir, args) int`
- Validates: both IDs required, ticket exists, blocker exists, not already blocked by, can't block self
- Reads current blocked-by list, appends blocker, writes back
- Output: `Blocked <id> by <blocker-id>`

**unblock.go**
- `cmdUnblock(out, errOut, cfg, workDir, args) int`
- Validates: both IDs required, ticket exists, is actually blocked by blocker
- Reads current blocked-by list, removes blocker, writes back
- Output: `Unblocked <id> from <blocker-id>`

**block_test.go / unblock_test.go**
- Test both commands together for block/unblock workflows

## Changes to Existing Files

**ticket.go** - Add helpers:
- `ReadTicketBlockedBy(path string) ([]string, error)`
- `UpdateTicketBlockedBy(path string, blockedBy []string) error`

**errors.go** - Add:
- `errBlockerIDRequired`
- `errNotBlockedBy`
- `errAlreadyBlockedBy`
- `errCannotBlockSelf`

**run.go**:
- Add `case "block":` and `case "unblock":`
- Add help text to `printUsage`

## Help Text
```
  block <id> <blocker>   Add blocker to ticket
  unblock <id> <blocker> Remove blocker from ticket
```

## Acceptance Criteria

## Functionality

- [ ] `tk block <id> <blocker-id>` adds blocker to ticket's blocked-by list
- [ ] `tk unblock <id> <blocker-id>` removes blocker from ticket's blocked-by list
- [ ] Both commands print confirmation message on success

## Validation

- [ ] block: error if ticket ID missing
- [ ] block: error if blocker ID missing  
- [ ] block: error if ticket does not exist
- [ ] block: error if blocker ticket does not exist
- [ ] block: error if ticket already blocked by blocker
- [ ] block: error if trying to block ticket by itself
- [ ] unblock: error if ticket ID missing
- [ ] unblock: error if blocker ID missing
- [ ] unblock: error if ticket does not exist
- [ ] unblock: error if ticket is not blocked by blocker

## Invariants

- [ ] blocked-by list remains valid YAML array format after block/unblock
- [ ] blocked-by list has no duplicates after block
- [ ] block then unblock returns ticket to original state
- [ ] unblock on ticket with multiple blockers only removes specified blocker
- [ ] File atomically updated (no partial writes)

## Help

- [ ] `tk block --help` shows usage
- [ ] `tk unblock --help` shows usage
- [ ] `tk --help` lists both commands

## Tests

- [ ] block_test.go covers all block scenarios
- [ ] unblock_test.go covers all unblock scenarios
- [ ] Integration test: create -> block -> unblock workflow
