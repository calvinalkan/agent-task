package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGobCacheLoadSave(t *testing.T) {
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

func TestGobCacheCorrupted(t *testing.T) {
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

func TestGobCacheMissing(t *testing.T) {
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

	// Create a cache file using binary cache
	cache := NewBinaryCache()

	err := SaveBinaryCache(tmpDir, cache)
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

func TestCacheNewFileAdded(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create initial ticket file
	ticket1Content := `---
id: existing001
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Existing Ticket
`

	err := os.WriteFile(filepath.Join(tmpDir, "existing001.md"), []byte(ticket1Content), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// First call - populates cache with one ticket
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results1))
	}

	// Add a new ticket file
	ticket2Content := `---
id: new002
status: open
blocked-by: []
created: 2024-01-02T00:00:00Z
type: bug
priority: 1
---
# New Ticket
`

	err = os.WriteFile(filepath.Join(tmpDir, "new002.md"), []byte(ticket2Content), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should detect new file and include it
	results2, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 2 {
		t.Fatalf("expected 2 results after adding file, got %d", len(results2))
	}

	// Verify both tickets are present
	ids := make(map[string]bool)
	for _, r := range results2 {
		ids[r.Summary.ID] = true
	}

	if !ids["existing001"] {
		t.Error("expected existing001 in results")
	}

	if !ids["new002"] {
		t.Error("expected new002 in results")
	}
}

func TestCacheCorruptedRecovery(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create ticket files
	for _, id := range []string{"ticket001", "ticket002"} {
		content := `---
id: ` + id + `
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Ticket ` + id + `
`

		err := os.WriteFile(filepath.Join(tmpDir, id+".md"), []byte(content), filePerms)
		if err != nil {
			t.Fatal(err)
		}
	}

	// First call - populates cache
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results1))
	}

	// Corrupt the cache file
	cachePath := filepath.Join(tmpDir, cacheFileName)

	err = os.WriteFile(cachePath, []byte("corrupted gob data!!!"), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should recover from corruption and return correct results
	results2, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 2 {
		t.Fatalf("expected 2 results after corruption recovery, got %d", len(results2))
	}

	// Verify both tickets are present
	ids := make(map[string]bool)
	for _, r := range results2 {
		ids[r.Summary.ID] = true
	}

	if !ids["ticket001"] {
		t.Error("expected ticket001 in results")
	}

	if !ids["ticket002"] {
		t.Error("expected ticket002 in results")
	}

	// Third call - should work normally (cache was rebuilt)
	results3, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results3) != 2 {
		t.Fatalf("expected 2 results on third call, got %d", len(results3))
	}
}

func TestCacheColdWithOffsetCachesAll(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create 5 ticket files
	for i, id := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		content := `---
id: ` + id + `
status: open
blocked-by: []
created: 2024-01-0` + string(rune('1'+i)) + `T00:00:00Z
type: feature
priority: 2
---
# Ticket ` + id + `
`

		err := os.WriteFile(filepath.Join(tmpDir, id+".md"), []byte(content), filePerms)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Ensure no cache exists
	_ = DeleteCache(tmpDir)

	// First call with offset=2 (cold cache) - should return tickets 3,4,5
	results1, err := ListTickets(tmpDir, ListTicketsOptions{
		NeedAll: false,
		Limit:   0, // no limit
		Offset:  2,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 3 {
		t.Fatalf("expected 3 results with offset=2, got %d", len(results1))
	}

	// Verify we got tickets after offset
	ids1 := make(map[string]bool)
	for _, r := range results1 {
		ids1[r.Summary.ID] = true
	}

	if ids1["a-001"] || ids1["b-002"] {
		t.Error("offset=2 should skip a-001 and b-002")
	}

	// Second call without offset - should return all 5 (proves all were cached)
	results2, err := ListTickets(tmpDir, ListTicketsOptions{
		NeedAll: false,
		Limit:   0,
		Offset:  0,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 5 {
		t.Fatalf("expected 5 results without offset, got %d (cache should have all tickets)", len(results2))
	}
}

func TestCacheMixedChanges(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create initial tickets A, B, C, D
	for _, id := range []string{"a-001", "b-002", "c-003", "d-004"} {
		content := `---
id: ` + id + `
status: open
blocked-by: []
created: 2024-01-01T00:00:00Z
type: feature
priority: 2
---
# Original ` + id + `
`

		err := os.WriteFile(filepath.Join(tmpDir, id+".md"), []byte(content), filePerms)
		if err != nil {
			t.Fatal(err)
		}
	}

	// First call - populates cache with A, B, C, D
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 4 {
		t.Fatalf("expected 4 results, got %d", len(results1))
	}

	// Now make multiple changes:
	// - A: unchanged (cache hit)
	// - B: modify (cache miss)
	// - C: delete
	// - E: add new

	// Modify B
	time.Sleep(10 * time.Millisecond) // ensure mtime changes

	modifiedB := `---
id: b-002
status: closed
blocked-by: []
created: 2024-01-01T00:00:00Z
closed: 2024-01-02T00:00:00Z
type: feature
priority: 2
---
# Modified b-002
`

	err = os.WriteFile(filepath.Join(tmpDir, "b-002.md"), []byte(modifiedB), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Delete C
	err = os.Remove(filepath.Join(tmpDir, "c-003.md"))
	if err != nil {
		t.Fatal(err)
	}

	// Add E
	newE := `---
id: e-005
status: open
blocked-by: []
created: 2024-01-05T00:00:00Z
type: bug
priority: 1
---
# New e-005
`

	err = os.WriteFile(filepath.Join(tmpDir, "e-005.md"), []byte(newE), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Second call - should handle all changes correctly
	results2, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	// Should have A, B, D, E (4 tickets)
	if len(results2) != 4 {
		t.Fatalf("expected 4 results after mixed changes, got %d", len(results2))
	}

	// Build map for verification
	resultMap := make(map[string]*TicketSummary)
	for _, r := range results2 {
		resultMap[r.Summary.ID] = r.Summary
	}

	// Verify A is present (unchanged, cache hit)
	if _, ok := resultMap["a-001"]; !ok {
		t.Error("a-001 should be present (unchanged)")
	}

	// Verify B is present with updated data (modified, cache miss)
	if b, ok := resultMap["b-002"]; !ok {
		t.Error("b-002 should be present (modified)")
	} else {
		if b.Status != "closed" {
			t.Errorf("b-002 should have status=closed, got %s", b.Status)
		}

		if b.Title != "Modified b-002" {
			t.Errorf("b-002 should have updated title, got %s", b.Title)
		}
	}

	// Verify C is gone (deleted)
	if _, ok := resultMap["c-003"]; ok {
		t.Error("c-003 should NOT be present (deleted)")
	}

	// Verify D is present (unchanged, cache hit)
	if _, ok := resultMap["d-004"]; !ok {
		t.Error("d-004 should be present (unchanged)")
	}

	// Verify E is present (new file)
	if e, ok := resultMap["e-005"]; !ok {
		t.Error("e-005 should be present (new)")
	} else if e.Title != "New e-005" {
		t.Errorf("e-005 should have correct title, got %s", e.Title)
	}
}

func TestCacheColdWithLimitCachesAll(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create 5 ticket files
	for i, id := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		content := `---
id: ` + id + `
status: open
blocked-by: []
created: 2024-01-0` + string(rune('1'+i)) + `T00:00:00Z
type: feature
priority: 2
---
# Ticket ` + id + `
`

		err := os.WriteFile(filepath.Join(tmpDir, id+".md"), []byte(content), filePerms)
		if err != nil {
			t.Fatal(err)
		}
	}

	// Ensure no cache exists
	_ = DeleteCache(tmpDir)

	// First call with limit=2 (cold cache)
	results1, err := ListTickets(tmpDir, ListTicketsOptions{
		NeedAll: false,
		Limit:   2,
		Offset:  0,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(results1))
	}

	// Second call without limit - should return all 5 (proves all were cached)
	results2, err := ListTickets(tmpDir, ListTicketsOptions{
		NeedAll: false,
		Limit:   0, // no limit
		Offset:  0,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 5 {
		t.Fatalf("expected 5 results without limit, got %d (cache should have all tickets)", len(results2))
	}

	// Verify all ticket IDs are present
	ids := make(map[string]bool)
	for _, r := range results2 {
		ids[r.Summary.ID] = true
	}

	for _, id := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		if !ids[id] {
			t.Errorf("expected %s in results", id)
		}
	}
}

func TestCachePartialReadWithUpdatePreservesAllEntries(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()

	// Create 5 ticket files
	for i, id := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		content := `---
id: ` + id + `
status: open
blocked-by: []
created: 2024-01-0` + string(rune('1'+i)) + `T00:00:00Z
type: feature
priority: 2
---
# Ticket ` + id + `
`
		err := os.WriteFile(filepath.Join(tmpDir, id+".md"), []byte(content), filePerms)
		if err != nil {
			t.Fatal(err)
		}
	}

	// First call - cold cache, builds full cache with all 5 tickets
	results1, err := ListTickets(tmpDir, ListTicketsOptions{NeedAll: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(results1) != 5 {
		t.Fatalf("expected 5 results on cold cache, got %d", len(results1))
	}

	// Second call - warm cache with limit=2 (partial read)
	results2, err := ListTickets(tmpDir, ListTicketsOptions{
		NeedAll: false,
		Limit:   2,
		Offset:  0,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results2) != 2 {
		t.Fatalf("expected 2 results with limit=2, got %d", len(results2))
	}

	// Modify one of the first 2 tickets (triggers cache update on partial read)
	time.Sleep(10 * time.Millisecond) // ensure mtime changes

	modifiedContent := `---
id: a-001
status: closed
blocked-by: []
created: 2024-01-01T00:00:00Z
closed: 2024-01-02T00:00:00Z
type: feature
priority: 2
---
# Modified a-001
`
	err = os.WriteFile(filepath.Join(tmpDir, "a-001.md"), []byte(modifiedContent), filePerms)
	if err != nil {
		t.Fatal(err)
	}

	// Third call - partial read again, should detect change and update cache
	results3, err := ListTickets(tmpDir, ListTicketsOptions{
		NeedAll: false,
		Limit:   2,
		Offset:  0,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(results3) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results3))
	}

	// Verify the modification was picked up
	if results3[0].Summary.Status != "closed" {
		t.Errorf("expected a-001 status=closed, got %s", results3[0].Summary.Status)
	}

	// Verify cache still has all 5 entries after partial update
	// (This is an implementation detail check, but necessary to verify
	// that partial reads don't lose cache entries)
	cache, loadErr := LoadBinaryCache(tmpDir)
	if loadErr != nil {
		t.Fatalf("failed to load cache: %v", loadErr)
	}
	defer cache.Close()

	if cache.entryCount != 5 {
		t.Errorf("cache should have 5 entries after partial update, got %d", cache.entryCount)
	}

	// Also verify we can lookup all entries
	for _, id := range []string{"a-001", "b-002", "c-003", "d-004", "e-005"} {
		filename := id + ".md"
		entry := cache.Lookup(filename)
		if entry == nil {
			t.Errorf("cache entry for %s was lost", filename)
		}
	}
}
