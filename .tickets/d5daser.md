---
schema_version: 1
id: d5daser
status: closed
closed: 2026-01-04T18:22:25Z
blocked-by: []
created: 2026-01-04T18:08:59Z
type: feature
priority: 2
assignee: Calvin Alkan
---
# Support global user config at ~/.config/tk/config.json

## Description

Add support for a global user configuration file following the XDG Base Directory Specification. This allows users to set personal defaults (e.g., preferred editor) that apply across all projects.

### Location

- `$XDG_CONFIG_HOME/tk/config.json` if `XDG_CONFIG_HOME` is set
- `~/.config/tk/config.json` otherwise (fallback)

### New Precedence Order (lowest to highest)

1. **Defaults** - hardcoded in `DefaultConfig()`
2. **Global user config** - `~/.config/tk/config.json` (optional, ignored if missing)
3. **Project config** - `.tk.json` in working directory (optional, ignored if missing)
4. **Explicit config** - `-c`/`--config` flag (must exist if specified)
5. **CLI overrides** - `--ticket-dir` flag

### Merging Behavior

Each level merges on top of the previous. Non-empty values from higher-precedence sources override lower ones.

Example:
```
~/.config/tk/config.json  →  {"editor": "nvim", "ticket_dir": "global-tickets"}
./.tk.json                →  {"ticket_dir": "project-issues"}
CLI                       →  (none)

Result: {"editor": "nvim", "ticket_dir": "project-issues"}
```

## Design Notes

### Implementation Changes

1. **config.go**:
   - Add `getGlobalConfigPath()` function that respects `$XDG_CONFIG_HOME`
   - Update `LoadConfig()` to load and merge global config before project config
   - Keep existing `mergeConfig()` logic (already handles field-by-field merging)

2. **print-config command**:
   - Should show the final merged configuration (already does this)
   - Consider adding `--show-sources` or verbose mode to show where each value came from (optional, future enhancement)

### Helper Function

```go
func getGlobalConfigPath() string {
    if xdgConfig := os.Getenv("XDG_CONFIG_HOME"); xdgConfig != "" {
        return filepath.Join(xdgConfig, "tk", "config.json")
    }
    if home, err := os.UserHomeDir(); err == nil {
        return filepath.Join(home, ".config", "tk", "config.json")
    }
    return ""
}
```

## Acceptance Criteria

- [ ] Global config is loaded from `$XDG_CONFIG_HOME/tk/config.json` when env var is set
- [ ] Global config falls back to `~/.config/tk/config.json` when `XDG_CONFIG_HOME` is not set
- [ ] Global config is optional (missing file is not an error)
- [ ] Global config is merged with defaults before project config
- [ ] Project config (`.tk.json`) overrides global config values
- [ ] Explicit config (`-c` flag) overrides both global and project config
- [ ] CLI flags override all config sources
- [ ] Invalid JSON in global config produces a clear error with file path
- [ ] Empty `ticket_dir` in global config is an error
- [ ] `print-config` shows the final merged result
- [ ] Works correctly when home directory cannot be determined (graceful skip)

## Tests

### New Tests for config_test.go

```go
// Global config loading

func TestConfig_GlobalConfig_Loaded(t *testing.T) {
    // Setup: create temp dir as fake home, set XDG_CONFIG_HOME
    // Create $XDG_CONFIG_HOME/tk/config.json with {"editor": "nvim"}
    // Run print-config with no project config
    // Assert: editor is "nvim", ticket_dir is default ".tickets"
}

func TestConfig_GlobalConfig_FallbackToHome(t *testing.T) {
    // Setup: create temp dir as fake home, unset XDG_CONFIG_HOME
    // Create ~/.config/tk/config.json with {"editor": "vim"}
    // Run print-config
    // Assert: editor is "vim"
}

func TestConfig_GlobalConfig_MissingIsNotError(t *testing.T) {
    // Setup: ensure no global config exists
    // Run print-config
    // Assert: exit code 0, defaults are used
}

func TestConfig_GlobalConfig_InvalidJSON(t *testing.T) {
    // Setup: create global config with invalid JSON
    // Run print-config
    // Assert: exit code 1, error message mentions global config path
}

func TestConfig_GlobalConfig_EmptyTicketDir(t *testing.T) {
    // Setup: create global config with {"ticket_dir": ""}
    // Run print-config
    // Assert: exit code 1, error about empty ticket_dir
}

// Merge precedence tests

func TestConfig_Precedence_ProjectOverridesGlobal(t *testing.T) {
    // Setup: global config has {"ticket_dir": "global-tickets", "editor": "nvim"}
    //        project config has {"ticket_dir": "project-tickets"}
    // Run print-config
    // Assert: ticket_dir is "project-tickets", editor is "nvim"
}

func TestConfig_Precedence_ExplicitOverridesGlobal(t *testing.T) {
    // Setup: global config has {"ticket_dir": "global-tickets"}
    //        explicit config has {"ticket_dir": "explicit-tickets"}
    // Run print-config -c explicit.json
    // Assert: ticket_dir is "explicit-tickets"
}

func TestConfig_Precedence_CLIOverridesGlobal(t *testing.T) {
    // Setup: global config has {"ticket_dir": "global-tickets"}
    // Run print-config --ticket-dir=cli-tickets
    // Assert: ticket_dir is "cli-tickets"
}

func TestConfig_Precedence_FullChain(t *testing.T) {
    // Setup: global has {"ticket_dir": "global", "editor": "nvim"}
    //        project has {"ticket_dir": "project"}
    // Run print-config --ticket-dir=cli
    // Assert: ticket_dir is "cli", editor is "nvim"
}

func TestConfig_GlobalConfig_XDGTakesPrecedenceOverHome(t *testing.T) {
    // Setup: create both $XDG_CONFIG_HOME/tk/config.json and ~/.config/tk/config.json
    //        with different values
    // Run print-config
    // Assert: XDG version is used
}

// Edge cases

func TestConfig_GlobalConfig_HomeDirUnavailable(t *testing.T) {
    // Setup: unset HOME and XDG_CONFIG_HOME (may need to skip on some systems)
    // Run print-config
    // Assert: gracefully skips global config, uses defaults
}

func TestConfig_GlobalConfig_PartialMerge(t *testing.T) {
    // Setup: global config only sets editor, project config only sets ticket_dir
    // Run print-config
    // Assert: both values are present in output
}
```

### Test Helpers Needed

- Helper to set/restore environment variables (XDG_CONFIG_HOME, HOME)
- Helper to create global config in temp directory
- Consider using `t.Setenv()` for env var management (Go 1.17+)
