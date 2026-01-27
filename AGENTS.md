# AGENTS.md

`tk` is a minimal file-based task management system optimized for AI agents. Tickets are stored as Markdown files with YAML frontmatter in a `.tickets/` directory, making them easy to read, edit, and version control alongside code.

## Architecture

- `cmd/tk/main.go` - CLI entrypoint; wires stdin/stdout/stderr, args, env, and signals into `internal/cli.Run`.
- `internal/cli/run.go` - Global flag parsing, config load, and command dispatch.
- `internal/cli/<command>.go` - Each command (create, ls, ready, etc.) is self-contained with its own flag parsing, help text, and implementation.
- `internal/ticket/ticket.go` - Core ticket parsing, serialization, and file operations.
- `internal/ticket/config.go` - Hierarchical config loading (global → project → CLI).
- `internal/ticket/cache_*.go` - Mtime-based binary cache for fast listing.
- `internal/ticket/lock.go` - File locking for concurrent access.
- `internal/testutil/*` - Shared helpers for CLI and ticket tests.

Tests run against `cli.Run()` for integration tests or directly against `internal/cli` command constructors and `internal/ticket` helpers for targeted unit tests.

## Commands

We use `make` in this project. Always use make.

```bash
make check # Runs everything, use this before committing
make build # Build the binary
make test # Run tests with race detector
make lint # Run all linters
make clean # Remove binary and lock files

# Running targeted test with GOLAGS
GOFLAGS="-run=TestName" make test
GOFLAGS="-run=TestPattern.*" make test
```

## Sandbox Notice

If you encounter errors like `read-only file system` or `permission denied` for commands,
you are running in a sandbox and will need to ask the user for to perform the action for you.

## Workflow

Use `tk` to manage development of `tk` itself:

1. `tk ready` - See actionable tickets (open, unblocked)
2. `tk start <id>` - Mark ticket as in progress, outputs full ticket spec
3. Implement the feature/fix
5. `make check` - Ensure all tests pass
6. `tk close <id>` - Mark ticket as done
7. Commit with conventional message referencing the ticket, include only files related to the ticket plus the ticket file itself

When in doubt, run `tk --help` to see all commands and their flags.
