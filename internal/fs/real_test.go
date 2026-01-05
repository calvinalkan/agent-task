package fs

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// =============================================================================
// Real FS Tests
//
// These tests verify our Real implementation's helper methods work correctly.
// We're NOT testing os.ReadFile, os.WriteFile etc (that's Go's job).
// We ARE testing:
//   - Exists() - our convenience method
//   - Lock() - our locking implementation
//   - WriteFileAtomic() - our atomic write wrapper
// =============================================================================

// -----------------------------------------------------------------------------
// Exists() Tests
// -----------------------------------------------------------------------------

// TestReal_Exists_ReturnsFalseForNonExistent verifies that Exists() returns
// (false, nil) for files that don't exist - not an error.
func TestReal_Exists_ReturnsFalseForNonExistent(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()

	exists, err := fs.Exists(filepath.Join(dir, "does-not-exist.txt"))

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	if got, want := exists, false; got != want {
		t.Fatalf("exists=%v, want=%v", got, want)
	}
}

// TestReal_Exists_ReturnsTrueForFile verifies that Exists() returns
// (true, nil) for files that exist.
func TestReal_Exists_ReturnsTrueForFile(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "exists.txt")

	// Create file
	if err := os.WriteFile(path, []byte("hello"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := fs.Exists(path)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	if got, want := exists, true; got != want {
		t.Fatalf("exists=%v, want=%v", got, want)
	}
}

// TestReal_Exists_ReturnsTrueForDirectory verifies that Exists() works
// for directories too, not just files.
func TestReal_Exists_ReturnsTrueForDirectory(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")

	if err := os.MkdirAll(subdir, 0755); err != nil {
		t.Fatalf("setup: %v", err)
	}

	exists, err := fs.Exists(subdir)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	if got, want := exists, true; got != want {
		t.Fatalf("exists=%v, want=%v", got, want)
	}
}

// -----------------------------------------------------------------------------
// Lock() Tests
// -----------------------------------------------------------------------------

// TestReal_Lock_AcquireAndRelease verifies basic lock acquire/release works.
func TestReal_Lock_AcquireAndRelease(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	lock, err := fs.Lock(path)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Lock err=%v, want=%v", got, want)
	}

	if got, want := lock.Close(), error(nil); !errors.Is(got, want) {
		t.Fatalf("Close err=%v, want=%v", got, want)
	}
}

// TestReal_Lock_SecondLockBlocks verifies that a second lock on the same
// path blocks until the first is released.
//
// This tests the actual locking behavior, not just the API.
func TestReal_Lock_SecondLockBlocks(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	// Acquire first lock
	lock1, err := fs.Lock(path)
	if err != nil {
		t.Fatalf("first Lock err=%v, want=nil", err)
	}

	// Try to acquire second lock in goroutine
	var (
		lock2     Locker
		lock2Err  error
		lock2Time time.Time
	)

	done := make(chan struct{})

	go func() {
		lock2, lock2Err = fs.Lock(path)
		lock2Time = time.Now()

		close(done)
	}()

	// Wait a bit to ensure goroutine is blocked
	time.Sleep(100 * time.Millisecond)

	// Release first lock
	releaseTime := time.Now()

	lock1.Close()

	// Wait for second lock to acquire
	select {
	case <-done:
		// Good - second lock acquired
	case <-time.After(3 * time.Second):
		t.Fatal("second Lock should acquire after first is released")
	}

	if got, want := lock2Err, error(nil); !errors.Is(got, want) {
		t.Fatalf("second Lock err=%v, want=%v", got, want)
	}

	// Verify second lock acquired AFTER first was released
	if got, want := lock2Time.After(releaseTime), true; got != want {
		t.Fatal("second lock should acquire after first is released")
	}

	lock2.Close()
}

// TestReal_Lock_CanReacquireAfterRelease verifies that after releasing a lock,
// it can be acquired again (no deadlock or leaked state).
func TestReal_Lock_CanReacquireAfterRelease(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	// Acquire and release multiple times
	for i := range 3 {
		lock, err := fs.Lock(path)
		if got, want := err, error(nil); !errors.Is(got, want) {
			t.Fatalf("attempt %d: Lock err=%v, want=%v", i, got, want)
		}

		lock.Close()
	}
}

// TestReal_Lock_DifferentPathsIndependent verifies that locks on different
// paths don't interfere with each other.
func TestReal_Lock_DifferentPathsIndependent(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path1 := filepath.Join(dir, "file1.txt")
	path2 := filepath.Join(dir, "file2.txt")

	// Acquire lock on path1
	lock1, err := fs.Lock(path1)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Lock(path1) err=%v, want=%v", got, want)
	}
	defer lock1.Close()

	// Should be able to acquire lock on path2 immediately
	lock2, err := fs.Lock(path2)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Lock(path2) err=%v, want=%v", got, want)
	}
	defer lock2.Close()
}

// TestReal_Lock_CreatesLocksDirectory verifies that Lock() creates the
// .locks subdirectory if it doesn't exist.
func TestReal_Lock_CreatesLocksDirectory(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	locksDir := filepath.Join(dir, ".locks")

	// Verify .locks doesn't exist yet
	if _, err := os.Stat(locksDir); !os.IsNotExist(err) {
		t.Fatal(".locks should not exist before Lock()")
	}

	// Acquire lock
	lock, err := fs.Lock(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Lock err=%v, want=%v", got, want)
	}
	defer lock.Close()

	// Verify .locks was created
	if _, err := os.Stat(locksDir); err != nil {
		t.Fatalf(".locks should exist after Lock(), err=%v", err)
	}
}

// TestReal_Lock_SurvivesLockFileDeletion verifies that if the lock file is
// deleted while waiting for the lock, Lock() retries and still acquires it.
//
// This tests the inode verification logic:
//  1. Process A opens lock file (inode 123)
//  2. Process A waits for flock
//  3. Process B deletes the lock file
//  4. Process C creates new lock file (inode 456)
//  5. Process A acquires flock, but inode changed! Must retry.
func TestReal_Lock_SurvivesLockFileDeletion(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	locksDir := filepath.Join(dir, ".locks")
	lockPath := filepath.Join(locksDir, "data.txt.lock")

	// Acquire first lock
	lock1, err := fs.Lock(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("first Lock err=%v, want=%v", got, want)
	}

	// Start goroutine that will wait for lock
	lock2Done := make(chan struct{})

	var (
		lock2    Locker
		lock2Err error
	)

	go func() {
		lock2, lock2Err = fs.Lock(path)

		close(lock2Done)
	}()

	// Give goroutine time to start waiting
	time.Sleep(50 * time.Millisecond)

	// Delete the lock file while goroutine is waiting!
	// This simulates the race condition the inode check protects against.
	os.Remove(lockPath)

	// Release first lock
	lock1.Close()

	// Second lock should still succeed (after retry)
	select {
	case <-lock2Done:
		if got, want := lock2Err, error(nil); !errors.Is(got, want) {
			t.Fatalf("second Lock err=%v, want=%v", got, want)
		}

		lock2.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("second Lock should succeed after lock file deletion + retry")
	}
}

// TestReal_Lock_SurvivesLockFileReplacement verifies that if the lock file is
// replaced (deleted + recreated) while waiting, Lock() retries correctly.
func TestReal_Lock_SurvivesLockFileReplacement(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	locksDir := filepath.Join(dir, ".locks")
	lockPath := filepath.Join(locksDir, "data.txt.lock")

	// Acquire first lock
	lock1, err := fs.Lock(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("first Lock err=%v, want=%v", got, want)
	}

	// Start goroutine that will wait for lock
	lock2Done := make(chan struct{})

	var (
		lock2    Locker
		lock2Err error
	)

	go func() {
		lock2, lock2Err = fs.Lock(path)

		close(lock2Done)
	}()

	// Give goroutine time to start waiting
	time.Sleep(50 * time.Millisecond)

	// Delete AND recreate the lock file - this changes the inode!
	os.Remove(lockPath)
	os.WriteFile(lockPath, []byte{}, 0644) // New inode

	// Release first lock
	lock1.Close()

	// Second lock should still succeed (detects inode change, retries)
	select {
	case <-lock2Done:
		if got, want := lock2Err, error(nil); !errors.Is(got, want) {
			t.Fatalf("second Lock err=%v, want=%v", got, want)
		}

		lock2.Close()
	case <-time.After(3 * time.Second):
		t.Fatal("second Lock should succeed after lock file replacement + retry")
	}
}

// TestReal_Lock_TimeoutWhenContended verifies that Lock() returns
// ErrDeadlineExceeded when it can't acquire the lock within the timeout.
func TestReal_Lock_TimeoutWhenContended(t *testing.T) {
	// Skip in short mode - this test waits for timeout
	if testing.Short() {
		t.Skip("skipping timeout test in short mode")
	}

	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	// Acquire lock and hold it
	lock1, err := fs.Lock(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("first Lock err=%v, want=%v", got, want)
	}
	defer lock1.Close()

	// Try to acquire second lock - should timeout
	start := time.Now()
	_, err = fs.Lock(path)
	elapsed := time.Since(start)

	if got, want := err, os.ErrDeadlineExceeded; !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	// Should have waited approximately lockTimeout (2 seconds)
	if got, want := elapsed >= 1*time.Second, true; got != want {
		t.Fatalf("elapsed=%v, want at least 1s", elapsed)
	}
}

// TestReal_Lock_ReleaseCleansUpLockFile verifies that releasing a lock
// removes the lock file.
func TestReal_Lock_ReleaseCleansUpLockFile(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	locksDir := filepath.Join(dir, ".locks")
	lockPath := filepath.Join(locksDir, "data.txt.lock")

	// Acquire lock
	lock, err := fs.Lock(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Lock err=%v, want=%v", got, want)
	}

	// Lock file should exist
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("lock file should exist while locked: %v", err)
	}

	// Release lock
	lock.Close()

	// Lock file should be removed
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatalf("lock file should be removed after release, err=%v", err)
	}
}

// -----------------------------------------------------------------------------
// WriteFileAtomic() Tests
// -----------------------------------------------------------------------------

// TestReal_WriteFileAtomic_CreatesFile verifies basic atomic write creates file.
func TestReal_WriteFileAtomic_CreatesFile(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := fs.WriteFileAtomic(path, []byte("hello"), 0644)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("WriteFileAtomic err=%v, want=%v", got, want)
	}

	data, err := os.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("ReadFile err=%v, want=%v", got, want)
	}

	if got, want := string(data), "hello"; got != want {
		t.Fatalf("content=%q, want=%q", got, want)
	}
}

// TestReal_WriteFileAtomic_OverwritesExisting verifies atomic write overwrites.
func TestReal_WriteFileAtomic_OverwritesExisting(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Write initial content
	fs.WriteFileAtomic(path, []byte("first"), 0644)

	// Overwrite
	err := fs.WriteFileAtomic(path, []byte("second"), 0644)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("WriteFileAtomic err=%v, want=%v", got, want)
	}

	data, _ := os.ReadFile(path)
	if got, want := string(data), "second"; got != want {
		t.Fatalf("content=%q, want=%q", got, want)
	}
}

// TestReal_WriteFileAtomic_NoTempFileLeftOnSuccess verifies no .tmp files
// are left behind after successful write.
func TestReal_WriteFileAtomic_NoTempFileLeftOnSuccess(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	fs.WriteFileAtomic(path, []byte("hello"), 0644)

	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if got, want := len(matches), 0; got != want {
		t.Fatalf("tempFileCount=%d, want=%d (found: %v)", got, want, matches)
	}
}

// TestReal_WriteFileAtomic_ConcurrentWritesSafe verifies concurrent atomic
// writes don't corrupt each other - each write is atomic.
func TestReal_WriteFileAtomic_ConcurrentWritesSafe(t *testing.T) {
	fs := NewReal()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	var wg sync.WaitGroup

	writers := 10
	writesPerWriter := 20

	// Spawn multiple concurrent writers
	for i := range writers {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			for range writesPerWriter {
				content := []byte("writer-" + string(rune('A'+id)) + "-write")
				fs.WriteFileAtomic(path, content, 0644)
			}
		}(i)
	}

	wg.Wait()

	// Final content should be valid (from one of the writers)
	data, err := os.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("ReadFile err=%v, want=%v", got, want)
	}

	// Content should start with "writer-" (not be corrupted/mixed)
	if got, want := len(data) >= 7 && string(data[:7]) == "writer-", true; got != want {
		t.Fatalf("content corrupted: got %q", data)
	}
}
