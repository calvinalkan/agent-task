package store_test

import (
	"errors"
	"maps"
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

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, relPath)

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("ticket missing at %s: %v", absPath, err)
	}

	assertSummaryMatchesFixture(t, &rows[0], fixture, relPath, fileInfo.ModTime().UnixNano())

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

	_, err = os.Stat(filepath.Join(ticketDir, deletePath))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted ticket still exists: %v", err)
	}

	absPut := filepath.Join(ticketDir, putPath)

	fileInfo, err := os.Stat(absPut)
	if err != nil {
		t.Fatalf("stat put: %v", err)
	}

	assertSummaryMatchesFixture(t, &rows[0], putFixture, putPath, fileInfo.ModTime().UnixNano())

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	files := listMarkdownFiles(t, ticketDir)
	if len(files) != 1 || files[0] != absPut {
		t.Fatalf("markdown files = %v, want [%s]", files, absPut)
	}

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

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat ticket: %v", err)
	}

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

	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want 1", rows)
	}

	assertSummaryMatchesFixture(t, &rows[0], &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Original",
	}, relPath, fileInfo.ModTime().UnixNano())
}

// Contract: invalid WAL paths return ErrWALReplay and leave WAL intact.
func Test_Open_Returns_Error_When_WAL_Path_Invalid(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		path func(root string) string
	}{
		{name: "empty", path: func(_ string) string { return "" }},
		{name: "backslash", path: func(_ string) string { return "bad\\path.md" }},
		{name: "absolute", path: func(root string) string { return filepath.Join(root, "abs.md") }},
		{name: "escapes_root", path: func(_ string) string { return "../escape.md" }},
		{name: "not_clean", path: func(_ string) string { return "bad/../path.md" }},
		{name: "missing_suffix", path: func(_ string) string { return "missing.txt" }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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
					Path:        tc.path(root),
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

			files := listMarkdownFiles(t, ticketDir)
			if len(files) != 0 {
				t.Fatalf("unexpected markdown files: %v", files)
			}
		})
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

	files := listMarkdownFiles(t, ticketDir)
	if len(files) != 0 {
		t.Fatalf("unexpected markdown files: %v", files)
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

	files := listMarkdownFiles(t, ticketDir)
	if len(files) != 0 {
		t.Fatalf("unexpected markdown files: %v", files)
	}
}

// Contract: committed WAL replays even when schema mismatch forces a rebuild.
func Test_Open_Replays_WAL_And_Rebuilds_When_Schema_Mismatch(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	createdAt := time.Date(2026, 1, 24, 12, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x8888888888888888)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Mismatch",
	}

	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          fixture.ID,
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(fixture),
			Content:     "# Mismatch\nBody\n",
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

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, relPath)

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat ticket: %v", err)
	}

	assertSummaryMatchesFixture(t, &rows[0], fixture, relPath, fileInfo.ModTime().UnixNano())

	expected := renderTicketFromFrontmatter(t, walFrontmatterFromTicket(fixture), "# Mismatch\nBody\n")

	actual := readFileString(t, absPath)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}
}

// Contract: replayed WAL content is normalized with a trailing newline.
func Test_Open_Replays_WAL_Appends_Newline_When_Content_Lacks_Trailing(t *testing.T) {
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

	createdAt := time.Date(2026, 1, 24, 13, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0x9999999999999999)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: createdAt,
		Title:     "No Newline",
	}

	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          fixture.ID,
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(fixture),
			Content:     "# No Newline\nBody",
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

	absPath := filepath.Join(ticketDir, relPath)

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat ticket: %v", err)
	}

	assertSummaryMatchesFixture(t, &rows[0], fixture, relPath, fileInfo.ModTime().UnixNano())

	expected := renderTicketFromFrontmatter(t, walFrontmatterFromTicket(fixture), "# No Newline\nBody")

	actual := readFileString(t, absPath)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}
}

// Contract: delete replay succeeds even if the file is already gone.
func Test_Open_Replays_WAL_Delete_When_File_Missing(t *testing.T) {
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

	createdAt := time.Date(2026, 1, 24, 14, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0xaaaaaaaaaaaaaaaa)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	err = os.MkdirAll(filepath.Dir(filepath.Join(ticketDir, relPath)), 0o750)
	if err != nil {
		t.Fatalf("mkdir parent dir: %v", err)
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		{
			Op:   "delete",
			ID:   id.String(),
			Path: relPath,
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

	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	files := listMarkdownFiles(t, ticketDir)
	if len(files) != 0 {
		t.Fatalf("unexpected markdown files: %v", files)
	}
}

// Contract: invalid WAL footer is treated as uncommitted and discarded.
func Test_Open_Truncates_WAL_When_Footer_Invalid(t *testing.T) {
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
	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          id.String(),
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(&ticketFixture{ID: id.String(), Status: "open", Type: "task", Priority: 2, CreatedAt: createdAt, Title: "Invalid Footer"}),
			Content:     "# Invalid Footer\nBody\n",
		},
	})

	file, err := os.OpenFile(walPath, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("open wal: %v", err)
	}

	t.Cleanup(func() { _ = file.Close() })

	info, err := file.Stat()
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	_, err = file.WriteAt([]byte{0xff}, info.Size()-testWalFooterSize)
	if err != nil {
		t.Fatalf("corrupt wal footer: %v", err)
	}

	err = file.Sync()
	if err != nil {
		t.Fatalf("sync wal: %v", err)
	}

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	info, err = os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, relPath)

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat ticket: %v", err)
	}

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

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	assertSummaryMatchesFixture(t, &rows[0], &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  2,
		CreatedAt: createdAt,
		Title:     "Original",
	}, relPath, fileInfo.ModTime().UnixNano())
}

// Contract: invalid WAL JSON is surfaced and preserves the WAL for inspection.
func Test_Open_Returns_Error_When_WAL_JSON_Invalid(t *testing.T) {
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

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalWithBody(t, walPath, []byte("{"))

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected wal parse error")
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() == 0 {
		t.Fatal("wal should remain after parse failure")
	}

	files := listMarkdownFiles(t, ticketDir)
	if len(files) != 0 {
		t.Fatalf("unexpected markdown files: %v", files)
	}
}

// Contract: malformed WAL operations are rejected without applying changes.
func Test_Open_Returns_Error_When_WAL_Ops_Are_Invalid(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 1, 26, 11, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0xbbbbbbbbbbbbbbbb)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: createdAt,
		Title:     "Bad WAL",
	}

	baseFrontmatter := walFrontmatterFromTicket(fixture)
	content := "# Bad WAL\nBody\n"

	cloneFrontmatter := func() map[string]any {
		out := make(map[string]any, len(baseFrontmatter))
		maps.Copy(out, baseFrontmatter)

		return out
	}

	cases := []struct {
		name   string
		record walRecord
	}{
		{
			name: "unknown_op",
			record: walRecord{
				Op:          "boom",
				ID:          id.String(),
				Path:        relPath,
				Frontmatter: baseFrontmatter,
				Content:     content,
			},
		},
		{
			name: "invalid_id",
			record: walRecord{
				Op:          "put",
				ID:          "not-a-uuid",
				Path:        relPath,
				Frontmatter: baseFrontmatter,
				Content:     content,
			},
		},
		{
			name: "non_v7_id",
			record: walRecord{
				Op:          "put",
				ID:          "550e8400-e29b-41d4-a716-446655440000",
				Path:        relPath,
				Frontmatter: baseFrontmatter,
				Content:     content,
			},
		},
		{
			name: "missing_frontmatter",
			record: walRecord{
				Op:      "put",
				ID:      id.String(),
				Path:    relPath,
				Content: content,
			},
		},
		{
			name: "frontmatter_missing_id",
			record: func() walRecord {
				fm := cloneFrontmatter()
				delete(fm, "id")

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
		{
			name: "frontmatter_missing_schema",
			record: func() walRecord {
				fm := cloneFrontmatter()
				delete(fm, "schema_version")

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
		{
			name: "frontmatter_non_integer",
			record: func() walRecord {
				fm := cloneFrontmatter()
				fm["priority"] = 1.5

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
		{
			name: "frontmatter_list_non_string",
			record: func() walRecord {
				fm := cloneFrontmatter()
				fm["blocked-by"] = []any{"ok", 123}

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
		{
			name: "frontmatter_object_non_scalar",
			record: func() walRecord {
				fm := cloneFrontmatter()
				fm["meta"] = map[string]any{"nested": []any{"x"}}

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
		{
			name: "frontmatter_empty_object",
			record: func() walRecord {
				fm := cloneFrontmatter()
				fm["meta"] = map[string]any{}

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
		{
			name: "frontmatter_list_empty_item",
			record: func() walRecord {
				fm := cloneFrontmatter()
				fm["blocked-by"] = []string{""}

				return walRecord{Op: "put", ID: id.String(), Path: relPath, Frontmatter: fm, Content: content}
			}(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
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

			walPath := filepath.Join(ticketDir, ".tk", "wal")
			writeWalFile(t, walPath, []walRecord{tc.record})

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

			files := listMarkdownFiles(t, ticketDir)
			if len(files) != 0 {
				t.Fatalf("unexpected markdown files: %v", files)
			}
		})
	}
}

// Contract: WAL replay errors after filesystem writes keep the WAL for recovery.
func Test_Open_Returns_Error_When_WAL_Index_Update_Fails(t *testing.T) {
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

	db := openIndex(t, ticketDir)

	_, err = db.Exec("DROP TABLE tickets")
	if err != nil {
		_ = db.Close()

		t.Fatalf("drop tickets: %v", err)
	}

	_, err = db.Exec("DROP TABLE ticket_blockers")
	if err != nil {
		_ = db.Close()

		t.Fatalf("drop blockers: %v", err)
	}

	err = db.Close()
	if err != nil {
		t.Fatalf("close db: %v", err)
	}

	createdAt := time.Date(2026, 1, 26, 12, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0xcccccccccccccccc)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: createdAt,
		Title:     "Index Failure",
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          fixture.ID,
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(fixture),
			Content:     "# Index Failure\nBody\n",
		},
	})

	_, err = store.Open(t.Context(), ticketDir)
	if err == nil {
		t.Fatal("expected index update error")
	}

	if !errors.Is(err, store.ErrIndexUpdate) {
		t.Fatalf("error = %v, want ErrIndexUpdate", err)
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() == 0 {
		t.Fatal("wal should remain after index failure")
	}

	absPath := filepath.Join(ticketDir, relPath)
	expected := renderTicketFromFrontmatter(t, walFrontmatterFromTicket(fixture), "# Index Failure\nBody\n")

	actual := readFileString(t, absPath)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}
}

// Contract: Query replays a committed WAL before returning results.
func Test_Query_Replays_WAL_When_Committed(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	ticketDir := filepath.Join(root, ".tickets")

	err := os.MkdirAll(ticketDir, 0o750)
	if err != nil {
		t.Fatalf("mkdir ticket dir: %v", err)
	}

	storeHandle, err := store.Open(t.Context(), ticketDir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}

	t.Cleanup(func() { _ = storeHandle.Close() })

	createdAt := time.Date(2026, 1, 26, 13, 0, 0, 0, time.UTC)
	id := makeUUIDv7(t, createdAt, 0xabc, 0xdddddddddddddddd)

	relPath, err := store.TicketPath(id)
	if err != nil {
		t.Fatalf("ticket path: %v", err)
	}

	fixture := &ticketFixture{
		ID:        id.String(),
		Status:    "open",
		Type:      "task",
		Priority:  1,
		CreatedAt: createdAt,
		Title:     "Query WAL",
	}

	walPath := filepath.Join(ticketDir, ".tk", "wal")
	writeWalFile(t, walPath, []walRecord{
		{
			Op:          "put",
			ID:          fixture.ID,
			Path:        relPath,
			Frontmatter: walFrontmatterFromTicket(fixture),
			Content:     "# Query WAL\nBody\n",
		},
	})

	rows, err := storeHandle.Query(t.Context(), nil)
	if err != nil {
		t.Fatalf("query: %v", err)
	}

	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}

	info, err := os.Stat(walPath)
	if err != nil {
		t.Fatalf("stat wal: %v", err)
	}

	if info.Size() != 0 {
		t.Fatalf("wal size = %d, want 0", info.Size())
	}

	absPath := filepath.Join(ticketDir, relPath)

	fileInfo, err := os.Stat(absPath)
	if err != nil {
		t.Fatalf("stat ticket: %v", err)
	}

	assertSummaryMatchesFixture(t, &rows[0], fixture, relPath, fileInfo.ModTime().UnixNano())

	expected := renderTicketFromFrontmatter(t, walFrontmatterFromTicket(fixture), "# Query WAL\nBody\n")

	actual := readFileString(t, absPath)
	if actual != expected {
		t.Fatalf("ticket content mismatch\n--- want ---\n%s\n--- got ---\n%s", expected, actual)
	}
}
