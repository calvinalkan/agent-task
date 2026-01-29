package mddb_test

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_Open_Creates_Schema_When_Directory_Empty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	mddbDir := filepath.Join(dir, ".mddb")

	info, err := os.Stat(mddbDir)
	if err != nil {
		t.Fatalf("stat .mddb: %v", err)
	}

	if !info.IsDir() {
		t.Fatal(".mddb is not a directory")
	}

	indexPath := filepath.Join(mddbDir, "index.sqlite")

	_, err = os.Stat(indexPath)
	if err != nil {
		t.Fatalf("stat index.sqlite: %v", err)
	}

	walPath := filepath.Join(mddbDir, "wal")

	_, err = os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	// Combined version = internal(1) * 10000 + user(1) = 10001
	if version != 10001 {
		t.Fatalf("user_version = %d, want 10001", version)
	}

	if !tableExists(t, db, testTableName) {
		t.Fatalf("%s table missing", testTableName)
	}
}

func Test_Open_Rebuilds_Index_When_Schema_Version_Mismatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Test Doc")
	writeTestDocFile(t, dir, doc)

	// Manually set wrong schema version
	db := openIndex(t, dir)

	_, err := db.Exec("PRAGMA user_version = 999")
	if err != nil {
		_ = db.Close()

		t.Fatalf("set user_version: %v", err)
	}

	_ = db.Close()

	// Reopen should rebuild
	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	// Verify doc is still accessible after rebuild
	got, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID() != doc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), doc.DocID)
	}

	db = openIndex(t, dir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	if version != 10001 {
		t.Fatalf("user_version = %d, want 10001", version)
	}
}

func Test_Close_Returns_Nil_When_Called_Multiple_Times(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	err := s.Close()
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

	var s *TestStore

	err := s.Close()
	if err != nil {
		t.Fatalf("close nil store: %v", err)
	}
}

func Test_Get_Returns_Doc_When_File_Exists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Test Doc")
	writeTestDocFile(t, dir, doc)

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	got, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID() != doc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), doc.DocID)
	}

	if got.Title() != "Test Doc" {
		t.Fatalf("title = %s, want Test Doc", got.Title())
	}

	if got.RelPath() != doc.DocPath {
		t.Fatalf("path = %s, want %s", got.RelPath(), doc.DocPath)
	}
}

func Test_Get_Returns_Error_When_File_Missing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "Missing")

	_, err := s.Get(t.Context(), doc.DocID)
	if err == nil {
		t.Fatal("expected error for missing doc")
	}

	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("error = %v, want contains 'not found'", err)
	}
}

func Test_Get_Returns_Error_When_ID_Is_Empty(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	_, err := s.Get(t.Context(), "")
	if err == nil {
		t.Fatal("expected error for empty ID")
	}
}

func Test_Get_Returns_Error_When_ID_Not_Found(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	// Non-existent ID
	_, err := s.Get(t.Context(), "nonexistent-id")
	if err == nil {
		t.Fatal("expected error for non-existent ID")
	}

	if !errors.Is(err, mddb.ErrNotFound) {
		t.Fatalf("error = %v, want %v", err, mddb.ErrNotFound)
	}
}

func Test_Get_Recovers_WAL_When_WAL_Appears_After_Open(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// Open store - WAL is empty at this point
	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "WAL Doc")

	// Write committed WAL while store is open (simulates crash mid-commit)
	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalFile(t, walPath, []walRecord{makeWalPutRecord(doc)})

	// Get should detect WAL, recover it, then return the doc
	got, err := s.Get(t.Context(), doc.DocID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	if got.ID() != doc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), doc.DocID)
	}

	if got.Title() != "WAL Doc" {
		t.Fatalf("title = %s, want WAL Doc", got.Title())
	}

	// Verify WAL was truncated after recovery
	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
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
