package cli_test

import (
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func TestReopenCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"reopen"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"reopen", "nonexistent"},
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

func TestReopenClosedTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)

	stdout, stderr, exitCode := c.Run("reopen", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Fatalf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Reopened")

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "status: open")
}

func TestReopenRemovesClosedTimestamp(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)

	content := c.ReadTicket(ticketID)
	if !strings.Contains(content, "closed: ") {
		t.Fatal("closed timestamp should exist before reopen")
	}

	c.MustRun("reopen", ticketID)

	content = c.ReadTicket(ticketID)
	cli.AssertNotContains(t, content, "closed: ")
}

func TestReopenOpenTicketError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	stderr := c.MustFail("reopen", ticketID)
	cli.AssertContains(t, stderr, "already open")
}

func TestReopenInProgressTicketError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)

	stderr := c.MustFail("reopen", ticketID)
	cli.AssertContains(t, stderr, "not closed")
}

func TestReopenStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustFail("reopen", "nonexistent")
}

func TestReopenFullCycleShowContent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)
	c.MustRun("reopen", ticketID)

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "status: open")
	cli.AssertContains(t, content, "id: "+ticketID)
	cli.AssertContains(t, content, "# Test ticket")
	cli.AssertNotContains(t, content, "closed: ")
}

func TestReopenHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"reopen", "--help"}},
		{name: "short flag", args: []string{"reopen", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk reopen")
			cli.AssertContains(t, stdout, "open")
		})
	}
}

func TestReopenWithClosedParentFails(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start and close both
	c.MustRun("start", parentID)
	c.MustRun("start", childID)
	c.MustRun("close", childID)
	c.MustRun("close", parentID)

	// Try to reopen child while parent is closed
	stderr := c.MustFail("reopen", childID)
	cli.AssertContains(t, stderr, "parent ticket is closed")
	cli.AssertContains(t, stderr, parentID)
}

func TestReopenWithOpenParentSucceeds(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start parent and child
	c.MustRun("start", parentID)
	c.MustRun("start", childID)

	// Close child
	c.MustRun("close", childID)

	// Reopen child while parent is still in_progress
	c.MustRun("reopen", childID)

	content := c.ReadTicket(childID)
	cli.AssertContains(t, content, "status: open")
}

func TestReopenWithInProgressParentSucceeds(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start parent and child, close child
	c.MustRun("start", parentID)
	c.MustRun("start", childID)
	c.MustRun("close", childID)

	// Reopen child
	c.MustRun("reopen", childID)

	content := c.ReadTicket(childID)
	cli.AssertContains(t, content, "status: open")
}

func TestReopenWithNoParentSucceeds(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Ticket without parent")

	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)
	c.MustRun("reopen", ticketID)

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "status: open")
}

func TestReopenParentThenChild(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start and close both
	c.MustRun("start", parentID)
	c.MustRun("start", childID)
	c.MustRun("close", childID)
	c.MustRun("close", parentID)

	// Reopen parent first
	c.MustRun("reopen", parentID)

	// Now reopen child should succeed
	c.MustRun("reopen", childID)

	content := c.ReadTicket(childID)
	cli.AssertContains(t, content, "status: open")
}
