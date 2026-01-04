---
schema_version: 1
id: d5czdg0
status: closed
closed: 2026-01-04T05:37:25Z
blocked-by: []
created: 2026-01-04T05:12:32Z
type: feature
priority: 3
assignee: Calvin Alkan
---
# Add repair command to fix ticket inconsistencies

Add `tk repair` command to fix ticket file inconsistencies.

```
tk repair <id>      # Repair specific ticket
tk repair --all     # Repair all tickets
```

Initial repair actions:
- Remove stale blocker references (blockers that don't exist)

## Design

## New file: repair.go

`cmdRepair(out, errOut, cfg, workDir, args) int`

### Flags

- `--all` - Repair all tickets instead of single ID
- `--dry-run` - Show what would be fixed without writing

### Algorithm (single ticket)

1. Read ticket's blocked-by list
2. For each blocker, check if ticket exists
3. If not, collect for removal
4. If any stale blockers found:
   - dry-run: print what would be removed
   - normal: update blocked-by list, print what was removed
5. Print summary

### Output

Normal:
```
Removed stale blocker: xyz123
Repaired d5cz5s0
```

Nothing to fix:
```
Nothing to repair
```

Dry-run:
```
Would remove stale blocker: xyz123
```

### Changes to ticket.go

Reuse `UpdateTicketBlockedBy()` from block/unblock implementation.

### Changes to run.go

- Add `case "repair":`
- Add help text

### Changes to ls.go and ready.go

When printing warnings about missing blockers, suggest repair:
```
warning: d5cz5s0: blocker xyz123 not found (run: tk repair d5cz5s0)
```

## Acceptance Criteria

## Functionality

- [ ] `tk repair <id>` fixes single ticket
- [ ] `tk repair --all` fixes all tickets
- [ ] Removes stale blocker references
- [ ] Prints what was fixed

## Flags

- [ ] `--dry-run` shows what would be fixed without writing
- [ ] `--all` processes all tickets

## Validation

- [ ] Error if no ID and no --all flag
- [ ] Error if ID doesn't exist
- [ ] Exit 0 if nothing to repair

## Output

- [ ] Lists each removed blocker
- [ ] Shows "Repaired <id>" on success
- [ ] Shows "Nothing to repair" when clean
- [ ] Dry-run prefixes with "Would"
- [ ] `ls` and `ready` warnings suggest `tk repair <id>`

## Invariants

- [ ] Repair is idempotent (running twice = same result)
- [ ] Only modifies blocked-by field
- [ ] File atomically updated

## Help

- [ ] `tk repair --help` shows usage
- [ ] `tk --help` lists repair command

## Tests

- [ ] repair_test.go covers stale blocker removal
- [ ] repair_test.go covers --dry-run
- [ ] repair_test.go covers --all
- [ ] repair_test.go covers nothing to repair case
