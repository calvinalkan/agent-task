package store_test

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/internal/store"
	"github.com/google/uuid"
)

// ptrEquals compares a *string to a string, treating nil as "".
func ptrEquals(ptr *string, val string) bool {
	if ptr == nil {
		return val == ""
	}

	return *ptr == val
}

// ptrVal returns the value of a *string, or "" if nil.
func ptrVal(ptr *string) string {
	if ptr == nil {
		return ""
	}

	return *ptr
}

// newTestTicket creates a ticket via store.NewTicket for testing.
func newTestTicket(t *testing.T, title string) *store.Ticket {
	t.Helper()

	ticket, err := store.NewTicket(title, "task", "open", 2)
	if err != nil {
		t.Fatalf("new ticket: %v", err)
	}

	return ticket
}

// putTicket creates a transaction, puts a ticket, and commits.
func putTicket(ctx context.Context, t *testing.T, s *store.Store, ticket *store.Ticket) *store.Ticket {
	t.Helper()

	tx, err := s.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	result, err := tx.Put(ticket)
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	err = tx.Commit(ctx)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	return result
}

// writeTicketFile writes a ticket to disk via store transaction.
// Opens a temporary store, puts the ticket, and closes it.
// Useful for reindex tests that need pre-existing files.
func writeTicketFile(t *testing.T, root string, ticket *store.Ticket) {
	t.Helper()

	s, err := store.Open(t.Context(), root)
	if err != nil {
		t.Fatalf("open store for write: %v", err)
	}

	putTicket(t.Context(), t, s, ticket)

	err = s.Close()
	if err != nil {
		t.Fatalf("close store: %v", err)
	}
}

// openIndex opens the SQLite index directly for inspection.
func openIndex(t *testing.T, ticketDir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", filepath.Join(ticketDir, ".tk", "index.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	return db
}

// countTickets returns the number of tickets in the index.
func countTickets(t *testing.T, db *sql.DB) int {
	t.Helper()

	row := db.QueryRow("SELECT COUNT(*) FROM tickets")

	var count int

	err := row.Scan(&count)
	if err != nil {
		t.Fatalf("count tickets: %v", err)
	}

	return count
}

// userVersion returns the SQLite user_version pragma.
func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, err
	}

	return version, nil
}

// readFileString reads a file and returns its contents as a string.
func readFileString(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}

	return string(data)
}

// WAL test helpers

type walRecord struct {
	Op     string        `json:"op"`
	ID     uuid.UUID     `json:"id"`
	Path   string        `json:"path"`
	Ticket *store.Ticket `json:"ticket,omitempty"`
}

const (
	testWalMagic      = "TKWAL001"
	testWalFooterSize = 32
)

var testWalCRC32C = crc32.MakeTable(crc32.Castagnoli)

// writeWalFile writes a complete WAL file with footer.
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
		t.Fatalf("write wal body: %v", err)
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

// writeWalBodyOnly writes a WAL file without footer (for corruption tests).
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
		t.Fatalf("write wal body: %v", err)
	}

	err = file.Sync()
	if err != nil {
		t.Fatalf("sync wal: %v", err)
	}
}

// writeWalWithBody writes a WAL file with custom body bytes (for corruption tests).
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
		t.Fatalf("write wal body: %v", err)
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

// makeWalPutRecord creates a WAL put record from a ticket.
func makeWalPutRecord(ticket *store.Ticket) walRecord {
	return walRecord{
		Op:     "put",
		ID:     ticket.ID,
		Path:   ticket.Path,
		Ticket: ticket,
	}
}

// makeWalPutRecordWithPath creates a WAL put record with a custom path (for testing path validation).
func makeWalPutRecordWithPath(ticket *store.Ticket, path string) walRecord {
	return walRecord{
		Op:     "put",
		ID:     ticket.ID,
		Path:   path,
		Ticket: ticket,
	}
}

// makeWalDeleteRecord creates a WAL delete record.
func makeWalDeleteRecord(id uuid.UUID, path string) walRecord {
	return walRecord{
		Op:   "delete",
		ID:   id,
		Path: path,
	}
}
