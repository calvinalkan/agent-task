package slotcache_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

func newTestOptions(path string) slotcache.Options {
	return slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
	}
}

func Test_BeginWrite_Returns_ErrBusy_When_Another_Cache_Instance_Holds_Writer(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "exclusive.slc")
	opts := newTestOptions(path)

	cache1, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open cache1: %v", err)
	}

	defer func() { _ = cache1.Close() }()

	writer1, err := cache1.Writer()
	if err != nil {
		t.Fatalf("BeginWrite on cache1: %v", err)
	}

	defer func() { _ = writer1.Close() }()

	cache2, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open cache2: %v", err)
	}

	defer func() { _ = cache2.Close() }()

	writer2, err := cache2.Writer()
	if err == nil {
		_ = writer2.Close()

		t.Fatal("BeginWrite on second cache instance for same path must return ErrBusy; got nil")
	}

	if !errors.Is(err, slotcache.ErrBusy) {
		t.Fatalf("BeginWrite on second cache instance for same path must return ErrBusy; got %v", err)
	}
}

func Test_BeginWrite_Succeeds_When_Different_Path_Has_Active_Writer(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path1 := filepath.Join(tmpDir, "cache1.slc")
	path2 := filepath.Join(tmpDir, "cache2.slc")

	opts1 := newTestOptions(path1)
	opts2 := newTestOptions(path2)

	cache1, err := slotcache.Open(opts1)
	if err != nil {
		t.Fatalf("Open cache1: %v", err)
	}

	defer func() { _ = cache1.Close() }()

	writer1, err := cache1.Writer()
	if err != nil {
		t.Fatalf("BeginWrite on cache1: %v", err)
	}

	defer func() { _ = writer1.Close() }()

	cache2, err := slotcache.Open(opts2)
	if err != nil {
		t.Fatalf("Open cache2: %v", err)
	}

	defer func() { _ = cache2.Close() }()

	writer2, err := cache2.Writer()
	if err != nil {
		t.Fatalf("BeginWrite on different path must succeed while another writer is active; got %v", err)
	}

	_ = writer2.Close()
}

func Test_BeginWrite_Returns_ErrBusy_When_Concurrent_Goroutines_Race(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "concurrent.slc")
	opts := newTestOptions(path)

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = cache.Close() }()

	const numGoroutines = 10

	start := make(chan struct{})
	releaseWinner := make(chan struct{})

	var (
		mu      sync.Mutex
		success int
		busy    int
		other   int
		writers []*slotcache.Writer
	)

	var wg sync.WaitGroup

	wg.Add(numGoroutines)

	for range numGoroutines {
		go func() {
			defer wg.Done()

			cacheInstance, openErr := slotcache.Open(opts)
			if openErr != nil {
				mu.Lock()

				other++

				mu.Unlock()

				return
			}

			defer func() { _ = cacheInstance.Close() }()

			<-start

			writerInstance, beginErr := cacheInstance.Writer()

			mu.Lock()

			switch {
			case beginErr == nil:
				success++

				writers = append(writers, writerInstance)
			case errors.Is(beginErr, slotcache.ErrBusy):
				busy++
			default:
				other++
			}

			mu.Unlock()

			if beginErr == nil {
				<-releaseWinner
			}
		}()
	}

	close(start)

	time.Sleep(50 * time.Millisecond)

	close(releaseWinner)

	wg.Wait()

	for _, writerInstance := range writers {
		_ = writerInstance.Close()
	}

	if success != 1 || busy != numGoroutines-1 || other != 0 {
		t.Fatalf("expected exactly 1 BeginWrite success and %d ErrBusy; got success=%d busy=%d other=%d",
			numGoroutines-1, success, busy, other)
	}
}

func Test_BeginWrite_Returns_ErrBusy_When_Another_Process_Holds_Writer(t *testing.T) {
	t.Parallel()

	if os.Getenv("TK_SLOTCACHE_BEGINWRITE_HELPER") == "1" {
		path := os.Getenv("TK_SLOTCACHE_PATH")
		if path == "" {
			t.Fatal("TK_SLOTCACHE_PATH not set")
		}

		opts := newTestOptions(path)

		cache, openErr := slotcache.Open(opts)
		if openErr != nil {
			t.Fatalf("subprocess Open failed: %v", openErr)
		}

		defer func() { _ = cache.Close() }()

		_, beginErr := cache.Writer()
		if beginErr == nil {
			t.Fatal("subprocess BeginWrite must return ErrBusy while parent holds writer; got nil")
		}

		if !errors.Is(beginErr, slotcache.ErrBusy) {
			t.Fatalf("subprocess BeginWrite must return ErrBusy while parent holds writer; got %v", beginErr)
		}

		return
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "crossproc.slc")
	opts := newTestOptions(path)

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = cache.Close() }()

	writer, err := cache.Writer()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}

	defer func() { _ = writer.Close() }()

	ctx := t.Context()
	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 2*time.Second)

	defer timeoutCancel()

	cmd := exec.CommandContext(timeoutCtx, os.Args[0],
		"-test.run=^Test_BeginWrite_Returns_ErrBusy_When_Another_Process_Holds_Writer$", "-test.v")

	cmd.Env = append(os.Environ(),
		"TK_SLOTCACHE_BEGINWRITE_HELPER=1",
		"TK_SLOTCACHE_PATH="+path,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	runErr := cmd.Run()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		t.Fatal("subprocess timed out: BeginWrite must be non-blocking (likely missing LOCK_NB)")
	}

	if runErr != nil {
		t.Fatalf("subprocess failed: %v", runErr)
	}
}

func Test_WritebackSync_Commits_Successfully_When_Mode_Is_Enabled(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "writeback_sync.slc")

	// Create cache with WritebackSync mode
	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
		Writeback:    slotcache.WritebackSync,
	}

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = cache.Close() }()

	// Write some data
	writer, err := cache.Writer()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}

	key := []byte("testkey1")
	index := []byte{0xDE, 0xAD, 0xBE, 0xEF}

	putErr := writer.Put(key, 12345, index)
	if putErr != nil {
		_ = writer.Close()

		t.Fatalf("Put: %v", putErr)
	}

	// Commit should trigger msync barriers
	commitErr := writer.Commit()
	if commitErr != nil {
		// ErrWriteback is acceptable if msync fails, but commit should complete
		if !errors.Is(commitErr, slotcache.ErrWriteback) {
			_ = writer.Close()

			t.Fatalf("Commit: %v", commitErr)
		}
	}

	_ = writer.Close()

	// Verify data is readable
	entry, found, getErr := cache.Get(key)
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}

	if !found {
		t.Fatal("Get: expected to find key after commit")
	}

	if entry.Revision != 12345 {
		t.Errorf("Get: revision = %d, want 12345", entry.Revision)
	}
}

func Test_WritebackNone_Commits_Without_Msync_When_Mode_Is_Default(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "writeback_none.slc")

	// Create cache with default (WritebackNone) mode
	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		SlotCapacity: 64,
		// Writeback not set = WritebackNone
	}

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = cache.Close() }()

	// Write some data
	writer, err := cache.Writer()
	if err != nil {
		t.Fatalf("BeginWrite: %v", err)
	}

	key := []byte("testkey2")
	index := []byte{0xCA, 0xFE, 0xBA, 0xBE}

	putErr := writer.Put(key, 67890, index)
	if putErr != nil {
		_ = writer.Close()

		t.Fatalf("Put: %v", putErr)
	}

	// Commit should NOT return ErrWriteback (no msync is called)
	commitErr := writer.Commit()
	if commitErr != nil {
		_ = writer.Close()

		t.Fatalf("Commit with WritebackNone should never return error, got: %v", commitErr)
	}

	_ = writer.Close()

	// Verify data is readable
	entry, found, getErr := cache.Get(key)
	if getErr != nil {
		t.Fatalf("Get: %v", getErr)
	}

	if !found {
		t.Fatal("Get: expected to find key after commit")
	}

	if entry.Revision != 67890 {
		t.Errorf("Get: revision = %d, want 67890", entry.Revision)
	}
}

// =============================================================================
// Seqlock Overlap Detection (ErrBusy when generation indicates overlap)
// =============================================================================

// Test_Get_Returns_ErrBusy_When_Bucket_Points_To_Tombstoned_Slot_And_Generation_Changes verifies
// that Get() returns ErrBusy (not ErrCorrupt) when it encounters a bucket pointing to
// a tombstoned slot AND generation changes during the read, indicating overlap.
func Test_Get_Returns_ErrBusy_When_Bucket_Points_To_Tombstoned_Slot_And_Generation_Changes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "overlap_tombstone.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true, // Simplify test: no cross-process locking
	}

	// Create and populate cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()

	// Verify key exists before corruption.
	_, found, getErr := cache.Get(key)
	if getErr != nil {
		t.Fatalf("Get before mutation failed: %v", getErr)
	}

	if !found {
		t.Fatal("Key should exist before mutation")
	}

	_ = cache.Close()

	// Now corrupt the file: clear the slot's USED flag (tombstone the slot) but leave
	// the bucket pointing to it, AND set generation to odd (simulating writer in progress).
	mutateFile(t, path, func(data []byte) {
		// Set generation to odd (simulating active writer).
		binary.LittleEndian.PutUint64(data[offGeneration:], 1) // odd

		// Clear slot 0's USED flag (tombstone the slot).
		slotOffset := slcHeaderSize // slot 0
		meta := binary.LittleEndian.Uint64(data[slotOffset:])
		meta &^= 1 // clear bit 0 (USED)
		binary.LittleEndian.PutUint64(data[slotOffset:], meta)
	})

	// Reopen and verify Get returns ErrBusy.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		// Open itself may return ErrBusy for odd generation without locking.
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, _, getErr2 := cache2.Get(key)
	if !errors.Is(getErr2, slotcache.ErrBusy) {
		t.Fatalf("Get() should return ErrBusy for bucket→tombstone overlap; got %v", getErr2)
	}
}

// Test_Get_Returns_ErrBusy_When_Bucket_Points_Beyond_Highwater_And_Generation_Changes verifies
// that Get() returns ErrBusy when a bucket references a slot beyond highwater AND generation
// indicates overlap.
func Test_Get_Returns_ErrBusy_When_Bucket_Points_Beyond_Highwater_And_Generation_Changes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "overlap_highwater.slc")

	key := []byte("testkey1")
	index := []byte{0x01, 0x02, 0x03, 0x04}

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create and populate cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	w, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(key, 100, index)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()
	_ = cache.Close()

	// Corrupt: set highwater to 0 (no slots allocated) but leave bucket pointing to slot 0.
	// Also set generation to odd to simulate overlap.
	mutateFile(t, path, func(data []byte) {
		// Set generation to odd.
		binary.LittleEndian.PutUint64(data[offGeneration:], 3)

		// Set highwater to 0.
		binary.LittleEndian.PutUint64(data[offSlotHighwater:], 0)
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return // Expected for odd generation
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, _, getErr := cache2.Get(key)
	if !errors.Is(getErr, slotcache.ErrBusy) {
		t.Fatalf("Get() should return ErrBusy for slot beyond highwater with overlap; got %v", getErr)
	}
}

// Test_Len_Returns_ErrBusy_When_Generation_Odd verifies that Len() returns ErrBusy
// when generation is odd (writer in progress), matching spec semantics.
func Test_Len_Returns_ErrBusy_When_Generation_Odd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "len_busy.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	_ = cache.Close()

	// Set generation to odd.
	mutateFile(t, path, func(data []byte) {
		binary.LittleEndian.PutUint64(data[offGeneration:], 5)
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return // Expected
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, lenErr := cache2.Len()
	if !errors.Is(lenErr, slotcache.ErrBusy) {
		t.Fatalf("Len() should return ErrBusy when generation is odd; got %v", lenErr)
	}
}

// Test_Scan_Returns_ErrBusy_When_Generation_Odd verifies that Scan() returns ErrBusy
// when generation is odd.
func Test_Scan_Returns_ErrBusy_When_Generation_Odd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_busy.slc")

	opts := slotcache.Options{
		Path:           path,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	// Create cache.
	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	_ = cache.Close()

	// Set generation to odd.
	mutateFile(t, path, func(data []byte) {
		binary.LittleEndian.PutUint64(data[offGeneration:], 7)
	})

	// Reopen.
	cache2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		if errors.Is(reopenErr, slotcache.ErrBusy) {
			return // Expected
		}

		t.Fatalf("Open after mutation failed: %v", reopenErr)
	}

	defer func() { _ = cache2.Close() }()

	_, scanErr := cache2.Scan(slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrBusy) {
		t.Fatalf("Scan() should return ErrBusy when generation is odd; got %v", scanErr)
	}
}

// =============================================================================
// Seqlock Cross-Process Concurrency (stress tests)
// =============================================================================

// Test duration for stress-style concurrency tests.
// Override via: go test ./pkg/slotcache -run Seqlock -slotcache.concurrency-stress=10s.
var flagConcurrencyStress = flag.Duration("slotcache.concurrency-stress", 1*time.Second, "duration for slotcache seqlock concurrency stress tests")

var (
	seqlockKey    = []byte("seqlock!")
	seqlockIndexA = []byte{0xAA, 0xAA, 0xAA, 0xAA}
	seqlockIndexB = []byte{0xBB, 0xBB, 0xBB, 0xBB}
)

const (
	seqlockRevA int64 = 0x00FF00FF00FF00FF
	seqlockRevB int64 = 0x0100010001000100
)

func Test_Open_Returns_ErrBusy_When_Generation_Odd_And_WriterLock_Held_Even_If_Header_Is_In_Flux(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "influx.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(create) failed: %v", openErr)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close(create) failed: %v", closeErr)
	}

	// Hold the writer lock to simulate an active writer.
	lockFile := lockFileExclusive(t, path)

	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	// Mutate the header into a state that can happen mid-commit:
	// - generation is odd
	// - header CRC is invalid (because other header fields changed but CRC didn't)
	mutateHeader(t, path, func(hdr []byte) {
		binary.LittleEndian.PutUint64(hdr[offGeneration:offGeneration+8], 1) // odd

		// Change a CRC-covered field without updating header_crc32c.
		lc := binary.LittleEndian.Uint64(hdr[offLiveCount : offLiveCount+8])
		binary.LittleEndian.PutUint64(hdr[offLiveCount:offLiveCount+8], lc+1)

		// Leave hdr[offHeaderCRC32C:offHeaderCRC32C+4] untouched so CRC mismatches.
		_ = hdr[offHeaderCRC32C]
	})

	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrBusy) {
		t.Fatalf("Open() must return ErrBusy for odd generation while writer lock is held; got %v", reopenErr)
	}
}

func Test_Open_Returns_ErrCorrupt_When_Generation_Odd_And_No_Writer_Lock_Held(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "crashed.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(create) failed: %v", openErr)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close(create) failed: %v", closeErr)
	}

	// Simulate a crashed writer by leaving generation odd but without holding the lock.
	mutateHeader(t, path, func(hdr []byte) {
		binary.LittleEndian.PutUint64(hdr[offGeneration:offGeneration+8], 1) // odd
	})

	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrCorrupt) {
		t.Fatalf("Open() must return ErrCorrupt for odd generation when no writer is active; got %v", reopenErr)
	}
}

func Test_Open_Returns_ErrBusy_When_Creating_New_File_And_WriterLock_Held(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "create_busy.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Ensure the cache file does not exist so Open() must take the create path.
	_ = os.Remove(path)

	lockFile := lockFileExclusive(t, path)

	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	c, err := slotcache.Open(opts)
	if err == nil {
		_ = c.Close()

		t.Fatal("Open() must return ErrBusy when creating a missing cache file while the writer lock is held; got nil")
	}

	if !errors.Is(err, slotcache.ErrBusy) {
		t.Fatalf("Open() must return ErrBusy when creating a missing cache file while the writer lock is held; got %v", err)
	}

	// With locking enabled, Open() must not create/replace the cache file when the lock is busy.
	_, statErr := os.Stat(path)
	if !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected cache file to not be created while lock is held; statErr=%v", statErr)
	}
}

func Test_Open_Returns_ErrBusy_When_Initializing_ZeroByte_File_And_WriterLock_Held(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "zerobyte_busy.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Pre-create a 0-byte file so Open() must take the "initialize in place" path.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatalf("create 0-byte file: %v", err)
	}

	_ = f.Close()

	lockFile := lockFileExclusive(t, path)

	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
	}()

	c, openErr := slotcache.Open(opts)
	if openErr == nil {
		_ = c.Close()

		t.Fatal("Open() must return ErrBusy when initializing a 0-byte cache file while the writer lock is held; got nil")
	}

	if !errors.Is(openErr, slotcache.ErrBusy) {
		t.Fatalf("Open() must return ErrBusy when initializing a 0-byte cache file while the writer lock is held; got %v", openErr)
	}

	st, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("stat 0-byte file: %v", statErr)
	}

	if st.Size() != 0 {
		t.Fatalf("expected cache file to remain 0 bytes while lock is held; got size=%d", st.Size())
	}
}

func Test_Seqlock_CrossProcess_Get_Does_Not_Observe_Torn_Updates_When_Writer_Commits_Concurrently(t *testing.T) {
	t.Parallel()

	// This is a stress-style test: it tries to catch seqlock/atomicity violations that
	// only manifest under real cross-process overlap.
	if os.Getenv("TK_SLOTCACHE_SEQLOCK_HELPER") == "1" {
		runSeqlockWriterHelper(t)

		return
	}

	duration := *flagConcurrencyStress
	if testing.Short() {
		duration = 250 * time.Millisecond
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "stress.slc")
	stopPath := filepath.Join(tmpDir, "STOP")
	readyPath := filepath.Join(tmpDir, "READY")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Ensure the file exists before starting the helper.
	c0, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	_ = c0.Close()

	ctx := t.Context()

	timeoutCtx, cancel := context.WithTimeout(ctx, duration+3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, os.Args[0],
		"-test.run=^Test_Seqlock_CrossProcess_Get_Does_Not_Observe_Torn_Updates_When_Writer_Commits_Concurrently$", "-test.v")

	cmd.Env = append(os.Environ(),
		"TK_SLOTCACHE_SEQLOCK_HELPER=1",
		"TK_SLOTCACHE_PATH="+path,
		"TK_SLOTCACHE_STOP="+stopPath,
		"TK_SLOTCACHE_READY="+readyPath,
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	startErr := cmd.Start()
	if startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	waitForFile(t, readyPath, 2*time.Second)

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(reader) failed: %v", err)
	}

	defer func() { _ = cache.Close() }()

	readerCtx, readerCancel := context.WithTimeout(ctx, duration)
	defer readerCancel()

	// Use multiple goroutines to increase the chance of overlapping the writer's
	// generation/revision writes.
	nReaders := max(2, runtime.GOMAXPROCS(0))

	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(nReaders)

	for range nReaders {
		go func() {
			defer wg.Done()

			for readerCtx.Err() == nil {
				entry, found, getErr := cache.Get(seqlockKey)
				if getErr != nil {
					if errors.Is(getErr, slotcache.ErrBusy) {
						continue
					}

					sendErr(errCh, fmt.Errorf("Get returned unexpected error: %w", getErr))

					return
				}

				if !found {
					continue
				}

				if !bytes.Equal(entry.Key, seqlockKey) {
					sendErr(errCh, fmt.Errorf("Get returned wrong key: got=%x want=%x", entry.Key, seqlockKey))

					return
				}

				if entry.Revision != seqlockRevA && entry.Revision != seqlockRevB {
					sendErr(errCh, fmt.Errorf("Get observed torn/invalid revision: got=0x%016X", uint64(entry.Revision)))

					return
				}

				if !bytes.Equal(entry.Index, seqlockIndexA) && !bytes.Equal(entry.Index, seqlockIndexB) {
					sendErr(errCh, fmt.Errorf("Get observed torn/invalid index: got=%x", entry.Index))

					return
				}
			}
		}()
	}

	wg.Wait()

	// Stop helper and wait for clean exit.
	touchFile(t, stopPath)

	waitErr := cmd.Wait()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		t.Fatal("helper timed out")
	}

	if waitErr != nil {
		t.Fatalf("helper failed: %v", waitErr)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func Test_Open_Does_Not_Return_ErrCorrupt_When_Writer_Commits_Concurrently(t *testing.T) {
	t.Parallel()

	// Parent/child split: the child loops commits; the parent loops Open/Close.
	if os.Getenv("TK_SLOTCACHE_OPEN_STRESS_HELPER") == "1" {
		runSeqlockWriterHelper(t)

		return
	}

	duration := *flagConcurrencyStress
	if testing.Short() {
		duration = 250 * time.Millisecond
	}

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "openstress.slc")
	stopPath := filepath.Join(tmpDir, "STOP")
	readyPath := filepath.Join(tmpDir, "READY")

	opts := slotcache.Options{
		Path:         cachePath,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Ensure the file exists before starting the helper.
	c0, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(create) failed: %v", openErr)
	}

	closeErr := c0.Close()
	if closeErr != nil {
		t.Fatalf("Close(create) failed: %v", closeErr)
	}

	ctx := t.Context()

	timeoutCtx, cancel := context.WithTimeout(ctx, duration+3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, os.Args[0],
		"-test.run=^Test_Open_Does_Not_Return_ErrCorrupt_When_Writer_Commits_Concurrently$", "-test.v")

	cmd.Env = append(os.Environ(),
		"TK_SLOTCACHE_OPEN_STRESS_HELPER=1",
		"TK_SLOTCACHE_PATH="+cachePath,
		"TK_SLOTCACHE_STOP="+stopPath,
		"TK_SLOTCACHE_READY="+readyPath,
		// Slow down commits a bit so Open has a better chance to observe odd-generation windows.
		"TK_SLOTCACHE_WRITEBACK=sync",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	startErr := cmd.Start()
	if startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	waitForFile(t, readyPath, 2*time.Second)

	deadline := time.Now().Add(duration)
	for time.Now().Before(deadline) {
		c, openErr := slotcache.Open(opts)
		if openErr == nil {
			closeErr := c.Close()
			if closeErr != nil {
				t.Fatalf("Close(opened) failed unexpectedly: %v", closeErr)
			}

			continue
		}

		if errors.Is(openErr, slotcache.ErrBusy) {
			continue
		}

		// Under active concurrent commits, Open must not misclassify transient state as corrupt.
		if errors.Is(openErr, slotcache.ErrCorrupt) {
			t.Fatalf("Open() returned ErrCorrupt while writer is committing; got %v", openErr)
		}

		t.Fatalf("Open() returned unexpected error while writer is committing: %v", openErr)
	}

	// Stop helper and wait for clean exit.
	touchFile(t, stopPath)

	waitErr := cmd.Wait()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		t.Fatal("helper timed out")
	}

	if waitErr != nil {
		t.Fatalf("helper failed: %v", waitErr)
	}
}

func Test_Reads_Return_ErrBusy_When_Generation_Is_Odd(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	cachePath := filepath.Join(tmpDir, "oddgen.slc")

	opts := slotcache.Options{
		Path:           cachePath,
		KeySize:        8,
		IndexSize:      4,
		UserVersion:    1,
		SlotCapacity:   64,
		DisableLocking: true,
	}

	cache, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}

	defer func() { _ = cache.Close() }()

	w, beginErr := cache.Writer()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	putErr := w.Put(seqlockKey, seqlockRevA, seqlockIndexA)
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	_ = w.Close()

	// Force an "in-progress" state: odd generation.
	mutateHeader(t, cachePath, func(hdr []byte) {
		binary.LittleEndian.PutUint64(hdr[offGeneration:offGeneration+8], 1)
	})

	_, lenErr := cache.Len()
	if !errors.Is(lenErr, slotcache.ErrBusy) {
		t.Fatalf("Len() must return ErrBusy when generation is odd; got %v", lenErr)
	}

	_, _, getErr := cache.Get(seqlockKey)
	if !errors.Is(getErr, slotcache.ErrBusy) {
		t.Fatalf("Get() must return ErrBusy when generation is odd; got %v", getErr)
	}
}

func runSeqlockWriterHelper(t *testing.T) {
	t.Helper()

	path := os.Getenv("TK_SLOTCACHE_PATH")
	stopPath := os.Getenv("TK_SLOTCACHE_STOP")
	readyPath := os.Getenv("TK_SLOTCACHE_READY")

	if path == "" || stopPath == "" || readyPath == "" {
		t.Fatal("missing helper env vars")
	}

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	if os.Getenv("TK_SLOTCACHE_WRITEBACK") == "sync" {
		opts.Writeback = slotcache.WritebackSync
	}

	cache, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("helper Open failed: %v", err)
	}

	defer func() { _ = cache.Close() }()

	touchFile(t, readyPath)

	i := 0

	for !fileExists(stopPath) {
		w, err := cache.Writer()
		if err != nil {
			if errors.Is(err, slotcache.ErrBusy) {
				continue
			}

			t.Fatalf("helper BeginWrite failed: %v", err)
		}

		rev := seqlockRevA
		idx := seqlockIndexA

		if i%2 == 1 {
			rev = seqlockRevB
			idx = seqlockIndexB
		}

		_ = w.Put(seqlockKey, rev, idx)
		_ = w.Commit()
		_ = w.Close()

		i++
	}
}

func lockFileExclusive(tb testing.TB, cachePath string) *os.File {
	tb.Helper()

	lockPath := cachePath + ".lock"

	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		tb.Fatalf("open lock file: %v", err)
	}

	lockErr := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if lockErr != nil {
		_ = f.Close()

		tb.Fatalf("flock: %v", lockErr)
	}

	return f
}

func fileExists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}

func touchFile(tb testing.TB, path string) {
	tb.Helper()

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		tb.Fatalf("touch %q: %v", path, err)
	}

	_ = f.Close()
}

func waitForFile(tb testing.TB, path string, timeout time.Duration) {
	tb.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fileExists(path) {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	tb.Fatalf("timed out waiting for file %q", path)
}

func sendErr(ch chan<- error, err error) {
	select {
	case ch <- err:
	default:
	}
}

// =============================================================================
// Seqlock Torn Bytes Regression Test
// =============================================================================

// Test_Seqlock_CrossProcess_Get_Does_Not_Return_Mixed_Revision_Or_Index_When_Bytes_Torn is
// an adversarial stress test that spawns a helper process to mutate the mmapped cache file
// byte-by-byte to simulate torn reads.
//
// While bytes are being torn:
//   - Cache.Get() may return ErrBusy (transient) and should be retried.
//   - If Cache.Get() returns an entry, it MUST be exactly one of two stable
//     states (A or B). It MUST NOT return a mixed/torn revision or index.
func Test_Seqlock_CrossProcess_Get_Does_Not_Return_Mixed_Revision_Or_Index_When_Bytes_Torn(t *testing.T) {
	t.Parallel()

	if os.Getenv("TK_SLOTCACHE_TORN_HELPER") == "1" {
		runTornBytesHelper(t)

		return
	}

	duration := *flagConcurrencyStress
	if testing.Short() {
		duration = 250 * time.Millisecond
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "tornbytes.slc")
	stopPath := filepath.Join(tmpDir, "STOP")
	readyPath := filepath.Join(tmpDir, "READY")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create initial file with a valid entry.
	c0, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w0, err := c0.Writer()
	if err != nil {
		t.Fatalf("BeginWrite(create) failed: %v", err)
	}

	_ = w0.Put(seqlockKey, seqlockRevA, seqlockIndexA)
	_ = w0.Commit()
	_ = w0.Close()
	_ = c0.Close()

	ctx := t.Context()

	timeoutCtx, cancel := context.WithTimeout(ctx, duration+3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, os.Args[0],
		"-test.run=^Test_Seqlock_CrossProcess_Get_Does_Not_Return_Mixed_Revision_Or_Index_When_Bytes_Torn$", "-test.v")

	cmd.Env = append(os.Environ(),
		"TK_SLOTCACHE_TORN_HELPER=1",
		"TK_SLOTCACHE_PATH="+path,
		"TK_SLOTCACHE_STOP="+stopPath,
		"TK_SLOTCACHE_READY="+readyPath,
		"TK_SLOTCACHE_DISABLE_LOCKING=1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	startErr := cmd.Start()
	if startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	waitForFile(t, readyPath, 2*time.Second)

	readerOpts := opts
	readerOpts.DisableLocking = true

	cache, err := slotcache.Open(readerOpts)
	if err != nil {
		if errors.Is(err, slotcache.ErrBusy) {
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				cache, err = slotcache.Open(readerOpts)
				if err == nil {
					break
				}

				if !errors.Is(err, slotcache.ErrBusy) {
					break
				}

				time.Sleep(5 * time.Millisecond)
			}
		}

		if err != nil {
			t.Fatalf("Open(reader) failed: %v", err)
		}
	}

	defer func() { _ = cache.Close() }()

	readerCtx, readerCancel := context.WithTimeout(ctx, duration)
	defer readerCancel()

	nReaders := max(2, runtime.GOMAXPROCS(0))

	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(nReaders)

	for range nReaders {
		go func() {
			defer wg.Done()

			for readerCtx.Err() == nil {
				entry, found, getErr := cache.Get(seqlockKey)
				if getErr != nil {
					if errors.Is(getErr, slotcache.ErrBusy) {
						continue
					}

					sendErr(errCh, fmt.Errorf("Get returned unexpected error: %w", getErr))

					return
				}

				if !found {
					continue
				}

				if !bytes.Equal(entry.Key, seqlockKey) {
					sendErr(errCh, fmt.Errorf("Get returned wrong key: got=%x want=%x", entry.Key, seqlockKey))

					return
				}

				if entry.Revision != seqlockRevA && entry.Revision != seqlockRevB {
					sendErr(errCh, fmt.Errorf("Get observed mixed/torn revision: got=0x%016X", uint64(entry.Revision)))

					return
				}

				if !bytes.Equal(entry.Index, seqlockIndexA) && !bytes.Equal(entry.Index, seqlockIndexB) {
					sendErr(errCh, fmt.Errorf("Get observed mixed/torn index: got=%x", entry.Index))

					return
				}
			}
		}()
	}

	wg.Wait()

	touchFile(t, stopPath)

	waitErr := cmd.Wait()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		t.Fatal("helper timed out")
	}

	if waitErr != nil {
		t.Fatalf("helper failed: %v", waitErr)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func runTornBytesHelper(t *testing.T) {
	t.Helper()

	path := os.Getenv("TK_SLOTCACHE_PATH")
	stopPath := os.Getenv("TK_SLOTCACHE_STOP")
	readyPath := os.Getenv("TK_SLOTCACHE_READY")

	if path == "" || stopPath == "" || readyPath == "" {
		t.Fatal("missing helper env vars")
	}

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("helper open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		t.Fatalf("helper stat file: %v", err)
	}

	sz := int(st.Size())

	data, err := syscall.Mmap(int(f.Fd()), 0, sz, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("helper mmap: %v", err)
	}

	defer func() { _ = syscall.Munmap(data) }()

	// Slot layout for keySize=8, indexSize=4:
	// slot_size = align8(8+8+0+8+4) = 32
	const (
		slotsOffset = slcHeaderSize
		slotID0     = 0
		slotSize    = 32
		slotOffset  = slotsOffset + slotID0*slotSize
		revOffset   = slotOffset + 16
		idxOffset   = slotOffset + 24
	)

	var (
		genA   uint64 = 100
		genOdd uint64 = 101
		genB   uint64 = 200
	)

	touchFile(t, readyPath)

	for !fileExists(stopPath) {
		// Commit A -> B.
		writeUint64Torn(data, offGeneration, genOdd)
		writeInt64Torn(data, revOffset, seqlockRevB)
		writeBytesTorn(data, idxOffset, seqlockIndexB)
		writeUint64Torn(data, offGeneration, genB)

		// Commit B -> A.
		writeUint64Torn(data, offGeneration, genOdd)
		writeInt64Torn(data, revOffset, seqlockRevA)
		writeBytesTorn(data, idxOffset, seqlockIndexA)
		writeUint64Torn(data, offGeneration, genA)
	}
}

func writeUint64Torn(data []byte, off int, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)

	for i := range 8 {
		data[off+i] = buf[i]

		time.Sleep(50 * time.Microsecond)
	}
}

func writeInt64Torn(data []byte, off int, v int64) {
	writeUint64Torn(data, off, uint64(v))
}

func writeBytesTorn(data []byte, off int, b []byte) {
	for i := range b {
		data[off+i] = b[i]

		time.Sleep(50 * time.Microsecond)
	}
}
