package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	ticket := newTestTicket(t, "Test Ticket")
	writeTicketFile(t, ticketDir, ticket)

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

	if rows[0].ID != ticket.ID {
		t.Fatalf("id = %s, want %s", rows[0].ID, ticket.ID)
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

	ticket, _ := store.NewTicket("Test Ticket", "task", "open", 2)
	ticket.Assignee = store.StringPtr("alice")
	ticket.ExternalRef = store.StringPtr("GH-123")

	writeTicketFile(t, ticketDir, ticket)

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	got, err := s.Get(t.Context(), ticket.ID.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID != ticket.ID {
		t.Fatalf("id = %s, want %s", got.ID, ticket.ID)
	}

	if got.Status != "open" {
		t.Fatalf("status = %s, want open", got.Status)
	}

	if got.Title != "Test Ticket" {
		t.Fatalf("title = %s, want Test Ticket", got.Title)
	}

	if got.Path != ticket.Path {
		t.Fatalf("path = %s, want %s", got.Path, ticket.Path)
	}

	if got.Assignee == nil || *got.Assignee != "alice" {
		t.Fatalf("assignee = %v, want alice", got.Assignee)
	}

	if got.ExternalRef == nil || *got.ExternalRef != "GH-123" {
		t.Fatalf("external_ref = %v, want GH-123", got.ExternalRef)
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

	ticket := newTestTicket(t, "Missing")

	_, err = s.Get(t.Context(), ticket.ID.String())
	if err == nil {
		t.Fatal("expected error for missing ticket")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want contains 'not found'", err)
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

	if !strings.Contains(err.Error(), "not UUIDv7") {
		t.Fatalf("error = %v, want contains 'not UUIDv7'", err)
	}
}

// Contract: Get recovers committed WAL that appears after Open.
func Test_Get_Recovers_WAL_When_WAL_Appears_After_Open(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	// Open store - WAL is empty at this point
	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket := newTestTicket(t, "WAL Ticket")

	// Write committed WAL while store is open (simulates crash mid-commit)
	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		makeWalPutRecord(ticket),
	})

	// Get should detect WAL, recover it, then return the ticket
	got, err := s.Get(t.Context(), ticket.ID.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID != ticket.ID {
		t.Fatalf("id = %s, want %s", got.ID, ticket.ID)
	}

	if got.Title != "WAL Ticket" {
		t.Fatalf("title = %s, want WAL Ticket", got.Title)
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
