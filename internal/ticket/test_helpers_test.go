package ticket_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"tk/internal/ticket"
)

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

func TestCreateTestTicketFullWithClosedStatus(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	createTestTicketFull(t, tmpDir, "closed-001", ticket.StatusClosed, "Closed Ticket", "task", 2, nil)

	content, err := os.ReadFile(filepath.Join(tmpDir, "closed-001.md"))
	if err != nil {
		t.Fatalf("failed to read ticket: %v", err)
	}

	if !strings.Contains(string(content), "status: closed") {
		t.Error("expected status: closed in ticket content")
	}

	if !strings.Contains(string(content), "closed: 20") {
		t.Error("expected closed timestamp in ticket content")
	}
}
