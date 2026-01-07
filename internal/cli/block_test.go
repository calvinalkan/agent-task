package cli_test

import (
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func TestBlockCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"block"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "missing blocker ID returns error",
			args:       []string{"block", "someid"},
			wantExit:   1,
			wantStderr: "blocker ID is required",
		},
		{
			name:       "nonexistent ticket returns error",
			args:       []string{"block", "nonexistent", "blocker"},
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

func TestBlockNonexistentBlocker(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	stderr := c.MustFail("block", ticketID, "nonexistent")
	cli.AssertContains(t, stderr, "ticket not found")
}

func TestBlockSelf(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	stderr := c.MustFail("block", ticketID, ticketID)
	cli.AssertContains(t, stderr, "cannot block itself")
}

func TestBlockTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")

	stdout, stderr, exitCode := c.Run("block", ticketID1, ticketID2)

	if got, want := exitCode, 0; got != want {
		t.Fatalf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Blocked")
	cli.AssertContains(t, stdout, ticketID1)
	cli.AssertContains(t, stdout, ticketID2)

	content := c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: ["+ticketID2+"]")
}

func TestBlockAlreadyBlocked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")

	c.MustRun("block", ticketID1, ticketID2)

	stderr := c.MustFail("block", ticketID1, ticketID2)
	cli.AssertContains(t, stderr, "already blocked by")
}

func TestBlockMultipleBlockers(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")
	ticketID3 := c.MustRun("create", "Ticket 3")

	c.MustRun("block", ticketID1, ticketID2)
	c.MustRun("block", ticketID1, ticketID3)

	content := c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: ["+ticketID2+", "+ticketID3+"]")
}

func TestBlockHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"block", "--help"}},
		{name: "short flag", args: []string{"block", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk block")
		})
	}
}
