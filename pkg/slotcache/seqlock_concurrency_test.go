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

// Test duration for stress-style concurrency tests.
// Override via: go test ./pkg/slotcache -run Seqlock -slotcache.concurrency-stress=10s.
var flagConcurrencyStress = flag.Duration("slotcache.concurrency-stress", 1*time.Second, "duration for slotcache seqlock concurrency stress tests")

const (
	slcHeaderSize = 256

	offLiveCount    = 0x030
	offGeneration   = 0x040
	offHeaderCRC32C = 0x070
)

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
	//
	// Open() should treat this as transient writer activity (ErrBusy) rather than ErrCorrupt,
	// because the writer lock is held by another process.
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
	// Note: generation is excluded from the header CRC per spec, so CRC remains valid.
	mutateHeader(t, path, func(hdr []byte) {
		binary.LittleEndian.PutUint64(hdr[offGeneration:offGeneration+8], 1) // odd
	})

	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrCorrupt) {
		t.Fatalf("Open() must return ErrCorrupt for odd generation when no writer is active; got %v", reopenErr)
	}
}

func Test_Seqlock_CrossProcess_Get_Does_Not_Observe_Torn_Updates_When_Writer_Commits_Concurrently(t *testing.T) {
	t.Parallel()

	// This is a stress-style test: it tries to catch seqlock/atomicity violations that
	// only manifest under real cross-process overlap.
	//
	// Default runtime is short (1s) but configurable via -slotcache.concurrency-stress.
	// If investigating a suspected issue, re-run with 10s or longer.
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

	w, beginErr := cache.BeginWrite()
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
		w, err := cache.BeginWrite()
		if err != nil {
			// If reader holds a writer somehow (shouldn't), treat as transient.
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

// mutateHeader reads the first 256 bytes, applies mutate, then writes it back.
func mutateHeader(tb testing.TB, path string, mutate func([]byte)) {
	tb.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		tb.Fatalf("open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	hdr := make([]byte, slcHeaderSize)

	n, err := f.ReadAt(hdr, 0)
	if err != nil {
		tb.Fatalf("read header: %v", err)
	}

	if n != slcHeaderSize {
		tb.Fatalf("read header size mismatch: got=%d want=%d", n, slcHeaderSize)
	}

	mutate(hdr)

	_, err = f.WriteAt(hdr, 0)
	if err != nil {
		tb.Fatalf("write header: %v", err)
	}

	_ = f.Sync()
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
