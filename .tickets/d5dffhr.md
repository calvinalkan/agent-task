---
schema_version: 1
id: d5dffhr
status: open
blocked-by: []
created: 2026-01-04T23:29:11Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Add agent identity tracking

Track which agent is working on a ticket. Replace the existing `assignee` field with a new `agent` field that is set automatically when a ticket is started via `tk start`. This enables multi-agent workflows where multiple AI agents work on tasks in parallel (e.g., using git worktrees).

## Design

## Overview

Remove the `assignee` field entirely and introduce a new `agent` field that identifies which agent is working on a ticket. The agent identity comes from configuration (not CLI flags or environment variables).

## Configuration

### Config Hierarchy (highest wins)
1. Defaults: `agent: "default-agent"`
2. Global config: `~/.config/tk/config.json`
3. Project config: `.tk.json` (committed)
4. **Worktree config: `.tk.worktree.json`** (gitignored, NEW)

### New Config File: `.tk.worktree.json`
- Loaded after `.tk.json`, before any overrides
- Should be added to `.gitignore` by users
- Enables per-worktree agent identity for multi-agent setups:
```
main/
  .tk.json
  .tickets/
.worktrees/
  agent-1/.tk.worktree.json  → {"agent": "claude-1"}
  agent-2/.tk.worktree.json  → {"agent": "claude-2"}
```

### Config Struct Changes
```go
type Config struct {
    TicketDir string `json:"ticket_dir"`
    Editor    string `json:"editor,omitempty"`
    Agent     string `json:"agent,omitempty"`  // NEW
}

func DefaultConfig() Config {
    return Config{
        TicketDir: ".tickets",
        Agent:     "default-agent",  // NEW
    }
}
```

## Ticket Format Changes

### Remove
- `assignee` field from frontmatter
- `--assignee` / `-a` flag from `tk create`
- `getGitUserName()` function

### Add
- `agent` field in frontmatter (set on `tk start`)

### Example Ticket
```yaml
---
schema_version: 1
id: abc123
status: in_progress
agent: claude-1           # NEW (added by tk start)
blocked-by: []
created: 2026-01-04T12:00:00Z
type: task
priority: 2
---
# My ticket title
```

## Behavior Changes

### `tk start <id>`
- Reads `agent` from config
- Adds `agent: <value>` field to ticket frontmatter
- Uses `addFieldToContent()` (same pattern as `closed` timestamp)
- Agent field stays on ticket permanently (not removed on close/reopen)

### `tk create`
- Remove `--assignee` / `-a` flag entirely
- Remove `getGitUserName()` helper function
- No agent field at creation time (added on start)

### `tk ls`
- Add `--agent <name>` flag: filter tickets by agent field
- Add `--mine` flag: filter tickets where agent matches configured agent

## Cache Changes (BREAKING)

The binary cache stores ticket summaries. Changes required:

1. **Bump `cacheVersionNum`** from 4 to 5
   - Old caches will automatically rebuild (existing behavior)

2. **Rename in `cache_binary.go`**:
   - `errAssigneeTooLong` → `errAgentTooLong`

3. **Update `TicketSummary` struct**:
   - Remove `Assignee string`
   - Add `Agent string`

4. **Update `readDataEntry()`**: read `agent` instead of `assignee`

5. **Update `encodeSummaryData()`**: validate/write `agent` instead of `assignee`

## Files to Modify

### Core Changes
- `config.go` - Add Agent field, load .tk.worktree.json
- `ticket.go` - Ticket/TicketSummary structs, FormatTicket, ParseTicketFrontmatter
- `cache_binary.go` - Version bump, rename assignee→agent throughout

### Commands
- `create.go` - Remove --assignee flag and getGitUserName()
- `start.go` - Add agent field when starting
- `ls.go` - Add --agent and --mine filters

### Tests
- `config_test.go` - Test .tk.worktree.json loading
- `create_test.go` - Remove assignee tests
- `start_test.go` - Add agent field tests
- `ls_test.go` - Add filter tests
- `cache_binary_test.go` - Update "assignee too long" → "agent too long"
- `ticket_test.go` - Update format expectations

### Other
- `seed-bench.go` - Update ticket template
- `run.go` - Update help text

## Files NOT Changed
- `show.go` - Just displays raw content
- `close.go` - Agent stays on closed ticket
- `reopen.go` - No agent changes
- `block.go` / `unblock.go` - Unrelated

## No Backwards Compatibility
- Existing tickets with `assignee` field: ignored (field can stay, won't be parsed)
- No migration needed
- Cache rebuilds automatically due to version bump

## Acceptance Criteria

## Configuration
- [ ] `Agent` field added to Config struct
- [ ] Default agent is "default-agent"
- [ ] `.tk.worktree.json` loaded after `.tk.json` in config hierarchy
- [ ] `mergeConfig()` handles Agent field correctly
- [ ] `ConfigSources` tracks worktree config path
- [ ] `print-config` shows agent and worktree source

## Assignee Removal
- [ ] `assignee` field removed from `Ticket` struct
- [ ] `assignee` field removed from `TicketSummary` struct
- [ ] `--assignee` / `-a` flag removed from `tk create`
- [ ] `getGitUserName()` function removed
- [ ] Help text updated (no assignee references)

## Agent Field
- [ ] `agent` field added to `Ticket` struct
- [ ] `agent` field added to `TicketSummary` struct
- [ ] `FormatTicket()` writes `agent:` if non-empty
- [ ] `ParseTicketFrontmatter()` parses `agent` field

## Start Command
- [ ] `tk start` adds `agent: <configured-agent>` to ticket
- [ ] Agent field persists after close/reopen

## Ls Command Filtering
- [ ] `--agent <name>` flag filters by exact agent match
- [ ] `--mine` flag filters by configured agent
- [ ] Filters work with other flags (--status, --priority, etc.)
- [ ] Help text documents new flags

## Cache
- [ ] `cacheVersionNum` bumped to 5
- [ ] `errAssigneeTooLong` renamed to `errAgentTooLong`
- [ ] Cache encodes/decodes agent field correctly
- [ ] Old caches rebuild automatically (no errors)

## Tests
- [ ] Config loading tests for .tk.worktree.json
- [ ] Config merge tests for agent field
- [ ] Create command tests updated (no assignee)
- [ ] Start command tests verify agent field written
- [ ] Ls filter tests for --agent and --mine
- [ ] Cache tests updated for agent field
- [ ] Ticket parsing tests for agent field
