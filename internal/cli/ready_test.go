package cli_test

import (
	"encoding/json"
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

	cli.AssertContains(t, stderr, "no tickets ready for pickup")
}

func TestReadyJSONEmpty(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("ready", "--json")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := strings.TrimSpace(stdout), "[]"; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}

	// No stderr message in JSON mode
	if got, want := stderr, ""; got != want {
		t.Errorf("stderr=%q, want=%q", got, want)
	}
}

func TestReadyJSONWithTickets(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "-p", "1", "Test ticket")

	stdout, _, exitCode := c.Run("ready", "--json")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	var tickets []map[string]any

	err := json.Unmarshal([]byte(stdout), &tickets)
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if got, want := len(tickets), 1; got != want {
		t.Fatalf("got %d tickets, want %d", got, want)
	}

	ticket := tickets[0]

	if got, want := ticket["id"], ticketID; got != want {
		t.Errorf("id=%v, want=%v", got, want)
	}

	if priority, ok := ticket["priority"].(float64); !ok {
		t.Errorf("priority should be float64, got %T", ticket["priority"])
	} else if priority != float64(1) {
		t.Errorf("priority=%v, want=%v", priority, float64(1))
	}

	if got, want := ticket["status"], "open"; got != want {
		t.Errorf("status=%v, want=%v", got, want)
	}

	if got, want := ticket["title"], "Test ticket"; got != want {
		t.Errorf("title=%v, want=%v", got, want)
	}

	if got, want := ticket["type"], "task"; got != want {
		t.Errorf("type=%v, want=%v", got, want)
	}

	// blocked_by should be empty array, not null
	blockedBy, ok := ticket["blocked_by"].([]any)
	if !ok {
		t.Errorf("blocked_by should be array, got %T", ticket["blocked_by"])
	} else if len(blockedBy) != 0 {
		t.Errorf("blocked_by should be empty, got %v", blockedBy)
	}
}

func TestReadyLimit(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "-p", "1", "P1 ticket")
	c.MustRun("create", "-p", "2", "P2 ticket")
	c.MustRun("create", "-p", "3", "P3 ticket")

	// Without limit, should show all 3
	stdout := c.MustRun("ready")

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if got, want := len(lines), 3; got != want {
		t.Errorf("without limit: got %d lines, want %d", got, want)
	}

	// With limit 1, should show only P1
	stdout = c.MustRun("ready", "--limit", "1")

	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	if got, want := len(lines), 1; got != want {
		t.Errorf("with limit 1: got %d lines, want %d", got, want)
	}

	cli.AssertContains(t, lines[0], "[P1]")

	// With limit 2, should show P1 and P2
	stdout = c.MustRun("ready", "--limit", "2")

	lines = strings.Split(strings.TrimSpace(stdout), "\n")
	if got, want := len(lines), 2; got != want {
		t.Errorf("with limit 2: got %d lines, want %d", got, want)
	}
}

func TestReadyLimitJSON(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "-p", "1", "P1 ticket")
	c.MustRun("create", "-p", "2", "P2 ticket")

	stdout := c.MustRun("ready", "--json", "--limit", "1")

	var tickets []map[string]any

	err := json.Unmarshal([]byte(stdout), &tickets)
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if got, want := len(tickets), 1; got != want {
		t.Fatalf("got %d tickets, want %d", got, want)
	}

	if priority, ok := tickets[0]["priority"].(float64); !ok {
		t.Errorf("priority should be float64, got %T", tickets[0]["priority"])
	} else if priority != float64(1) {
		t.Errorf("priority=%v, want=%v (should be highest priority)", priority, float64(1))
	}
}

func TestReadyFieldID(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id1 := c.MustRun("create", "-p", "1", "P1 ticket")
	id2 := c.MustRun("create", "-p", "2", "P2 ticket")

	stdout := c.MustRun("ready", "--field", "id")
	lines := strings.Split(strings.TrimSpace(stdout), "\n")

	if got, want := len(lines), 2; got != want {
		t.Fatalf("got %d lines, want %d", got, want)
	}

	// Should be sorted by priority
	if got, want := lines[0], id1; got != want {
		t.Errorf("line[0]=%q, want=%q", got, want)
	}

	if got, want := lines[1], id2; got != want {
		t.Errorf("line[1]=%q, want=%q", got, want)
	}
}

func TestReadyFieldWithLimit(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id1 := c.MustRun("create", "-p", "1", "P1 ticket")
	c.MustRun("create", "-p", "2", "P2 ticket")

	stdout := c.MustRun("ready", "--field", "id", "--limit", "1")

	if got, want := strings.TrimSpace(stdout), id1; got != want {
		t.Errorf("stdout=%q, want=%q", got, want)
	}
}

func TestReadyFieldJSON(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	id1 := c.MustRun("create", "-p", "1", "P1 ticket")
	id2 := c.MustRun("create", "-p", "2", "P2 ticket")

	stdout := c.MustRun("ready", "--json", "--field", "id")

	var ids []string

	err := json.Unmarshal([]byte(stdout), &ids)
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if got, want := len(ids), 2; got != want {
		t.Fatalf("got %d ids, want %d", got, want)
	}

	if got, want := ids[0], id1; got != want {
		t.Errorf("ids[0]=%q, want=%q", got, want)
	}

	if got, want := ids[1], id2; got != want {
		t.Errorf("ids[1]=%q, want=%q", got, want)
	}
}

func TestReadyFieldPriorityJSON(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "-p", "1", "P1 ticket")
	c.MustRun("create", "-p", "2", "P2 ticket")

	stdout := c.MustRun("ready", "--json", "--field", "priority")

	var priorities []int

	err := json.Unmarshal([]byte(stdout), &priorities)
	if err != nil {
		t.Fatalf("failed to parse JSON: %v", err)
	}

	if got, want := priorities[0], 1; got != want {
		t.Errorf("priorities[0]=%d, want=%d", got, want)
	}

	if got, want := priorities[1], 2; got != want {
		t.Errorf("priorities[1]=%d, want=%d", got, want)
	}
}

func TestReadyFieldInvalid(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "Test ticket")

	_, stderr, exitCode := c.Run("ready", "--field", "invalid")

	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	cli.AssertContains(t, stderr, "invalid field")
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

	// External frontmatter edits should surface warnings and exit 1.
	if got, want := exitCode, 1; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	// Ticket should not be ready.
	cli.AssertNotContains(t, stdout, ticketID)
	cli.AssertContains(t, stderr, "blocked by non-existent ticket")
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

func TestReadyChildWithOpenParentExcluded(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	stdout := c.MustRun("ready")

	// Parent should be ready
	cli.AssertTicketListed(t, stdout, parentID)
	// Child should NOT be ready (parent is open)
	cli.AssertTicketNotListed(t, stdout, childID)
}

func TestReadyChildWithInProgressParentIncluded(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start parent
	c.MustRun("start", parentID)

	stdout := c.MustRun("ready")

	// Parent should NOT be ready (in_progress)
	cli.AssertTicketNotListed(t, stdout, parentID)
	// Child should now be ready
	cli.AssertTicketListed(t, stdout, childID)
}

func TestReadyShowsParentInOutput(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Start parent so child is ready
	c.MustRun("start", parentID)

	stdout := c.MustRun("ready")

	cli.AssertContains(t, stdout, childID)
	cli.AssertContains(t, stdout, "(parent: "+parentID+")")
}

func TestReadyMultipleLevelsOfParents(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	grandparentID := c.MustRun("create", "Grandparent")
	parentID := c.MustRun("create", "Parent", "--parent", grandparentID)
	childID := c.MustRun("create", "Child", "--parent", parentID)

	// Initially only grandparent is ready
	stdout := c.MustRun("ready")
	cli.AssertTicketListed(t, stdout, grandparentID)
	cli.AssertTicketNotListed(t, stdout, parentID)
	cli.AssertTicketNotListed(t, stdout, childID)

	// Start grandparent - now parent is ready
	c.MustRun("start", grandparentID)
	stdout = c.MustRun("ready")
	cli.AssertTicketNotListed(t, stdout, grandparentID)
	cli.AssertTicketListed(t, stdout, parentID)
	cli.AssertTicketNotListed(t, stdout, childID)

	// Start parent - now child is ready
	c.MustRun("start", parentID)
	stdout = c.MustRun("ready")
	cli.AssertTicketNotListed(t, stdout, grandparentID)
	cli.AssertTicketNotListed(t, stdout, parentID)
	cli.AssertTicketListed(t, stdout, childID)
}

func TestReadyNoParentTicketIsReady(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Standalone ticket")

	stdout := c.MustRun("ready")

	cli.AssertContains(t, stdout, ticketID)
	cli.AssertNotContains(t, stdout, "(parent:")
}

func TestReadyCacheRegenAfterVersionChange(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	// Create parent and child
	parentID := c.MustRun("create", "Parent ticket")
	childID := c.MustRun("create", "Child ticket", "--parent", parentID)

	// Build cache
	c.MustRun("ready")

	// Delete cache
	cachePath := filepath.Join(c.TicketDir(), ".cache")

	err := os.Remove(cachePath)
	if err != nil {
		t.Fatalf("failed to remove cache: %v", err)
	}

	// Ready should still work correctly after cache regen
	stdout := c.MustRun("ready")

	// Parent should be ready, child should not (parent is open)
	cli.AssertTicketListed(t, stdout, parentID)
	cli.AssertTicketNotListed(t, stdout, childID)
}

func TestReadyParentCheckAfterCacheRegen(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)

	parentID := c.MustRun("create", "Parent")
	childID := c.MustRun("create", "Child", "--parent", parentID)

	// Start parent
	c.MustRun("start", parentID)

	// Delete cache to force regen
	cachePath := filepath.Join(c.TicketDir(), ".cache")
	_ = os.Remove(cachePath)

	// Parent is in_progress; child should now be ready.
	stdout := c.MustRun("ready")
	cli.AssertTicketNotListed(t, stdout, parentID) // in_progress
	cli.AssertTicketListed(t, stdout, childID)
}
