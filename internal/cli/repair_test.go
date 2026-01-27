package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/cli"

	"github.com/calvinalkan/agent-task/internal/ticket"
)

func Test_Repair_Command_When_Invoked(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		args       []string
		wantExit   int
		wantStderr string
	}{
		{
			name:       "missing ID and no --all returns error",
			args:       []string{"repair"},
			wantExit:   1,
			wantStderr: "ticket ID is required",
		},
		{
			name:       "nonexistent ticket returns error",
			args:       []string{"repair", "nonexistent"},
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

func Test_Repair_Stale_Blocker_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	// Manually add a stale blocker to the ticket file
	content := c.ReadTicket(ticketID)
	newContent := strings.Replace(content, "blocked-by: []", "blocked-by: [nonexistent]", 1)
	c.WriteTicket(ticketID, newContent)

	stdout, stderr, exitCode := c.Run("repair", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Removed stale blocker: nonexistent")
	cli.AssertContains(t, stdout, "Repaired "+ticketID)

	content = c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "blocked-by: []")
}

func Test_Repair_Dry_Run_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	content := c.ReadTicket(ticketID)
	newContent := strings.Replace(content, "blocked-by: []", "blocked-by: [stale123]", 1)
	c.WriteTicket(ticketID, newContent)

	stdout, stderr, exitCode := c.Run("repair", "--dry-run", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Would remove stale blocker: stale123")

	// Verify the blocker was NOT removed (dry-run)
	content = c.ReadTicket(ticketID)
	cli.AssertContains(t, content, "blocked-by: [stale123]")
}

func Test_Repair_All_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID1 := c.MustRun("create", "Ticket 1")
	ticketID2 := c.MustRun("create", "Ticket 2")
	blockerID1 := c.MustRun("create", "Blocker 1")
	blockerID2 := c.MustRun("create", "Blocker 2")

	c.MustRun("block", ticketID1, blockerID1)
	c.MustRun("block", ticketID2, blockerID2)

	// Backdate cache so directory changes are detected
	backdateCacheRepair(t, c.TicketDir())

	// External deletion changes directory mtime and triggers reconcile
	_ = os.Remove(filepath.Join(c.TicketDir(), blockerID1+".md"))
	_ = os.Remove(filepath.Join(c.TicketDir(), blockerID2+".md"))

	stdout, stderr, exitCode := c.Run("repair", "--all")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Removed stale blocker: "+blockerID1)
	cli.AssertContains(t, stdout, "Removed stale blocker: "+blockerID2)
	cli.AssertContains(t, stdout, "Repaired "+ticketID1)
	cli.AssertContains(t, stdout, "Repaired "+ticketID2)
}

func Test_Repair_Nothing_To_Repair_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Clean ticket")

	stdout, stderr, exitCode := c.Run("repair", ticketID)

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Nothing to repair")
}

func Test_Repair_All_Nothing_To_Repair_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	c.MustRun("create", "Clean ticket")

	stdout, stderr, exitCode := c.Run("repair", "--all")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Nothing to repair")
}

func Test_Repair_Help_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, stderr, exitCode := c.Run("repair", "--help")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	if got, want := stderr, ""; got != want {
		t.Errorf("stderr=%q, want=%q", got, want)
	}

	cli.AssertContains(t, stdout, "Usage: tk repair")
	cli.AssertContains(t, stdout, "--dry-run")
	cli.AssertContains(t, stdout, "--all")
}

func Test_Repair_Idempotent_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	content := c.ReadTicket(ticketID)
	newContent := strings.Replace(content, "blocked-by: []", "blocked-by: [stale]", 1)
	c.WriteTicket(ticketID, newContent)

	// First repair
	stdout1, stderr1, exitCode := c.Run("repair", ticketID)
	if got, want := exitCode, 0; got != want {
		t.Errorf("first repair: exitCode=%d, want=%d, stderr=%s", got, want, stderr1)
	}

	_ = stdout1

	// Second repair
	stdout2, stderr2, exitCode := c.Run("repair", ticketID)
	if got, want := exitCode, 0; got != want {
		t.Errorf("second repair: exitCode=%d, want=%d, stderr=%s", got, want, stderr2)
	}

	cli.AssertContains(t, stdout2, "Nothing to repair")
}

func Test_Repair_With_Valid_Blocker_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	blockerID := c.MustRun("create", "Blocker ticket")
	blockedID := c.MustRun("create", "Blocked ticket")

	c.MustRun("block", blockedID, blockerID)

	stdout, stderr, exitCode := c.Run("repair", blockedID)

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d, stderr=%s", got, want, stderr)
	}

	cli.AssertContains(t, stdout, "Nothing to repair")

	content := c.ReadTicket(blockedID)
	cli.AssertContains(t, content, "blocked-by: ["+blockerID+"]")
}

func Test_Repair_Main_Help_Shows_Repair_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	stdout, _, exitCode := c.Run("--help")

	if got, want := exitCode, 0; got != want {
		t.Errorf("exitCode=%d, want=%d", got, want)
	}

	cli.AssertContains(t, stdout, "repair")
}

func backdateCacheRepair(t *testing.T, ticketDir string) {
	t.Helper()

	cachePath := filepath.Join(ticketDir, ticket.CacheFileName)
	past := time.Now().Add(-10 * time.Second)

	err := os.Chtimes(cachePath, past, past)
	if err != nil {
		t.Fatalf("failed to backdate cache: %v", err)
	}
}
