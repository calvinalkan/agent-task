package ticket_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/internal/ticket"
)

func Test_Cache_Version4_Header_And_Entry_Size_When_Invoked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	entries := map[string]ticket.CacheEntry{
		"a-001.md": {
			Mtime: time.Unix(0, 1),
			Summary: ticket.Summary{
				SchemaVersion: 1,
				ID:            "a-001",
				Status:        ticket.StatusOpen,
				Title:         "Title",
				Type:          "task",
				Priority:      2,
				Created:       "2026-01-04T00:00:00Z",
				Path:          "/tickets/a-001.md",
			},
		},
	}

	cachePath := writeCacheFileCT(t, tmpDir, entries)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}

	if got := string(data[0:4]); got != ticket.TestCacheMagic {
		t.Fatalf("magic = %q, want %q", got, ticket.TestCacheMagic)
	}

	if got := binary.LittleEndian.Uint16(data[4:6]); got != ticket.TestCacheVersionNum {
		t.Fatalf("version = %d, want %d", got, ticket.TestCacheVersionNum)
	}

	if got := binary.LittleEndian.Uint32(data[6:10]); got != 1 {
		t.Fatalf("entry count = %d, want %d", got, 1)
	}

	// For a single-entry cache, the first data offset should be header + indexEntrySize.
	entryOffset := ticket.TestCacheHeaderSize

	dataOffset := binary.LittleEndian.Uint32(data[entryOffset+40 : entryOffset+44])
	if dataOffset != uint32(ticket.TestCacheHeaderSize+ticket.TestIndexEntrySize) {
		t.Fatalf("data offset = %d, want %d", dataOffset, ticket.TestCacheHeaderSize+ticket.TestIndexEntrySize)
	}
}

func Test_Priority_And_Type_Bytes_Encoding_When_Invoked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	entries := map[string]ticket.CacheEntry{
		"a-001.md": {
			Mtime: time.Unix(0, 1),
			Summary: ticket.Summary{
				SchemaVersion: 1,
				ID:            "a-001",
				Status:        ticket.StatusInProgress,
				Title:         "Title",
				Type:          "epic",
				Priority:      3,
				Created:       "2026-01-04T00:00:00Z",
				Path:          "/tickets/a-001.md",
			},
		},
	}

	cachePath := writeCacheFileCT(t, tmpDir, entries)

	data, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}

	entryOffset := ticket.TestCacheHeaderSize

	if got := data[entryOffset+46]; got != ticket.TestStatusByteInProgress {
		t.Fatalf("status byte = %d, want %d", got, ticket.TestStatusByteInProgress)
	}

	if got := data[entryOffset+47]; got != 3 {
		t.Fatalf("priority byte = %d, want %d", got, 3)
	}

	if got := data[entryOffset+48]; got != ticket.TestTypeByteEpic {
		t.Fatalf("type byte = %d, want %d", got, ticket.TestTypeByteEpic)
	}
}

func Test_Binary_Search_With56_Byte_Entries_When_Invoked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	entries := make(map[string]ticket.CacheEntry, 1000)

	for i := range 1000 {
		filename := "t" + strings.Repeat("0", 5-len(strconvItoaCT(i))) + strconvItoaCT(i) + ".md"
		id := strings.TrimSuffix(filename, ".md")

		entries[filename] = ticket.CacheEntry{
			Mtime: time.Unix(0, int64(i+1)),
			Summary: ticket.Summary{
				SchemaVersion: 1,
				ID:            id,
				Status:        ticket.StatusOpen,
				Title:         "Ticket " + id,
				Type:          "task",
				Priority:      2,
				Created:       "2026-01-04T00:00:00Z",
				Path:          "/tickets/" + filename,
			},
		}
	}

	writeCacheFileCT(t, tmpDir, entries)

	cache, err := ticket.LoadBinaryCache(tmpDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache failed: %v", err)
	}

	defer func() { _ = cache.Close() }()

	for _, i := range []int{0, 1, 499, 500, 999} {
		filename := "t" + strings.Repeat("0", 5-len(strconvItoaCT(i))) + strconvItoaCT(i) + ".md"

		entry := cache.Lookup(filename)
		if entry == nil {
			t.Fatalf("Lookup(%s) returned nil", filename)
		}

		if got, want := entry.Summary.ID, strings.TrimSuffix(filename, ".md"); got != want {
			t.Fatalf("Lookup(%s) ID = %q, want %q", filename, got, want)
		}
	}
}

func Test_Filter_Entries_When_Invoked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	entries := map[string]ticket.CacheEntry{
		"a.md": {Mtime: time.Unix(0, 1), Summary: ticket.Summary{SchemaVersion: 1, ID: "a", Status: ticket.StatusOpen, Title: "A", Type: "bug", Priority: 1, Created: "2026-01-04T00:00:00Z", Path: "/tickets/a.md"}},
		"b.md": {Mtime: time.Unix(0, 2), Summary: ticket.Summary{SchemaVersion: 1, ID: "b", Status: ticket.StatusOpen, Title: "B", Type: "feature", Priority: 2, Created: "2026-01-04T00:00:00Z", Path: "/tickets/b.md"}},
		"c.md": {Mtime: time.Unix(0, 3), Summary: ticket.Summary{SchemaVersion: 1, ID: "c", Status: ticket.StatusInProgress, Title: "C", Type: "bug", Priority: 2, Created: "2026-01-04T00:00:00Z", Path: "/tickets/c.md"}},
		"d.md": {Mtime: time.Unix(0, 4), Summary: ticket.Summary{SchemaVersion: 1, ID: "d", Status: ticket.StatusClosed, Title: "D", Type: "task", Priority: 3, Created: "2026-01-04T00:00:00Z", Path: "/tickets/d.md"}},
	}

	writeCacheFileCT(t, tmpDir, entries)

	cache, err := ticket.LoadBinaryCache(tmpDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache failed: %v", err)
	}

	defer func() { _ = cache.Close() }()

	// Status filter
	open := cache.FilterEntries(int(ticket.TestStatusByteOpen), 0, -1, 0, 0)
	if len(open) != 2 {
		t.Fatalf("open matches = %d, want %d", len(open), 2)
	}

	// Priority filter
	p2 := cache.FilterEntries(-1, 2, -1, 0, 0)
	if len(p2) != 2 {
		t.Fatalf("priority=2 matches = %d, want %d", len(p2), 2)
	}

	// Type filter
	bug := cache.FilterEntries(-1, 0, int(ticket.TestTypeByteBug), 0, 0)
	if len(bug) != 2 {
		t.Fatalf("type=bug matches = %d, want %d", len(bug), 2)
	}

	// Combined (AND)
	openBug := cache.FilterEntries(int(ticket.TestStatusByteOpen), 0, int(ticket.TestTypeByteBug), 0, 0)
	if len(openBug) != 1 {
		t.Fatalf("open+bug matches = %d, want %d", len(openBug), 1)
	}

	// Limit
	limited := cache.FilterEntries(-1, 0, -1, 2, 0)
	if len(limited) != 2 {
		t.Fatalf("limit=2 matches = %d, want %d", len(limited), 2)
	}

	// Offset
	offset := cache.FilterEntries(-1, 0, -1, 0, 2)
	if len(offset) != 2 {
		t.Fatalf("offset=2 matches = %d, want %d", len(offset), 2)
	}

	// Offset out of bounds returns nil
	if got := cache.FilterEntries(-1, 0, -1, 0, 100); got != nil {
		t.Fatalf("offset out of bounds: got non-nil slice len=%d", len(got))
	}
}

func Test_Cache_Load_Errors_When_Invoked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, ticket.CacheFileName)

	// Empty file = too small.
	err := os.WriteFile(cachePath, []byte{}, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr := ticket.LoadBinaryCache(tmpDir)
	if !errors.Is(loadErr, ticket.ErrTestFileTooSmall) {
		t.Fatalf("empty cache load err = %v, want %v", loadErr, ticket.ErrTestFileTooSmall)
	}

	// Wrong magic.
	data := make([]byte, ticket.TestCacheHeaderSize)
	copy(data[0:4], "XXXX")

	err = os.WriteFile(cachePath, data, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr = ticket.LoadBinaryCache(tmpDir)
	if !errors.Is(loadErr, ticket.ErrTestInvalidMagic) {
		t.Fatalf("invalid magic err = %v, want %v", loadErr, ticket.ErrTestInvalidMagic)
	}

	// Wrong version.
	data = make([]byte, ticket.TestCacheHeaderSize)
	copy(data[0:4], ticket.TestCacheMagic)
	binary.LittleEndian.PutUint16(data[4:6], 3)

	err = os.WriteFile(cachePath, data, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr = ticket.LoadBinaryCache(tmpDir)
	if !errors.Is(loadErr, ticket.ErrTestVersionMismatch) {
		t.Fatalf("version mismatch err = %v, want %v", loadErr, ticket.ErrTestVersionMismatch)
	}

	// Truncated file (header says 1 entry but file lacks index).
	data = make([]byte, ticket.TestCacheHeaderSize)
	copy(data[0:4], ticket.TestCacheMagic)
	binary.LittleEndian.PutUint16(data[4:6], ticket.TestCacheVersionNum)
	binary.LittleEndian.PutUint32(data[6:10], 1)

	err = os.WriteFile(cachePath, data, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr = ticket.LoadBinaryCache(tmpDir)
	if !errors.Is(loadErr, ticket.ErrTestFileTooSmall) {
		t.Fatalf("truncated cache err = %v, want %v", loadErr, ticket.ErrTestFileTooSmall)
	}
}

func Test_Update_And_Delete_Cache_Entry_When_Invoked(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	mkdirErr := os.MkdirAll(ticketDir, 0o750)
	if mkdirErr != nil {
		t.Fatal(mkdirErr)
	}

	// Seed an empty cache so UpdateCacheEntry uses the update path.
	writeCacheFileCT(t, ticketDir, map[string]ticket.CacheEntry{})

	createTestTicketFullCT(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, nil)
	path := filepath.Join(ticketDir, "a-001.md")

	summary, err := ticket.ParseTicketFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}

	updateErr := ticket.UpdateCacheEntry(ticketDir, "a-001.md", &summary)
	if updateErr != nil {
		t.Fatalf("UpdateCacheEntry failed: %v", updateErr)
	}

	cache, err := ticket.LoadBinaryCache(ticketDir)
	if err != nil {
		t.Fatal(err)
	}

	if cache.Lookup("a-001.md") == nil {
		_ = cache.Close()

		t.Fatal("expected a-001.md to exist in cache after update")
	}

	_ = cache.Close()

	// Add a new entry.
	createTestTicketFullCT(t, ticketDir, "b-002", ticket.StatusOpen, "B", "bug", 1, nil)
	path = filepath.Join(ticketDir, "b-002.md")

	summary, err = ticket.ParseTicketFrontmatter(path)
	if err != nil {
		t.Fatal(err)
	}

	updateErr = ticket.UpdateCacheEntry(ticketDir, "b-002.md", &summary)
	if updateErr != nil {
		t.Fatalf("UpdateCacheEntry (new file) failed: %v", updateErr)
	}

	cache, err = ticket.LoadBinaryCache(ticketDir)
	if err != nil {
		t.Fatal(err)
	}

	if cache.Lookup("a-001.md") == nil || cache.Lookup("b-002.md") == nil {
		_ = cache.Close()

		t.Fatal("expected both cache entries after update")
	}

	_ = cache.Close()

	// Delete one entry.
	deleteErr := ticket.DeleteCacheEntry(ticketDir, "a-001.md")
	if deleteErr != nil {
		t.Fatalf("DeleteCacheEntry failed: %v", deleteErr)
	}

	cache, err = ticket.LoadBinaryCache(ticketDir)
	if err != nil {
		t.Fatal(err)
	}

	if cache.Lookup("a-001.md") != nil {
		_ = cache.Close()

		t.Fatal("expected a-001.md to be removed from cache")
	}

	if cache.Lookup("b-002.md") == nil {
		_ = cache.Close()

		t.Fatal("expected b-002.md to remain in cache")
	}

	_ = cache.Close()
}

func Test_Cache_Size_Limit_Validation_When_Invoked(t *testing.T) {
	t.Parallel()

	setup := func(t *testing.T) (string, ticket.Summary) {
		t.Helper()

		tmpDir := t.TempDir()
		ticketDir := filepath.Join(tmpDir, ".tickets")

		mkdirErr := os.MkdirAll(ticketDir, 0o750)
		if mkdirErr != nil {
			t.Fatal(mkdirErr)
		}

		// Seed an empty cache so UpdateCacheEntry uses the update path.
		writeCacheFileCT(t, ticketDir, map[string]ticket.CacheEntry{})

		createTestTicketFullCT(t, ticketDir, "a-001", ticket.StatusOpen, "A", "task", 2, nil)
		path := filepath.Join(ticketDir, "a-001.md")

		baseSummary, err := ticket.ParseTicketFrontmatter(path)
		if err != nil {
			t.Fatal(err)
		}

		return ticketDir, baseSummary
	}

	t.Run("filename too long", func(t *testing.T) {
		t.Parallel()

		ticketDir, baseSummary := setup(t)
		longFilename := strings.Repeat("a", 40) + ".md"

		err := ticket.UpdateCacheEntry(ticketDir, longFilename, &baseSummary)
		if err == nil || !strings.Contains(err.Error(), "filename too long") {
			t.Fatalf("expected long filename error, got %v", err)
		}
	})

	t.Run("assignee too long", func(t *testing.T) {
		t.Parallel()

		ticketDir, baseSummary := setup(t)
		baseSummary.Assignee = strings.Repeat("x", 256)

		err := ticket.UpdateCacheEntry(ticketDir, "a-001.md", &baseSummary)
		if err == nil || !strings.Contains(err.Error(), "assignee too long") {
			t.Fatalf("expected assignee too long error, got %v", err)
		}
	})

	t.Run("blocker too long", func(t *testing.T) {
		t.Parallel()

		ticketDir, baseSummary := setup(t)
		baseSummary.BlockedBy = []string{strings.Repeat("b", 256)}

		err := ticket.UpdateCacheEntry(ticketDir, "a-001.md", &baseSummary)
		if err == nil || !strings.Contains(err.Error(), "blocker ID too long") {
			t.Fatalf("expected blocker ID too long error, got %v", err)
		}
	})

	t.Run("too many blockers", func(t *testing.T) {
		t.Parallel()

		ticketDir, baseSummary := setup(t)

		baseSummary.BlockedBy = make([]string, 256)
		for i := range baseSummary.BlockedBy {
			baseSummary.BlockedBy[i] = "x"
		}

		err := ticket.UpdateCacheEntry(ticketDir, "a-001.md", &baseSummary)
		if err == nil || !strings.Contains(err.Error(), "too many blockers") {
			t.Fatalf("expected too many blockers error, got %v", err)
		}
	})

	t.Run("entry too large", func(t *testing.T) {
		t.Parallel()

		ticketDir, baseSummary := setup(t)
		baseSummary.Title = strings.Repeat("t", 400)

		baseSummary.BlockedBy = make([]string, 255)
		for i := range baseSummary.BlockedBy {
			baseSummary.BlockedBy[i] = strings.Repeat("x", 255)
		}

		err := ticket.UpdateCacheEntry(ticketDir, "a-001.md", &baseSummary)
		if err == nil || !strings.Contains(err.Error(), "entry too large") {
			t.Fatalf("expected entry too large error, got %v", err)
		}
	})
}

func strconvItoaCT(i int) string {
	// Small helper to avoid importing strconv in this file.
	if i == 0 {
		return "0"
	}

	var buf [32]byte

	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + (i % 10))
		i /= 10
	}

	return string(buf[pos:])
}

func writeCacheFileCT(t *testing.T, dir string, entries map[string]ticket.CacheEntry) string {
	t.Helper()

	path := filepath.Join(dir, ticket.CacheFileName)

	err := ticket.TestWriteBinaryCache(path, entries)
	if err != nil {
		t.Fatalf("writeBinaryCache failed: %v", err)
	}

	return path
}

func createTestTicketFullCT(t *testing.T, ticketDir, ticketID, status, title, ticketType string, priority int, blockedBy []string) {
	t.Helper()

	blockedByStr := "[]"
	if len(blockedBy) > 0 {
		blockedByStr = "[" + strings.Join(blockedBy, ", ") + "]"
	}

	closedLine := ""
	if status == ticket.StatusClosed {
		closedLine = "closed: " + time.Now().UTC().Format(time.RFC3339) + "\n"
	}

	content := "---\n" +
		"schema_version: 1\n" +
		"id: " + ticketID + "\n" +
		"status: " + status + "\n" +
		"blocked-by: " + blockedByStr + "\n" +
		"created: 2026-01-04T00:00:00Z\n" +
		"type: " + ticketType + "\n" +
		"priority: " + string(rune('0'+priority)) + "\n" +
		closedLine +
		"---\n" +
		"# " + title + "\n"

	path := filepath.Join(ticketDir, ticketID+".md")

	err := os.WriteFile(path, []byte(content), ticket.TestFilePerms)
	if err != nil {
		t.Fatalf("failed to create test ticket: %v", err)
	}
}
