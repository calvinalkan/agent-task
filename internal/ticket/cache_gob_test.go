package ticket_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"tk/internal/ticket"
)

func writeCacheFileGob(t *testing.T, dir string, entries map[string]ticket.CacheEntry) string {
	t.Helper()

	path := filepath.Join(dir, ticket.CacheFileName)

	err := ticket.TestWriteBinaryCache(path, entries)
	if err != nil {
		t.Fatalf("writeBinaryCache failed: %v", err)
	}

	return path
}

func TestGobCacheLoadSave(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create initial cache
	cache := &ticket.Cache{
		Entries: map[string]ticket.CacheEntry{
			"test.md": {
				Mtime: time.Now(),
				Summary: ticket.Summary{
					ID:     "test123",
					Status: ticket.StatusOpen,
					Title:  "Test Ticket",
					Type:   "task",
					// Required by cache encoding.
					Priority: 2,
					Created:  "2026-01-04T00:00:00Z",
					Path:     "/tickets/test.md",
				},
			},
		},
	}

	// Save cache
	err := ticket.SaveCache(tmpDir, cache)
	if err != nil {
		t.Fatalf("SaveCache failed: %v", err)
	}

	// Verify cache file exists
	cachePath := filepath.Join(tmpDir, ticket.CacheFileName)

	_, statErr := os.Stat(cachePath)
	if os.IsNotExist(statErr) {
		t.Fatal("cache file should exist")
	}

	// Load cache
	loaded, loadErr := ticket.LoadCache(tmpDir)
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

	if entry.Summary.Status != ticket.StatusOpen {
		t.Errorf("expected status open, got %s", entry.Summary.Status)
	}
}

func TestGobCacheCorrupted(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Write corrupted cache file
	cachePath := filepath.Join(tmpDir, ticket.CacheFileName)

	err := os.WriteFile(cachePath, []byte("invalid gob data"), ticket.TestFilePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Load should return errCacheCorrupt
	cache, loadErr := ticket.LoadCache(tmpDir)

	if !errors.Is(loadErr, ticket.ErrTestCacheCorrupt) {
		t.Errorf("expected errCacheCorrupt, got %v", loadErr)
	}

	if cache != nil {
		t.Error("cache should be nil on error")
	}
}

func TestGobCacheMissing(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Load from non-existent cache
	cache, loadErr := ticket.LoadCache(tmpDir)

	if !errors.Is(loadErr, ticket.ErrTestCacheNotFound) {
		t.Errorf("expected errCacheNotFound, got %v", loadErr)
	}

	if cache != nil {
		t.Error("cache should be nil on error")
	}
}

func TestDeleteCache(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, ticket.TestDirPerms)
	if err != nil {
		t.Fatal(err)
	}

	// Create a cache file using binary cache.
	cachePath := writeCacheFileGob(t, ticketDir, map[string]ticket.CacheEntry{})

	_, statErr := os.Stat(cachePath)
	if os.IsNotExist(statErr) {
		t.Fatal("cache file should exist")
	}

	// Delete it.
	err = ticket.DeleteCache(ticketDir)
	if err != nil {
		t.Fatalf("DeleteCache failed: %v", err)
	}

	// Verify it's gone.
	_, statErr = os.Stat(cachePath)
	if !os.IsNotExist(statErr) {
		t.Fatal("cache file should be deleted")
	}

	// Delete again should not error (idempotent).
	err = ticket.DeleteCache(ticketDir)
	if err != nil {
		t.Fatalf("DeleteCache on missing file should not error: %v", err)
	}
}
