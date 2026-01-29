package mddb_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/calvinalkan/agent-task/pkg/mddb"
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

	if version != 10001 {
		t.Fatalf("user_version = %d, want 10001", version)
	}

	if count := countDocs(t, db); count != 2 {
		t.Fatalf("doc count = %d, want 2", count)
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
	validDoc := putTestDoc(t.Context(), t, s, newTestDoc(t, "Valid"))

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

	if !errors.Is(err, mddb.ErrIndexScan) {
		t.Fatalf("error = %v, want ErrIndexScan", err)
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
	validDoc := putTestDoc(t.Context(), t, s, newTestDoc(t, "Valid"))

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
