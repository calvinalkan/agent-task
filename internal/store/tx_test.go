package store_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/store"
	"github.com/google/uuid"
)

// TestTx_Put_And_Commit_Creates_Ticket verifies the core create flow:
// Begin → Put → Commit writes a ticket file and updates the SQLite index.
func Test_Tx_Creates_Ticket_When_Put_And_Commit(t *testing.T) {
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

	ticket := newTestTicket(t, "Test Ticket")

	result, err := tx.Put(ticket)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	// Verify IDs match
	if result.ID != ticket.ID {
		t.Fatalf("id mismatch: got %s, want %s", result.ID, ticket.ID)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file exists.
	absPath := filepath.Join(ticketDir, ticket.Path)

	info, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat ticket file: %v", err)
	}

	if info.IsDir() {
		t.Fatal("ticket path is a directory")
	}

	// Verify file content contains expected frontmatter.
	content := readFileString(t, absPath)
	if !strings.Contains(content, "id: "+ticket.ID.String()) {
		t.Fatalf("file missing id, content:\n%s", content)
	}

	if !strings.Contains(content, "status: open") {
		t.Fatalf("file missing status, content:\n%s", content)
	}

	if !strings.Contains(content, "title: Test Ticket") {
		t.Fatalf("file missing title, content:\n%s", content)
	}

	// Verify SQLite index was updated.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(results))
	}

	if results[0].ID != ticket.ID {
		t.Fatalf("query id = %s, want %s", results[0].ID, ticket.ID)
	}

	if results[0].Title != "Test Ticket" {
		t.Fatalf("query title = %s, want Test Ticket", results[0].Title)
	}

	// Verify WAL was truncated.
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	walInfo, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if walInfo.Size() != 0 {
		t.Fatalf("wal size = %d, want 0 (should be truncated)", walInfo.Size())
	}
}

// TestTx_Put_Updates_Existing_Ticket verifies that Put with an existing ID
// overwrites the ticket file and updates the index.
func Test_Tx_Updates_Ticket_When_Put_With_Existing_ID(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Create initial ticket
	ticket := newTestTicket(t, "Original Title")
	ticket = putTicket(t.Context(), t, s, ticket)

	// Update ticket with new title and status
	tx2, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	ticket.Title = "Updated Title"
	ticket.Status = "closed"
	ticket.ClosedAt = store.TimePtr(time.Now().UTC())

	_, err = tx2.Put(ticket)
	if err != nil {
		t.Fatalf("put update: %v", err)
	}

	err = tx2.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit update: %v", err)
	}

	// Verify file content was updated.
	absPath := filepath.Join(ticketDir, ticket.Path)
	content := readFileString(t, absPath)

	if !strings.Contains(content, "status: closed") {
		t.Fatalf("file not updated, content:\n%s", content)
	}

	if !strings.Contains(content, "title: Updated Title") {
		t.Fatalf("title not updated, content:\n%s", content)
	}

	// Verify index was updated.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(results))
	}

	if results[0].Status != "closed" {
		t.Fatalf("status = %s, want closed", results[0].Status)
	}

	if results[0].Title != "Updated Title" {
		t.Fatalf("title = %s, want Updated Title", results[0].Title)
	}
}

// TestTx_Delete_Removes_Ticket verifies that Delete removes the file and
// updates the SQLite index on Commit.
func Test_Tx_Removes_Ticket_When_Delete_And_Commit(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Create ticket.
	ticket := putTicket(t.Context(), t, s, newTestTicket(t, "To Be Deleted"))

	absPath := filepath.Join(ticketDir, ticket.Path)

	// Verify file exists before delete.
	_, err = os.Stat(absPath)
	if err != nil {
		t.Fatalf("file should exist before delete: %v", err)
	}

	// Delete ticket.
	tx2, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx2.Delete(ticket.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx2.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit delete: %v", err)
	}

	// Verify file was removed.
	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("file should not exist after delete, err = %v", err)
	}

	// Verify index was updated.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 tickets after delete, got %d", len(results))
	}
}

// TestTx_Rollback_Discards_Changes verifies that Rollback discards all
// buffered operations without writing to disk.
func Test_Tx_Discards_Changes_When_Rollback(t *testing.T) {
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

	ticket, err := tx.Put(newTestTicket(t, "Should Not Exist"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify file was NOT created.
	absPath := filepath.Join(ticketDir, ticket.Path)

	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("file should not exist after rollback, err = %v", err)
	}

	// Verify index has no tickets.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 tickets after rollback, got %d", len(results))
	}
}

// TestTx_Rollback_Is_Idempotent verifies that Rollback can be called multiple
// times without error.
func Test_Tx_Returns_Nil_When_Rollback_Called_Multiple_Times(t *testing.T) {
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

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("first rollback: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("second rollback: %v", err)
	}
}

// TestTx_Empty_Commit_Succeeds verifies that committing a transaction with
// no operations succeeds without error.
func Test_Tx_Succeeds_When_Commit_With_No_Operations(t *testing.T) {
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

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("empty commit: %v", err)
	}
}

// TestTx_Operations_After_Commit_Return_Error verifies that Put/Delete/Commit
// on a committed transaction return an error.
func Test_Tx_Returns_Error_When_Operations_After_Commit(t *testing.T) {
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

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, err = tx.Put(newTestTicket(t, "After Commit"))
	if err == nil || !strings.Contains(err.Error(), "transaction closed") {
		t.Fatalf("put after commit: got %v, want 'transaction closed'", err)
	}

	err = tx.Delete(uuid.MustParse("01234567-89ab-7def-8123-456789abcdef"))
	if err == nil || !strings.Contains(err.Error(), "transaction closed") {
		t.Fatalf("delete after commit: got %v, want 'transaction closed'", err)
	}

	err = tx.Commit(t.Context())
	if err == nil || !strings.Contains(err.Error(), "transaction closed") {
		t.Fatalf("commit after commit: got %v, want 'transaction closed'", err)
	}
}

// Test_Tx_Returns_Error_When_Operations_After_Rollback verifies that Put/Delete/Commit
// on a rolled-back transaction return an error.
func Test_Tx_Returns_Error_When_Operations_After_Rollback(t *testing.T) {
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

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	_, err = tx.Put(newTestTicket(t, "After Rollback"))
	if err == nil || !strings.Contains(err.Error(), "transaction closed") {
		t.Fatalf("put after rollback: got %v, want 'transaction closed'", err)
	}

	err = tx.Delete(uuid.MustParse("01234567-89ab-7def-8123-456789abcdef"))
	if err == nil || !strings.Contains(err.Error(), "transaction closed") {
		t.Fatalf("delete after rollback: got %v, want 'transaction closed'", err)
	}

	err = tx.Commit(t.Context())
	if err == nil || !strings.Contains(err.Error(), "transaction closed") {
		t.Fatalf("commit after rollback: got %v, want 'transaction closed'", err)
	}
}

// Test_Tx_Returns_Error_When_Put_Missing_Required_Fields verifies that Put returns errors for
// missing required fields.
func Test_Tx_Returns_Error_When_Put_Missing_Required_Fields(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	tests := []struct {
		name   string
		modify func(*store.Ticket)
		errMsg string
	}{
		{
			name:   "missing status",
			modify: func(ticket *store.Ticket) { ticket.Status = "" },
			errMsg: "status",
		},
		{
			name:   "missing type",
			modify: func(ticket *store.Ticket) { ticket.Type = "" },
			errMsg: "type",
		},
		{
			name:   "invalid priority",
			modify: func(ticket *store.Ticket) { ticket.Priority = 0 },
			errMsg: "priority",
		},
		{
			name:   "missing created_at",
			modify: func(ticket *store.Ticket) { ticket.CreatedAt = time.Time{} },
			errMsg: "created_at",
		},
		{
			name:   "missing title",
			modify: func(ticket *store.Ticket) { ticket.Title = "" },
			errMsg: "title",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := s.Begin(t.Context())
			if err != nil {
				t.Fatalf("begin: %v", err)
			}

			defer func() { _ = tx.Rollback() }()

			ticket := newTestTicket(t, "Test")
			tt.modify(ticket)

			_, err = tx.Put(ticket)
			if err == nil {
				t.Fatalf("expected error for %s", tt.name)
			}

			if !strings.Contains(err.Error(), tt.errMsg) {
				t.Fatalf("error = %v, want contains %q", err, tt.errMsg)
			}
		})
	}
}

// Test_Tx_Returns_Error_When_Delete_With_Invalid_ID verifies that Delete returns errors for
// invalid IDs.
func Test_Tx_Returns_Error_When_Delete_With_Invalid_ID(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = s.Close() })

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	// uuid.Nil should fail validation
	err = tx.Delete(uuid.Nil)
	if err == nil {
		t.Fatal("expected error for nil UUID")
	}
	if !strings.Contains(err.Error(), "not UUIDv7") {
		t.Fatalf("error = %v, want contains 'not UUIDv7'", err)
	}

	// UUIDv4 should fail validation
	err = tx.Delete(uuid.MustParse("550e8400-e29b-41d4-a716-446655440000"))
	if err == nil {
		t.Fatal("expected error for UUIDv4")
	}
	if !strings.Contains(err.Error(), "not UUIDv7") {
		t.Fatalf("error = %v, want contains 'not UUIDv7'", err)
	}
}

// TestTx_Delete_Nonexistent_Succeeds verifies that deleting a ticket that
// doesn't exist succeeds (idempotent).
func Test_Tx_Succeeds_When_Delete_Nonexistent_Ticket(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	// Create a ticket to get a valid ID, but don't actually put it
	nonexistent := newTestTicket(t, "Nonexistent")

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(nonexistent.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
}

// TestTx_Multiple_Puts_Last_Wins verifies that multiple Puts for the same ID
// within a transaction results in only the last Put being applied.
func Test_Tx_Applies_Last_Put_When_Multiple_Puts_Same_ID(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Create ticket to get a valid ID
	first := newTestTicket(t, "First Title")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// First put
	_, err = tx.Put(first)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}

	// Second put with same ID - different values
	second := newTestTicket(t, "Second Title")
	second.ID = first.ID
	second.ShortID = first.ShortID
	second.Path = first.Path
	second.Status = "in_progress"
	second.Type = "bug"
	second.Priority = 3

	_, err = tx.Put(second)
	if err != nil {
		t.Fatalf("second put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify only the second put was applied.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(results))
	}

	if results[0].Status != "in_progress" {
		t.Fatalf("status = %s, want in_progress", results[0].Status)
	}

	if results[0].Type != "bug" {
		t.Fatalf("type = %s, want bug", results[0].Type)
	}

	if results[0].Title != "Second Title" {
		t.Fatalf("title = %s, want Second Title", results[0].Title)
	}
}

// TestTx_Put_Then_Delete_Removes_Ticket verifies that Put followed by Delete
// for the same ID results in the ticket being deleted.
func Test_Tx_Removes_Ticket_When_Put_Then_Delete_Same_ID(t *testing.T) {
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

	// Put then delete
	toDelete := newTestTicket(t, "Will Be Deleted")

	ticket, err := tx.Put(toDelete)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Delete(ticket.ID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file does not exist.
	absPath := filepath.Join(ticketDir, ticket.Path)

	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("file should not exist after put+delete, err = %v", err)
	}

	// Verify index is empty.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 0 {
		t.Fatalf("expected 0 tickets, got %d", len(results))
	}
}

// TestTx_Put_With_Optional_Fields verifies that optional fields are correctly
// written to the file and index.
func Test_Tx_Writes_Optional_Fields_When_Put_With_All_Fields(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Create tickets to use as parent and blocker
	parent := newTestTicket(t, "Parent")
	blocker := newTestTicket(t, "Blocker")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	closedAt := time.Now().UTC()
	ticket, _ := store.NewTicket("Bug Fix", "bug", "closed", 1)
	ticket.ClosedAt = &closedAt
	ticket.Assignee = store.StringPtr("alice")
	ticket.Parent = &parent.ID
	ticket.ExternalRef = store.StringPtr("GH-42")
	ticket.BlockedBy = uuid.UUIDs{blocker.ID}

	ticket, err = tx.Put(ticket)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file content.
	absPath := filepath.Join(ticketDir, ticket.Path)
	content := readFileString(t, absPath)

	if !strings.Contains(content, "assignee: alice") {
		t.Fatalf("file missing assignee, content:\n%s", content)
	}

	if !strings.Contains(content, "parent: "+parent.ID.String()) {
		t.Fatalf("file missing parent, content:\n%s", content)
	}

	if !strings.Contains(content, "external-ref: GH-42") {
		t.Fatalf("file missing external-ref, content:\n%s", content)
	}

	if !strings.Contains(content, "blocked-by:") {
		t.Fatalf("file missing blocked-by, content:\n%s", content)
	}

	if !strings.Contains(content, blocker.ID.String()) {
		t.Fatalf("file missing blocker id, content:\n%s", content)
	}

	// Verify index.
	results, err := s.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(results))
	}

	if !ptrEquals(results[0].Assignee, "alice") {
		t.Fatalf("assignee = %s, want alice", ptrVal(results[0].Assignee))
	}

	if results[0].Parent == nil || *results[0].Parent != parent.ID {
		t.Fatalf("parent = %v, want %s", results[0].Parent, parent.ID)
	}

	if !ptrEquals(results[0].ExternalRef, "GH-42") {
		t.Fatalf("external_ref = %s, want GH-42", ptrVal(results[0].ExternalRef))
	}

	if len(results[0].BlockedBy) != 1 || results[0].BlockedBy[0] != blocker.ID {
		t.Fatalf("blocked_by = %v, want [%s]", results[0].BlockedBy, blocker.ID)
	}
}

// TestTx_Commit_Writes_WAL_Before_Files tests that the WAL is written before
// files are updated. This ensures crash recovery works.
func Test_Tx_Persists_Ticket_When_Commit_And_Reopen(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	tx, err := s.Begin(t.Context())
	if err != nil {
		_ = s.Close()

		t.Fatalf("begin: %v", err)
	}

	ticket, err := tx.Put(newTestTicket(t, "WAL Test"))
	if err != nil {
		_ = tx.Rollback()
		_ = s.Close()

		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		_ = s.Close()

		t.Fatalf("commit: %v", err)
	}

	// Close the store and verify WAL was truncated (normal case).
	_ = s.Close()

	// Reopen and verify the ticket is there.
	s2, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	defer func() { _ = s2.Close() }()

	results, err := s2.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 ticket, got %d", len(results))
	}

	if results[0].ID != ticket.ID {
		t.Fatalf("id = %s, want %s", results[0].ID, ticket.ID)
	}
}

// TestTx_Begin_Recovers_Pending_WAL verifies that Begin recovers any pending
// WAL state before starting a new transaction.
func Test_Tx_Recovers_WAL_When_Begin_With_Pending_WAL(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	// First, create a store and a ticket.
	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	_ = s.Close()

	// Write a committed WAL manually (simulating crash mid-recovery).
	ticket := newTestTicket(t, "WAL Recovery Test")

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		makeWalPutRecord(ticket),
	})

	// Reopen and begin a new transaction - should recover the WAL first.
	s2, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}

	defer func() { _ = s2.Close() }()

	tx, err := s2.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// Rollback immediately - we only wanted Begin to trigger WAL recovery.
	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify the WAL was recovered - ticket should exist now.
	results, err := s2.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 ticket after WAL recovery, got %d", len(results))
	}

	if results[0].ID != ticket.ID {
		t.Fatalf("id = %s, want %s", results[0].ID, ticket.ID)
	}
}

// TestTx_Begin_Returns_Error_When_Store_Nil verifies that Begin on a nil store
// returns an error.
func Test_Tx_Returns_Error_When_Begin_On_Nil_Store(t *testing.T) {
	t.Parallel()

	var s *store.Store

	_, err := s.Begin(t.Context())
	if err == nil {
		t.Fatal("expected error for nil store")
	}

	if !strings.Contains(err.Error(), "not open") {
		t.Fatalf("error = %v, want contains 'not open'", err)
	}
}

// TestTx_Rollback_On_Nil_Tx_Returns_Nil verifies that Rollback on a nil Tx
// returns nil (safe to call).
func Test_Tx_Returns_Nil_When_Rollback_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *store.Tx

	err := tx.Rollback()
	if err != nil {
		t.Fatalf("rollback nil tx: %v", err)
	}
}

// TestTx_Commit_On_Nil_Tx_Returns_Error verifies that Commit on a nil Tx
// returns an error.
func Test_Tx_Returns_Error_When_Commit_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *store.Tx

	err := tx.Commit(t.Context())
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "tx is nil") {
		t.Fatalf("error = %v, want contains 'tx is nil'", err)
	}
}

// TestTx_Put_On_Nil_Tx_Returns_Error verifies that Put on a nil Tx
// returns an error.
func Test_Tx_Returns_Error_When_Put_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *store.Tx

	_, err := tx.Put(&store.Ticket{})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "tx is nil") {
		t.Fatalf("error = %v, want contains 'tx is nil'", err)
	}
}

// TestTx_Delete_On_Nil_Tx_Returns_Error verifies that Delete on a nil Tx
// returns an error.
func Test_Tx_Returns_Error_When_Delete_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *store.Tx

	err := tx.Delete(uuid.MustParse("01234567-89ab-7def-8123-456789abcdef"))
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "tx is nil") {
		t.Fatalf("error = %v, want contains 'tx is nil'", err)
	}
}

// Test_Tx_Preserves_Body_When_Put_And_Get verifies that body content is preserved through Put and Get.
func Test_Tx_Preserves_Body_When_Put_And_Get(t *testing.T) {
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

	body := "This is the ticket body.\n\nIt has multiple paragraphs."
	toCreate := newTestTicket(t, "Body Test")
	toCreate.Body = body

	ticket, err := tx.Put(toCreate)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify body in file.
	absPath := filepath.Join(ticketDir, ticket.Path)
	content := readFileString(t, absPath)

	if !strings.Contains(content, body) {
		t.Fatalf("file missing body, content:\n%s", content)
	}

	// Get ticket and verify body.
	got, err := s.Get(t.Context(), ticket.ID.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Body != body {
		t.Fatalf("body = %q, want %q", got.Body, body)
	}
}

// Test_Tx_Preserves_Body_When_Update_Via_Get_Put verifies that body is preserved when updating via Get → Put.
func Test_Tx_Preserves_Body_When_Update_Via_Get_Put(t *testing.T) {
	t.Parallel()

	ticketDir := t.TempDir()

	s, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Create ticket with body.
	body := "Original body content."
	toCreate := newTestTicket(t, "Original Title")
	toCreate.Body = body

	ticket := putTicket(t.Context(), t, s, toCreate)

	// Get ticket, modify title, put back.
	got, err := s.Get(t.Context(), ticket.ID.String())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	got.Title = "Modified Title"

	tx2, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = tx2.Put(got)
	if err != nil {
		t.Fatalf("put update: %v", err)
	}

	err = tx2.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit update: %v", err)
	}

	// Verify body is preserved.
	updated, err := s.Get(t.Context(), ticket.ID.String())
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}

	if updated.Title != "Modified Title" {
		t.Fatalf("title = %q, want %q", updated.Title, "Modified Title")
	}

	if updated.Body != body {
		t.Fatalf("body = %q, want %q", updated.Body, body)
	}
}
