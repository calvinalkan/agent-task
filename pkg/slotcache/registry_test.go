package slotcache_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Test_RegistryEntry_Is_Pruned_When_All_Handles_Close verifies that the global
// registry doesn't grow unboundedly. When all cache handles for a file are closed,
// the registry entry should be removed to prevent memory leaks.
//
// Why this matters: Long-running processes that open and close many different
// cache files would accumulate stale registry entries without this pruning.
// Each entry contains a sync.RWMutex and bool, which is small individually but
// adds up over millions of files.
func Test_RegistryEntry_Is_Pruned_When_All_Handles_Close(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix syscalls")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "registry_prune.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
	}

	// Open cache - should create registry entry.
	cache1, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open cache1: %v", err)
	}

	// Verify entry exists in registry.
	if !slotcache.RegistryEntryExistsForTesting(cache1) {
		t.Fatal("registry entry should exist after Open")
	}

	// Open second handle to same file.
	cache2, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open cache2: %v", err)
	}

	// Close first handle - entry should still exist (refcount > 0).
	err = cache1.Close()
	if err != nil {
		t.Fatalf("Close cache1: %v", err)
	}

	// Use cache2 to check (cache1's identity is still valid in the struct).
	if !slotcache.RegistryEntryExistsForTesting(cache2) {
		t.Fatal("registry entry should still exist after closing one of two handles")
	}

	// Close second handle - entry should now be pruned.
	err = cache2.Close()
	if err != nil {
		t.Fatalf("Close cache2: %v", err)
	}

	// After both closed, entry should be pruned. cache2's identity is still valid.
	if slotcache.RegistryEntryExistsForTesting(cache2) {
		t.Fatal("registry entry should be pruned after all handles closed")
	}
}

// Test_RegistryEntry_Is_Pruned_When_All_Multiple_Handles_Close verifies that multiple
// Open() calls correctly increment the reference count and the entry is only
// pruned when ALL handles are closed.
func Test_RegistryEntry_Is_Pruned_When_All_Multiple_Handles_Close(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix syscalls")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "registry_refcount.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
	}

	const numHandles = 5

	handles := make([]slotcache.Cache, numHandles)

	// Open multiple handles to the same file.
	for i := range numHandles {
		c, openErr := slotcache.Open(opts)
		if openErr != nil {
			t.Fatalf("Open handle %d: %v", i, openErr)
		}

		handles[i] = c
	}

	// Verify entry exists with correct refcount.
	refCount, exists := slotcache.GetRegistryEntryForTesting(handles[0])
	if !exists {
		t.Fatal("registry entry should exist")
	}

	if refCount != numHandles {
		t.Fatalf("expected refCount=%d, got %d", numHandles, refCount)
	}

	// Close all but one handle.
	for i := range numHandles - 1 {
		closeErr := handles[i].Close()
		if closeErr != nil {
			t.Fatalf("Close handle %d: %v", i, closeErr)
		}
	}

	// Entry should still exist.
	if !slotcache.RegistryEntryExistsForTesting(handles[numHandles-1]) {
		t.Fatal("registry entry should still exist with one handle open")
	}

	// Close last handle.
	err := handles[numHandles-1].Close()
	if err != nil {
		t.Fatalf("Close last handle: %v", err)
	}

	// Entry should be pruned.
	if slotcache.RegistryEntryExistsForTesting(handles[numHandles-1]) {
		t.Fatal("registry entry should be pruned after all handles closed")
	}
}

// Test_Close_Is_Idempotent_When_Called_Multiple_Times_With_Registry_Pruning verifies that calling Close()
// multiple times doesn't panic or corrupt the registry state.
func Test_Close_Is_Idempotent_When_Called_Multiple_Times_With_Registry_Pruning(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix syscalls")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "registry_idempotent.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// First close should succeed.
	err = c.Close()
	if err != nil {
		t.Fatalf("First Close: %v", err)
	}

	// Second close should be a no-op and return nil.
	err = c.Close()
	if err != nil {
		t.Fatalf("Second Close should be idempotent: %v", err)
	}

	// Third close should also be fine.
	err = c.Close()
	if err != nil {
		t.Fatalf("Third Close should be idempotent: %v", err)
	}
}

// Test_RegistryEntry_Is_Created_Fresh_When_Reopening_After_Full_Prune verifies that after
// all handles are closed and the registry entry is pruned, opening a new
// handle to the same file creates a fresh registry entry.
func Test_RegistryEntry_Is_Created_Fresh_When_Reopening_After_Full_Prune(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix syscalls")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "registry_fresh.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
	}

	// First open/close cycle.
	cache1, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("First Open: %v", err)
	}

	err = cache1.Close()
	if err != nil {
		t.Fatalf("First Close: %v", err)
	}

	// Entry should be pruned.
	if slotcache.RegistryEntryExistsForTesting(cache1) {
		t.Fatal("registry entry should be pruned after close")
	}

	// Second open/close cycle.
	cache2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Second Open: %v", openErr)
	}

	// Fresh entry should exist with refcount=1.
	refCount, exists := slotcache.GetRegistryEntryForTesting(cache2)
	if !exists {
		t.Fatal("fresh registry entry should exist after re-open")
	}

	if refCount != 1 {
		t.Fatalf("expected refCount=1 for fresh entry, got %d", refCount)
	}

	err = cache2.Close()
	if err != nil {
		t.Fatalf("Second Close: %v", err)
	}
}
