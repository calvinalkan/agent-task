---
id: d5cz5s0
status: closed
closed: 2026-01-04T05:18:38Z
blocked-by: [d5cz9br]
created: 2026-01-04T04:56:04Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add ready command to list actionable tickets

Add `tk ready` command that lists tickets that can be worked on right now:
- Status is `open` or `in_progress`
- All `blocked-by` tickets are `closed` (or blocked-by is empty)

Output sorted by priority (P1 first), then by ID.

Example:
```
d5cyp4r  [P1][open]        - Critical bug fix
d5cypt0  [P2][in_progress] - Feature X
d5cz1ng  [P3][open]        - Refactor Y
```

## Design

## New file: ready.go

`cmdReady(out, errOut, cfg, workDir, args) int`

### Algorithm

1. Load all ticket summaries via `ListTickets()`
2. Build map: ticketID → status (for blocker lookup)
3. Filter to open/in_progress tickets
4. For each, check if all blockers are closed (lookup in map)
5. Sort by priority (ascending, P1 first), then by ID
6. Output formatted lines

### Output format

```
<id>  [P<n>][<status>] - <title>
```

Align columns for readability (pad ID, priority, status).

### Edge cases

- Blocker ticket doesn't exist (deleted): treat as resolved, print warning to stderr
- No ready tickets: print nothing (exit 0)

## Changes to run.go

- Add `case "ready":`
- Add `readyHelp` to `printUsage`

## Help text

```
  ready                  List actionable tickets (unblocked, not closed)
```

## Acceptance Criteria

## Functionality

- [ ] `tk ready` lists open/in_progress tickets with all blockers closed
- [ ] Output sorted by priority (P1 first), then by ID
- [ ] Tickets with empty blocked-by list are included
- [ ] Tickets with all blockers closed are included
- [ ] Closed tickets are excluded

## Filtering

- [ ] Ticket blocked by open ticket: excluded
- [ ] Ticket blocked by in_progress ticket: excluded  
- [ ] Ticket blocked by closed ticket: included
- [ ] Ticket blocked by non-existent ticket: included (treat as resolved), prints warning

## Output format

- [ ] Shows priority in output: [P1], [P2], etc.
- [ ] Shows status in output: [open], [in_progress]
- [ ] Shows title

## Help

- [ ] `tk ready --help` shows usage
- [ ] `tk --help` lists ready command

## Invariants

- [ ] Every active ticket is either ready or not ready (complete coverage)
- [ ] Closed tickets never appear in ready
- [ ] Empty blocked-by list = always ready (if active)
- [ ] Only direct blockers matter (not transitive)
- [ ] Missing blocker = resolved (with warning)
- [ ] Priority ordering: P1 before P2 before P3 before P4, then by ID
- [ ] Idempotent: same output on repeated runs with no changes

## Tests

- [ ] ready_test.go covers basic filtering
- [ ] ready_test.go covers priority sorting
- [ ] ready_test.go covers blocker resolution logic
- [ ] ready_test.go covers missing blocker edge case
- [ ] ready_test.go covers invariants
