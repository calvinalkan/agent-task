---
schema_version: 1
id: d5djp00
status: open
blocked-by: []
created: 2026-01-05T03:07:44Z
type: chore
priority: 2
assignee: Calvin Alkan
---
# Refactor config to resolve TicketDir as absolute path

After LoadConfig resolves the configuration, TicketDir should be an absolute path. Currently every command has to manually check and join with workDir, which is an anti-pattern repeated across 11 files.

Rename the field from `TicketDir` to `TicketDirAbs` to make it explicit that it contains an absolute path.

## Acceptance Criteria

1. Rename `Config.TicketDir` to `Config.TicketDirAbs` to indicate it's an absolute path
2. LoadConfig resolves TicketDirAbs to an absolute path using workDir before returning
3. All commands use `cfg.TicketDirAbs` directly without filepath.IsAbs checks
4. Remove the repeated pattern from all 11 command files (block.go, close.go, create.go, editor.go, ls.go, ready.go, reopen.go, repair.go, show.go, start.go, unblock.go)
5. `tk print-config` shows the resolved absolute path for ticket_dir
6. All existing tests pass
7. make check passes
