package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/store"
)

// Contract: committed WAL replays to files and updates SQLite before truncation.
func Test_Open_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	initStore, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	_ = initStore.Close()

	ticket := newTestTicket(t, "WAL Ticket")
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	writeWalFile(t, walPath, []walRecord{
		makeWalPutRecord(ticket),
	})

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	if rows[0].ID != ticket.ID {
		t.Fatalf("id = %s, want %s", rows[0].ID, ticket.ID)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, ticket.Path)

	_, statErr := os.Stat(absPath)
	if statErr != nil {
		t.Fatalf("ticket file missing at %s: %v", absPath, statErr)
	}
}

// Contract: committed WAL applies put/delete operations to filesystem and index.
func Test_Open_Replays_WAL_Put_And_Delete_When_Committed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	// Create a ticket to delete - write it to disk first
	toDelete := newTestTicket(t, "To Delete")
	writeTicketFile(t, ticketDir, toDelete)

	// Create a ticket to put via WAL
	toPut := newTestTicket(t, "To Put")

	initStore, openErr := store.Open(t.Context(), ticketDir)
	if openErr != nil {
		t.Fatalf("init store: %v", openErr)
	}

	_ = initStore.Close()

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		makeWalDeleteRecord(toDelete.ID, toDelete.Path),
		makeWalPutRecord(toPut),
	})

	storeHandle, openErr := store.Open(t.Context(), ticketDir)
	if openErr != nil {
		t.Fatalf("open store: %v", openErr)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	// Deleted ticket should not exist
	deleteAbsPath := filepath.Join(ticketDir, toDelete.Path)

	_, deleteStatErr := os.Stat(deleteAbsPath)
	if !os.IsNotExist(deleteStatErr) {
		t.Fatalf("deleted ticket should not exist: %v", deleteStatErr)
	}

	// Put ticket should exist
	putAbsPath := filepath.Join(ticketDir, toPut.Path)

	_, putStatErr := os.Stat(putAbsPath)
	if putStatErr != nil {
		t.Fatalf("put ticket missing: %v", putStatErr)
	}

	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	if rows[0].ID != toPut.ID {
		t.Fatalf("id = %s, want %s", rows[0].ID, toPut.ID)
	}
}

// Contract: uncommitted WAL (missing footer) is ignored and WAL is truncated.
func Test_Open_Ignores_Uncommitted_WAL_When_Footer_Missing(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	// Create a valid ticket on disk first
	existing := newTestTicket(t, "Existing Ticket")
	writeTicketFile(t, ticketDir, existing)

	initStore, openErr := store.Open(t.Context(), ticketDir)
	if openErr != nil {
		t.Fatalf("init store: %v", openErr)
	}

	_ = initStore.Close()

	// Write WAL without footer (uncommitted)
	uncommitted := newTestTicket(t, "Uncommitted")
	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalBodyOnly(t, walPath, []walRecord{
		makeWalPutRecord(uncommitted),
	})

	storeHandle, openErr := store.Open(t.Context(), ticketDir)
	if openErr != nil {
		t.Fatalf("open store: %v", openErr)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	// Uncommitted ticket should not exist
	absPath := filepath.Join(ticketDir, uncommitted.Path)

	_, statErr := os.Stat(absPath)
	if !os.IsNotExist(statErr) {
		t.Fatalf("uncommitted ticket should not exist: %v", statErr)
	}

	// WAL should be truncated
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	// Only the existing ticket should be in index
	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	if rows[0].ID != existing.ID {
		t.Fatalf("id = %s, want %s", rows[0].ID, existing.ID)
	}
}

// Contract: WAL replay validates paths match ticket IDs.
func Test_Open_Returns_Error_When_WAL_Path_Mismatches_ID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	initStore, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	_ = initStore.Close()

	ticket := newTestTicket(t, "Wrong Path")
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	// Write WAL with wrong path
	writeWalFile(t, walPath, []walRecord{
		makeWalPutRecordWithPath(ticket, "wrong/path.md"),
	})

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected error for path mismatch")
	}

	if !errors.Is(err, store.ErrWALReplay) {
		t.Fatalf("error = %v, want ErrWALReplay", err)
	}
}

// Contract: WAL with invalid JSON body returns ErrWALReplay.
func Test_Open_Returns_ErrWALReplay_When_Body_Invalid_JSON(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	initStore, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	_ = initStore.Close()

	// Write WAL with invalid JSON body (valid footer but JSON won't parse)
	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalWithBody(t, walPath, []byte("corrupted content that won't parse"))

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected error for invalid WAL body")
	}

	if !errors.Is(err, store.ErrWALReplay) {
		t.Fatalf("error = %v, want ErrWALReplay", err)
	}
}

// Contract: WAL with invalid footer checksum returns ErrWALCorrupt.
func Test_Open_Returns_ErrWALCorrupt_When_Footer_CRC_Mismatches(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	// Create existing ticket
	existing := newTestTicket(t, "Existing")
	writeTicketFile(t, ticketDir, existing)

	initStore, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("init store: %v", err)
	}

	_ = initStore.Close()

	ticket := newTestTicket(t, "Invalid Footer")
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	// Write valid WAL first
	writeWalFile(t, walPath, []walRecord{
		makeWalPutRecord(ticket),
	})

	// Corrupt the file by modifying body without updating CRC
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}

	// Modify a byte in the body (before footer)
	if len(data) > 40 {
		data[10] ^= 0xFF
	}

	err = os.WriteFile(walPath, data, 0o600)
	if err != nil {
		t.Fatalf("write corrupted wal: %v", err)
	}

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected error for CRC mismatch")
	}

	if !errors.Is(err, store.ErrWALCorrupt) {
		t.Fatalf("error = %v, want ErrWALCorrupt", err)
	}
}

// Contract: Put writes to WAL and creates parent directories.
func Test_Tx_Creates_Parent_Dirs_When_Put(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket := newTestTicket(t, "New Ticket")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	result, err := tx.Put(ticket)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file exists with parent directories
	absPath := filepath.Join(ticketDir, result.Path)

	_, statErr := os.Stat(absPath)
	if statErr != nil {
		t.Fatalf("ticket file missing: %v", statErr)
	}

	// Verify parent directory was created
	parentDir := filepath.Dir(absPath)

	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("parent dir missing: %v", err)
	}

	if !info.IsDir() {
		t.Fatal("parent path is not a directory")
	}
}

// Contract: Delete removes file and updates index.
func Test_Tx_Removes_File_When_Delete(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Create ticket first
	ticket := putTicket(t.Context(), t, s, newTestTicket(t, "To Delete"))

	// Reindex to ensure it's in SQLite
	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	// Verify file exists
	absPath := filepath.Join(ticketDir, ticket.Path)

	_, statErr := os.Stat(absPath)
	if statErr != nil {
		t.Fatalf("ticket file should exist before delete: %v", statErr)
	}

	// Delete
	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(ticket.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file was removed
	_, removeStatErr := os.Stat(absPath)
	if !os.IsNotExist(removeStatErr) {
		t.Fatalf("ticket file should not exist after delete: %v", removeStatErr)
	}

	// Verify removed from index
	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex after delete: %v", err)
	}

	rows, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after delete, got %d", len(rows))
	}
}

// Contract: Rollback does not apply WAL operations.
func Test_WAL_Not_Applied_When_Rollback(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	ticket := newTestTicket(t, "Will Rollback")

	result, err := tx.Put(ticket)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify file was not created
	absPath := filepath.Join(ticketDir, result.Path)

	_, statErr := os.Stat(absPath)
	if !os.IsNotExist(statErr) {
		t.Fatalf("ticket file should not exist after rollback: %v", statErr)
	}

	// Verify not in index
	rows, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 0 {
		t.Fatalf("expected 0 rows after rollback, got %d", len(rows))
	}
}

// Contract: Body content is preserved through WAL round-trip.
func Test_Tx_Preserves_Body_When_Put(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	ticket := newTestTicket(t, "With Body")
	ticket.Body = "This is the body content.\n\nWith multiple paragraphs."

	result := putTicket(t.Context(), t, s, ticket)

	// Read back via Get
	got, err := s.Get(t.Context(), result.ID.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Body != ticket.Body {
		t.Fatalf("body = %q, want %q", got.Body, ticket.Body)
	}

	// Also verify file content
	absPath := filepath.Join(ticketDir, result.Path)
	content := readFileString(t, absPath)

	if !strings.Contains(content, "This is the body content.") {
		t.Fatalf("file missing body content:\n%s", content)
	}
}
