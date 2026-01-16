package slotcache_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

const osWindows = "windows"

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

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix flock (syscall.Flock)")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "exclusive.slc")
	opts := newTestOptions(path)

	cache1, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open cache1: %v", err)
	}

	defer func() { _ = cache1.Close() }()

	writer1, err := cache1.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite on cache1: %v", err)
	}

	defer func() { _ = writer1.Close() }()

	cache2, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open cache2: %v", err)
	}

	defer func() { _ = cache2.Close() }()

	writer2, err := cache2.BeginWrite()
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

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix flock (syscall.Flock)")
	}

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

	writer1, err := cache1.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite on cache1: %v", err)
	}

	defer func() { _ = writer1.Close() }()

	cache2, err := slotcache.Open(opts2)
	if err != nil {
		t.Fatalf("Open cache2: %v", err)
	}

	defer func() { _ = cache2.Close() }()

	writer2, err := cache2.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite on different path must succeed while another writer is active; got %v", err)
	}

	_ = writer2.Close()
}

func Test_BeginWrite_Returns_ErrBusy_When_Concurrent_Goroutines_Race(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix flock (syscall.Flock)")
	}

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
		writers []slotcache.Writer
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

			writerInstance, beginErr := cacheInstance.BeginWrite()

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

	if runtime.GOOS == osWindows {
		t.Skip("requires Unix flock (syscall.Flock)")
	}

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

		_, beginErr := cache.BeginWrite()
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

	writer, err := cache.BeginWrite()
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
