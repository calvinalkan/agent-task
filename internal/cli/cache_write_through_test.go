// Tests for cache write-through functionality.
package cli_test

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"tk/internal/cli"
	"tk/internal/ticket"
)

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

	path := filepath.Join(ticketDir, ticketID+".md")

	err := os.WriteFile(path, []byte(content), 0o600)
	if err != nil {
		t.Fatalf("failed to create test ticket: %v", err)
	}
}

func TestWriteThroughCacheCreateStartCloseReopen(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	var createOut bytes.Buffer

	exitCode := cli.Run(nil, &createOut, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "create", "Test ticket"}, nil)
	if exitCode != 0 {
		t.Fatal("create failed")
	}

	ticketID := strings.TrimSpace(createOut.String())

	summary := loadCachedSummary(t, ticketDir, ticketID+".md")
	if summary.ID != ticketID {
		t.Fatalf("cache ID = %q, want %q", summary.ID, ticketID)
	}

	if summary.Status != ticket.StatusOpen {
		t.Fatalf("cache status = %q, want %q", summary.Status, ticket.StatusOpen)
	}

	// Start updates cache.
	exitCode = cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "start", ticketID}, nil)
	if exitCode != 0 {
		t.Fatal("start failed")
	}

	summary = loadCachedSummary(t, ticketDir, ticketID+".md")
	if summary.Status != ticket.StatusInProgress {
		t.Fatalf("cache status after start = %q, want %q", summary.Status, ticket.StatusInProgress)
	}

	// Close updates cache (adds closed timestamp).
	exitCode = cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "close", ticketID}, nil)
	if exitCode != 0 {
		t.Fatal("close failed")
	}

	summary = loadCachedSummary(t, ticketDir, ticketID+".md")
	if summary.Status != ticket.StatusClosed {
		t.Fatalf("cache status after close = %q, want %q", summary.Status, ticket.StatusClosed)
	}

	if summary.Closed == "" {
		t.Fatal("expected closed timestamp in cache after close")
	}

	// Reopen updates cache (clears closed timestamp).
	exitCode = cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "reopen", ticketID}, nil)
	if exitCode != 0 {
		t.Fatal("reopen failed")
	}

	summary = loadCachedSummary(t, ticketDir, ticketID+".md")
	if summary.Status != ticket.StatusOpen {
		t.Fatalf("cache status after reopen = %q, want %q", summary.Status, ticket.StatusOpen)
	}

	if summary.Closed != "" {
		t.Fatalf("expected closed timestamp cleared in cache after reopen, got %q", summary.Closed)
	}
}

func TestWriteThroughCacheBlockUnblock(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	// Create two tickets.
	var outA, outB bytes.Buffer
	cli.Run(nil, &outA, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "create", "Ticket A"}, nil)
	cli.Run(nil, &outB, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "create", "Ticket B"}, nil)

	ticketA := strings.TrimSpace(outA.String())
	ticketB := strings.TrimSpace(outB.String())

	// Block A by B.
	exitCode := cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "block", ticketA, ticketB}, nil)
	if exitCode != 0 {
		t.Fatal("block failed")
	}

	summary := loadCachedSummary(t, ticketDir, ticketA+".md")
	if len(summary.BlockedBy) != 1 || summary.BlockedBy[0] != ticketB {
		t.Fatalf("cache blocked-by after block = %+v, want [%s]", summary.BlockedBy, ticketB)
	}

	// Unblock A from B.
	exitCode = cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "unblock", ticketA, ticketB}, nil)
	if exitCode != 0 {
		t.Fatal("unblock failed")
	}

	summary = loadCachedSummary(t, ticketDir, ticketA+".md")
	if len(summary.BlockedBy) != 0 {
		t.Fatalf("cache blocked-by after unblock = %+v, want empty", summary.BlockedBy)
	}
}

func TestWriteThroughCacheRepairUpdatesEntry(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	ensureErr := ensureDir(ticketDir)
	if ensureErr != nil {
		t.Fatal(ensureErr)
	}

	// Create a ticket with a stale blocker (file exists, blocker doesn't).
	createTestTicketFull(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, []string{"missing-123"})

	// Build cache once.
	_, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}

	// Repair should remove missing blocker and update cache.
	exitCode := cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "repair", "a-001"}, nil)
	if exitCode != 0 {
		t.Fatal("repair failed")
	}

	summary := loadCachedSummary(t, ticketDir, "a-001.md")
	if len(summary.BlockedBy) != 0 {
		t.Fatalf("cache blocked-by after repair = %+v, want empty", summary.BlockedBy)
	}
}

func TestConcurrentWritesDoNotCorruptCache(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	var out1, out2 bytes.Buffer
	cli.Run(nil, &out1, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "create", "Ticket 1"}, nil)
	cli.Run(nil, &out2, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "create", "Ticket 2"}, nil)

	id1 := strings.TrimSpace(out1.String())
	id2 := strings.TrimSpace(out2.String())

	cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "start", id1}, nil)
	cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "start", id2}, nil)

	var waitGroup sync.WaitGroup
	waitGroup.Add(2)

	go func() {
		defer waitGroup.Done()

		cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "close", id1}, nil)
	}()

	go func() {
		defer waitGroup.Done()

		cli.Run(nil, &bytes.Buffer{}, &bytes.Buffer{}, []string{"tk", "-C", tmpDir, "close", id2}, nil)
	}()

	waitGroup.Wait()

	// Cache must be loadable and contain both updates.
	s1 := loadCachedSummary(t, ticketDir, id1+".md")
	s2 := loadCachedSummary(t, ticketDir, id2+".md")

	if s1.Status != ticket.StatusClosed || s2.Status != ticket.StatusClosed {
		t.Fatalf("expected both tickets closed in cache, got %q and %q", s1.Status, s2.Status)
	}
}

func ensureDir(path string) error {
	err := os.MkdirAll(path, 0o750)
	if err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}

	return nil
}
