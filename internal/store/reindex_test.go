package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver for integration tests

	"github.com/calvinalkan/agent-task/internal/store"
)

func Test_Rebuild_Builds_SQLite_Index_When_Tickets_Are_Valid(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	ticketA, _ := store.NewTicket("Alpha Task", "task", "open", 2)
	ticketB, _ := store.NewTicket("Beta Bug", "bug", "closed", 3)
	ticketB.ClosedAt = store.TimePtr(time.Now().UTC())
	ticketB.BlockedBy = uuid.UUIDs{ticketA.ID}

	writeTicketFile(t, ticketDir, ticketA)
	writeTicketFile(t, ticketDir, ticketB)

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if indexed != 2 {
		t.Fatalf("indexed = %d, want 2", indexed)
	}

	db := openIndex(t, ticketDir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	const wantSchemaVersion = 1
	if version != wantSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, wantSchemaVersion)
	}

	if count := countTickets(t, db); count != 2 {
		t.Fatalf("ticket count = %d, want 2", count)
	}

	blockers := readBlockers(t, db)
	if len(blockers) != 1 || blockers[0].ticketID != ticketB.ID.String() || blockers[0].blockerID != ticketA.ID.String() {
		t.Fatalf("blockers = %+v, want [%s -> %s]", blockers, ticketB.ID, ticketA.ID)
	}
}

// Contract: rebuild replays a committed WAL before rebuilding the index.
func Test_Rebuild_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	// Initialize store first (creates schema)
	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket := newTestTicket(t, "Rebuild WAL")

	// Write WAL after store is open
	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		makeWalPutRecord(ticket),
	})

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if indexed != 1 {
		t.Fatalf("indexed = %d, want 1", indexed)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	// Verify file was written
	absPath := filepath.Join(ticketDir, ticket.Path)

	_, statErr := os.Stat(absPath)
	if statErr != nil {
		t.Fatalf("ticket file not found: %v", statErr)
	}

	db := openIndex(t, ticketDir)
	t.Cleanup(func() { _ = db.Close() })

	if count := countTickets(t, db); count != 1 {
		t.Fatalf("ticket count = %d, want 1", count)
	}
}

func Test_Rebuild_Skips_Orphaned_Tickets_When_Path_Mismatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Create valid ticket (indexed)
	validTicket := putTicket(t.Context(), t, s, newTestTicket(t, "Valid Ticket"))

	// Close store, add orphan file at wrong path, reopen
	err = s.Close()
	if err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Write a ticket file at wrong path (bypassing store index)
	orphanRel := filepath.Join("2026", "01-21", "orphan.md")
	orphanTicket := newTestTicket(t, "Orphan Ticket")
	writeRawPath(t, ticketDir, orphanRel, makeTicketContent(t, orphanTicket))

	s, err = store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err == nil {
		t.Fatal("expected rebuild error for orphan")
	}

	if !errors.Is(err, store.ErrIndexScan) {
		t.Fatalf("error = %v, want ErrIndexScan", err)
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	scanErr := requireIndexScanError(t, err)
	assertIssuePaths(t, scanErr, []string{orphanRel})

	// Verify original index is preserved (valid ticket still indexed)
	rows, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 || rows[0].ID != validTicket.ID {
		t.Fatalf("rows = %d, want 1 with valid ticket", len(rows))
	}
}

func Test_Rebuild_Returns_Context_Error_When_Canceled(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	indexed, err := s.Reindex(ctx)
	if err == nil {
		t.Fatal("expected rebuild error for canceled context")
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func Test_Rebuild_Indexes_Valid_Tickets_When_Other_Files_Invalid(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	// Create valid ticket (indexed)
	validTicket := putTicket(t.Context(), t, s, newTestTicket(t, "Valid"))

	// Close store to add invalid files bypassing index
	err = s.Close()
	if err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Write invalid ticket files directly (bypassing store index)
	badSchemaTicket := newTestTicket(t, "Wrong Schema")
	badSchemaContent := makeTicketContent(t, badSchemaTicket)
	badSchemaContent = strings.Replace(badSchemaContent, "schema_version: 1\n", "schema_version: 2\n", 1)
	writeRawPath(t, ticketDir, badSchemaTicket.Path, badSchemaContent)

	missingTitleTicket := newTestTicket(t, "Missing Title")
	missingTitleContent := makeTicketContent(t, missingTitleTicket)
	missingTitleContent = strings.Replace(missingTitleContent, "title: Missing Title\n", "", 1)
	writeRawPath(t, ticketDir, missingTitleTicket.Path, missingTitleContent)

	s, err = store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err == nil {
		t.Fatal("expected rebuild error for invalid tickets")
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	if !errors.Is(err, store.ErrIndexScan) {
		t.Fatalf("error = %v, want ErrIndexScan", err)
	}

	scanErr := requireIndexScanError(t, err)
	assertIssuePaths(t, scanErr, []string{
		badSchemaTicket.Path,
		missingTitleTicket.Path,
	})

	// Verify original index is preserved (valid ticket still indexed)
	rows, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 || rows[0].ID != validTicket.ID {
		t.Fatalf("rows = %d, want 1 with valid ticket", len(rows))
	}
}

func Test_Rebuild_Skips_Tk_Directory_Files_When_Markdown_Present(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	internalPath := filepath.Join(".tk", "ignored.md")
	writeRawPath(t, ticketDir, internalPath, "---\nnot: valid\n")

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	db := openIndex(t, ticketDir)

	defer func() { _ = db.Close() }()

	count := countTickets(t, db)
	if count != 0 {
		t.Fatalf("ticket count = %d, want 0", count)
	}
}

type blockerRow struct {
	ticketID  string
	blockerID string
}

func readBlockers(t *testing.T, db *sql.DB) []blockerRow {
	t.Helper()

	rows, err := db.Query("SELECT ticket_id, blocker_id FROM ticket_blockers ORDER BY ticket_id, blocker_id")
	if err != nil {
		t.Fatalf("query blockers: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var results []blockerRow

	for rows.Next() {
		var row blockerRow

		scanErr := rows.Scan(&row.ticketID, &row.blockerID)
		if scanErr != nil {
			t.Fatalf("scan blocker: %v", scanErr)
		}

		results = append(results, row)
	}

	err = rows.Err()
	if err != nil {
		t.Fatalf("rows error: %v", err)
	}

	return results
}

func requireIndexScanError(t *testing.T, err error) *store.IndexScanError {
	t.Helper()

	var scanErr *store.IndexScanError
	if !errors.As(err, &scanErr) {
		t.Fatalf("error = %v, want IndexScanError", err)
	}

	if scanErr.Total != len(scanErr.Issues) {
		t.Fatalf("scan error total = %d, issues = %d", scanErr.Total, len(scanErr.Issues))
	}

	return scanErr
}

func assertIssuePaths(t *testing.T, scanErr *store.IndexScanError, paths []string) {
	t.Helper()

	seen := make(map[string]struct{}, len(scanErr.Issues))
	for _, issue := range scanErr.Issues {
		seen[issue.Path] = struct{}{}
	}

	for _, path := range paths {
		if _, ok := seen[path]; !ok {
			t.Fatalf("missing issue for %s", path)
		}
	}
}

// makeTicketContent creates valid ticket file content as a string.
// Used for writing ticket files without going through store.
func makeTicketContent(t *testing.T, ticket *store.Ticket) string {
	t.Helper()

	return fmt.Sprintf(`---
id: %s
schema_version: 1
created: %s
priority: %d
status: %s
title: %s
type: %s
---
`,
		ticket.ID,
		ticket.CreatedAt.Format(time.RFC3339),
		ticket.Priority,
		ticket.Status,
		ticket.Title,
		ticket.Type,
	)
}

func writeRawPath(t *testing.T, root, relPath, contents string) {
	t.Helper()

	absPath := filepath.Join(root, relPath)

	err := os.MkdirAll(filepath.Dir(absPath), 0o750)
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	err = os.WriteFile(absPath, []byte(contents), 0o644)
	if err != nil {
		t.Fatalf("write file: %v", err)
	}
}
