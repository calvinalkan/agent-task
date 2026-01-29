package mddb_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Open_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Initialize store
	s := openTestStore(t, dir)
	_ = s.Close()

	doc := newTestDoc(t, "WAL Doc")
	walPath := filepath.Join(dir, ".mddb", "wal")

	writeWalFile(t, walPath, []walRecord{makeWalPutRecord(doc)})

	s = openTestStore(t, dir)
	t.Cleanup(func() { _ = s.Close() })

	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(dir, doc.DocPath)

	_, err = os.Stat(absPath)
	if err != nil {
		t.Fatalf("doc file missing: %v", err)
	}
}

func Test_Open_Replays_Put_And_Delete_When_WAL_Committed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create a doc to delete
	toDelete := newTestDoc(t, "To Delete")
	writeTestDocFile(t, dir, toDelete)

	// Create a doc to put via WAL
	toPut := newTestDoc(t, "To Put")

	s := openTestStore(t, dir)
	_ = s.Close()

	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalFile(t, walPath, []walRecord{
		makeWalDeleteRecord(toDelete.DocID, toDelete.DocPath),
		makeWalPutRecord(toPut),
	})

	s = openTestStore(t, dir)
	t.Cleanup(func() { _ = s.Close() })

	// Deleted doc should not exist
	_, err := os.Stat(filepath.Join(dir, toDelete.DocPath))
	if !os.IsNotExist(err) {
		t.Fatalf("deleted doc should not exist: %v", err)
	}

	// Put doc should exist
	_, err = os.Stat(filepath.Join(dir, toPut.DocPath))
	if err != nil {
		t.Fatalf("put doc missing: %v", err)
	}

	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM " + testTableName).Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func Test_Open_Ignores_WAL_When_Footer_Missing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Create existing doc
	existing := newTestDoc(t, "Existing")
	writeTestDocFile(t, dir, existing)

	s := openTestStore(t, dir)
	_ = s.Close()

	// Write WAL without footer (uncommitted)
	uncommitted := newTestDoc(t, "Uncommitted")
	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalBodyOnly(t, walPath, []walRecord{makeWalPutRecord(uncommitted)})

	s = openTestStore(t, dir)
	t.Cleanup(func() { _ = s.Close() })

	// Uncommitted doc should not exist
	_, err := os.Stat(filepath.Join(dir, uncommitted.DocPath))
	if !os.IsNotExist(err) {
		t.Fatalf("uncommitted doc should not exist: %v", err)
	}

	// WAL should be truncated
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	// Only existing doc in index
	got, err := s.Get(t.Context(), existing.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID() != existing.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), existing.DocID)
	}
}

func Test_Open_Returns_Error_When_WAL_Body_Invalid_JSON(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)
	_ = s.Close()

	// Write WAL with invalid JSON
	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalWithBody(t, walPath, []byte("corrupted content"))

	_, err := mddb.Open(t.Context(), testConfig(dir))
	if err == nil {
		t.Fatal("expected error for invalid WAL body")
	}

	if !errors.Is(err, mddb.ErrWALReplay) {
		t.Fatalf("error = %v, want ErrWALReplay", err)
	}
}

func Test_Open_Returns_Error_When_WAL_CRC_Mismatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	existing := newTestDoc(t, "Existing")
	writeTestDocFile(t, dir, existing)

	s := openTestStore(t, dir)
	_ = s.Close()

	doc := newTestDoc(t, "Invalid Footer")
	walPath := filepath.Join(dir, ".mddb", "wal")

	// Write valid WAL
	writeWalFile(t, walPath, []walRecord{makeWalPutRecord(doc)})

	// Corrupt the body
	data, err := os.ReadFile(walPath)
	if err != nil {
		t.Fatalf("read wal: %v", err)
	}

	if len(data) > 40 {
		data[10] ^= 0xFF
	}

	err = os.WriteFile(walPath, data, 0o600)
	if err != nil {
		t.Fatalf("write corrupted wal: %v", err)
	}

	_, err = mddb.Open(t.Context(), testConfig(dir))
	if err == nil {
		t.Fatal("expected error for CRC mismatch")
	}

	if !errors.Is(err, mddb.ErrWALCorrupt) {
		t.Fatalf("error = %v, want ErrWALCorrupt", err)
	}
}

func Test_Tx_Creates_Parent_Dirs_When_Put(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "New Doc")

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	result, err := tx.Create(doc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Verify file and parent dir exist
	absPath := filepath.Join(dir, result.DocPath)

	_, err = os.Stat(absPath)
	if err != nil {
		t.Fatalf("doc file missing: %v", err)
	}

	parentDir := filepath.Dir(absPath)

	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("parent dir missing: %v", err)
	}

	if !info.IsDir() {
		t.Fatal("parent path is not a directory")
	}
}

func Test_Tx_Removes_File_When_Delete(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := createTestDoc(t.Context(), t, s, newTestDoc(t, "To Delete"))

	absPath := filepath.Join(dir, doc.DocPath)

	_, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("doc file should exist: %v", err)
	}

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

	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("doc file should not exist: %v", err)
	}

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

func Test_WAL_Not_Applied_When_Rollback(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	doc, err := tx.Create(newTestDoc(t, "Will Rollback"))
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = tx.Rollback()
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	absPath := filepath.Join(dir, doc.DocPath)

	_, err = os.Stat(absPath)
	if !os.IsNotExist(err) {
		t.Fatalf("doc file should not exist: %v", err)
	}

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
