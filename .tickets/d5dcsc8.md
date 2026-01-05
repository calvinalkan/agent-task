---
schema_version: 1
id: d5dcsc8
status: closed
closed: 2026-01-05T16:02:23Z
blocked-by: []
created: 2026-01-04T20:25:21Z
type: task
priority: 3
assignee: Calvin Alkan
---
# Restructure codebase with cmd/ and internal/ packages

Move from flat file structure to organized cmd/ + internal/ packages for better maintainability and multi-binary support.

## Design

### Current State
- 39 .go files flat in root
- main.go, run.go, commands, ticket logic all mixed together
- Benchmark scripts use `//go:build ignore`

### Target Structure
```
tk/
├── go.mod, Makefile, AGENTS.md, .gitignore, etc.
│
├── cmd/
│   ├── tk/main.go              # CLI entry point
│   └── tk-bench/main.go        # Benchmark tool (imports ticket directly)
│
└── internal/
    ├── cli/                    # CLI concerns only
    │   ├── run.go              # Run(), flag parsing, command routing
    │   ├── run_test.go
    │   ├── cmd_create.go
    │   ├── cmd_create_test.go
    │   ├── cmd_ls.go
    │   ├── cmd_ls_test.go
    │   ├── cmd_show.go
    │   ├── cmd_show_test.go
    │   ├── cmd_close.go
    │   ├── cmd_close_test.go
    │   ├── cmd_start.go
    │   ├── cmd_start_test.go
    │   ├── cmd_reopen.go
    │   ├── cmd_reopen_test.go
    │   ├── cmd_block.go
    │   ├── cmd_block_test.go
    │   ├── cmd_unblock.go
    │   ├── cmd_unblock_test.go
    │   ├── cmd_ready.go
    │   ├── cmd_ready_test.go
    │   ├── cmd_repair.go
    │   ├── cmd_repair_test.go
    │   ├── cmd_editor.go
    │   ├── cmd_editor_test.go
    │   └── help.go
    │
    └── ticket/                 # Domain + infrastructure
        ├── ticket.go           # Ticket struct, parse, serialize
        ├── ticket_test.go
        ├── id.go               # ID generation
        ├── cache.go            # Binary cache (was cache_binary.go)
        ├── cache_test.go
        ├── cache_gob.go
        ├── cache_gob_test.go
        ├── lock.go             # File locking
        ├── lock_test.go
        ├── config.go           # Config loading
        └── config_test.go
```

### Import Flow
```go
// cmd/tk/main.go
import "tk/internal/cli"
func main() { os.Exit(cli.Run(...)) }

// cmd/tk-bench/main.go
import "tk/internal/ticket"    // Direct access, no CLI overhead

// internal/cli/cmd_create.go
import "tk/internal/ticket"
```

### Dependency Direction
```
cmd/tk/main.go → cli → ticket
cmd/tk-bench/main.go → ticket
```

cli/ depends on ticket/, never the reverse.

### Key Decisions
- **Module path stays `tk`** - no need for github.com path for CLI tool
- **Errors stay local** - define errors at top of each file, no central errors.go
- **Two packages only** - cli + ticket, not over-engineered
- **Migration only** - no new features, just move files and update packages/imports

### File Mapping (current → new)
| Current | New Location |
|---------|--------------|
| main.go | cmd/tk/main.go |
| run.go | internal/cli/run.go |
| create.go | internal/cli/cmd_create.go |
| ls.go | internal/cli/cmd_ls.go |
| show.go | internal/cli/cmd_show.go |
| close.go | internal/cli/cmd_close.go |
| start.go | internal/cli/cmd_start.go |
| reopen.go | internal/cli/cmd_reopen.go |
| block.go | internal/cli/cmd_block.go |
| unblock.go | internal/cli/cmd_unblock.go |
| ready.go | internal/cli/cmd_ready.go |
| repair.go | internal/cli/cmd_repair.go |
| editor.go | internal/cli/cmd_editor.go |
| errors.go | internal/ticket/errors.go (keep as single file for now) |
| ticket.go | internal/ticket/ticket.go |
| cache_binary.go | internal/ticket/cache.go |
| cache_gob.go | internal/ticket/cache_gob.go |
| cache_write_through.go | internal/ticket/cache_write_through.go |
| lock.go | internal/ticket/lock.go |
| config.go | internal/ticket/config.go |
| bench/bench.go | cmd/tk-bench/main.go |
| bench/seed-bench.go | (merge into tk-bench or separate cmd) |

## Acceptance Criteria

- [ ] cmd/tk/main.go exists and builds
- [ ] cmd/tk-bench/main.go exists and builds
- [ ] internal/cli/ contains Run() and all cmd_* files
- [ ] internal/ticket/ contains ticket, cache, lock, config
- [ ] errors.go moved to internal/ticket/errors.go (single file for now)
- [ ] All tests pass: `go test ./...`
- [ ] `go build ./...` builds both binaries
- [ ] No circular dependencies
- [ ] Makefile updated for new structure
