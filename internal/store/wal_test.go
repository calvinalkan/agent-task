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
