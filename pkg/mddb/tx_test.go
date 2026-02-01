package mddb_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Tx_Creates_Doc_When_Create_And_Commit(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc := newTestDoc(t, "Test Doc")

	result, err := tx.Create(doc)
	if err != nil {
		t.Fatalf("create: %v", err)
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

func Test_Tx_Create_Returns_Error_When_Path_Is_Directory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Test Doc")
	absPath := filepath.Join(dir, doc.DocPath)

	if err := os.MkdirAll(filepath.Dir(absPath), 0o750); err != nil {
		t.Fatalf("mkdir parent: %v", err)
	}

	if err := os.Mkdir(absPath, 0o750); err != nil {
		t.Fatalf("mkdir doc path: %v", err)
	}

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	_, err = tx.Create(doc)
	if err == nil {
		t.Fatal("expected error")
	}

	var mErr *mddb.Error
	if !errors.As(err, &mErr) {
		t.Fatalf("error type = %T, want *mddb.Error", err)
	}

	if mErr.Path != doc.DocPath {
		t.Fatalf("path = %q, want %q", mErr.Path, doc.DocPath)
	}

	if !strings.Contains(mErr.Err.Error(), "not a regular file") {
		t.Fatalf("error = %v, want regular file error", err)
	}
}

func Test_Tx_Create_Returns_Error_When_Index_Query_Fails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	_, err = tx.DB().ExecContext(t.Context(), "DROP TABLE "+testTableName)
	if err != nil {
		t.Fatalf("drop table: %v", err)
	}

	doc := newTestDoc(t, "Test Doc")

	_, err = tx.Create(doc)
	if err == nil {
		t.Fatal("expected error")
	}

	var mErr *mddb.Error
	if !errors.As(err, &mErr) {
		t.Fatalf("error type = %T, want *mddb.Error", err)
	}

	if mErr.ID != doc.DocID {
		t.Fatalf("id = %q, want %q", mErr.ID, doc.DocID)
	}

	if mErr.Path != doc.DocPath {
		t.Fatalf("path = %q, want %q", mErr.Path, doc.DocPath)
	}

	if !strings.Contains(mErr.Err.Error(), "sqlite:") {
		t.Fatalf("error = %v, want sqlite error", err)
	}
}

func Test_Tx_Updates_Doc_When_Update_With_Existing_ID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	// Create initial doc
	doc := newTestDoc(t, "Original Title")
	createTestDoc(t.Context(), t, s, doc)

	// Update doc
	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc.DocTitle = "Updated Title"
	doc.DocStatus = "closed"

	_, err = tx.Update(doc)
	if err != nil {
		t.Fatalf("update: %v", err)
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

	doc := createTestDoc(t.Context(), t, s, newTestDoc(t, "To Delete"))
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

	err = tx.Delete(doc.DocID)
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

	doc, err := tx.Create(newTestDoc(t, "Should Not Exist"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	// Verify file NOT created
	absPath := filepath.Join(dir, doc.DocPath)

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

	_, err = tx.Create(newTestDoc(t, "After Commit"))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("create after commit: got %v, want 'closed'", err)
	}

	err = tx.Delete("01934567-89ab-7def-8123-456789abcdef")
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

	_, err = tx.Create(newTestDoc(t, "After Rollback"))
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("create after rollback: got %v, want 'closed'", err)
	}
}

func Test_Tx_Keeps_Other_Ops_When_Delete_NotFound(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	docA := newTestDoc(t, "Alpha")

	_, err = tx.Create(docA)
	if err != nil {
		t.Fatalf("create a: %v", err)
	}

	missing := newTestDoc(t, "Missing")

	err = tx.Delete(missing.DocID)
	if err == nil || !errors.Is(err, mddb.ErrNotFound) {
		t.Fatalf("delete: got %v, want ErrNotFound", err)
	}

	docB := newTestDoc(t, "Beta")

	_, err = tx.Create(docB)
	if err != nil {
		t.Fatalf("create b: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	_, err = s.Get(t.Context(), docA.DocID)
	if err != nil {
		t.Fatalf("get a: %v", err)
	}

	_, err = s.Get(t.Context(), docB.DocID)
	if err != nil {
		t.Fatalf("get b: %v", err)
	}
}

func Test_Tx_Calls_AfterDelete_When_Delete_Commits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	var gotID string

	cfg := testConfig(dir)
	cfg.AfterDelete = func(_ context.Context, _ *sql.Tx, id string) error {
		gotID = id

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	doc := createTestDoc(t.Context(), t, s, newTestDoc(t, "To Delete"))

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(doc.DocID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	if gotID != doc.DocID {
		t.Fatalf("after delete id = %q, want %q", gotID, doc.DocID)
	}
}

func Test_Tx_Returns_ErrCommitIncomplete_When_AfterDelete_Fails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	cfg := testConfig(dir)
	cfg.AfterDelete = func(_ context.Context, _ *sql.Tx, _ string) error {
		return errors.New("after delete failed")
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	doc := createTestDoc(t.Context(), t, s, newTestDoc(t, "To Delete"))

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(doc.DocID)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err == nil || !errors.Is(err, mddb.ErrCommitIncomplete) {
		t.Fatalf("commit: got %v, want ErrCommitIncomplete", err)
	}

	walPath := filepath.Join(dir, ".mddb", "wal")

	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat wal: %v", statErr)
	}

	if info.Size() == 0 {
		t.Fatalf("wal size = %d, want > 0", info.Size())
	}
}

func Test_Tx_Returns_ErrCommitIncomplete_When_AfterCreate_Fails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	cfg := testConfig(dir)
	cfg.AfterCreate = func(_ context.Context, _ *sql.Tx, _ *TestDoc) error {
		return errors.New("after create failed")
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc := newTestDoc(t, "To Create")

	_, err = tx.Create(doc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = tx.Commit(t.Context())
	if err == nil || !errors.Is(err, mddb.ErrCommitIncomplete) {
		t.Fatalf("commit: got %v, want ErrCommitIncomplete", err)
	}

	walPath := filepath.Join(dir, ".mddb", "wal")

	info, statErr := os.Stat(walPath)
	if statErr != nil {
		t.Fatalf("stat wal: %v", statErr)
	}

	if info.Size() == 0 {
		t.Fatalf("wal size = %d, want > 0", info.Size())
	}
}

func Test_Tx_Calls_AfterUpdate_When_Update_Commits(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	var (
		gotID    string
		gotTitle string
		calls    int
	)

	cfg := testConfig(dir)
	cfg.AfterUpdate = func(_ context.Context, _ *sql.Tx, doc *TestDoc) error {
		calls++
		gotID = doc.DocID
		gotTitle = doc.DocTitle

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	doc := createTestDoc(t.Context(), t, s, newTestDoc(t, "Original"))
	if calls != 0 {
		t.Fatalf("after create calls = %d, want 0", calls)
	}

	updated := *doc
	updated.DocTitle = "Updated"

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = tx.Update(&updated)
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	if calls != 1 {
		t.Fatalf("after update calls = %d, want 1", calls)
	}

	if gotID != updated.DocID {
		t.Fatalf("after update id = %q, want %q", gotID, updated.DocID)
	}

	if gotTitle != updated.DocTitle {
		t.Fatalf("after update title = %q, want %q", gotTitle, updated.DocTitle)
	}
}

func Test_Tx_Returns_ErrNotFound_When_Delete_Nonexistent_Doc(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	nonexistent := newTestDoc(t, "Nonexistent")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	err = tx.Delete(nonexistent.DocID)
	if err == nil || !errors.Is(err, mddb.ErrNotFound) {
		t.Fatalf("delete: got %v, want ErrNotFound", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func Test_Tx_Applies_Last_Create_When_Multiple_Creates_Same_ID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	first := newTestDoc(t, "First Title")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = tx.Create(first)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}

	// Second create with same ID (overwrites in buffer since not committed yet)
	second := &TestDoc{
		DocID:       first.DocID,
		DocShort:    first.DocShort,
		DocPath:     first.DocPath,
		DocTitle:    "Second Title",
		DocStatus:   "in_progress",
		DocPriority: 3,
	}

	_, err = tx.Create(second)
	if err != nil {
		t.Fatalf("second create: %v", err)
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

	doc, err := tx.Create(newTestDoc(t, "Will Be Deleted"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = tx.Delete(doc.ID())
	if err != nil {
		t.Fatalf("delete: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file doesn't exist
	absPath := filepath.Join(dir, doc.DocPath)

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

	doc, err := tx.Create(newTestDoc(t, "Persist Test"))
	if err != nil {
		_ = tx.Rollback()
		_ = s.Close()

		t.Fatalf("create: %v", err)
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

func Test_Tx_Returns_Error_When_Create_On_Nil_Tx(t *testing.T) {
	t.Parallel()

	var tx *mddb.Tx[TestDoc]

	_, err := tx.Create(&TestDoc{})
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

	err := tx.Delete("01934567-89ab-7def-8123-456789abcdef")
	if err == nil {
		t.Fatal("expected error for nil tx")
	}

	if !strings.Contains(err.Error(), "nil") {
		t.Fatalf("error = %v, want contains 'nil'", err)
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
	err = tx.Delete("")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}

	if !strings.Contains(err.Error(), "empty") {
		t.Fatalf("error = %v, want contains 'empty'", err)
	}
}

func Test_Tx_Create_Returns_ErrAlreadyExists_When_Doc_Exists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	// Create initial doc
	doc := newTestDoc(t, "Existing Doc")
	createTestDoc(t.Context(), t, s, doc)

	// Try to create again with same ID
	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	_, err = tx.Create(doc)
	if err == nil || !errors.Is(err, mddb.ErrAlreadyExists) {
		t.Fatalf("create: got %v, want ErrAlreadyExists", err)
	}
}

func Test_Tx_Update_Returns_ErrNotFound_When_Doc_Not_Exists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	defer func() { _ = tx.Rollback() }()

	doc := newTestDoc(t, "Non-existing Doc")

	_, err = tx.Update(doc)
	if err == nil || !errors.Is(err, mddb.ErrNotFound) {
		t.Fatalf("update: got %v, want ErrNotFound", err)
	}
}
