package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/cli"
)

func TestReadyEmptyDir(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("ready")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := stdout, ""; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}

	_ = stderr
}

func TestReadyOpenTicket(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	stdout := c.MustRun("ready")

	cli.AssertContains(t, stdout, ticketID)
	cli.AssertContains(t, stdout, "[open]")
}

func TestReadyInProgressExcluded(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)

	stdout := c.MustRun("ready")

	// in_progress tickets should NOT be in ready list
	cli.AssertNotContains(t, stdout, ticketID)
}

func TestReadyClosedExcluded(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")
	c.MustRun("start", ticketID)
	c.MustRun("close", ticketID)

	stdout := c.MustRun("ready")

	cli.AssertNotContains(t, stdout, ticketID)
}

func TestReadyBlockedByOpen(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	blockerID := c.MustRun("create", "Blocker")
	blockedID := c.MustRun("create", "Blocked")
	c.MustRun("block", blockedID, blockerID)

	stdout := c.MustRun("ready")

	// Blocker should be ready
	cli.AssertContains(t, stdout, blockerID)
	// Blocked ticket should not be ready
	cli.AssertNotContains(t, stdout, blockedID)
}

func TestReadyBlockedByInProgress(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	blockerID := c.MustRun("create", "Blocker")
	blockedID := c.MustRun("create", "Blocked")
	c.MustRun("start", blockerID)
	c.MustRun("block", blockedID, blockerID)

	stdout := c.MustRun("ready")

	// Blocked ticket should not be ready
	cli.AssertNotContains(t, stdout, blockedID)
}

func TestReadyBlockedByClosed(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	blockerID := c.MustRun("create", "Blocker")
	blockedID := c.MustRun("create", "Blocked")
	c.MustRun("block", blockedID, blockerID)
	c.MustRun("start", blockerID)
	c.MustRun("close", blockerID)

	stdout := c.MustRun("ready")

	// Blocked ticket should now be ready
	cli.AssertContains(t, stdout, blockedID)
	// Closed blocker should not appear (check for ID followed by space to avoid prefix match)
	cli.AssertNotContains(t, stdout, blockerID+" ")
}

func TestReadyBlockedByNonExistent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	// Manually add a non-existent blocker
	path := filepath.Join(c.TicketDir(), ticketID+".md")

	contentBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	content := string(contentBytes)
	newContent := strings.Replace(content, "blocked-by: []", "blocked-by: [nonexistent123]", 1)

	err = os.WriteFile(path, []byte(newContent), 0o600)
	if err != nil {
		t.Fatalf("failed to write ticket: %v", err)
	}

	stdout, stderr, exitCode := c.Run("ready")

	// External frontmatter edits do not invalidate the cache; ready trusts cache.
	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	// Ticket should still be ready
	cli.AssertContains(t, stdout, ticketID)

	if got, want := stderr, ""; got != want {
		t.Errorf("stderr=%q, want=%q", got, want)
	}
}

func TestReadySortByPriority(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create tickets with different priorities (in wrong order)
	p3ID := c.MustRun("create", "-p", "3", "P3 ticket")
	p1ID := c.MustRun("create", "-p", "1", "P1 ticket")
	p2ID := c.MustRun("create", "-p", "2", "P2 ticket")

	stdout := c.MustRun("ready")
	lines := strings.Split(stdout, "\n")

	if got, want := len(lines), 3; got != want {
		t.Fatalf("got %d lines, want %d", got, want)
	}

	// Check order: P1, P2, P3
	cli.AssertContains(t, lines[0], p1ID)
	cli.AssertContains(t, lines[1], p2ID)
	cli.AssertContains(t, lines[2], p3ID)

	// Verify priority labels
	cli.AssertContains(t, lines[0], "[P1]")
	cli.AssertContains(t, lines[1], "[P2]")
	cli.AssertContains(t, lines[2], "[P3]")
}

func TestReadySamePrioritySortByID(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create tickets with same priority
	id1 := c.MustRun("create", "-p", "2", "First ticket")
	id2 := c.MustRun("create", "-p", "2", "Second ticket")

	stdout := c.MustRun("ready")
	lines := strings.Split(stdout, "\n")

	// First created (earlier ID) should come first
	if len(lines) >= 2 {
		cli.AssertContains(t, lines[0], id1)
		cli.AssertContains(t, lines[1], id2)
	}
}

func TestReadyOutputFormat(t *testing.T) {
	t.Parallel()

	t.Run("output shows priority and status and title", func(t *testing.T) {
		t.Parallel()

		c := cli.NewCLI(t)
		ticketID := c.MustRun("create", "-p", "1", "My important task")

		stdout := c.MustRun("ready")

		cli.AssertContains(t, stdout, ticketID)
		cli.AssertContains(t, stdout, "[P1]")
		cli.AssertContains(t, stdout, "[open]")
		cli.AssertContains(t, stdout, "My important task")
	})
}

func TestReadyHelp(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
	}{
		{name: "long flag", args: []string{"ready", "--help"}},
		{name: "short flag", args: []string{"ready", "-h"}},
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

			cli.AssertContains(t, stdout, "Usage: tk ready")
		})
	}
}

func TestReadyInMainHelp(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout := c.MustRun("--help")
	cli.AssertContains(t, stdout, "ready")
}

func TestReadyActiveTicketCoverage(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create tickets with various states
	openID := c.MustRun("create", "Open ticket")
	blockerID := c.MustRun("create", "Blocker")
	blockedID := c.MustRun("create", "Blocked ticket")
	closedID := c.MustRun("create", "To be closed")

	// Block one ticket
	c.MustRun("block", blockedID, blockerID)

	// Close one ticket
	c.MustRun("start", closedID)
	c.MustRun("close", closedID)

	// Get ready list
	readyOutput := c.MustRun("ready")

	// Get all active (non-closed) tickets
	lsOutput := c.MustRun("ls")

	// Verify invariants:
	cli.AssertContains(t, readyOutput, openID)
	cli.AssertContains(t, readyOutput, blockerID)
	cli.AssertNotContains(t, readyOutput, blockedID)
	cli.AssertNotContains(t, readyOutput, closedID)
	cli.AssertContains(t, lsOutput, blockedID)
}

func TestReadyIdempotent(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "-p", "1", "P1 ticket")
	c.MustRun("create", "-p", "2", "P2 ticket")

	// Run ready twice
	stdout1 := c.MustRun("ready")
	stdout2 := c.MustRun("ready")

	if got, want := stdout1, stdout2; got != want {
		t.Errorf("output differs between runs:\nfirst: %q\nsecond: %q", got, want)
	}
}
