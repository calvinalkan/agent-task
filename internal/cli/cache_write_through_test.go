package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/cli"
	"github.com/calvinalkan/agent-task/internal/ticket"
)

func Test_Write_Through_Cache_Create_Start_Close_Reopen_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketID := c.MustRun("create", "Test ticket")

	summary := loadCachedSummary(t, c.TicketDir(), ticketID+".md")
	if got, want := summary.ID, ticketID; got != want {
		t.Fatalf("cache ID=%q, want=%q", got, want)
	}

	if got, want := summary.Status, ticket.StatusOpen; got != want {
		t.Fatalf("cache status=%q, want=%q", got, want)
	}

	c.MustRun("start", ticketID)

	summary = loadCachedSummary(t, c.TicketDir(), ticketID+".md")
	if got, want := summary.Status, ticket.StatusInProgress; got != want {
		t.Fatalf("cache status after start=%q, want=%q", got, want)
	}

	c.MustRun("close", ticketID)

	summary = loadCachedSummary(t, c.TicketDir(), ticketID+".md")
	if got, want := summary.Status, ticket.StatusClosed; got != want {
		t.Fatalf("cache status after close=%q, want=%q", got, want)
	}

	if summary.Closed == "" {
		t.Fatal("expected closed timestamp in cache after close")
	}

	c.MustRun("reopen", ticketID)

	summary = loadCachedSummary(t, c.TicketDir(), ticketID+".md")
	if got, want := summary.Status, ticket.StatusOpen; got != want {
		t.Fatalf("cache status after reopen=%q, want=%q", got, want)
	}

	if summary.Closed != "" {
		t.Fatalf("expected closed timestamp cleared in cache after reopen, got=%q", summary.Closed)
	}
}

func Test_Write_Through_Cache_Block_Unblock_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketA := c.MustRun("create", "Ticket A")
	ticketB := c.MustRun("create", "Ticket B")

	c.MustRun("block", ticketA, ticketB)

	summary := loadCachedSummary(t, c.TicketDir(), ticketA+".md")
	if len(summary.BlockedBy) != 1 || summary.BlockedBy[0] != ticketB {
		t.Fatalf("cache blocked-by after block=%+v, want=[%s]", summary.BlockedBy, ticketB)
	}

	c.MustRun("unblock", ticketA, ticketB)

	summary = loadCachedSummary(t, c.TicketDir(), ticketA+".md")
	if len(summary.BlockedBy) != 0 {
		t.Fatalf("cache blocked-by after unblock=%+v, want=empty", summary.BlockedBy)
	}
}

func Test_Write_Through_Cache_Repair_Updates_Entry_When_Invoked(t *testing.T) {
	t.Parallel()

	c := cli.NewCLI(t)
	ticketDir := c.TicketDir()

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatal(err)
	}

	createTestTicketFull(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, []string{"missing-123"})

	// Build cache once
	_, err = ticket.ListTickets(ticketDir, &ticket.ListTicketsOptions{Limit: 0}, nil)
	if err != nil {
		t.Fatal(err)
	}

	c.MustRun("repair", "a-001")

	summary := loadCachedSummary(t, ticketDir, "a-001.md")
	if len(summary.BlockedBy) != 0 {
		t.Fatalf("cache blocked-by after repair=%+v, want=empty", summary.BlockedBy)
	}
}

func Test_Concurrent_Writes_Do_Not_Corrupt_Cache_When_Invoked(t *testing.T) {
	t.Parallel()

	th := cli.NewCLI(t)
	id1 := th.MustRun("create", "Ticket 1")
	id2 := th.MustRun("create", "Ticket 2")

	th.MustRun("start", id1)
	th.MustRun("start", id2)

	var wg sync.WaitGroup

	wg.Go(func() {
		th.Run("close", id1)
	})

	wg.Go(func() {
		th.Run("close", id2)
	})

	wg.Wait()

	s1 := loadCachedSummary(t, th.TicketDir(), id1+".md")
	s2 := loadCachedSummary(t, th.TicketDir(), id2+".md")

	if got, want := s1.Status, ticket.StatusClosed; got != want {
		t.Fatalf("ticket1 status=%q, want=%q", got, want)
	}

	if got, want := s2.Status, ticket.StatusClosed; got != want {
		t.Fatalf("ticket2 status=%q, want=%q", got, want)
	}
}

func loadCachedSummary(t *testing.T, ticketDir, filename string) ticket.Summary {
	t.Helper()

	cache, err := ticket.LoadBinaryCache(ticketDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache failed: %v", err)
	}

	defer func() { _ = cache.Close() }()

	entry := cache.Lookup(filename)
	if entry == nil {
		t.Fatalf("cache lookup returned nil for %s", filename)
	}

	return entry.Summary
}

func createTestTicketFull(t *testing.T, ticketDir, ticketID, status, title, ticketType string, priority int, blockedBy []string) {
	t.Helper()

	blockedByStr := "[]"
	if len(blockedBy) > 0 {
		blockedByStr = "[" + strings.Join(blockedBy, ", ") + "]"
	}

	closedLine := ""
	if status == ticket.StatusClosed {
		closedLine = "closed: " + time.Now().UTC().Format(time.RFC3339) + "\n"
	}

	content := "---\n" +
		"schema_version: 1\n" +
		"id: " + ticketID + "\n" +
		"status: " + status + "\n" +
		"blocked-by: " + blockedByStr + "\n" +
		"created: 2026-01-04T00:00:00Z\n" +
		"type: " + ticketType + "\n" +
		"priority: " + string(rune('0'+priority)) + "\n" +
		closedLine +
		"---\n" +
		"# " + title + "\n"

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("failed to create ticket dir: %v", err)
	}

	path := filepath.Join(ticketDir, ticketID+".md")

	err = os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("failed to create test ticket: %v", err)
	}
}
