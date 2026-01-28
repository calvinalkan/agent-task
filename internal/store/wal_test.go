package store_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/store"
)

// Contract: committed WAL replays to files and updates SQLite before truncation.
func Test_Open_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	_, err = store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	createdAt := time.Date(2026, 1, 24, 9, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x1111111111111111)

	shortID, err := store.ShortIDFromUUID(id)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	relPath := filepath.Join(createdAt.Format("2006/01-02"), shortID+".md")
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "WAL Ticket",
	}

	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          fixture.ID,
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(fixture),
			Content:     "# WAL Ticket\nBody\n",
		},
	})

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	if rows[0].ID != fixture.ID {
		t.Fatalf("row id = %s, want %s", rows[0].ID, fixture.ID)
	}

	if rows[0].Title != fixture.Title {
		t.Fatalf("row title = %s, want %s", rows[0].Title, fixture.Title)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, relPath)

	_, err = os.Stat(absPath)
	if err != nil {
		t.Fatalf("ticket missing at %s: %v", absPath, err)
	}

	expected := renderTicketFromFrontmatter(t, walFrontmatterFromTicket(fixture), "# WAL Ticket\nBody\n")

	actual := readFileString(t, absPath)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}
}

// Contract: committed WAL applies put/delete operations to filesystem and index.
func Test_Open_Replays_WAL_Put_And_Delete_When_Committed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	createdAt := time.Date(2026, 1, 23, 8, 0, 0, 0, time.UTC)
	deleteID := makeUUIDv7(t, createdAt, 0xabc, 0x3333333333333333)

	deletePath, err := store.TicketPath(deleteID)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        deleteID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "To Delete",
	})

	_, err = store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	putCreatedAt := createdAt.Add(2 * time.Hour)
	putID := makeUUIDv7(t, putCreatedAt, 0xabc, 0x4444444444444444)

	putPath, err := store.TicketPath(putID)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	putFixture := &ticketFixture{
		ID:        putID.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: putCreatedAt,
		BlockedBy: []string{deleteID.String()},
		Title:     "Inserted",
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		{
			Op:   "delete",
			ID:   deleteID.String(),
			Path: deletePath,
		},
		{
			Op:          "put",
			ID:          putFixture.ID,
			Path:        putPath,
			Frontmatter: walFrontmatterFromTicket(putFixture),
			Content:     "# Inserted\nBody\n",
		},
	})

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	if rows[0].ID != putFixture.ID {
		t.Fatalf("row id = %s, want %s", rows[0].ID, putFixture.ID)
	}

	if len(rows[0].BlockedBy) != 1 || rows[0].BlockedBy[0] != deleteID.String() {
		t.Fatalf("blocked_by = %v, want [%s]", rows[0].BlockedBy, deleteID.String())
	}

	_, err = os.Stat(filepath.Join(ticketDir, deletePath))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted ticket still exists: %v", err)
	}

	absPut := filepath.Join(ticketDir, putPath)
	expected := renderTicketFromFrontmatter(t, walFrontmatterFromTicket(putFixture), "# Inserted\nBody\n")

	actual := readFileString(t, absPut)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}
}

// Contract: uncommitted WALs are truncated and do not change files.
func Test_Open_Truncates_WAL_And_Rebuilds_When_Uncommitted(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	createdAt := time.Date(2026, 1, 25, 7, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x5555555555555555)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	writeTicket(t, ticketDir, &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Original",
	})

	_, err = store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalBodyOnly(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          id.String(),
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(&ticketFixture{ID: id.String(), Status: "open", Type: "task", Priority: 2, CreatedAt: createdAt, Title: "Uncommitted"}),
			Content:     "# Uncommitted\nBody\n",
		},
	})

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, relPath)
	expected := renderTicket(&ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Original",
	})

	actual := readFileString(t, absPath)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}

	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 || rows[0].Title != "Original" {
		t.Fatalf("rows = %+v, want Original", rows)
	}
}

// Contract: invalid WAL paths return ErrWALReplay and leave WAL intact.
func Test_Open_Returns_Error_When_WAL_Path_Invalid(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	_, err = store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	createdAt := time.Date(2026, 1, 26, 9, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x6666666666666666)
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          id.String(),
			Path:        "bad/../path.md",
			Frontmatter: walFrontmatterFromTicket(&ticketFixture{ID: id.String(), Status: "open", Type: "task", Priority: 1, CreatedAt: createdAt, Title: "Bad Path"}),
			Content:     "# Bad Path\nBody\n",
		},
	})

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected wal replay error")
	}

	if !errors.Is(err, store.ErrWALReplay) {
		t.Fatalf("error = %v, want ErrWALReplay", err)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() == 0 {
		t.Fatal("wal should remain after replay failure")
	}

	expectedPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	_, err = os.Stat(filepath.Join(ticketDir, expectedPath))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected ticket created: %v", err)
	}
}

// Contract: mismatched WAL path returns ErrWALReplay without applying ops.
func Test_Open_Returns_Error_When_WAL_Path_Mismatched(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	_, err = store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	createdAt := time.Date(2026, 1, 26, 10, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x7777777777777777)
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          id.String(),
			Path:        "wrong.md",
			Frontmatter: walFrontmatterFromTicket(&ticketFixture{ID: id.String(), Status: "open", Type: "task", Priority: 1, CreatedAt: createdAt, Title: "Wrong Path"}),
			Content:     "# Wrong Path\nBody\n",
		},
	})

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected wal replay error")
	}

	if !errors.Is(err, store.ErrWALReplay) {
		t.Fatalf("error = %v, want ErrWALReplay", err)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() == 0 {
		t.Fatal("wal should remain after replay failure")
	}

	expectedPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	_, err = os.Stat(filepath.Join(ticketDir, expectedPath))
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected ticket created: %v", err)
	}
}

// Contract: checksum mismatches are surfaced as ErrWALCorrupt and leave WAL intact.
func Test_Open_Returns_Error_When_WAL_Is_Corrupt(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	_, err = store.Rebuild(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("rebuild: %v", err)
	}

	createdAt := time.Date(2026, 1, 24, 9, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x2222222222222222)

	shortID, err := store.ShortIDFromUUID(id)
	if err != nil {
		t.Fatalf("short id: %v", err)
	}

	relPath := filepath.Join(createdAt.Format("2006/01-02"), shortID+".md")
	walPath := filepath.Join(ticketDir, ".tk", "wal")

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Corrupt WAL",
	}

	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          fixture.ID,
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(fixture),
			Content:     "# Corrupt WAL\nBody\n",
		},
	})

	file, err := os.OpenFile(walPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	_, err = file.WriteAt([]byte{0xff}, 0)
	if err != nil {
		t.Fatalf("corrupt wal: %v", err)
	}

	err = file.Sync()
	if err != nil {
		t.Fatalf("sync wal: %v", err)
	}

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected wal corrupt error")
	}

	if !errors.Is(err, store.ErrWALCorrupt) {
		t.Fatalf("error = %v, want ErrWALCorrupt", err)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() == 0 {
		t.Fatal("wal should remain after corrupt detection")
	}
}
