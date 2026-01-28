package store_test

import (
	"database/sql"
	"os"
	"path/filepath"
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
