package cli_test

import (
	"strings"
	"testing"

	"tk/internal/cli"
)

func TestStartCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"start"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"start", "nonexistent"},
			wantExit:   1,
			wantStderr: "ticket not found",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := cli.NewCLI(t)
			_, stderr, exitCode := c.Run(tt.args...)

			if got, want := exitCode, tt.wantExit; got != want {
				t.Errorf("exitCode=%d, want=%d", got, want)
			}

			if tt.wantStderr != "" {
				if got, want := stderr, tt.wantStderr; !strings.Contains(got, want) {
					t.Errorf("stderr=%q, want to contain %q", got, want)
				}
			}
		})
	}
}

func TestStartOpenTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	stdout, stderr, exitCode := c.Run("start", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Fatalf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Started")
	cli.AssertContains(t, stdout, "status: in_progress")
	cli.AssertContains(t, stdout, "Test ticket")

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "status: in_progress")
}

func TestStartAlreadyInProgressTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)

	stderr := c.MustFail("start", ticketID)
	cli.AssertContains(t, stderr, "ticket is not open")
	cli.AssertContains(t, stderr, "in_progress")
}

func TestStartClosedTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	content := c.ReadTicket(ticketID)
	content = strings.Replace(content, "status: open", "status: closed", 1)
	c.WriteTicket(ticketID, content)

	stderr := c.MustFail("start", ticketID)
	cli.AssertContains(t, stderr, "ticket is not open")
	cli.AssertContains(t, stderr, "closed")
}

func TestStartStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustFail("start", "nonexistent")
}

func TestStartHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"start", "--help"}},
		{name: "short flag", args: []string{"start", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk start")
			cli.AssertContains(t, stdout, "in_progress")
		})
	}
}
