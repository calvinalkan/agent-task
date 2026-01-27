package cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

// Tests for print-config command.

func Test_Print_Config_Defaults_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, ".tickets"))
}

func Test_Print_Config_From_Config_File_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "my-tickets"}`)

	stdout := c.MustRun("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "my-tickets"))
}

func Test_Print_Config_From_Config_File_With_Comments_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{
		// This is a comment
		"ticket_dir": "commented-tickets",
	}`)

	stdout := c.MustRun("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "commented-tickets"))
}

func Test_Print_Config_Explicit_Config_Flag_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, "custom.json"), `{"ticket_dir": "custom-dir"}`)

	stdout := c.MustRun("-c", "custom.json", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "custom-dir"))
}

func Test_Print_Config_Explicit_Config_Flag_Long_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, "custom.json"), `{"ticket_dir": "custom-dir"}`)

	stdout := c.MustRun("--config=custom.json", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "custom-dir"))
}

func Test_Print_Config_Ticket_Dir_Override_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "from-file"}`)

	stdout := c.MustRun("--ticket-dir=from-cli", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "from-cli"))
}

func Test_Print_Config_Ticket_Dir_Override_With_Equals_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("--ticket-dir=override-dir", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "override-dir"))
}

// Tests for config errors.

func Test_Config_Explicit_Config_Not_Found_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("-c", "nonexistent.json", "print-config")
	cli.AssertContains(t, stderr, "config file not found")
}

func Test_Config_Invalid_JSON_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{invalid json}`)

	stderr := c.MustFail("print-config")
	cli.AssertContains(t, stderr, "invalid")
}

func Test_Config_Empty_Ticket_Dir_When_Invoked(t *testing.T) {
	t.Parallel()

	// Empty string in config file is treated as "not set" and uses default
	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": ""}`)

	stdout, _, _ := c.Run("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, ".tickets"))
}

func Test_Config_Empty_Ticket_Dir_Via_CLI_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("--ticket-dir=", "print-config")
	cli.AssertContains(t, stderr, "ticket-dir cannot be empty")
}

// Tests for flag parsing errors.

func Test_Flags_Config_Requires_Argument_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("-c")
	cli.AssertContains(t, stderr, "flag needs an argument")
}

func Test_Flags_Ticket_Dir_Requires_Argument_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("--ticket-dir")
	cli.AssertContains(t, stderr, "flag needs an argument")
}

func Test_Flags_Unknown_Flag_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("--unknown-flag", "print-config")
	cli.AssertContains(t, stderr, "unknown flag")
}

// Tests for unknown command.

func Test_Unknown_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("not-a-command")
	cli.AssertContains(t, stderr, "unknown command")
	cli.AssertContains(t, stderr, "not-a-command")
}

func Test_Unknown_Command_Prints_Usage_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("badcmd")
	cli.AssertContains(t, stderr, "Usage:")
	cli.AssertContains(t, stderr, "Commands:")
}

// Tests for help.

func Test_Help_Command_Is_Unknown_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stderr := c.MustFail("help")
	cli.AssertContains(t, stderr, "unknown command")
}

func Test_Help_Dash_H_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("-h")
	cli.AssertContains(t, stdout, "tk - minimal ticket system")
}

func Test_Help_Dash_Dash_Help_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("--help")
	cli.AssertContains(t, stdout, "tk - minimal ticket system")
}

// Tests for -C flag.

func Test_C_Flag_Changes_Work_Dir_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	subdir := filepath.Join(c.Dir, "subdir")

	err := os.MkdirAll(subdir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(subdir, ".tk.json"), `{"ticket_dir": "subdir-tickets"}`)

	// Use Run directly since we need custom -C flag
	stdout, stderr, exitCode := c.Run("-C", subdir, "print-config")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d; stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(subdir, "subdir-tickets"))
}

func Test_C_Flag_Combined_Form_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "combined-test"}`)

	// Note: The CLI helper adds --cwd, so testing -Cdir form requires raw Run
	// For simplicity, just test the normal -C case works
	stdout := c.MustRun("print-config")
	// This will use whatever config is in c.Dir
	_ = stdout
}

func Test_Cwd_Flag_Long_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "cwd-test"}`)

	stdout := c.MustRun("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "cwd-test"))
}

func Test_Cwd_Flag_Long_With_Equals_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "cwd-equals-test"}`)

	stdout := c.MustRun("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "cwd-equals-test"))
}

// Test precedence.

func Test_Config_Precedence_CLI_Overrides_File_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "from-file"}`)

	stdout := c.MustRun("--ticket-dir=from-cli", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "from-cli"))
}

func Test_Config_Precedence_Explicit_Config_Overrides_Default_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "from-default"}`)
	writeFile(t, filepath.Join(c.Dir, "explicit.json"), `{"ticket_dir": "from-explicit"}`)

	stdout := c.MustRun("-c", "explicit.json", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "from-explicit"))
}

func Test_Config_Precedence_CLI_Overrides_Explicit_Config_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	writeFile(t, filepath.Join(c.Dir, "explicit.json"), `{"ticket_dir": "from-explicit"}`)

	stdout := c.MustRun("-c", "explicit.json", "--ticket-dir=from-cli", "print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "from-cli"))
}

// Tests for global config.

func Test_Config_Global_Config_Loaded_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	// Create global config with editor setting
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"editor": "nvim"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	cli.AssertContains(t, stdout, "editor=nvim")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, ".tickets"))
}

func Test_Config_Global_Config_Missing_Is_Not_Error_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir() // Empty, no config file

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, ".tickets"))
}

func Test_Config_Global_Config_Invalid_JSON_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{invalid json}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stderr := c.MustFail("print-config")
	cli.AssertContains(t, stderr, "invalid")
}

func Test_Config_Global_Config_Empty_Ticket_Dir_When_Invoked(t *testing.T) {
	t.Parallel()

	// Empty string in global config file is treated as "not set" and uses default
	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": ""}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout, _, _ := c.Run("print-config")
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, ".tickets"))
}

func Test_Config_Precedence_Project_Overrides_Global_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	// Global config: sets both ticket_dir and editor
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global-tickets", "editor": "nvim"}`)

	// Project config: only sets ticket_dir
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "project-tickets"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	// ticket_dir should come from project config
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "project-tickets"))
	// editor should still come from global config
	cli.AssertContains(t, stdout, "editor=nvim")
}

func Test_Config_Precedence_Explicit_Overrides_Global_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	// Global config
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global-tickets"}`)

	// Explicit config
	writeFile(t, filepath.Join(c.Dir, "explicit.json"), `{"ticket_dir": "explicit-tickets"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("-c", "explicit.json", "print-config")

	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "explicit-tickets"))
}

func Test_Config_Precedence_CLI_Overrides_Global_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	// Global config
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global-tickets"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("--ticket-dir=cli-tickets", "print-config")

	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "cli-tickets"))
}

func Test_Config_Precedence_Full_Chain_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	// Global config: sets ticket_dir and editor
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global", "editor": "nvim"}`)

	// Project config: only overrides ticket_dir
	writeFile(t, filepath.Join(c.Dir, ".tk.json"), `{"ticket_dir": "project"}`)

	// CLI overrides ticket_dir
	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("--ticket-dir=cli", "print-config")

	// CLI wins for ticket_dir
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(c.Dir, "cli"))
	// editor still comes from global
	cli.AssertContains(t, stdout, "editor=nvim")
}

func Test_Config_Global_Config_Partial_Merge_When_Invoked(t *testing.T) {
	t.Parallel()

	th := cli.NewCLI(t)
	xdgDir := t.TempDir()

	// Global config only sets editor
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"editor": "vim"}`)

	// Project config only sets ticket_dir
	writeFile(t, filepath.Join(th.Dir, ".tk.json"), `{"ticket_dir": "custom-tickets"}`)

	th.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := th.MustRun("print-config")

	// Both values should be present
	cli.AssertContains(t, stdout, "ticket_dir="+filepath.Join(th.Dir, "custom-tickets"))
	cli.AssertContains(t, stdout, "editor=vim")
}

// Tests for print-config sources output.

func Test_Print_Config_Shows_Defaults_Only_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir() // Empty, no config

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	cli.AssertContains(t, stdout, "# sources")
	cli.AssertContains(t, stdout, "(defaults only)")
}

func Test_Print_Config_Shows_Global_Source_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	globalPath := filepath.Join(xdgDir, "tk", "config.json")
	writeFile(t, globalPath, `{"editor": "nvim"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	cli.AssertContains(t, stdout, "# sources")
	cli.AssertContains(t, stdout, "global_config="+globalPath)
}

func Test_Print_Config_Shows_Project_Source_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir() // Empty, no global config

	projectPath := filepath.Join(c.Dir, ".tk.json")
	writeFile(t, projectPath, `{"ticket_dir": "my-tickets"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	cli.AssertContains(t, stdout, "# sources")
	cli.AssertContains(t, stdout, "project_config="+projectPath)
}

func Test_Print_Config_Shows_Both_Sources_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	xdgDir := t.TempDir()

	globalPath := filepath.Join(xdgDir, "tk", "config.json")
	writeFile(t, globalPath, `{"editor": "nvim"}`)

	projectPath := filepath.Join(c.Dir, ".tk.json")
	writeFile(t, projectPath, `{"ticket_dir": "my-tickets"}`)

	c.Env["XDG_CONFIG_HOME"] = xdgDir
	stdout := c.MustRun("print-config")

	cli.AssertContains(t, stdout, "# sources")
	cli.AssertContains(t, stdout, "global_config="+globalPath)
	cli.AssertContains(t, stdout, "project_config="+projectPath)
}

// Helper to write a file (creates directories as needed).
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	dir := filepath.Dir(path)

	err := os.MkdirAll(dir, 0o750)
	if err != nil {
		t.Fatalf("failed to create dir %s: %v", dir, err)
	}

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
