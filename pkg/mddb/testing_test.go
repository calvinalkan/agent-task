package mddb_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
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

func (d TestDoc) ID() string    { return d.DocID }
func (d TestDoc) Title() string { return d.DocTitle }
func (d TestDoc) Body() string  { return d.DocBody }
func (d TestDoc) Frontmatter() frontmatter.Frontmatter {
	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("status"), frontmatter.StringValue(d.DocStatus))
	fm.MustSet([]byte("priority"), frontmatter.IntValue(d.DocPriority))

	return fm
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

func documentFromTestDoc(doc mddb.IndexableDocument) (*TestDoc, error) {
	idStr := string(doc.ID)

	id, err := uuid.Parse(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse id: %w", err)
	}

	status, _ := doc.Frontmatter.GetString([]byte("status"))
	priority, _ := doc.Frontmatter.GetInt([]byte("priority"))

	return &TestDoc{
		DocID:       idStr,
		DocShort:    shortIDFromUUID(id),
		DocPath:     pathFromID(id),
		DocMtime:    doc.MtimeNS,
		DocTitle:    string(doc.Title),
		DocStatus:   status,
		DocPriority: priority,
		DocBody:     string(doc.Body),
	}, nil
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
		BaseDir:      dir,
		DocumentFrom: documentFromTestDoc,
		LockTimeout:  o.lockTimeout,
		SQLSchema: mddb.NewBaseSQLSchema(testTableName).
			Text("status", true).
			Int("priority", true).
			Text("body", false),
		RelPathFromID: func(id string) string {
			uid, err := uuid.Parse(id)
			if err != nil {
				return ""
			}

			return pathFromID(uid)
		},
		ShortIDFromID: func(id string) string {
			uid, err := uuid.Parse(id)
			if err != nil {
				return ""
			}

			return shortIDFromUUID(uid)
		},
		SQLColumnValues: func(doc mddb.IndexableDocument) []any {
			status, _ := doc.Frontmatter.GetString([]byte("status"))
			priority, _ := doc.Frontmatter.GetInt([]byte("priority"))

			return []any{status, priority, string(doc.Body)}
		},
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

// createTestDoc creates a transaction, creates a doc, and commits.
func createTestDoc(ctx context.Context, t *testing.T, s *mddb.MDDB[TestDoc], doc *TestDoc) *TestDoc {
	t.Helper()

	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	result, err := tx.Create(doc)
	if err != nil {
		t.Fatalf("create: %v", err)
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
	createTestDoc(t.Context(), t, s, doc)

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

// testSchemaVersion is a dummy value for doc frontmatter in tests.
// The actual schema fingerprint is stored in PRAGMA user_version, not in docs.
const testSchemaVersion = 1

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
	var fm frontmatter.Frontmatter
	fm.MustSet([]byte("id"), frontmatter.StringValue(doc.DocID))
	fm.MustSet([]byte("schema_version"), frontmatter.IntValue(testSchemaVersion))
	fm.MustSet([]byte("title"), frontmatter.StringValue(doc.DocTitle))
	fm.MustSet([]byte("status"), frontmatter.StringValue(doc.DocStatus))
	fm.MustSet([]byte("priority"), frontmatter.IntValue(doc.DocPriority))

	yamlStr, err := fm.MarshalYAML(frontmatter.WithKeyPriority([]byte("id"), []byte("schema_version"), []byte("title")))
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
