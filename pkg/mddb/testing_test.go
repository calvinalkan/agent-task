package mddb_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/calvinalkan/agent-task/pkg/mddb"
	"github.com/calvinalkan/agent-task/pkg/mddb/frontmatter"
)

// -----------------------------------------------------------------------------
// TestDoc: minimal Document implementation for tests
// -----------------------------------------------------------------------------

type TestDoc struct {
	DocID       string `json:"id"`
	DocShort    string `json:"short_id"`
	DocPath     string `json:"path"`
	DocMtime    int64  `json:"mtime_ns"`
	DocTitle    string `json:"title"`
	DocStatus   string `json:"status"`
	DocPriority int64  `json:"priority"`
	DocBody     string `json:"body"`
}

func (d TestDoc) ID() string      { return d.DocID }
func (d TestDoc) RelPath() string { return d.DocPath }
func (d TestDoc) ShortID() string { return d.DocShort }
func (d TestDoc) MtimeNS() int64  { return d.DocMtime }
func (d TestDoc) Title() string   { return d.DocTitle }
func (d TestDoc) Body() string    { return d.DocBody }
func (TestDoc) Validate() error   { return nil }
func (d TestDoc) Frontmatter() frontmatter.Frontmatter {
	return frontmatter.Frontmatter{
		"title":    frontmatter.String(d.DocTitle),
		"status":   frontmatter.String(d.DocStatus),
		"priority": frontmatter.Int(d.DocPriority),
	}
}

// newTestDoc creates a TestDoc with generated ID.
func newTestDoc(t *testing.T, title string) *TestDoc {
	t.Helper()

	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("new DocID: %v", err)
	}

	return &TestDoc{
		DocID:       id.String(),
		DocShort:    shortIDFromUUID(id),
		DocPath:     pathFromID(id),
		DocTitle:    title,
		DocStatus:   "open",
		DocPriority: 2,
	}
}

// ID derivation (test-only, mirrors what a real consumer would do)

const (
	shortIDLength  = 12
	crockfordBase  = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	pathDateLayout = "2006/01-02"
)

func shortIDFromUUID(id uuid.UUID) string {
	randA := (uint16(id[6]&0x0f) << 8) | uint16(id[7])
	randB := (uint64(id[8]&0x3f) << 56) |
		(uint64(id[9]) << 48) |
		(uint64(id[10]) << 40) |
		(uint64(id[11]) << 32) |
		(uint64(id[12]) << 24) |
		(uint64(id[13]) << 16) |
		(uint64(id[14]) << 8) |
		uint64(id[15])
	top60 := (uint64(randA) << 48) | (randB >> 14)

	var buf [shortIDLength]byte
	for i := shortIDLength - 1; i >= 0; i-- {
		buf[i] = crockfordBase[top60&0x1f]
		top60 >>= 5
	}

	return string(buf[:])
}

func pathFromID(id uuid.UUID) string {
	short := shortIDFromUUID(id)
	sec, nsec := id.Time().UnixTime()
	t := time.Unix(sec, nsec).UTC()

	return filepath.Join(t.Format(pathDateLayout), short+".md")
}

// -----------------------------------------------------------------------------
// Config callbacks for TestDoc
// -----------------------------------------------------------------------------

func parseTestDoc(idStr string, fm frontmatter.Frontmatter, body string, mtimeNS int64) (*TestDoc, error) {
	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse id: %w", err)
	}

	title, _ := fm.GetString("title")
	status, _ := fm.GetString("status")
	priority, _ := fm.GetInt("priority")

	return &TestDoc{
		DocID:       idStr,
		DocShort:    shortIDFromUUID(id),
		DocPath:     pathFromID(id),
		DocMtime:    mtimeNS,
		DocTitle:    title,
		DocStatus:   status,
		DocPriority: priority,
		DocBody:     body,
	}, nil
}

func recreateTestIndex(ctx context.Context, tx *sql.Tx, tableName string) error {
	stmts := []string{
		"DROP TABLE IF EXISTS " + tableName,
		fmt.Sprintf(`CREATE TABLE %s (
			id TEXT PRIMARY KEY,
			short_id TEXT NOT NULL,
			path TEXT NOT NULL,
			mtime_ns INTEGER NOT NULL,
			title TEXT NOT NULL,
			status TEXT NOT NULL,
			priority INTEGER NOT NULL,
			body TEXT NOT NULL
		) WITHOUT ROWID`, tableName),
		fmt.Sprintf("CREATE INDEX idx_%s_short_id ON %s(short_id)", tableName, tableName),
	}

	for _, stmt := range stmts {
		_, err := tx.ExecContext(ctx, stmt)
		if err != nil {
			return err
		}
	}

	return nil
}

type testDocStmts struct {
	insert *sql.Stmt
	del    *sql.Stmt
}

func prepareTestDocStmts(ctx context.Context, tx *sql.Tx, tableName string) (mddb.PreparedStatements[TestDoc], error) {
	insert, err := tx.PrepareContext(ctx, fmt.Sprintf(`
		INSERT OR REPLACE INTO %s (id, short_id, path, mtime_ns, title, status, priority, body)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, tableName))
	if err != nil {
		return nil, err
	}

	success := false

	defer func() {
		if !success {
			_ = insert.Close()
		}
	}()

	del, err := tx.PrepareContext(ctx, fmt.Sprintf("DELETE FROM %s WHERE id = ?", tableName))
	if err != nil {
		return nil, err
	}

	success = true

	return &testDocStmts{insert: insert, del: del}, nil
}

func (s *testDocStmts) Upsert(ctx context.Context, doc *TestDoc) error {
	_, err := s.insert.ExecContext(ctx,
		doc.DocID, doc.DocShort, doc.DocPath, doc.DocMtime,
		doc.DocTitle, doc.DocStatus, doc.DocPriority, doc.DocBody)

	return err
}

func (s *testDocStmts) Delete(ctx context.Context, id string) error {
	_, err := s.del.ExecContext(ctx, id)

	return err
}

func (s *testDocStmts) Close() error {
	return errors.Join(s.insert.Close(), s.del.Close())
}

// -----------------------------------------------------------------------------
// Test shims: wrap new generic API for existing tests
// -----------------------------------------------------------------------------

const testTableName = "docs"

// TestStore is a type alias for tests.
type TestStore = mddb.MDDB[TestDoc]

type testOpts struct {
	lockTimeout time.Duration
}

type testOpt func(*testOpts)

func withTestLockTimeout() testOpt {
	return func(o *testOpts) { o.lockTimeout = 10 * time.Millisecond }
}

func testConfig(dir string, opts ...testOpt) mddb.Config[TestDoc] {
	o := testOpts{}
	for _, opt := range opts {
		opt(&o)
	}

	return mddb.Config[TestDoc]{
		Dir:           dir,
		TableName:     testTableName,
		LockTimeout:   o.lockTimeout,
		SchemaVersion: 1,
		Parse:         parseTestDoc,
		RecreateIndex: recreateTestIndex,
		Prepare:       prepareTestDocStmts,
	}
}

// openTestStore opens a store with TestDoc config.
func openTestStore(t *testing.T, dir string, opts ...testOpt) *mddb.MDDB[TestDoc] {
	t.Helper()

	s, err := mddb.Open(t.Context(), testConfig(dir, opts...))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	return s
}

// putTestDoc creates a transaction, puts a doc, and commits.
func putTestDoc(ctx context.Context, t *testing.T, s *mddb.MDDB[TestDoc], doc *TestDoc) *TestDoc {
	t.Helper()

	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	result, err := tx.Put(doc)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	return result
}

// writeTestDocFile writes a doc to disk via store transaction.
func writeTestDocFile(t *testing.T, dir string, doc *TestDoc) {
	t.Helper()

	s := openTestStore(t, dir)
	putTestDoc(t.Context(), t, s, doc)

	err := s.Close()
	if err != nil {
		t.Fatalf("close store: %v", err)
	}
}

// -----------------------------------------------------------------------------
// Generic test helpers
// -----------------------------------------------------------------------------

func openIndex(t *testing.T, dir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", filepath.Join(dir, ".mddb", "index.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	return db
}

func countDocs(t *testing.T, db *sql.DB) int {
	t.Helper()

	row := db.QueryRow("SELECT COUNT(*) FROM " + testTableName)

	var count int

	err := row.Scan(&count)
	if err != nil {
		t.Fatalf("count docs: %v", err)
	}

	return count
}

func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, err
	}

	return version, nil
}

func readFileString(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}

	return string(data)
}

// -----------------------------------------------------------------------------
// WAL test helpers
// -----------------------------------------------------------------------------

type walRecord struct {
	Op      string `json:"op"`
	ID      string `json:"id"`
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
}

const (
	testWalMagic      = "MDDB0001"
	testWalFooterSize = 32
)

const testSchemaVersion = 10001

var testWalCRC32C = crc32.MakeTable(crc32.Castagnoli)

func writeWalFile(t *testing.T, path string, ops []walRecord) {
	t.Helper()

	body, err := encodeWalOps(ops)
	if err != nil {
		t.Fatalf("encode wal ops: %v", err)
	}

	footer := encodeWalFooter(body)

	err = os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		t.Fatalf("mkdir wal dir: %v", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	_, err = file.Write(body)
	if err != nil {
		t.Fatalf("write wal DocBody: %v", err)
	}

	_, err = file.Write(footer)
	if err != nil {
		t.Fatalf("write wal footer: %v", err)
	}

	err = file.Sync()
	if err != nil {
		t.Fatalf("sync wal: %v", err)
	}
}

func writeWalBodyOnly(t *testing.T, path string, ops []walRecord) {
	t.Helper()

	body, err := encodeWalOps(ops)
	if err != nil {
		t.Fatalf("encode wal ops: %v", err)
	}

	err = os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		t.Fatalf("mkdir wal dir: %v", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	_, err = file.Write(body)
	if err != nil {
		t.Fatalf("write wal DocBody: %v", err)
	}

	err = file.Sync()
	if err != nil {
		t.Fatalf("sync wal: %v", err)
	}
}

func writeWalWithBody(t *testing.T, path string, body []byte) {
	t.Helper()

	footer := encodeWalFooter(body)

	err := os.MkdirAll(filepath.Dir(path), 0o750)
	if err != nil {
		t.Fatalf("mkdir wal dir: %v", err)
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	_, err = file.Write(body)
	if err != nil {
		t.Fatalf("write wal DocBody: %v", err)
	}

	_, err = file.Write(footer)
	if err != nil {
		t.Fatalf("write wal footer: %v", err)
	}

	err = file.Sync()
	if err != nil {
		t.Fatalf("sync wal: %v", err)
	}
}

func encodeWalOps(ops []walRecord) ([]byte, error) {
	var buf strings.Builder

	enc := json.NewEncoder(&buf)
	for _, op := range ops {
		err := enc.Encode(op)
		if err != nil {
			return nil, err
		}
	}

	return []byte(buf.String()), nil
}

func encodeWalFooter(body []byte) []byte {
	buf := make([]byte, testWalFooterSize)
	copy(buf[:8], testWalMagic)

	bodyLen := uint64(len(body))
	binary.LittleEndian.PutUint64(buf[8:16], bodyLen)
	binary.LittleEndian.PutUint64(buf[16:24], ^bodyLen)

	crc := crc32.Checksum(body, testWalCRC32C)
	binary.LittleEndian.PutUint32(buf[24:28], crc)
	binary.LittleEndian.PutUint32(buf[28:32], ^crc)

	return buf
}

func makeWalPutRecord(doc *TestDoc) walRecord {
	return walRecord{Op: "put", ID: doc.DocID, Path: doc.DocPath, Content: renderDocContent(doc)}
}

func makeWalDeleteRecord(id string, path string) walRecord {
	return walRecord{Op: "delete", ID: id, Path: path}
}

func renderDocContent(doc *TestDoc) string {
	fm := frontmatter.Frontmatter{
		"id":             frontmatter.String(doc.DocID),
		"schema_version": frontmatter.Int(testSchemaVersion),
		"title":          frontmatter.String(doc.DocTitle),
		"status":         frontmatter.String(doc.DocStatus),
		"priority":       frontmatter.Int(doc.DocPriority),
	}

	yamlStr, err := fm.MarshalYAML()
	if err != nil {
		panic(err)
	}

	var b strings.Builder
	b.WriteString(yamlStr)

	if doc.DocBody != "" {
		b.WriteString("\n")
		b.WriteString(doc.DocBody)

		if !strings.HasSuffix(doc.DocBody, "\n") {
			b.WriteString("\n")
		}
	}

	return b.String()
}
