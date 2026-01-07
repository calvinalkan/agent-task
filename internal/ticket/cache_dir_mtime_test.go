package ticket_test

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/ticket"
)

func createTestTicketFullMtime(t *testing.T, ticketDir, ticketID, status, title, ticketType string, priority int, blockedBy []string) {
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

	err := os.WriteFile(path, []byte(content), ticket.TestFilePerms)
	if err != nil {
		t.Fatalf("failed to create test ticket: %v", err)
	}
}

func TestListTicketsEmptyDirCreatesCache(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets failed: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 results, got %d", len(results))
	}

	_, statErr := os.Stat(filepath.Join(ticketDir, ticket.CacheFileName))
	if statErr != nil {
		t.Fatalf("expected cache file to exist: %v", statErr)
	}

	cache, err := ticket.LoadBinaryCache(ticketDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache failed: %v", err)
	}

	defer func() { _ = cache.Close() }()

	if got := cache.FilterEntries(-1, 0, -1, 0, 0); len(got) != 0 {
		t.Fatalf("expected empty cache, got %d entries", len(got))
	}
}

func backdateCacheMtime(t *testing.T, ticketDir string) {
	t.Helper()

	cachePath := filepath.Join(ticketDir, ticket.CacheFileName)
	past := time.Now().Add(-10 * time.Second)

	err := os.Chtimes(cachePath, past, past)
	if err != nil {
		t.Fatalf("failed to backdate cache: %v", err)
	}
}

func TestListTicketsDirMtimeAdditionTriggersReconcile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	createTestTicketFullMtime(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, nil)

	// Cold build.
	_, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (cold) failed: %v", err)
	}

	// Backdate cache so directory changes are detected.
	backdateCacheMtime(t, ticketDir)

	// External add.
	createTestTicketFullMtime(t, ticketDir, "b-002", ticket.StatusOpen, "B", "bug", 1, nil)

	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (reconcile) failed: %v", err)
	}

	ids := make(map[string]bool)

	for _, r := range results {
		if r.Err != nil {
			continue
		}

		ids[r.Summary.ID] = true
	}

	if !ids["a-001"] || !ids["b-002"] {
		t.Fatalf("expected both tickets after reconcile, got: %+v", ids)
	}
}

func TestListTicketsDirMtimeDeletionTriggersReconcile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	createTestTicketFullMtime(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, nil)
	createTestTicketFullMtime(t, ticketDir, "b-002", ticket.StatusOpen, "B", "task", 2, nil)

	_, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (cold) failed: %v", err)
	}

	// Backdate cache so directory changes are detected.
	backdateCacheMtime(t, ticketDir)

	removeErr := os.Remove(filepath.Join(ticketDir, "a-001.md"))
	if removeErr != nil {
		t.Fatal(removeErr)
	}

	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (reconcile) failed: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result after deletion, got %d", len(results))
	}

	if results[0].Err != nil {
		t.Fatalf("unexpected parse error: %v", results[0].Err)
	}

	if got := results[0].Summary.ID; got != "b-002" {
		t.Fatalf("expected remaining ticket b-002, got %q", got)
	}
}

func TestListTicketsDoesNotReparseWhenDirUnchanged(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	createTestTicketFullMtime(t, ticketDir, "a-001", ticket.StatusOpen, "Original", "task", 2, nil)

	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (cold) failed: %v", err)
	}

	if got := results[0].Summary.Title; got != "Original" {
		t.Fatalf("expected cached title Original, got %q", got)
	}

	// External content edit (dir mtime should not change).
	path := filepath.Join(ticketDir, "a-001.md")

	edited := strings.ReplaceAll(string(mustReadFileMtime(t, path)), "# Original\n", "# Modified\n")

	writeErr := os.WriteFile(path, []byte(edited), ticket.TestFilePerms)
	if writeErr != nil {
		t.Fatal(writeErr)
	}

	results, err = ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (warm) failed: %v", err)
	}

	if got := results[0].Summary.Title; got != "Original" {
		t.Fatalf("expected cached title Original, got %q", got)
	}
}

func TestListTicketsReconcileDoesNotReparseExisting(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	createTestTicketFullMtime(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, nil)

	_, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (cold) failed: %v", err)
	}

	// Backdate cache so directory changes are detected.
	backdateCacheMtime(t, ticketDir)

	// Corrupt existing file without touching directory mtime.
	corruptErr := os.WriteFile(filepath.Join(ticketDir, "a-001.md"), []byte("invalid file"), ticket.TestFilePerms)
	if corruptErr != nil {
		t.Fatal(corruptErr)
	}

	// External add triggers reconcile; existing entry should come from cache without re-parse.
	createTestTicketFullMtime(t, ticketDir, "b-002", ticket.StatusOpen, "B", "task", 2, nil)

	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (reconcile) failed: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results after reconcile, got %d", len(results))
	}

	for _, r := range results {
		if r.Err != nil {
			t.Fatalf("unexpected parse error during reconcile: %v", r.Err)
		}
	}
}

func TestListTicketsCorruptCacheRebuildsAndWarns(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	createTestTicketFullMtime(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, nil)

	_, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, io.Discard)
	if err != nil {
		t.Fatalf("ListTickets (cold) failed: %v", err)
	}

	// Corrupt cache file.
	corruptErr := os.WriteFile(filepath.Join(ticketDir, ticket.CacheFileName), []byte("corrupt"), ticket.TestFilePerms)
	if corruptErr != nil {
		t.Fatal(corruptErr)
	}

	var diag bytes.Buffer

	results, err := ticket.ListTickets(ticketDir, ticket.ListTicketsOptions{Limit: 0}, &diag)
	if err != nil {
		t.Fatalf("ListTickets (rebuild) failed: %v", err)
	}

	if !strings.Contains(diag.String(), "loading cache: invalid format, rebuilding") {
		t.Fatalf("expected rebuild warning, got: %q", diag.String())
	}

	if len(results) != 1 || results[0].Err != nil || results[0].Summary.ID != "a-001" {
		t.Fatalf("unexpected results after rebuild: %+v", results)
	}

	// Cache should be valid again.
	cache, err := ticket.LoadBinaryCache(ticketDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache after rebuild failed: %v", err)
	}

	_ = cache.Close()
}

func TestCacheMtimeIsNotOlderThanDirMtimeAfterWrite(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	summary := ticket.Summary{
		SchemaVersion: 1,
		ID:            "a-001",
		Status:        ticket.StatusOpen,
		Created:       "2026-01-04T12:00:00Z",
		Type:          "task",
		Priority:      2,
		Assignee:      "Test User",
		Title:         "A",
		Path:          filepath.Join(ticketDir, "a-001.md"),
	}

	data, err := ticket.TestEncodeSummaryData(&summary)
	if err != nil {
		t.Fatalf("encodeSummaryData failed: %v", err)
	}

	cachePath := filepath.Join(ticketDir, ticket.CacheFileName)

	entries := map[string]ticket.TestRawCacheEntry{
		"a-001.md": {
			Filename:   "a-001.md",
			Mtime:      time.Now().UnixNano(),
			Status:     ticket.TestStatusByteOpen,
			Priority:   2,
			TicketType: ticket.TestTypeByteTask,
			Data:       data,
		},
	}

	writeErr := ticket.ExportWriteBinaryCacheRaw(cachePath, entries)
	if writeErr != nil {
		t.Fatalf("writeBinaryCacheRaw failed: %v", writeErr)
	}

	needReconcile, err := ticket.TestDirMtimeNewerThanCache(ticketDir, cachePath)
	if err != nil {
		t.Fatalf("dirMtimeNewerThanCache failed: %v", err)
	}

	if needReconcile {
		t.Fatal("expected cache mtime to be >= directory mtime after write")
	}
}

func mustReadFileMtime(t *testing.T, path string) []byte {
	t.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestCreateTestTicketFullMtimeWithClosedStatus(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	createTestTicketFullMtime(t, tmpDir, "closed-001", ticket.StatusClosed, "Closed", "task", 2, nil)

	content, err := os.ReadFile(filepath.Join(tmpDir, "closed-001.md"))
	if err != nil {
		t.Fatalf("failed to read ticket: %v", err)
	}

	if !strings.Contains(string(content), "status: closed") {
		t.Error("expected status: closed")
	}
}

func TestCreateTestTicketFullMtimeWithBlockers(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	createTestTicketFullMtime(t, tmpDir, "blocked-001", ticket.StatusOpen, "Blocked", "task", 2, []string{"blocker-1", "blocker-2"})

	content, err := os.ReadFile(filepath.Join(tmpDir, "blocked-001.md"))
	if err != nil {
		t.Fatalf("failed to read ticket: %v", err)
	}

	if !strings.Contains(string(content), "blocked-by: [blocker-1, blocker-2]") {
		t.Error("expected blocked-by list")
	}
}
