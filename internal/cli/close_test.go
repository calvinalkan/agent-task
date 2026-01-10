package cli_test

import (
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func TestCloseCommand(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID returns error",
			args:       []string{"close"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ID returns error",
			args:       []string{"close", "nonexistent"},
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

func TestCloseInProgressTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)

	stdout, stderr, exitCode := c.Run("close", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Fatalf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Closed")

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "status: closed")
}

func TestCloseAddsTimestamp(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "closed: ")

	// Verify it's in ISO 8601 format (contains T and Z)
	if !strings.Contains(content, "closed: 20") || !strings.Contains(content, "T") {
		t.Errorf("closed timestamp not in expected format, content=%q", content)
	}
}

func TestCloseOpenTicketError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	stderr := c.MustFail("close", ticketID)
	cli.AssertContains(t, stderr, "must be started first")
}

func TestCloseAlreadyClosedTicketError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	content := c.ReadTicket(ticketID)
	content = strings.Replace(content, "status: open", "status: closed", 1)
	c.WriteTicket(ticketID, content)

	stderr := c.MustFail("close", ticketID)
	cli.AssertContains(t, stderr, "already closed")
}

func TestCloseStdoutEmptyOnError(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustFail("close", "nonexistent")
}

func TestCloseHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"close", "--help"}},
		{name: "short flag", args: []string{"close", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk close")
			cli.AssertContains(t, stdout, "closed")
		})
	}
}

func TestCloseWithOpenChildFails(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start parent
	c.MustRun("start", parentID)

	// Try to close parent while child is open
	stderr := c.MustFail("close", parentID)
	cli.AssertContains(t, stderr, "ticket has open children")
	cli.AssertContains(t, stderr, childID)
}

func TestCloseWithInProgressChildFails(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start parent and child
	c.MustRun("start", parentID)
	c.MustRun("start", childID)

	// Try to close parent while child is in_progress
	stderr := c.MustFail("close", parentID)
	cli.AssertContains(t, stderr, "ticket has open children")
	cli.AssertContains(t, stderr, childID)
}

func TestCloseWithClosedChildSucceeds(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start and close both
	c.MustRun("start", parentID)
	c.MustRun("start", childID)
	c.MustRun("close", childID)
	c.MustRun("close", parentID)

	// Verify parent is closed
	content := c.ReadTicket(parentID)
	cli.AssertContains(t, content, "status: closed")
}

func TestCloseWithMultipleChildrenOneOpen(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	child1ID := c.MustRun("create", "Child 1", "--parent", parentID)
	child2ID := c.MustRun("create", "Child 2", "--parent", parentID)

	// Start parent and both children
	c.MustRun("start", parentID)
	c.MustRun("start", child1ID)
	c.MustRun("start", child2ID)

	// Close only child1
	c.MustRun("close", child1ID)

	// Try to close parent - should fail because child2 is still open
	stderr := c.MustFail("close", parentID)
	cli.AssertContains(t, stderr, "ticket has open children")
	cli.AssertContains(t, stderr, child2ID)
}

func TestCloseWithNoChildrenSucceeds(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Ticket without children")

	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)

	content := c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "status: closed")
}
