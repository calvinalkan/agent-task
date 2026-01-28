package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"

	"github.com/calvinalkan/agent-task/internal/store"
)

func Test_Open_Creates_Schema_When_Directory_Empty(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	tkDir := filepath.Join(ticketDir, ".tk")

	info, err := os.Stat(tkDir)
	if err != nil {
		t.Fatalf("stat .tk: %v", err)
	}

	if !info.IsDir() {
		t.Fatal(".tk is not a directory")
	}

	indexPath := filepath.Join(tkDir, "index.sqlite")

	_, err = os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat index.sqlite: %v", err)
	}

	walPath := filepath.Join(tkDir, "wal")

	_, err = os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	db := openIndex(t, ticketDir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}

	hasTickets := tableExists(t, db, "tickets")
	if !hasTickets {
		t.Fatal("tickets table missing")
	}

	hasBlockers := tableExists(t, db, "ticket_blockers")
	if !hasBlockers {
		t.Fatal("ticket_blockers table missing")
	}
}

func Test_Open_Rebuilds_Index_When_Schema_Version_Mismatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	id, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	createdAt := time.Date(2026, 1, 28, 10, 0, 0, 0, time.UTC)

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Test Ticket",
	})

	// First open creates schema
	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	_ = s.Close()

	// Manually set wrong schema version
	db := openIndex(t, ticketDir)

	_, err = db.Exec("PRAGMA user_version = 999")
	if err != nil {
		_ = db.Close()

		t.Fatalf("set user_version: %v", err)
	}

	_ = db.Close()

	// Reopen should rebuild
	s, err = store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}

	defer func() { _ = s.Close() }()

	rows, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	if rows[0].ID != id.String() {
		t.Fatalf("id = %s, want %s", rows[0].ID, id.String())
	}

	db = openIndex(t, ticketDir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	if version != 1 {
		t.Fatalf("user_version = %d, want 1", version)
	}
}

func Test_Close_Returns_Nil_When_Called_Multiple_Times(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	err = s.Close()
	if err != nil {
		t.Fatalf("first close: %v", err)
	}

	err = s.Close()
	if err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func Test_Close_Returns_Nil_When_Store_Is_Nil(t *testing.T) {
	t.Parallel()

	var s *store.Store

	err := s.Close()
	if err != nil {
		t.Fatalf("close nil store: %v", err)
	}
}

func Test_Get_Returns_Ticket_When_File_Exists(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	createdAt := time.Date(2026, 1, 28, 10, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0x123, 0x456789ABCDEF0123)

	fixture := &ticketFixture{
		ID:          id.String(),
		Status:      "open",
		Type:        "task",
		Priority:    2,
		CreatedAt:   createdAt,
		Title:       "Test Ticket",
		Assignee:    "alice",
		ExternalRef: "GH-123",
	}

	relPath := writeTicket(t, ticketDir, fixture)

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket, err := s.Get(t.Context(), id.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if ticket.ID != id.String() {
		t.Fatalf("id = %s, want %s", ticket.ID, id.String())
	}

	if ticket.Status != "open" {
		t.Fatalf("status = %s, want open", ticket.Status)
	}

	if ticket.Title != "Test Ticket" {
		t.Fatalf("title = %s, want Test Ticket", ticket.Title)
	}

	if ticket.Path != relPath {
		t.Fatalf("path = %s, want %s", ticket.Path, relPath)
	}

	if ticket.Assignee != "alice" {
		t.Fatalf("assignee = %s, want alice", ticket.Assignee)
	}

	if ticket.ExternalRef != "GH-123" {
		t.Fatalf("external_ref = %s, want GH-123", ticket.ExternalRef)
	}
}

func Test_Get_Returns_Error_When_File_Missing(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	createdAt := time.Date(2026, 1, 28, 10, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xABC, 0xDEF0123456789ABC)

	_, err = s.Get(t.Context(), id.String())
	if err == nil {
		t.Fatal("expected error for missing ticket")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want contains 'not found'", err)
	}
}

func Test_Get_Returns_Error_When_File_Contains_Different_ID(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	createdAt := time.Date(2026, 1, 28, 10, 0, 0, 0, time.UTC)

	// Create ticket with one ID
	actualID := makeUUIDv7(t, createdAt, 0x111, 0x222333444555666)
	fixture := &ticketFixture{
		ID:        actualID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Test Ticket",
	}

	// Write to canonical path for actualID
	writeTicket(t, ticketDir, fixture)

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Request with a different ID - file at wrongID's path has actualID in frontmatter
	wrongID := makeUUIDv7(t, createdAt, 0x999, 0x888777666555444)

	// Write a file at wrongID's path but with actualID in frontmatter
	wrongPath, err := store.PathFromID(wrongID)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	writeTicketAtPath(t, ticketDir, wrongPath, fixture) // fixture still has actualID

	_, err = s.Get(t.Context(), wrongID.String())
	if err == nil {
		t.Fatal("expected error for ID mismatch")
	}

	// ticketFromFrontmatter returns path mismatch error
	if !strings.Contains(err.Error(), "validate path") {
		t.Fatalf("error = %v, want contains 'validate path'", err)
	}
}

func Test_Get_Returns_Error_When_ID_Is_Empty(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	_, err = s.Get(t.Context(), "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want contains 'empty'", err)
	}
}

func Test_Get_Returns_Error_When_UUID_Is_Invalid(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	_, err = s.Get(t.Context(), "not-a-uuid")
	if err == nil {
		t.Fatal("expected error for invalid UUID")
	}

	if !strings.Contains(err.Error(), "invalid UUID") {
		t.Fatalf("error = %v, want contains 'invalid UUID'", err)
	}
}

func Test_Get_Returns_Error_When_UUID_Is_Not_V7(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// UUIDv4 instead of v7
	_, err = s.Get(t.Context(), "550e8400-e29b-41d4-a716-446655440000")
	if err == nil {
		t.Fatal("expected error for non-UUIDv7")
	}

	if !strings.Contains(err.Error(), "version") {
		t.Fatalf("error = %v, want contains 'version'", err)
	}
}

// Contract: Get recovers committed WAL that appears after Open.
// This simulates a crash mid-commit where WAL was written but store wasn't cleanly closed.
func Test_Get_Recovers_WAL_When_WAL_Appears_After_Open(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	// Open store - WAL is empty at this point
	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	createdAt := time.Date(2026, 1, 28, 10, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0x555, 0x666777888999AAA)

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "WAL Ticket",
	}

	relPath, err := store.PathFromID(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	fm := walFrontmatterFromTicket(fixture)
	content := "# WAL Ticket\n\nBody\n"

	// Write committed WAL while store is open (simulates crash mid-commit)
	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          id.String(),
			Path:        relPath,
			Frontmatter: frontmatterToAny(t, fm),
			Content:     content,
		},
	})

	// Get should detect WAL, recover it, then return the ticket
	ticket, err := s.Get(t.Context(), id.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if ticket.ID != id.String() {
		t.Fatalf("id = %s, want %s", ticket.ID, id.String())
	}

	if ticket.Title != "WAL Ticket" {
		t.Fatalf("title = %s, want WAL Ticket", ticket.Title)
	}

	// Verify WAL was truncated after recovery
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0 (should be truncated after recovery)", info.Size())
	}
}

func tableExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()

	row := db.QueryRow(`
		SELECT COUNT(*)
		FROM sqlite_master
		WHERE type = 'table' AND name = ?`, name)

	var count int

	err := row.Scan(&count)
	if err != nil {
		t.Fatalf("check table %s: %v", name, err)
	}

	return count > 0
}
