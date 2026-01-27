package cli_test

import (
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func Test_Unblock_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"unblock"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "missing blocker ID returns error",
			args:       []string{"unblock", "someid"},
			wantExit:   1,
			wantStderr: "ticket not found",
		},
		{
			name:       "nonexistent ticket returns error",
			args:       []string{"unblock", "nonexistent", "blocker"},
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

func Test_Unblock_Not_Blocked_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")

	stderr := c.MustFail("unblock", ticketID1, ticketID2)
	cli.AssertContains(t, stderr, "not blocked by")
}

func Test_Unblock_Returns_Error_When_Blocker_ID_Missing_For_Existing_Ticket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Ticket 1")

	stderr := c.MustFail("unblock", ticketID)
	cli.AssertContains(t, stderr, "blocker ID is required")
}

func Test_Unblock_Ticket_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")

	c.MustRun("block", ticketID1, ticketID2)

	content := c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: ["+ticketID2+"]")

	stdout, stderr, exitCode := c.Run("unblock", ticketID1, ticketID2)

	if got, want := exitCode, 0; got != want {
		t.Fatalf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Unblocked")
	cli.AssertContains(t, stdout, ticketID1)
	cli.AssertContains(t, stdout, ticketID2)

	content = c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: []")
}

func Test_Unblock_Only_Removes_Specific_Blocker_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")
	ticketID3 := c.MustRun("create", "Ticket 3")

	c.MustRun("block", ticketID1, ticketID2)
	c.MustRun("block", ticketID1, ticketID3)

	c.MustRun("unblock", ticketID1, ticketID2)

	content := c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: ["+ticketID3+"]")
}

func Test_Block_Then_Unblock_Returns_To_Original_State_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")

	originalContent := c.ReadTicket(ticketID1)
	cli.AssertContains(t, originalContent, "blocked-by: []")

	c.MustRun("block", ticketID1, ticketID2)
	c.MustRun("unblock", ticketID1, ticketID2)

	finalContent := c.ReadTicket(ticketID1)
	cli.AssertContains(t, finalContent, "blocked-by: []")
}

func Test_Unblock_Help_When_Invoked(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"unblock", "--help"}},
		{name: "short flag", args: []string{"unblock", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk unblock")
		})
	}
}

func Test_Create_Block_Unblock_Workflow_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Feature ticket")
	ticketID2 := c.MustRun("create", "Blocker ticket")

	content := c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: []")

	c.MustRun("block", ticketID1, ticketID2)

	content = c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: ["+ticketID2+"]")

	c.MustRun("unblock", ticketID1, ticketID2)

	content = c.ReadTicket(ticketID1)
	cli.AssertContains(t, content, "blocked-by: []")
}
