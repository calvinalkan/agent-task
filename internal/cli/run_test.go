package cli_test

import (
	"bytes"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func Test_Invalid_Global_Flag_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("--invalid-flag", "ls")

	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := stdout, ""; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}

	// Should show error message
	cli.AssertContains(t, stderr, "unknown flag")
	cli.AssertContains(t, stderr, "--invalid-flag")

	// Should show valid global options
	cli.AssertContains(t, stderr, "Global flags:")
	cli.AssertContains(t, stderr, "--help")
	cli.AssertContains(t, stderr, "--cwd")
	cli.AssertContains(t, stderr, "--config")
	cli.AssertContains(t, stderr, "--ticket-dir")
}

func Test_Empty_Ticket_Dir_Flag_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("--ticket-dir=", "ls")

	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := stdout, ""; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}

	// Should show error message
	cli.AssertContains(t, stderr, "ticket-dir cannot be empty")

	// Should show valid global options
	cli.AssertContains(t, stderr, "Global flags:")
	cli.AssertContains(t, stderr, "--help")
	cli.AssertContains(t, stderr, "--cwd")
	cli.AssertContains(t, stderr, "--config")
	cli.AssertContains(t, stderr, "--ticket-dir")
}

func Test_Bare_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	// Call Run directly without test helper (which adds --cwd)
	var stdout, stderr bytes.Buffer

	exitCode := cli.Run(nil, &stdout, &stderr, []string{"tk"}, nil, nil)

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := stderr.String(), ""; got != want {
		t.Errorf("stderr=%q, want=%q", got, want)
	}

	cli.AssertContains(t, stdout.String(), "tk - minimal ticket system")
	cli.AssertContains(t, stdout.String(), "--cwd")
	cli.AssertContains(t, stdout.String(), "create <title>")
}

func Test_Main_Help_When_Invoked(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"--help"}},
		{name: "short flag", args: []string{"-h"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			stdout, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, 0; got != want {
				t.Errorf("exitCode=%d, want=%d", got, want)
			}

			if got, want := stderr, ""; got != want {
				t.Errorf("stderr=%q, want=%q", got, want)
			}

			cli.AssertContains(t, stdout, "tk - minimal ticket system")
			cli.AssertContains(t, stdout, "--cwd")
			cli.AssertContains(t, stdout, "create <title>")
			cli.AssertContains(t, stdout, "start <id>")
		})
	}
}

func Test_No_Command_With_Flags_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("--cwd", c.Dir)

	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := stdout, ""; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}

	// Should show error message
	cli.AssertContains(t, stderr, "no command provided")

	// Should still show full usage to help user
	cli.AssertContains(t, stderr, "tk - minimal ticket system")
	cli.AssertContains(t, stderr, "Commands:")
	cli.AssertContains(t, stderr, "create <title>")
}

func Test_Invalid_Command_Flag_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("create", "--invalid-flag")

	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	// Should show command usage on stdout
	cli.AssertContains(t, stdout, "Usage: tk create <title>")
	cli.AssertContains(t, stdout, "Flags:")

	// Should show error message on stderr
	cli.AssertContains(t, stderr, "error:")
	cli.AssertContains(t, stderr, "unknown flag")
	cli.AssertContains(t, stderr, "--invalid-flag")
}
