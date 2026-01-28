package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3" // sqlite3 driver for integration tests

	"github.com/calvinalkan/agent-task/internal/store"
)

func Test_Rebuild_Builds_SQLite_Index_When_Tickets_Are_Valid(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	idA, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	idB, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	createdAt := time.Date(2026, 1, 20, 10, 11, 12, 0, time.UTC)
	closedAt := createdAt.Add(2 * time.Hour)

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idA.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Alpha Task",
	})

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        idB.String(),
		Status:    "closed",
		Type:      "bug",
		Priority:  3,
		CreatedAt: createdAt,
		ClosedAt:  &closedAt,
		BlockedBy: []string{idA.String()},
		Title:     "Beta Bug",
	})

	writeIgnoredTicket(t, ticketDir)

	indexed, err := store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if indexed != 2 {
		t.Fatalf("indexed = %d, want 2", indexed)
	}

	db := openIndex(t, ticketDir)

	defer func() { _ = db.Close() }()

	version, err := userVersion(t.Context(), db)
	if err != nil {
		t.Fatalf("user_version: %v", err)
	}

	const wantSchemaVersion = 1
	if version != wantSchemaVersion {
		t.Fatalf("user_version = %d, want %d", version, wantSchemaVersion)
	}

	rows, err := db.Query(`
		SELECT id, short_id, path, mtime_ns, status, type, priority, assignee, parent, created_at, closed_at, external_ref, title
		FROM tickets
		ORDER BY id`)
	if err != nil {
		t.Fatalf("query tickets: %v", err)
	}

	defer func() { _ = rows.Close() }()

	found := make(map[string]struct{})

	for rows.Next() {
		var (
			id       string
			shortID  string
			path     string
			mtimeNS  int64
			status   string
			typeName string
			priority int64
			assignee sql.NullString
			parent   sql.NullString
			created  int64
			closed   sql.NullInt64
			extRef   sql.NullString
			title    string
		)

		scanErr := rows.Scan(&id, &shortID, &path, &mtimeNS, &status, &typeName, &priority, &assignee, &parent, &created, &closed, &extRef, &title)
		if scanErr != nil {
			t.Fatalf("scan ticket: %v", scanErr)
		}

		found[id] = struct{}{}

		parsedID, parseErr := uuidFromString(id)
		if parseErr != nil {
			t.Fatalf("parse id: %v", parseErr)
		}

		expectedShort, shortErr := store.ShortIDFromUUID(parsedID)
		if shortErr != nil {
			t.Fatalf("short id: %v", shortErr)
		}

		if shortID != expectedShort {
			t.Fatalf("short_id = %s, want %s", shortID, expectedShort)
		}

		expectedPath, pathErr := store.TicketPath(parsedID)
		if pathErr != nil {
			t.Fatalf("ticket path: %v", pathErr)
		}

		if path != expectedPath {
			t.Fatalf("path = %s, want %s", path, expectedPath)
		}

		if mtimeNS <= 0 {
			t.Fatalf("mtime_ns = %d, want > 0", mtimeNS)
		}

		switch id {
		case idA.String():
			if status != "open" {
				t.Fatalf("status = %s, want open", status)
			}

			if typeName != "task" {
				t.Fatalf("type = %s, want task", typeName)
			}

			if priority != 2 {
				t.Fatalf("priority = %d, want 2", priority)
			}

			if created != createdAt.Unix() {
				t.Fatalf("created_at = %d, want %d", created, createdAt.Unix())
			}

			if closed.Valid {
				t.Fatal("closed_at valid for open ticket")
			}

			if title != "Alpha Task" {
				t.Fatalf("title = %s, want Alpha Task", title)
			}
		case idB.String():
			if status != "closed" {
				t.Fatalf("status = %s, want closed", status)
			}

			if typeName != "bug" {
				t.Fatalf("type = %s, want bug", typeName)
			}

			if priority != 3 {
				t.Fatalf("priority = %d, want 3", priority)
			}

			if created != createdAt.Unix() {
				t.Fatalf("created_at = %d, want %d", created, createdAt.Unix())
			}

			if !closed.Valid || closed.Int64 != closedAt.Unix() {
				t.Fatalf("closed_at = %v, want %d", closed, closedAt.Unix())
			}

			if title != "Beta Bug" {
				t.Fatalf("title = %s, want Beta Bug", title)
			}
		default:
			t.Fatalf("unexpected id %s", id)
		}
	}

	err = rows.Err()
	if err != nil {
		t.Fatalf("rows error: %v", err)
	}

	if len(found) != 2 {
		t.Fatalf("ticket count = %d, want 2", len(found))
	}

	blockers := readBlockers(t, db)
	if len(blockers) != 1 || blockers[0].ticketID != idB.String() || blockers[0].blockerID != idA.String() {
		t.Fatalf("blockers = %+v, want [%s -> %s]", blockers, idB.String(), idA.String())
	}
}

func Test_Rebuild_Skips_Orphaned_Tickets_When_Path_Mismatches(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	validID, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	orphanID, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	createdAt := time.Date(2026, 1, 21, 9, 0, 0, 0, time.UTC)

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        validID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: createdAt,
		Title:     "Valid Ticket",
	})

	orphanRel := filepath.Join("2026", "01-21", "orphan.md")
	writeTicketAtPath(t, ticketDir, orphanRel, &ticketFixture{
		ID:        orphanID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Orphan Ticket",
	})

	indexed, err := store.Rebuild(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected rebuild error for orphan")
	}

	if !errors.Is(err, store.ErrIndexScan) {
		t.Fatalf("error = %v, want ErrIndexScan", err)
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	assertIndexMissing(t, ticketDir)

	scanErr := requireIndexScanError(t, err)
	assertIssuePaths(t, scanErr, []string{filepath.Join(ticketDir, orphanRel)})
}

func Test_Rebuild_Returns_Context_Error_When_Canceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	indexed, err := store.Rebuild(ctx, t.TempDir())
	if err == nil {
		t.Fatal("expected rebuild error for canceled context")
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func Test_Rebuild_Indexes_Valid_Tickets_When_Other_Files_Invalid(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	id, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	_ = writeTicket(t, ticketDir, &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: time.Date(2026, 1, 23, 9, 0, 0, 0, time.UTC),
		Title:     "Valid",
	})

	badID, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	schemaTicket := &ticketFixture{
		ID:        badID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: time.Date(2026, 1, 23, 9, 0, 0, 0, time.UTC),
		Title:     "Wrong Schema",
	}
	schemaContent := renderTicket(schemaTicket)
	schemaContent = strings.Replace(schemaContent, "schema_version: 1\n", "schema_version: 2\n", 1)
	badPath := writeRawTicket(t, ticketDir, badID, schemaContent)
	assertFileStartsWith(t, ticketDir, badPath, "---")

	missingTitleID, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	missingTitleTicket := &ticketFixture{
		ID:        missingTitleID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: time.Date(2026, 1, 23, 10, 0, 0, 0, time.UTC),
		Title:     "Missing Title",
	}
	missingTitleContent := renderTicket(missingTitleTicket)
	missingTitleContent = strings.Replace(missingTitleContent, fmt.Sprintf("# %s\n", missingTitleTicket.Title), "", 1)
	missingTitlePath := writeRawTicket(t, ticketDir, missingTitleID, missingTitleContent)
	assertFileStartsWith(t, ticketDir, missingTitlePath, "---")

	indexed, err := store.Rebuild(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected rebuild error for invalid tickets")
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	if !errors.Is(err, store.ErrIndexScan) {
		t.Fatalf("error = %v, want ErrIndexScan", err)
	}

	assertIndexMissing(t, ticketDir)

	scanErr := requireIndexScanError(t, err)
	assertIssuePaths(t, scanErr, []string{
		filepath.Join(ticketDir, badPath),
		filepath.Join(ticketDir, missingTitlePath),
	})
}

func Test_Rebuild_Skips_Tk_Directory_Files_When_Markdown_Present(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	internalPath := filepath.Join(".tk", "ignored.md")
	writeRawPath(t, ticketDir, internalPath, "---\nnot: valid\n")

	indexed, err := store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	if indexed != 0 {
		t.Fatalf("indexed = %d, want 0", indexed)
	}

	db := openIndex(t, ticketDir)

	defer func() { _ = db.Close() }()

	count := countTickets(t, db)
	if count != 0 {
		t.Fatalf("ticket count = %d, want 0", count)
	}
}

type blockerRow struct {
	ticketID  string
	blockerID string
}

func readBlockers(t *testing.T, db *sql.DB) []blockerRow {
	t.Helper()

	rows, err := db.Query("SELECT ticket_id, blocker_id FROM ticket_blockers ORDER BY ticket_id, blocker_id")
	if err != nil {
		t.Fatalf("query blockers: %v", err)
	}

	defer func() { _ = rows.Close() }()

	var results []blockerRow

	for rows.Next() {
		var row blockerRow

		scanErr := rows.Scan(&row.ticketID, &row.blockerID)
		if scanErr != nil {
			t.Fatalf("scan blocker: %v", scanErr)
		}

		results = append(results, row)
	}

	err = rows.Err()
	if err != nil {
		t.Fatalf("rows error: %v", err)
	}

	return results
}

func assertIndexMissing(t *testing.T, ticketDir string) {
	t.Helper()

	path := filepath.Join(ticketDir, ".tk", "index.sqlite")

	_, err := os.Stat(path)
	if err == nil {
		t.Fatalf("expected index missing at %s", path)
	}

	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat %s: %v", path, err)
	}
}

func requireIndexScanError(t *testing.T, err error) *store.IndexScanError {
	t.Helper()

	var scanErr *store.IndexScanError
	if !errors.As(err, &scanErr) {
		t.Fatalf("error = %v, want IndexScanError", err)
	}

	if scanErr.Total != len(scanErr.Issues) {
		t.Fatalf("scan error total = %d, issues = %d", scanErr.Total, len(scanErr.Issues))
	}

	return scanErr
}

func assertIssuePaths(t *testing.T, scanErr *store.IndexScanError, paths []string) {
	t.Helper()

	seen := make(map[string]struct{}, len(scanErr.Issues))
	for _, issue := range scanErr.Issues {
		seen[issue.Path] = struct{}{}
	}

	for _, path := range paths {
		if _, ok := seen[path]; !ok {
			t.Fatalf("missing issue for %s", path)
		}
	}
}

func writeRawTicket(t *testing.T, root string, id uuid.UUID, contents string) string {
	t.Helper()

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	writeRawPath(t, root, relPath, contents)

	return relPath
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

func assertFileStartsWith(t *testing.T, root, relPath, want string) {
	t.Helper()

	absPath := filepath.Join(root, relPath)

	data, err := os.ReadFile(absPath)
	if err != nil {
		t.Fatalf("read file %s: %v", relPath, err)
	}

	firstLine := strings.SplitN(string(data), "\n", 2)[0]
	if firstLine != want {
		t.Fatalf("file %s first line = %q, want %q", relPath, firstLine, want)
	}
}

func writeIgnoredTicket(t *testing.T, root string) {
	t.Helper()

	id, err := store.NewUUIDv7()
	if err != nil {
		t.Fatalf("uuidv7: %v", err)
	}

	relPath := filepath.Join(".tk", "ignored.md")
	writeTicketAtPath(t, root, relPath, &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: time.Date(2026, 1, 22, 8, 0, 0, 0, time.UTC),
		Title:     "Ignored",
	})
}
