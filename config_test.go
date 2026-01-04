package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Test helpers.

func runTk(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()

	var out, errOut bytes.Buffer

	fullArgs := append([]string{"tk", "-C", dir}, args...)
	exitCode := Run(nil, &out, &errOut, fullArgs, nil)

	return out.String(), errOut.String(), exitCode
}

func assertExitCode(t *testing.T, got, want int, stderr string) {
	t.Helper()

	if got != want {
		t.Errorf("exit code = %d, want %d\nstderr: %s", got, want, stderr)
	}
}

func assertStdoutEmpty(t *testing.T, stdout string) {
	t.Helper()

	if stdout != "" {
		t.Errorf("stdout should be empty, got: %q", stdout)
	}
}

func assertStderrEmpty(t *testing.T, stderr string) {
	t.Helper()

	if stderr != "" {
		t.Errorf("stderr should be empty, got: %q", stderr)
	}
}

func assertStdoutContains(t *testing.T, stdout, substr string) {
	t.Helper()

	if !strings.Contains(stdout, substr) {
		t.Errorf("stdout should contain %q, got: %q", substr, stdout)
	}
}

func assertStderrContains(t *testing.T, stderr, substr string) {
	t.Helper()

	if !strings.Contains(stderr, substr) {
		t.Errorf("stderr should contain %q, got: %q", substr, stderr)
	}
}

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

// Tests for print-config command.

func TestPrintConfig_Defaults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": ".tickets"`)
}

func TestPrintConfig_FromConfigFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "my-tickets"}`)

	stdout, stderr, code := runTk(t, dir, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "my-tickets"`)
}

func TestPrintConfig_FromConfigFileWithComments(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{
		// This is a comment
		"ticket_dir": "commented-tickets",
	}`)

	stdout, stderr, code := runTk(t, dir, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "commented-tickets"`)
}

func TestPrintConfig_ExplicitConfigFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "custom.json"), `{"ticket_dir": "custom-dir"}`)

	stdout, stderr, code := runTk(t, dir, "-c", "custom.json", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "custom-dir"`)
}

func TestPrintConfig_ExplicitConfigFlagLong(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "custom.json"), `{"ticket_dir": "custom-dir"}`)

	stdout, stderr, code := runTk(t, dir, "--config=custom.json", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "custom-dir"`)
}

func TestPrintConfig_TicketDirOverride(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "from-file"}`)

	stdout, stderr, code := runTk(t, dir, "--ticket-dir=from-cli", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "from-cli"`)
}

func TestPrintConfig_TicketDirOverrideWithEquals(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "--ticket-dir=override-dir", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "override-dir"`)
}

// Tests for config errors.

func TestConfig_ExplicitConfigNotFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "-c", "nonexistent.json", "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "config file not found")
}

func TestConfig_InvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{invalid json}`)

	stdout, stderr, code := runTk(t, dir, "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "invalid")
}

func TestConfig_EmptyTicketDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": ""}`)

	stdout, stderr, code := runTk(t, dir, "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "ticket_dir cannot be empty")
}

func TestConfig_EmptyTicketDirViaCLI(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "--ticket-dir=", "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "ticket_dir cannot be empty")
}

// Tests for flag parsing errors.

func TestFlags_ConfigRequiresArgument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "-c")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "requires an argument")
}

func TestFlags_TicketDirRequiresArgument(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "--ticket-dir")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "requires an argument")
}

func TestFlags_UnknownFlag(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "--unknown-flag", "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "unknown flag")
}

// Tests for unknown command.

func TestUnknownCommand(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "not-a-command")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "unknown command")
	assertStderrContains(t, stderr, "not-a-command")
}

func TestUnknownCommand_PrintsUsage(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "badcmd")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "Usage:")
	assertStderrContains(t, stderr, "Commands:")
}

// Tests for help.

func TestHelp_CommandIsUnknown(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "help")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "unknown command")
}

func TestHelp_DashH(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "-h")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, "tk - minimal ticket system")
}

func TestHelp_DashDashHelp(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	stdout, stderr, code := runTk(t, dir, "--help")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, "tk - minimal ticket system")
}

func TestHelp_NoArgs(t *testing.T) {
	t.Parallel()

	var out, errOut bytes.Buffer

	code := Run(nil, &out, &errOut, []string{"tk"}, nil)

	assertExitCode(t, code, 0, errOut.String())
	assertStderrEmpty(t, errOut.String())
	assertStdoutContains(t, out.String(), "tk - minimal ticket system")
}

// Tests for -C flag.

func TestCFlag_ChangesWorkDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")

	err := os.MkdirAll(subdir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	writeFile(t, filepath.Join(subdir, ".tk.json"), `{"ticket_dir": "subdir-tickets"}`)

	var out, errOut bytes.Buffer

	code := Run(nil, &out, &errOut, []string{"tk", "-C", subdir, "print-config"}, nil)

	assertExitCode(t, code, 0, errOut.String())
	assertStdoutContains(t, out.String(), `"ticket_dir": "subdir-tickets"`)
}

func TestCFlag_CombinedForm(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "combined-test"}`)

	var out, errOut bytes.Buffer

	code := Run(nil, &out, &errOut, []string{"tk", "-C" + dir, "print-config"}, nil)

	assertExitCode(t, code, 0, errOut.String())
	assertStdoutContains(t, out.String(), `"ticket_dir": "combined-test"`)
}

func TestCwdFlag_Long(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "cwd-test"}`)

	var out, errOut bytes.Buffer

	code := Run(nil, &out, &errOut, []string{"tk", "--cwd", dir, "print-config"}, nil)

	assertExitCode(t, code, 0, errOut.String())
	assertStderrEmpty(t, errOut.String())
	assertStdoutContains(t, out.String(), `"ticket_dir": "cwd-test"`)
}

func TestCwdFlag_LongWithEquals(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "cwd-equals-test"}`)

	var out, errOut bytes.Buffer

	code := Run(nil, &out, &errOut, []string{"tk", "--cwd=" + dir, "print-config"}, nil)

	assertExitCode(t, code, 0, errOut.String())
	assertStderrEmpty(t, errOut.String())
	assertStdoutContains(t, out.String(), `"ticket_dir": "cwd-equals-test"`)
}

// Test precedence.

func TestConfig_Precedence_CLIOverridesFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "from-file"}`)

	stdout, stderr, code := runTk(t, dir, "--ticket-dir=from-cli", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "from-cli"`)
}

func TestConfig_Precedence_ExplicitConfigOverridesDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "from-default"}`)
	writeFile(t, filepath.Join(dir, "explicit.json"), `{"ticket_dir": "from-explicit"}`)

	stdout, stderr, code := runTk(t, dir, "-c", "explicit.json", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "from-explicit"`)
}

func TestConfig_Precedence_CLIOverridesExplicitConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "explicit.json"), `{"ticket_dir": "from-explicit"}`)

	stdout, stderr, code := runTk(t, dir, "-c", "explicit.json", "--ticket-dir=from-cli", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "from-cli"`)
}

// Tests for global config.

func runTkWithEnv(t *testing.T, dir string, env []string, args ...string) (string, string, int) {
	t.Helper()

	var out, errOut bytes.Buffer

	fullArgs := append([]string{"tk", "-C", dir}, args...)
	exitCode := Run(nil, &out, &errOut, fullArgs, env)

	return out.String(), errOut.String(), exitCode
}

func TestConfig_GlobalConfig_Loaded(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	// Create global config with editor setting
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"editor": "nvim"}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"editor": "nvim"`)
	assertStdoutContains(t, stdout, `"ticket_dir": ".tickets"`)
}

func TestConfig_GlobalConfig_MissingIsNotError(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir() // Empty, no config file

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": ".tickets"`)
}

func TestConfig_GlobalConfig_InvalidJSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{invalid json}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "invalid")
}

func TestConfig_GlobalConfig_EmptyTicketDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": ""}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "print-config")

	assertExitCode(t, code, 1, stderr)
	assertStdoutEmpty(t, stdout)
	assertStderrContains(t, stderr, "ticket_dir cannot be empty")
}

func TestConfig_Precedence_ProjectOverridesGlobal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	// Global config: sets both ticket_dir and editor
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global-tickets", "editor": "nvim"}`)

	// Project config: only sets ticket_dir
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "project-tickets"}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	// ticket_dir should come from project config
	assertStdoutContains(t, stdout, `"ticket_dir": "project-tickets"`)
	// editor should still come from global config
	assertStdoutContains(t, stdout, `"editor": "nvim"`)
}

func TestConfig_Precedence_ExplicitOverridesGlobal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	// Global config
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global-tickets"}`)

	// Explicit config
	writeFile(t, filepath.Join(dir, "explicit.json"), `{"ticket_dir": "explicit-tickets"}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "-c", "explicit.json", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "explicit-tickets"`)
}

func TestConfig_Precedence_CLIOverridesGlobal(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	// Global config
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global-tickets"}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "--ticket-dir=cli-tickets", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	assertStdoutContains(t, stdout, `"ticket_dir": "cli-tickets"`)
}

func TestConfig_Precedence_FullChain(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	// Global config: sets ticket_dir and editor
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"ticket_dir": "global", "editor": "nvim"}`)

	// Project config: only overrides ticket_dir
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "project"}`)

	// CLI overrides ticket_dir
	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "--ticket-dir=cli", "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	// CLI wins for ticket_dir
	assertStdoutContains(t, stdout, `"ticket_dir": "cli"`)
	// editor still comes from global
	assertStdoutContains(t, stdout, `"editor": "nvim"`)
}

func TestConfig_GlobalConfig_PartialMerge(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	xdgDir := t.TempDir()

	// Global config only sets editor
	writeFile(t, filepath.Join(xdgDir, "tk", "config.json"), `{"editor": "vim"}`)

	// Project config only sets ticket_dir
	writeFile(t, filepath.Join(dir, ".tk.json"), `{"ticket_dir": "custom-tickets"}`)

	env := []string{"XDG_CONFIG_HOME=" + xdgDir}
	stdout, stderr, code := runTkWithEnv(t, dir, env, "print-config")

	assertExitCode(t, code, 0, stderr)
	assertStderrEmpty(t, stderr)
	// Both values should be present
	assertStdoutContains(t, stdout, `"ticket_dir": "custom-tickets"`)
	assertStdoutContains(t, stdout, `"editor": "vim"`)
}
