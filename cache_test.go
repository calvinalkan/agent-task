package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCacheLoadSave(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create initial cache
	cache := &TicketCache{
		Entries: map[string]CacheEntry{
			"test.md": {
				Mtime: time.Now(),
				Summary: TicketSummary{
					ID:     "test123",
					Status: StatusOpen,
					Title:  "Test Ticket",
				},
			},
		},
	}

	// Save cache
	err := SaveCache(tmpDir, cache)
	if err != nil {
		t.Fatalf("SaveCache failed: %v", err)
	}

	// Verify cache file exists
	cachePath := filepath.Join(tmpDir, cacheFileName)

	_, statErr := os.Stat(cachePath)
	if os.IsNotExist(statErr) {
		t.Fatal("cache file should exist")
	}

	// Load cache
	loaded, loadErr := LoadCache(tmpDir)
	if loadErr != nil {
		t.Fatalf("LoadCache failed: %v", loadErr)
	}

	if len(loaded.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(loaded.Entries))
	}

	entry, ok := loaded.Entries["test.md"]
	if !ok {
		t.Fatal("expected test.md entry")
	}

	if entry.Summary.ID != "test123" {
		t.Errorf("expected ID test123, got %s", entry.Summary.ID)
	}

	if entry.Summary.Status != StatusOpen {
		t.Errorf("expected status open, got %s", entry.Summary.Status)
	}
}

func TestCacheCorrupted(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Write corrupted cache file
	cachePath := filepath.Join(tmpDir, cacheFileName)

	err := os.WriteFile(cachePath, []byte("invalid gob data"), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Load should return errCacheCorrupt
	cache, loadErr := LoadCache(tmpDir)

	if !errors.Is(loadErr, errCacheCorrupt) {
		t.Errorf("expected errCacheCorrupt, got %v", loadErr)
	}

	if cache != nil {
		t.Error("cache should be nil on error")
	}
}

func TestCacheMissing(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Load from non-existent cache
	cache, loadErr := LoadCache(tmpDir)

	if !errors.Is(loadErr, errCacheNotFound) {
		t.Errorf("expected errCacheNotFound, got %v", loadErr)
	}

	if cache != nil {
		t.Error("cache should be nil on error")
	}
}

func TestCacheHitOnUnchangedFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket file
	ticketContent := `---
id: cache001
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Cache Test Ticket
`

	ticketPath := filepath.Join(tmpDir, "cache001.md")

	err := os.WriteFile(ticketPath, []byte(ticketContent), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// First call - cache miss, should parse file
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results1))
	}

	if results1[0].Summary.ID != "cache001" {
		t.Errorf("expected ID cache001, got %s", results1[0].Summary.ID)
	}

	// Verify cache was created
	cachePath := filepath.Join(tmpDir, cacheFileName)

	_, statErr := os.Stat(cachePath)
	if os.IsNotExist(statErr) {
		t.Fatal("cache file should exist after first ListTickets")
	}

	// Second call - cache hit, should use cached data
	results2, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results2))
	}

	if results2[0].Summary.ID != "cache001" {
		t.Errorf("expected ID cache001, got %s", results2[0].Summary.ID)
	}
}

func TestCacheMissOnModifiedFile(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a ticket file
	ticketContent := `---
id: modify001
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Original Title
`

	ticketPath := filepath.Join(tmpDir, "modify001.md")

	err := os.WriteFile(ticketPath, []byte(ticketContent), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// First call - populates cache
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if results1[0].Summary.Title != "Original Title" {
		t.Errorf("expected 'Original Title', got %s", results1[0].Summary.Title)
	}

	// Wait a bit to ensure mtime changes
	time.Sleep(10 * time.Millisecond)

	// Modify the file
	modifiedContent := `---
id: modify001
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Modified Title
`

	err = os.WriteFile(ticketPath, []byte(modifiedContent), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should detect mtime change and re-parse
	results2, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if results2[0].Summary.Title != "Modified Title" {
		t.Errorf("expected 'Modified Title', got %s", results2[0].Summary.Title)
	}
}

func TestCacheCleanupDeletedFiles(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create two ticket files
	for _, ticketID := range []string{"del001", "del002"} {
		content := `---
id: ` + ticketID + `
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Ticket ` + ticketID + `
`

		writeErr := os.WriteFile(filepath.Join(tmpDir, ticketID+".md"), []byte(content), filePerms)
		if writeErr != nil {
			t.Fatal(writeErr)
		}
	}

	// First call - populates cache with both files
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results1))
	}

	// Delete one file
	err = os.Remove(filepath.Join(tmpDir, "del001.md"))
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should not show deleted file
	results2, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 1 {
		t.Fatalf("expected 1 result after deletion, got %d", len(results2))
	}

	if results2[0].Summary.ID != "del002" {
		t.Errorf("expected del002, got %s", results2[0].Summary.ID)
	}
}

func TestDeleteCache(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create a cache file
	cache := &TicketCache{Entries: make(map[string]CacheEntry)}

	err := SaveCache(tmpDir, cache)
	if err != nil {
		t.Fatal(err)
	}

	// Verify it exists
	cachePath := filepath.Join(tmpDir, cacheFileName)

	_, statErr := os.Stat(cachePath)
	if os.IsNotExist(statErr) {
		t.Fatal("cache file should exist")
	}

	// Delete it
	err = DeleteCache(tmpDir)
	if err != nil {
		t.Fatalf("DeleteCache failed: %v", err)
	}

	// Verify it's gone
	_, statErr = os.Stat(cachePath)
	if !os.IsNotExist(statErr) {
		t.Fatal("cache file should be deleted")
	}

	// Delete again should not error (idempotent)
	err = DeleteCache(tmpDir)
	if err != nil {
		t.Fatalf("DeleteCache on missing file should not error: %v", err)
	}
}
