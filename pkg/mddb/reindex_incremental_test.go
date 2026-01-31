package mddb_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/mddb"
)

func Test_ReindexIncremental_Returns_No_Changes_When_Files_Unmodified(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")

	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, docA)
	createTestDoc(t.Context(), t, s, docB)
	_ = s.Close()

	calls := 0
	cfg := testConfig(dir)
	cfg.AfterIncrementalIndex = func(_ context.Context, _ *sql.Tx, _ []mddb.IndexableDocument, _ []string) error {
		calls++

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("result = %+v, want no changes", result)
	}

	if result.Skipped != 2 {
		t.Fatalf("skipped = %d, want 2", result.Skipped)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	if count := countDocs(t, db); count != result.Total {
		t.Fatalf("total = %d, want %d", result.Total, count)
	}

	if calls != 0 {
		t.Fatalf("after incremental index calls = %d, want 0", calls)
	}
}

func Test_ReindexIncremental_Updates_Inserts_Deletes_When_Files_Change(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")
	docC := newTestDoc(t, "Gamma")

	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, docA)
	createTestDoc(t.Context(), t, s, docB)
	createTestDoc(t.Context(), t, s, docC)
	_ = s.Close()

	// Update docB on disk.
	docBUpdated := *docB
	docBUpdated.DocTitle = "Beta Updated"
	docBUpdated.DocStatus = reindexStatusClosed
	writeRawDocFile(t, dir, docBUpdated.DocPath, &docBUpdated)

	// Delete docC on disk.
	absC := filepath.Join(dir, docC.DocPath)
	if err := os.Remove(absC); err != nil {
		t.Fatalf("remove docC: %v", err)
	}

	// Add new docD on disk.
	docD := newTestDoc(t, "Delta")
	writeRawDocFile(t, dir, docD.DocPath, docD)

	var (
		gotUpserts []string
		gotDeletes []string
	)

	cfg := testConfig(dir)
	cfg.AfterIncrementalIndex = func(_ context.Context, _ *sql.Tx, upserts []mddb.IndexableDocument, deleted []string) error {
		for _, doc := range upserts {
			gotUpserts = append(gotUpserts, string(append([]byte(nil), doc.ID...)))
		}

		gotDeletes = append(gotDeletes, deleted...)

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Inserted != 1 || result.Updated != 1 || result.Deleted != 1 || result.Skipped != 1 {
		t.Fatalf("result = %+v, want inserted=1 updated=1 deleted=1 skipped=1", result)
	}

	if result.Total != 3 {
		t.Fatalf("total = %d, want 3", result.Total)
	}

	wantUpserts := map[string]bool{
		docB.DocID: true,
		docD.DocID: true,
	}
	for _, id := range gotUpserts {
		delete(wantUpserts, id)
	}

	if len(wantUpserts) != 0 {
		t.Fatalf("after incremental index missing upserts: %v", wantUpserts)
	}

	if len(gotDeletes) != 1 || gotDeletes[0] != docC.DocID {
		t.Fatalf("after incremental index deletes = %v, want %s", gotDeletes, docC.DocID)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	if count := countDocs(t, db); count != result.Total {
		t.Fatalf("total = %d, want %d", result.Total, count)
	}

	// docB updated
	var title string

	row := db.QueryRow("SELECT title FROM "+testTableName+" WHERE id = ?", docB.DocID)
	if err := row.Scan(&title); err != nil {
		t.Fatalf("query docB: %v", err)
	}

	if title != docBUpdated.DocTitle {
		t.Fatalf("docB title = %q, want %q", title, docBUpdated.DocTitle)
	}

	// docC deleted
	row = db.QueryRow("SELECT COUNT(*) FROM "+testTableName+" WHERE id = ?", docC.DocID)

	var count int
	if err := row.Scan(&count); err != nil {
		t.Fatalf("query docC: %v", err)
	}

	if count != 0 {
		t.Fatalf("docC count = %d, want 0", count)
	}

	// size_bytes for docB and docD
	assertSize := func(doc *TestDoc) {
		t.Helper()

		absPath := filepath.Join(dir, doc.DocPath)

		info, err := os.Stat(absPath)
		if err != nil {
			t.Fatalf("stat %s: %v", absPath, err)
		}

		var size int64

		row := db.QueryRow("SELECT size_bytes FROM "+testTableName+" WHERE id = ?", doc.DocID)
		if err := row.Scan(&size); err != nil {
			t.Fatalf("query size_bytes for %s: %v", doc.DocID, err)
		}

		if size != info.Size() {
			t.Fatalf("size_bytes for %s = %d, want %d", doc.DocID, size, info.Size())
		}
	}

	assertSize(&docBUpdated)
	assertSize(docD)
}

func Test_ReindexIncremental_Returns_Error_When_Path_Mismatches(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	validDoc := createTestDoc(t.Context(), t, s, newTestDoc(t, "Valid"))
	if err := s.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	orphan := newTestDoc(t, "Orphan")
	orphan.DocPath = filepath.Join("2026", "01-21", "orphan.md")
	writeRawDocFile(t, dir, orphan.DocPath, orphan)

	s = openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	_, err := s.ReindexIncremental(t.Context())
	if err == nil {
		t.Fatal("expected error")
	}

	var scanErr *mddb.IndexScanError
	if !errors.As(err, &scanErr) {
		t.Fatalf("error = %v, want *IndexScanError", err)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	if count := countDocs(t, db); count != 1 {
		t.Fatalf("doc count = %d, want 1", count)
	}

	if got, getErr := s.Get(t.Context(), validDoc.DocID); getErr != nil || got.ID() != validDoc.DocID {
		t.Fatalf("get valid doc failed: %v", getErr)
	}
}

func Test_ReindexIncremental_Returns_Error_When_AfterIncrementalIndex_Fails(t *testing.T) {
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

	// Update docA on disk.
	docAUpdated := *docA
	docAUpdated.DocTitle = "Alpha Modified"
	docAUpdated.DocStatus = reindexStatusClosed
	writeRawDocFile(t, dir, docAUpdated.DocPath, &docAUpdated)

	cfg := testConfig(dir)
	cfg.AfterIncrementalIndex = func(_ context.Context, _ *sql.Tx, _ []mddb.IndexableDocument, _ []string) error {
		return errors.New("after incremental index failed")
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	_, err = s.ReindexIncremental(t.Context())
	if err == nil {
		t.Fatal("expected error from AfterIncrementalIndex")
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	var title string

	row := db.QueryRow("SELECT title FROM "+testTableName+" WHERE id = ?", docA.DocID)
	if scanErr := row.Scan(&title); scanErr != nil {
		t.Fatalf("query docA: %v", scanErr)
	}

	if title != "Alpha" {
		t.Fatalf("docA title = %q, want Alpha", title)
	}
}

func Test_ReindexIncremental_Calls_AfterDelete_When_File_Removed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")

	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, docA)
	createTestDoc(t.Context(), t, s, docB)
	_ = s.Close()

	absB := filepath.Join(dir, docB.DocPath)
	if err := os.Remove(absB); err != nil {
		t.Fatalf("remove docB: %v", err)
	}

	var deleted []string

	cfg := testConfig(dir)
	cfg.AfterDelete = func(_ context.Context, _ *sql.Tx, id string) error {
		deleted = append(deleted, id)

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.Deleted)
	}

	if len(deleted) != 1 || deleted[0] != docB.DocID {
		t.Fatalf("after delete ids = %v, want %s", deleted, docB.DocID)
	}
}

func Test_ReindexIncremental_Calls_AfterIncrementalIndex_With_Empty_Deletes_When_Upserts_Only(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")

	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, docA)
	_ = s.Close()

	updated := *docA
	updated.DocTitle = "Alpha Changed"
	writeRawDocFile(t, dir, updated.DocPath, &updated)

	var (
		gotUpserts []string
		gotDeletes []string
	)

	cfg := testConfig(dir)
	cfg.AfterIncrementalIndex = func(_ context.Context, _ *sql.Tx, upserts []mddb.IndexableDocument, deleted []string) error {
		for _, doc := range upserts {
			gotUpserts = append(gotUpserts, string(append([]byte(nil), doc.ID...)))
		}

		gotDeletes = append(gotDeletes, deleted...)

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Updated != 1 || result.Inserted != 0 || result.Deleted != 0 {
		t.Fatalf("result = %+v, want updated=1", result)
	}

	if len(gotUpserts) != 1 || gotUpserts[0] != docA.DocID {
		t.Fatalf("upserts = %v, want %s", gotUpserts, docA.DocID)
	}

	if len(gotDeletes) != 0 {
		t.Fatalf("deletes = %v, want empty", gotDeletes)
	}
}

func Test_ReindexIncremental_Calls_AfterIncrementalIndex_With_Empty_Upserts_When_Deletes_Only(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	docA := newTestDoc(t, "Alpha")
	docB := newTestDoc(t, "Beta")

	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, docA)
	createTestDoc(t.Context(), t, s, docB)
	_ = s.Close()

	absB := filepath.Join(dir, docB.DocPath)
	if err := os.Remove(absB); err != nil {
		t.Fatalf("remove docB: %v", err)
	}

	var (
		gotUpserts []string
		gotDeletes []string
	)

	cfg := testConfig(dir)
	cfg.AfterIncrementalIndex = func(_ context.Context, _ *sql.Tx, upserts []mddb.IndexableDocument, deleted []string) error {
		for _, doc := range upserts {
			gotUpserts = append(gotUpserts, string(append([]byte(nil), doc.ID...)))
		}

		gotDeletes = append(gotDeletes, deleted...)

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Deleted != 1 || result.Inserted != 0 || result.Updated != 0 {
		t.Fatalf("result = %+v, want deleted=1", result)
	}

	if len(gotUpserts) != 0 {
		t.Fatalf("upserts = %v, want empty", gotUpserts)
	}

	if len(gotDeletes) != 1 || gotDeletes[0] != docB.DocID {
		t.Fatalf("deletes = %v, want %s", gotDeletes, docB.DocID)
	}
}

func Test_ReindexIncremental_Returns_Error_When_Context_Canceled(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, newTestDoc(t, "Alpha"))
	_ = s.Close()

	s = openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := s.ReindexIncremental(ctx)
	if err == nil {
		t.Fatal("expected error for canceled context")
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func Test_ReindexIncremental_Skips_Internal_Mddb_Files_When_Present(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)
	_ = s.Close()

	internalPath := filepath.Join(".mddb", "ignored.md")
	writeRawPath(t, dir, internalPath, "---\nnot: valid\n---\n")

	s = openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Total != 0 || result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want all zeros", result)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	if count := countDocs(t, db); count != 0 {
		t.Fatalf("doc count = %d, want 0", count)
	}
}

func Test_ReindexIncremental_Does_Not_Call_AfterRecreateSchema_When_Incremental(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Alpha")
	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, doc)
	_ = s.Close()

	calls := 0
	cfg := testConfig(dir)
	cfg.AfterRecreateSchema = func(_ context.Context, _ *sql.Tx) error {
		calls++

		return nil
	}

	s, err := mddb.Open(t.Context(), cfg)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	defer func() { _ = s.Close() }()

	calls = 0

	_, err = s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if calls != 0 {
		t.Fatalf("after recreate schema calls = %d, want 0", calls)
	}
}

func Test_ReindexIncremental_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	s := openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	doc := newTestDoc(t, "WAL Doc")

	walPath := filepath.Join(dir, ".mddb", "wal")
	writeWalFile(t, walPath, []walRecord{makeWalPutRecord(doc)})

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Total != 1 || result.Skipped != 1 || result.Inserted != 0 || result.Updated != 0 || result.Deleted != 0 {
		t.Fatalf("result = %+v, want total=1 skipped=1", result)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(dir, doc.DocPath)
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("doc file not found: %v", err)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	if count := countDocs(t, db); count != 1 {
		t.Fatalf("doc count = %d, want 1", count)
	}
}

func Test_ReindexIncremental_Updates_When_Size_Changes_With_Same_Mtime(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	doc := newTestDoc(t, "Alpha")
	s := openTestStore(t, dir)
	createTestDoc(t.Context(), t, s, doc)
	_ = s.Close()

	absPath := filepath.Join(dir, doc.DocPath)

	info, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat original: %v", err)
	}

	originalMtime := info.ModTime()

	original := readFileString(t, absPath)
	updatedContent := original + "\nextra content\n"
	writeRawPath(t, dir, doc.DocPath, updatedContent)

	if chtimesErr := os.Chtimes(absPath, originalMtime, originalMtime); chtimesErr != nil {
		t.Fatalf("chtimes: %v", chtimesErr)
	}

	s = openTestStore(t, dir)

	defer func() { _ = s.Close() }()

	result, err := s.ReindexIncremental(t.Context())
	if err != nil {
		t.Fatalf("reindex incremental: %v", err)
	}

	if result.Updated != 1 || result.Inserted != 0 || result.Deleted != 0 || result.Skipped != 0 {
		t.Fatalf("result = %+v, want updated=1", result)
	}

	db := openIndex(t, dir)

	defer func() { _ = db.Close() }()

	info, err = os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat updated: %v", err)
	}

	var size int64

	row := db.QueryRow("SELECT size_bytes FROM "+testTableName+" WHERE id = ?", doc.DocID)
	if err := row.Scan(&size); err != nil {
		t.Fatalf("query size_bytes: %v", err)
	}

	if size != info.Size() {
		t.Fatalf("size_bytes = %d, want %d", size, info.Size())
	}
}
