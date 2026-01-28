package store_test

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

	"github.com/calvinalkan/agent-task/internal/store"
)

type ticketFixture struct {
	ID          string
	Status      string
	Type        string
	Priority    int
	CreatedAt   time.Time
	ClosedAt    *time.Time
	BlockedBy   []string
	Assignee    string
	Parent      string
	ExternalRef string
	Title       string
}

// makeUUIDv7 builds deterministic UUIDv7 values so ordering tests stay stable.
func makeUUIDv7(t *testing.T, ts time.Time, randA uint16, randB uint64) uuid.UUID {
	t.Helper()

	ms := uint64(ts.UnixMilli())
	if ms>>48 != 0 {
		t.Fatal("timestamp out of range for uuidv7")
	}

	var b [16]byte

	b[0] = byte(ms >> 40)
	b[1] = byte(ms >> 32)
	b[2] = byte(ms >> 24)
	b[3] = byte(ms >> 16)
	b[4] = byte(ms >> 8)
	b[5] = byte(ms)

	b[6] = byte(0x70 | ((randA >> 8) & 0x0f))
	b[7] = byte(randA)

	b[8] = byte(0x80 | ((randB >> 56) & 0x3f))
	b[9] = byte(randB >> 48)
	b[10] = byte(randB >> 40)
	b[11] = byte(randB >> 32)
	b[12] = byte(randB >> 24)
	b[13] = byte(randB >> 16)
	b[14] = byte(randB >> 8)
	b[15] = byte(randB)

	id := uuid.UUID(b)
	if id.Version() != 7 || id.Variant() != uuid.RFC4122 {
		t.Fatal("constructed uuid is not v7")
	}

	return id
}

func uuidFromString(raw string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil {
		return uuid.UUID{}, err
	}

	return parsed, nil
}

func openIndex(t *testing.T, ticketDir string) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite3", filepath.Join(ticketDir, ".tk", "index.sqlite"))
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}

	return db
}

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

func userVersion(ctx context.Context, db *sql.DB) (int, error) {
	row := db.QueryRowContext(ctx, "PRAGMA user_version")

	var version int

	err := row.Scan(&version)
	if err != nil {
		return 0, err
	}

	return version, nil
}

func writeTicket(t *testing.T, root string, ticket *ticketFixture) string {
	t.Helper()

	id, err := uuidFromString(ticket.ID)
	if err != nil {
		t.Fatalf("parse uuid: %v", err)
	}

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	writeTicketAtPath(t, root, relPath, ticket)

	return relPath
}

func writeTicketAtPath(t *testing.T, root, relPath string, ticket *ticketFixture) {
	t.Helper()

	absPath := filepath.Join(root, relPath)

	err := os.MkdirAll(filepath.Dir(absPath), 0o750)
	if err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	content := renderTicket(ticket)

	err = os.WriteFile(absPath, []byte(content), 0o644)
	if err != nil {
		t.Fatalf("write file: %v", err)
	}
}

func renderTicket(ticket *ticketFixture) string {
	builder := strings.Builder{}
	fmt.Fprint(&builder, "---\n")
	fmt.Fprintf(&builder, "id: %s\n", ticket.ID)
	fmt.Fprint(&builder, "schema_version: 1\n")
	fmt.Fprintf(&builder, "status: %s\n", ticket.Status)
	fmt.Fprintf(&builder, "type: %s\n", ticket.Type)
	fmt.Fprintf(&builder, "priority: %d\n", ticket.Priority)
	fmt.Fprintf(&builder, "created: %s\n", ticket.CreatedAt.UTC().Format(time.RFC3339))

	if ticket.ClosedAt != nil {
		fmt.Fprintf(&builder, "closed: %s\n", ticket.ClosedAt.UTC().Format(time.RFC3339))
	}

	if len(ticket.BlockedBy) > 0 {
		fmt.Fprint(&builder, "blocked-by:\n")

		for _, blocker := range ticket.BlockedBy {
			fmt.Fprintf(&builder, "  - %s\n", blocker)
		}
	}

	if ticket.Assignee != "" {
		fmt.Fprintf(&builder, "assignee: %s\n", ticket.Assignee)
	}

	if ticket.Parent != "" {
		fmt.Fprintf(&builder, "parent: %s\n", ticket.Parent)
	}

	if ticket.ExternalRef != "" {
		fmt.Fprintf(&builder, "external-ref: %s\n", ticket.ExternalRef)
	}

	fmt.Fprint(&builder, "---\n\n")
	fmt.Fprintf(&builder, "# %s\n", ticket.Title)
	fmt.Fprint(&builder, "Body\n")

	return builder.String()
}

type walRecord struct {
	Op          string         `json:"op"`
	ID          string         `json:"id"`
	Path        string         `json:"path"`
	Frontmatter map[string]any `json:"frontmatter,omitempty"`
	Content     string         `json:"content,omitempty"`
}

const (
	testWalMagic      = "TKWAL001"
	testWalFooterSize = 32
)

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

func walFrontmatterFromTicket(ticket *ticketFixture) map[string]any {
	fm := map[string]any{
		"id":             ticket.ID,
		"schema_version": 1,
		"status":         ticket.Status,
		"type":           ticket.Type,
		"priority":       ticket.Priority,
		"created":        ticket.CreatedAt.UTC().Format(time.RFC3339),
	}

	if ticket.ClosedAt != nil {
		fm["closed"] = ticket.ClosedAt.UTC().Format(time.RFC3339)
	}

	if len(ticket.BlockedBy) > 0 {
		fm["blocked-by"] = ticket.BlockedBy
	}

	if ticket.Assignee != "" {
		fm["assignee"] = ticket.Assignee
	}

	if ticket.Parent != "" {
		fm["parent"] = ticket.Parent
	}

	if ticket.ExternalRef != "" {
		fm["external-ref"] = ticket.ExternalRef
	}

	return fm
}
