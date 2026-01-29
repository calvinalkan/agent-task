package mddb_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Tx_Creates_Doc_When_Put_And_Commit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc := newTestDoc(t, "Test Doc")

	result, err := tx.Put(doc)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	if result.ID() != doc.DocID {
		t.Fatalf("id mismatch: got %s, want %s", result.ID(), doc.DocID)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file exists
	absPath := filepath.Join(dir, doc.DocPath)

	info, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}

	if info.IsDir() {
		t.Fatal("path is a directory")
	}

	// Verify file content
	content := readFileString(t, absPath)
	if !strings.Contains(content, "id: "+doc.DocID) {
		t.Fatalf("file missing id, content:\n%s", content)
	}

	if !strings.Contains(content, "title: Test Doc") {
		t.Fatalf("file missing title, content:\n%s", content)
	}

	// Verify SQLite index
	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM "+testTableName+" WHERE id = ?", doc.DocID).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	// Verify WAL was truncated
	walPath := filepath.Join(dir, ".mddb", "wal")

	walInfo, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if walInfo.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", walInfo.Size())
	}
}

func Test_Tx_Updates_Doc_When_Put_With_Existing_ID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	// Create initial doc
	doc := newTestDoc(t, "Original Title")
	putTestDoc(t.Context(), t, s, doc)

	// Update doc
	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc.DocTitle = "Updated Title"
	doc.DocStatus = "closed"

	_, err = tx.Put(doc)
	if err != nil {
		t.Fatalf("put update: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file content
	absPath := filepath.Join(dir, doc.DocPath)

	content := readFileString(t, absPath)
	if !strings.Contains(content, "status: closed") {
		t.Fatalf("file not updated, content:\n%s", content)
	}

	if !strings.Contains(content, "title: Updated Title") {
		t.Fatalf("title not updated, content:\n%s", content)
	}

	// Verify index
	got, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Title() != "Updated Title" {
		t.Fatalf("title = %s, want Updated Title", got.Title())
	}
}

func Test_Tx_Removes_Doc_When_Delete_And_Commit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := putTestDoc(t.Context(), t, s, newTestDoc(t, "To Delete"))
	absPath := filepath.Join(dir, doc.DocPath)

	// Verify file exists
	_, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("file should exist: %v", err)
	}

	// Delete
	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(doc.DocID, doc.DocPath)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file removed
	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("file should not exist, err = %v", err)
	}

	// Verify index updated
	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func Test_Tx_Discards_Changes_When_Rollback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc, err := tx.Put(newTestDoc(t, "Should Not Exist"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify file NOT created
	absPath := filepath.Join(dir, doc.RelPath())

	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("file should not exist, err = %v", err)
	}

	// Verify index empty
	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func Test_Tx_Returns_Nil_When_Rollback_Called_Multiple_Times(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

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

func Test_Tx_Succeeds_When_Commit_With_No_Operations(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

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

func Test_Tx_Returns_Error_When_Operations_After_Commit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, err = tx.Put(newTestDoc(t, "After Commit"))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("put after commit: got %v, want 'closed'", err)
	}

	err = tx.Delete("01934567-89ab-7def-8123-456789abcdef", "any/path.md")
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("delete after commit: got %v, want 'closed'", err)
	}

	err = tx.Commit(t.Context())
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("commit after commit: got %v, want 'closed'", err)
	}
}

func Test_Tx_Returns_Error_When_Operations_After_Rollback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	_, err = tx.Put(newTestDoc(t, "After Rollback"))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("put after rollback: got %v, want 'closed'", err)
	}
}

func Test_Tx_Succeeds_When_Delete_Nonexistent_Doc(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	nonexistent := newTestDoc(t, "Nonexistent")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(nonexistent.DocID, nonexistent.DocPath)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func Test_Tx_Applies_Last_Put_When_Multiple_Puts_Same_ID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	first := newTestDoc(t, "First Title")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = tx.Put(first)
	if err != nil {
		t.Fatalf("first put: %v", err)
	}

	// Second put with same ID
	second := &TestDoc{
		DocID:       first.DocID,
		DocShort:    first.DocShort,
		DocPath:     first.DocPath,
		DocTitle:    "Second Title",
		DocStatus:   "in_progress",
		DocPriority: 3,
	}

	_, err = tx.Put(second)
	if err != nil {
		t.Fatalf("second put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify only second applied
	got, err := s.Get(t.Context(), first.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Title() != "Second Title" {
		t.Fatalf("title = %s, want Second Title", got.Title())
	}
}

func Test_Tx_Removes_Doc_When_Put_Then_Delete_Same_ID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc, err := tx.Put(newTestDoc(t, "Will Be Deleted"))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Delete(doc.ID(), doc.RelPath())
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file doesn't exist
	absPath := filepath.Join(dir, doc.RelPath())

	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("file should not exist, err = %v", err)
	}

	// Verify index empty
	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
}

func Test_Tx_Persists_Doc_When_Commit_And_Reopen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	tx, err := s.Begin(t.Context())
	if err != nil {
		_ = s.Close()

		t.Fatalf("begin: %v", err)
	}

	doc, err := tx.Put(newTestDoc(t, "Persist Test"))
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

	_ = s.Close()

	// Reopen and verify
	s2 := openTestStore(t, dir)

	defer func() { _ = s2.Close() }()

	got, err := s2.Get(t.Context(), doc.ID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID() != doc.ID() {
		t.Fatalf("id = %s, want %s", got.ID(), doc.ID())
	}
}

func Test_Tx_Recovers_WAL_When_Begin_With_Pending_WAL(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Initialize store
	s := openTestStore(t, dir)
	_ = s.Close()

	// Write committed WAL (simulating crash)
	doc := newTestDoc(t, "WAL Recovery Test")
	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalFile(t, walPath, []walRecord{makeWalPutRecord(doc)})

	// Reopen - Begin should recover WAL
	s2 := openTestStore(t, dir)

	defer func() { _ = s2.Close() }()

	tx, err := s2.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_ = tx.Rollback()

	// Verify WAL was recovered
	got, err := s2.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID() != doc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), doc.DocID)
	}
}

func Test_Tx_Returns_Error_When_Begin_On_Nil_Store(t *testing.T) {
	t.Parallel()

	var s *TestStore

	_, err := s.Begin(t.Context())
	if err == nil {
		t.Fatal("expected error for nil store")
	}

	if !errors.Is(err, mddb.ErrClosed) {
		t.Fatalf("error = %v, want %v", err, mddb.ErrClosed)
	}
}

func Test_Tx_Returns_Nil_When_Rollback_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *mddb.Tx[TestDoc]

	err := tx.Rollback()
	if err != nil {
		t.Fatalf("rollback nil tx: %v", err)
	}
}

func Test_Tx_Returns_Error_When_Commit_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *mddb.Tx[TestDoc]

	err := tx.Commit(t.Context())
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("error = %v, want contains 'nil'", err)
	}
}

func Test_Tx_Returns_Error_When_Put_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *mddb.Tx[TestDoc]

	_, err := tx.Put(&TestDoc{})
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("error = %v, want contains 'nil'", err)
	}
}

func Test_Tx_Returns_Error_When_Delete_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *mddb.Tx[TestDoc]

	err := tx.Delete("01934567-89ab-7def-8123-456789abcdef", "any/path.md")
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("error = %v, want contains 'nil'", err)
	}
}

func Test_Tx_Preserves_Body_When_Put_And_Get(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	body := "This is the body.\n\nMultiple paragraphs."
	doc := newTestDoc(t, "Body Test")
	doc.DocBody = body

	result, err := tx.Put(doc)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify body in file
	absPath := filepath.Join(dir, result.RelPath())

	content := readFileString(t, absPath)
	if !strings.Contains(content, body) {
		t.Fatalf("file missing body, content:\n%s", content)
	}

	// Verify body via Get
	got, err := s.Get(t.Context(), result.ID())
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.Body() != body {
		t.Fatalf("body = %q, want %q", got.Body(), body)
	}
}

func Test_Tx_Preserves_Body_When_Update_Via_Get_Put(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	// Create doc with body
	body := "Original body content."
	doc := newTestDoc(t, "Original Title")
	doc.DocBody = body
	putTestDoc(t.Context(), t, s, doc)

	// Get, modify, put back
	got, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	// Create updated doc (preserve body)
	updated := &TestDoc{
		DocID:       got.ID(),
		DocShort:    got.ShortID(),
		DocPath:     got.RelPath(),
		DocMtime:    got.MtimeNS(),
		DocTitle:    "Modified Title",
		DocStatus:   got.DocStatus,
		DocPriority: got.DocPriority,
		DocBody:     got.Body(),
	}

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = tx.Put(updated)
	if err != nil {
		t.Fatalf("put update: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify body preserved
	final, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get updated: %v", err)
	}

	if final.Title() != "Modified Title" {
		t.Fatalf("title = %q, want Modified Title", final.Title())
	}

	if final.Body() != body {
		t.Fatalf("body = %q, want %q", final.Body(), body)
	}
}

func Test_Tx_Returns_Error_When_Delete_With_Empty_ID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	// Empty ID should fail
	err = tx.Delete("", "any/path.md")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want contains 'empty'", err)
	}
}
