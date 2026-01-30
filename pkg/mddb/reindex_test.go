package mddb_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

const (
	reindexStatusOpen   = "open"
	reindexStatusClosed = "closed"
)

func Test_Reindex_Builds_SQLite_Index_When_Docs_Valid(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")

	writeTestDocFile(t, dir, docA)
	writeTestDocFile(t, dir, docB)

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	if indexed != 2 {
		t.Fatalf("indexed = %d, want 2", indexed)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	// Fingerprint-based version should be non-zero after reindex.
	if version == 0 {
		t.Fatal("user_version = 0, want non-zero fingerprint")
	}

	if count := countDocs(t, db); count != 2 {
		t.Fatalf("doc count = %d, want 2", count)
	}
}

func Test_Reindex_Binds_TextColumns_When_UsingBorrowedBytes(t *testing.T) {
	t.Parallel()

	// Guard: we rely on unsafe.String for TEXT binding (not BLOB) during inserts.
	// go-sqlite3 currently uses sqlite3_bind_text + SQLITE_TRANSIENT for strings.
	// If the driver ever changes this behavior, comparisons would break.
	dir := t.TempDir()

	doc := newTestDoc(t, "Alpha")
	writeTestDocFile(t, dir, doc)

	s := openTestStore(t, dir)
	defer func() { _ = s.Close() }()

	_, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	db := openIndex(t, dir)
	defer func() { _ = db.Close() }()

	var typeofID string
	row := db.QueryRow("SELECT typeof(id) FROM "+testTableName+" WHERE id = ?", doc.DocID)
	if err := row.Scan(&typeofID); err != nil {
		t.Fatalf("typeof(id): %v", err)
	}

	if typeofID != "text" {
		t.Fatalf("typeof(id) = %q, want text", typeofID)
	}

	var count int
	row = db.QueryRow("SELECT COUNT(*) FROM "+testTableName+" WHERE id = ?", doc.DocID)
	if err := row.Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}

	if count != 1 {
		t.Fatalf("count = %d, want 1", count)
	}
}

func Test_Reindex_Calls_AfterBulkIndex_When_Reindexing(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")
	docC := newTestDoc(t, "Gamma")

	writeTestDocFile(t, dir, docA)
	writeTestDocFile(t, dir, docB)
	writeTestDocFile(t, dir, docC)

	var (
		calls  int
		gotIDs []string
	)

	cfg := testConfig(dir)
	cfg.AfterBulkIndex = func(_ context.Context, _ *sql.Tx, batch []mddb.IndexableDocument) error {
		calls++

		for _, doc := range batch {
			gotIDs = append(gotIDs, string(append([]byte(nil), doc.ID...)))
		}

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Reset counters in case Open triggered a reindex.
	calls = 0
	gotIDs = nil

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	if indexed != 3 {
		t.Fatalf("indexed = %d, want 3", indexed)
	}

	if calls != 1 {
		t.Fatalf("after bulk index calls = %d, want 1", calls)
	}

	want := map[string]bool{
		docA.DocID: true,
		docB.DocID: true,
		docC.DocID: true,
	}

	for _, id := range gotIDs {
		delete(want, id)
	}

	if len(want) != 0 {
		t.Fatalf("after bulk index missing ids: %v", want)
	}
}

func Test_Reindex_Returns_Error_When_AfterBulkIndex_Fails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")

	seed := openTestStore(t, dir)
	createTestDoc(t.Context(), t, seed, docA)
	createTestDoc(t.Context(), t, seed, docB)

	if err := seed.Close(); err != nil {
		t.Fatalf("close seed: %v", err)
	}

	// Mutate files on disk without updating SQLite.
	docA.DocTitle = "Alpha Updated"
	docA.DocStatus = reindexStatusClosed
	writeRawDocFile(t, dir, docA.DocPath, docA)

	docB.DocTitle = "Beta Updated"
	docB.DocStatus = reindexStatusClosed
	writeRawDocFile(t, dir, docB.DocPath, docB)

	cfg := testConfig(dir)
	cfg.AfterBulkIndex = func(_ context.Context, _ *sql.Tx, _ []mddb.IndexableDocument) error {
		return errors.New("after bulk index failed")
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	_, err = s.Reindex(t.Context())
	if err == nil {
		t.Fatal("expected error from AfterBulkIndex")
	}

	type row struct {
		title  string
		status string
	}

	rows, err := mddb.Query(t.Context(), s, func(db *sql.DB) (map[string]row, error) {
		result := make(map[string]row)
		query := "SELECT id, title, status FROM " + testTableName + " WHERE id IN (?, ?)"

		sqlRows, qErr := db.Query(query, docA.DocID, docB.DocID)
		if qErr != nil {
			return nil, qErr
		}

		defer func() { _ = sqlRows.Close() }()

		for sqlRows.Next() {
			var id, title, status string

			scanErr := sqlRows.Scan(&id, &title, &status)
			if scanErr != nil {
				return nil, scanErr
			}

			result[id] = row{title: title, status: status}
		}

		rowsErr := sqlRows.Err()
		if rowsErr != nil {
			return nil, rowsErr
		}

		return result, nil
	})
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	gotA, ok := rows[docA.DocID]
	if !ok {
		t.Fatalf("missing row for %s", docA.DocID)
	}

	if gotA.title != "Alpha" || gotA.status != reindexStatusOpen {
		t.Fatalf("docA = %q/%q, want Alpha/open", gotA.title, gotA.status)
	}

	gotB, ok := rows[docB.DocID]
	if !ok {
		t.Fatalf("missing row for %s", docB.DocID)
	}

	if gotB.title != "Beta" || gotB.status != reindexStatusOpen {
		t.Fatalf("docB = %q/%q, want Beta/open", gotB.title, gotB.status)
	}
}

func Test_Open_Calls_AfterRecreateSchema_When_Schema_Version_Mismatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// First open without hook to create initial schema.
	s := openTestStore(t, dir)

	err := s.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	var calls int

	cfg := testConfig(dir)
	// Add a column to change schema fingerprint, forcing reindex on Open.
	cfg.SQLSchema = cfg.SQLSchema.Text("extra_col", false)
	cfg.SQLColumnValues = func(doc mddb.IndexableDocument) []any {
		status, _ := doc.Frontmatter.GetString([]byte("status"))
		priority, _ := doc.Frontmatter.GetInt([]byte("priority"))

		return []any{status, priority, string(doc.Body), ""}
	}
	cfg.AfterRecreateSchema = func(ctx context.Context, tx *sql.Tx) error {
		calls++

		_, execErr := tx.ExecContext(ctx, `CREATE TABLE related (
			doc_id TEXT PRIMARY KEY,
			extra TEXT NOT NULL
		)`)

		return execErr
	}

	s, err = mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	if calls != 1 {
		t.Fatalf("after recreate schema calls = %d, want 1", calls)
	}

	// Verify the related table was created.
	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	var tableName string

	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='related'").Scan(&tableName)
	if err != nil {
		t.Fatalf("query related table: %v", err)
	}

	if tableName != "related" {
		t.Fatalf("table name = %q, want related", tableName)
	}
}

func Test_Reindex_Calls_AfterRecreateSchema_When_Called_Explicitly(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	var calls int

	cfg := testConfig(dir)
	cfg.AfterRecreateSchema = func(ctx context.Context, tx *sql.Tx) error {
		calls++

		_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS related`)
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `CREATE TABLE related (
			doc_id TEXT PRIMARY KEY,
			extra TEXT NOT NULL
		)`)

		return err
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Open on fresh dir triggers reindex, so calls = 1.
	if calls != 1 {
		t.Fatalf("after open calls = %d, want 1", calls)
	}

	// Explicit Reindex should call hook again.
	_, err = s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	if calls != 2 {
		t.Fatalf("after reindex calls = %d, want 2", calls)
	}

	// Verify the related table exists.
	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	var tableName string

	err = db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='related'").Scan(&tableName)
	if err != nil {
		t.Fatalf("query related table: %v", err)
	}

	if tableName != "related" {
		t.Fatalf("table name = %q, want related", tableName)
	}
}

func Test_Reindex_Recreates_Related_Tables_When_Schema_Changes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Alpha")

	cfg := testConfig(dir)
	cfg.AfterRecreateSchema = func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS related`)
		if err != nil {
			return err
		}

		_, err = tx.ExecContext(ctx, `CREATE TABLE related (
			doc_id TEXT PRIMARY KEY,
			extra TEXT NOT NULL
		)`)

		return err
	}

	// First open - creates tables via reindex (fresh dir).
	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	// Create doc via transaction.
	tx, err := s.Begin(t.Context())
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	_, err = tx.Create(doc)
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	err = tx.Commit(t.Context())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Insert data into related table.
	_, err = mddb.Query(t.Context(), s, func(db *sql.DB) (any, error) {
		_, execErr := db.Exec("INSERT INTO related (doc_id, extra) VALUES (?, ?)", doc.DocID, "test-data")

		return nil, execErr
	})
	if err != nil {
		t.Fatalf("insert related: %v", err)
	}

	err = s.Close()
	if err != nil {
		t.Fatalf("close: %v", err)
	}

	// Add a column to change schema fingerprint, forcing recreate.
	cfg.SQLSchema = cfg.SQLSchema.Text("extra_col", false)
	cfg.SQLColumnValues = func(doc mddb.IndexableDocument) []any {
		status, _ := doc.Frontmatter.GetString([]byte("status"))
		priority, _ := doc.Frontmatter.GetInt([]byte("priority"))

		return []any{status, priority, string(doc.Body), ""}
	}

	s, err = mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open v2: %v", err)
	}

	defer func() { _ = s.Close() }()

	// Verify related table exists but is empty (recreated).
	count, err := mddb.Query(t.Context(), s, func(db *sql.DB) (int, error) {
		var n int

		scanErr := db.QueryRow("SELECT COUNT(*) FROM related").Scan(&n)

		return n, scanErr
	})
	if err != nil {
		t.Fatalf("count related: %v", err)
	}

	if count != 0 {
		t.Fatalf("related count = %d, want 0 (table should be recreated empty)", count)
	}
}

func Test_Reindex_Returns_Error_When_AfterRecreateSchema_Fails(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Alpha")
	writeTestDocFile(t, dir, doc)

	// First open to create initial schema.
	s := openTestStore(t, dir)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Capture original version (fingerprint of original schema).
	db := openIndex(t, dir)

	originalVersion, err := userVersion(t.Context(), db)
	if err != nil {
		_ = db.Close()

		t.Fatalf("user_version: %v", err)
	}

	_ = db.Close()

	// Reopen with modified schema to force reindex, but AfterRecreateSchema fails.
	cfg := testConfig(dir)
	cfg.SQLSchema = cfg.SQLSchema.Text("extra_col", false)
	cfg.SQLColumnValues = func(doc mddb.IndexableDocument) []any {
		status, _ := doc.Frontmatter.GetString([]byte("status"))
		priority, _ := doc.Frontmatter.GetInt([]byte("priority"))

		return []any{status, priority, string(doc.Body), ""}
	}
	cfg.AfterRecreateSchema = func(_ context.Context, _ *sql.Tx) error {
		return errors.New("after recreate schema failed")
	}

	_, err = mddb.Open(t.Context(), cfg)
	if err == nil {
		t.Fatal("expected error from AfterRecreateSchema")
	}

	if !strings.Contains(err.Error(), "after recreate schema") {
		t.Fatalf("error = %q, want to contain 'after recreate schema'", err.Error())
	}

	// Verify the schema version was NOT updated (tx rolled back).
	db = openIndex(t, dir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	// Should still have original fingerprint, not new schema's fingerprint.
	if version != originalVersion {
		t.Fatalf("user_version = %d, want %d (rollback should preserve old version)", version, originalVersion)
	}
}

func Test_Reindex_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "WAL Doc")

	// Write WAL after store is open
	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalFile(t, walPath, []walRecord{makeWalPutRecord(doc)})

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
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
	absPath := filepath.Join(dir, doc.DocPath)

	_, err = os.Stat(absPath)
	if err != nil {
		t.Fatalf("doc file not found: %v", err)
	}

	db := openIndex(t, dir)
	t.Cleanup(func() { _ = db.Close() })

	if count := countDocs(t, db); count != 1 {
		t.Fatalf("doc count = %d, want 1", count)
	}
}

func Test_Reindex_Reports_Error_When_Path_Mismatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	// Create valid doc
	validDoc := createTestDoc(t.Context(), t, s, newTestDoc(t, "Valid"))

	err := s.Close()
	if err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Write a doc file at wrong path (bypassing store)
	orphanDoc := newTestDoc(t, "Orphan")
	orphanRel := filepath.Join("2026", "01-21", "orphan.md")
	writeRawDocFile(t, dir, orphanRel, orphanDoc)

	s = openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err == nil {
		t.Fatal("expected reindex error for orphan")
	}

	var scanErr *mddb.IndexScanError
	if !errors.As(err, &scanErr) {
		t.Fatalf("error = %v, want *IndexScanError", err)
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	// Verify original doc still accessible
	got, err := s.Get(t.Context(), validDoc.DocID)
	if err != nil {
		t.Fatalf("get valid doc: %v", err)
	}

	if got.ID() != validDoc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), validDoc.DocID)
	}
}

func Test_Reindex_Returns_Context_Error_When_Canceled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	indexed, err := s.Reindex(ctx)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func Test_Reindex_Allows_Schema_Mismatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	// Create valid doc
	validDoc := createTestDoc(t.Context(), t, s, newTestDoc(t, "Valid"))

	err := s.Close()
	if err != nil {
		t.Fatalf("close store: %v", err)
	}

	// Write doc with wrong schema version
	badDoc := newTestDoc(t, "Bad Schema")
	badContent := makeDocContent(badDoc)
	badContent = strings.Replace(badContent, fmt.Sprintf("schema_version: %d\n", testSchemaVersion), "schema_version: 99999\n", 1)
	writeRawPath(t, dir, badDoc.DocPath, badContent)

	s = openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	if indexed != 2 {
		t.Fatalf("indexed = %d, want 2", indexed)
	}

	// Verify original doc still accessible
	got, err := s.Get(t.Context(), validDoc.DocID)
	if err != nil {
		t.Fatalf("get valid doc: %v", err)
	}

	if got.ID() != validDoc.DocID {
		t.Fatalf("id = %s, want %s", got.ID(), validDoc.DocID)
	}

	gotBad, err := s.Get(t.Context(), badDoc.DocID)
	if err != nil {
		t.Fatalf("get bad doc: %v", err)
	}

	if gotBad.ID() != badDoc.DocID {
		t.Fatalf("id = %s, want %s", gotBad.ID(), badDoc.DocID)
	}
}

func Test_Reindex_Skips_Files_When_In_Mddb_Directory(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	internalPath := filepath.Join(".mddb", "ignored.md")
	writeRawPath(t, dir, internalPath, "---\nnot: valid\n---\n")

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	indexed, err := s.Reindex(t.Context())
	if err != nil {
		t.Fatalf("reindex: %v", err)
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	if count := countDocs(t, db); count != 0 {
		t.Fatalf("doc count = %d, want 0", count)
	}
}

// makeDocContent creates valid doc file content.
func makeDocContent(doc *TestDoc) string {
	return fmt.Sprintf(`---
id: %s
schema_version: %d
title: %s
status: %s
priority: %d
---
`,
		doc.DocID,
		testSchemaVersion,
		doc.DocTitle,
		doc.DocStatus,
		doc.DocPriority,
	)
}

// writeRawDocFile writes a doc at a specific path with proper frontmatter.
func writeRawDocFile(t *testing.T, root, relPath string, doc *TestDoc) {
	t.Helper()
	writeRawPath(t, root, relPath, makeDocContent(doc))
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
