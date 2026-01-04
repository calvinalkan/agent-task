package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBinaryCacheWriteRead(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create test entries
	entries := map[string]CacheEntry{
		"ticket-001.md": {
			Mtime: time.Unix(1704067200, 0),
			Summary: TicketSummary{
				ID:        "ticket-001",
				Status:    StatusOpen,
				Title:     "First ticket",
				Type:      "feature",
				Priority:  2,
				Created:   "2024-01-01T00:00:00Z",
				Path:      "/tickets/ticket-001.md",
				BlockedBy: []string{},
			},
		},
		"ticket-002.md": {
			Mtime: time.Unix(1704067300, 0),
			Summary: TicketSummary{
				ID:        "ticket-002",
				Status:    StatusClosed,
				Title:     "Second ticket with longer title",
				Type:      "bug",
				Priority:  1,
				Created:   "2024-01-01T01:00:00Z",
				Closed:    "2024-01-02T00:00:00Z",
				Assignee:  "alice",
				Path:      "/tickets/ticket-002.md",
				BlockedBy: []string{"ticket-001"},
			},
		},
		"ticket-003.md": {
			Mtime: time.Unix(1704067400, 0),
			Summary: TicketSummary{
				ID:        "ticket-003",
				Status:    StatusInProgress,
				Title:     "Third ticket",
				Type:      "task",
				Priority:  3,
				Created:   "2024-01-01T02:00:00Z",
				Path:      "/tickets/ticket-003.md",
				BlockedBy: []string{"ticket-001", "ticket-002"},
			},
		},
	}

	// Write cache
	bc := NewBinaryCache()
	for filename, entry := range entries {
		bc.Update(filename, entry)
	}

	err := SaveBinaryCache(tmpDir, bc)
	if err != nil {
		t.Fatalf("SaveBinaryCache failed: %v", err)
	}

	// Verify file exists
	cachePath := filepath.Join(tmpDir, cacheFileName)
	info, err := os.Stat(cachePath)

	if err != nil {
		t.Fatalf("cache file should exist: %v", err)
	}

	t.Logf("Cache file size: %d bytes", info.Size())

	// Load cache
	loaded, err := LoadBinaryCache(tmpDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache failed: %v", err)
	}

	defer loaded.Close()

	// Lookup each entry and verify all fields
	for filename, expected := range entries {
		got := loaded.Lookup(filename)
		if got == nil {
			t.Errorf("Lookup(%s) returned nil", filename)
			continue
		}

		e := expected.Summary
		g := got.Summary

		if g.ID != e.ID {
			t.Errorf("%s: ID = %s, want %s", filename, g.ID, e.ID)
		}
		if g.Status != e.Status {
			t.Errorf("%s: Status = %s, want %s", filename, g.Status, e.Status)
		}
		if g.Title != e.Title {
			t.Errorf("%s: Title = %s, want %s", filename, g.Title, e.Title)
		}
		if g.Type != e.Type {
			t.Errorf("%s: Type = %s, want %s", filename, g.Type, e.Type)
		}
		if g.Priority != e.Priority {
			t.Errorf("%s: Priority = %d, want %d", filename, g.Priority, e.Priority)
		}
		if g.Created != e.Created {
			t.Errorf("%s: Created = %s, want %s", filename, g.Created, e.Created)
		}
		if g.Closed != e.Closed {
			t.Errorf("%s: Closed = %s, want %s", filename, g.Closed, e.Closed)
		}
		if g.Assignee != e.Assignee {
			t.Errorf("%s: Assignee = %s, want %s", filename, g.Assignee, e.Assignee)
		}
		if g.Path != e.Path {
			t.Errorf("%s: Path = %s, want %s", filename, g.Path, e.Path)
		}
		if len(g.BlockedBy) != len(e.BlockedBy) {
			t.Errorf("%s: BlockedBy length = %d, want %d", filename, len(g.BlockedBy), len(e.BlockedBy))
		}
	}

	// Lookup non-existent
	got := loaded.Lookup("nonexistent.md")
	if got != nil {
		t.Error("Lookup(nonexistent.md) should return nil")
	}
}

func TestBinaryCacheBinarySearch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create many entries to test binary search
	entries := make(map[string]CacheEntry)
	for i := range 1000 {
		filename := fmt.Sprintf("t%06d.md", i)
		entries[filename] = CacheEntry{
			Mtime: time.Unix(1704067200+int64(i), 0),
			Summary: TicketSummary{
				ID:        fmt.Sprintf("t%06d", i),
				Status:    StatusOpen,
				Title:     fmt.Sprintf("Ticket %d", i),
				BlockedBy: []string{},
			},
		}
	}

	// Write cache
	bc := NewBinaryCache()
	for filename, entry := range entries {
		bc.Update(filename, entry)
	}

	err := SaveBinaryCache(tmpDir, bc)
	if err != nil {
		t.Fatalf("SaveBinaryCache failed: %v", err)
	}

	// Load and test lookups
	loaded, err := LoadBinaryCache(tmpDir)
	if err != nil {
		t.Fatalf("LoadBinaryCache failed: %v", err)
	}

	defer loaded.Close()

	// Test various positions
	testCases := []int{0, 1, 499, 500, 501, 998, 999}
	for _, i := range testCases {
		filename := fmt.Sprintf("t%06d.md", i)
		got := loaded.Lookup(filename)

		if got == nil {
			t.Errorf("Lookup(%s) returned nil", filename)

			continue
		}

		expectedID := fmt.Sprintf("t%06d", i)
		if got.Summary.ID != expectedID {
			t.Errorf("Lookup(%s): got ID %s, want %s", filename, got.Summary.ID, expectedID)
		}
	}
}

func TestBinaryCacheVersionMismatch(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, cacheFileName)

	// Write file with wrong version
	data := make([]byte, cacheHeaderSize)
	copy(data[0:4], cacheMagic)
	data[4] = 99 // wrong version
	data[5] = 0

	err := os.WriteFile(cachePath, data, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr := LoadBinaryCache(tmpDir)
	if loadErr != errVersionMismatch {
		t.Errorf("expected errVersionMismatch, got %v", loadErr)
	}
}

func TestBinaryCacheInvalidMagic(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, cacheFileName)

	// Write file with wrong magic
	data := make([]byte, cacheHeaderSize)
	copy(data[0:4], "XXXX")

	err := os.WriteFile(cachePath, data, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	_, loadErr := LoadBinaryCache(tmpDir)
	if loadErr != errInvalidMagic {
		t.Errorf("expected errInvalidMagic, got %v", loadErr)
	}
}

func TestBinaryCacheNotFound(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	_, loadErr := LoadBinaryCache(tmpDir)
	if loadErr != errCacheNotFound {
		t.Errorf("expected errCacheNotFound, got %v", loadErr)
	}
}

func TestBinaryCacheUpdate(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create initial cache
	bc := NewBinaryCache()
	bc.Update("ticket-001.md", CacheEntry{
		Mtime: time.Unix(1704067200, 0),
		Summary: TicketSummary{
			ID:     "ticket-001",
			Status: StatusOpen,
			Title:  "Original title",
		},
	})

	err := SaveBinaryCache(tmpDir, bc)
	if err != nil {
		t.Fatal(err)
	}

	// Load, modify, save
	loaded, err := LoadBinaryCache(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	// Mark original as valid and add update
	loaded.MarkValid("ticket-001.md")
	loaded.Update("ticket-002.md", CacheEntry{
		Mtime: time.Unix(1704067300, 0),
		Summary: TicketSummary{
			ID:     "ticket-002",
			Status: StatusOpen,
			Title:  "New ticket",
		},
	})

	err = SaveBinaryCache(tmpDir, loaded)
	if err != nil {
		t.Fatal(err)
	}

	loaded.Close()

	// Reload and verify both entries exist
	reloaded, err := LoadBinaryCache(tmpDir)
	if err != nil {
		t.Fatal(err)
	}

	defer reloaded.Close()

	entry1 := reloaded.Lookup("ticket-001.md")
	if entry1 == nil || entry1.Summary.Title != "Original title" {
		t.Error("ticket-001 should exist with original title")
	}

	entry2 := reloaded.Lookup("ticket-002.md")
	if entry2 == nil || entry2.Summary.Title != "New ticket" {
		t.Error("ticket-002 should exist with new title")
	}
}
