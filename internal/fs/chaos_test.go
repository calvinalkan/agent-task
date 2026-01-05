package fs

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
)

// =============================================================================
// Chaos FS Tests
//
// These tests verify the Chaos wrapper works correctly:
//   - Injects faults when enabled
//   - Passes through to underlying FS when disabled
//   - Stats are counted correctly
//   - chaosFile intercepts Read/Write operations
//
// We're testing OUR code (Chaos), not the underlying FS.
// =============================================================================

// -----------------------------------------------------------------------------
// Fault Injection Tests - "Does Chaos inject faults when enabled?"
// -----------------------------------------------------------------------------

// TestChaos_InjectsWriteFault verifies that with 100% WriteFailRate,
// all writes fail with a real OS error (ENOSPC, EIO, etc).
func TestChaos_InjectsWriteFault(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate: 1.0, // 100% - always fail
	})
	chaosFS.SetMode(ChaosModeInject)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	err := chaosFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Should be a PathError with a real syscall error
	if got, want := err != nil, true; got != want {
		t.Fatalf("err=%v, want non-nil", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T", err)
	}

	// Should be one of the write-related errors
	validErrs := []error{syscall.ENOSPC, syscall.EIO, syscall.EROFS, syscall.EDQUOT, syscall.EFBIG}
	isValid := false

	for _, e := range validErrs {
		if errors.Is(err, e) {
			isValid = true

			break
		}
	}

	if got, want := isValid, true; got != want {
		t.Fatalf("err=%v, want one of %v", err, validErrs)
	}
}

// TestChaos_InjectsReadFault verifies that with 100% ReadFailRate,
// all reads fail with a real OS error (EIO, ENOENT, etc).
func TestChaos_InjectsReadFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Create file with real FS first
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Now read with chaos
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		ReadFailRate: 1.0, // 100% - always fail
	})
	chaosFS.SetMode(ChaosModeInject)

	_, err := chaosFS.ReadFile(path)

	// Should be a PathError with a real syscall error
	if got, want := err != nil, true; got != want {
		t.Fatalf("err=%v, want non-nil", err)
	}

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T", err)
	}
}

// TestChaos_InjectsOpenFault verifies that with 100% OpenFailRate,
// Open/Create/OpenFile fail with real OS errors.
func TestChaos_InjectsOpenFault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		OpenFailRate: 1.0, // 100%
	})
	chaosFS.SetMode(ChaosModeInject)

	_, err := chaosFS.Open(path)

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Open err should be *os.PathError, got %T", err)
	}

	_, err = chaosFS.Create(filepath.Join(dir, "new.txt"))
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Create err should be *os.PathError, got %T", err)
	}
}

// TestChaos_InjectsLockFault verifies that with 100% LockFailRate,
// Lock() fails with os.ErrDeadlineExceeded (same as real timeout).
func TestChaos_InjectsLockFault(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		LockFailRate: 1.0, // 100%
	})
	chaosFS.SetMode(ChaosModeInject)

	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	_, err := chaosFS.Lock(path)

	if got, want := errors.Is(err, os.ErrDeadlineExceeded), true; got != want {
		t.Fatalf("err=%v, want os.ErrDeadlineExceeded", err)
	}
}

// -----------------------------------------------------------------------------
// Error Compatibility Tests - "Do chaos errors work with errors.Is()?"
// -----------------------------------------------------------------------------

// TestChaos_ErrorsWorkWithErrorsIs verifies that injected errors can be
// checked with errors.Is() just like real OS errors.
//
// This is critical for code that handles specific errors:
//
//	if errors.Is(err, syscall.ENOSPC) {
//	    // Handle disk full
//	}
func TestChaos_ErrorsWorkWithErrorsIs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Test that we can use errors.Is() to check for specific syscall errors
	t.Run("WriteError_ENOSPC", func(t *testing.T) {
		// Use a specific seed that produces ENOSPC (found by inspection)
		chaosFS := NewChaos(realFS, 0, ChaosConfig{WriteFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		err := chaosFS.WriteFileAtomic(path, []byte("x"), 0644)

		// Should be able to use errors.Is
		var pathErr *os.PathError
		if got, want := errors.As(err, &pathErr), true; got != want {
			t.Fatalf("should be *os.PathError, got %T", err)
		}

		// The underlying error should be a syscall.Errno
		var errno syscall.Errno
		if got, want := errors.As(pathErr.Err, &errno), true; got != want {
			t.Fatalf("underlying error should be syscall.Errno, got %T", pathErr.Err)
		}
	})

	t.Run("ReadError_EIO", func(t *testing.T) {
		chaosFS := NewChaos(realFS, 0, ChaosConfig{ReadFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.ReadFile(path)

		var pathErr *os.PathError
		if got, want := errors.As(err, &pathErr), true; got != want {
			t.Fatalf("should be *os.PathError, got %T", err)
		}

		var errno syscall.Errno
		if got, want := errors.As(pathErr.Err, &errno), true; got != want {
			t.Fatalf("underlying error should be syscall.Errno, got %T", pathErr.Err)
		}
	})

	t.Run("LockError_Deadline", func(t *testing.T) {
		chaosFS := NewChaos(realFS, 0, ChaosConfig{LockFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.Lock(path)

		// Lock uses os.ErrDeadlineExceeded (not syscall error)
		if got, want := errors.Is(err, os.ErrDeadlineExceeded), true; got != want {
			t.Fatalf("should be os.ErrDeadlineExceeded, got %v", err)
		}
	})
}

// -----------------------------------------------------------------------------
// Pass-Through Tests - "Does Chaos behave like Real when disabled?"
// -----------------------------------------------------------------------------

// TestChaos_PassesThroughWhenDisabled verifies that with chaos disabled,
// operations succeed even with 100% fail rates configured.
func TestChaos_PassesThroughWhenDisabled(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		// All at 100% - would all fail if enabled
		ReadFailRate:  1.0,
		WriteFailRate: 1.0,
		OpenFailRate:  1.0,
		LockFailRate:  1.0,
	})
	// NOT enabled - should pass through

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Write should succeed
	err := chaosFS.WriteFileAtomic(path, []byte("hello"), 0644)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("WriteFileAtomic err=%v, want=%v", got, want)
	}

	// Read should succeed
	data, err := chaosFS.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("ReadFile err=%v, want=%v", got, want)
	}

	if got, want := string(data), "hello"; got != want {
		t.Fatalf("content=%q, want=%q", got, want)
	}

	// Open should succeed
	f, err := chaosFS.Open(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Open err=%v, want=%v", got, want)
	}

	f.Close()
}

func TestChaos_CanToggleModes(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate: 1.0,
	})

	dir := t.TempDir()

	// Passthrough by default - should succeed
	err := chaosFS.WriteFileAtomic(filepath.Join(dir, "1.txt"), []byte("a"), 0644)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("disabled: err=%v, want=%v", got, want)
	}

	// Inject - should fail
	chaosFS.SetMode(ChaosModeInject)

	err = chaosFS.WriteFileAtomic(filepath.Join(dir, "2.txt"), []byte("b"), 0644)
	if got, want := err != nil, true; got != want {
		t.Fatalf("enabled: err=%v, want non-nil", err)
	}

	// Passthrough again - should succeed
	chaosFS.SetMode(ChaosModePassthrough)

	err = chaosFS.WriteFileAtomic(filepath.Join(dir, "3.txt"), []byte("c"), 0644)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("re-disabled: err=%v, want=%v", got, want)
	}
}

func TestChaos_Passthrough_IgnoresStickyState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()
	if err := realFS.WriteFileAtomic(path, []byte("hello"), 0o644); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}

	chaosFS := NewChaos(realFS, 0, ChaosConfig{ReadFailRate: 1.0})
	chaosFS.setState(path, PathIOError)

	// Passthrough should ignore sticky state and fault rates.
	chaosFS.SetMode(ChaosModePassthrough)

	data, err := chaosFS.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("ReadFile err=%v, want nil", err)
	}

	if got, want := string(data), "hello"; got != want {
		t.Fatalf("ReadFile=%q, want %q", got, want)
	}

	// StickyOnly should apply sticky state but keep rates disabled.
	chaosFS.SetMode(ChaosModeStickyOnly)

	_, err = chaosFS.ReadFile(path)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Fatalf("StickyOnly ReadFile err=%v, want EIO", err)
	}
}

func TestChaos_StickyOnly_DisablesFaultRates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 0, ChaosConfig{
		WriteFailRate:    1.0,
		PartialWriteRate: 1.0,
	})

	chaosFS.SetMode(ChaosModeStickyOnly)

	// Even at 100% rates, StickyOnly should not inject random failures.
	err := chaosFS.WriteFileAtomic(path, []byte("hello"), 0o644)
	if err != nil {
		t.Fatalf("WriteFileAtomic err=%v, want nil (StickyOnly disables rates)", err)
	}
}

// -----------------------------------------------------------------------------
// Stats Tests - "Are fault counts tracked correctly?"
// -----------------------------------------------------------------------------

// TestChaos_StatsCountFaults verifies Stats() correctly counts injected faults.
func TestChaos_StatsCountFaults(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate: 1.0,
		ReadFailRate:  1.0,
	})
	chaosFS.SetMode(ChaosModeInject)

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Create file with real FS for reading
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Trigger faults
	chaosFS.WriteFileAtomic(path, []byte("x"), 0644) // fail
	chaosFS.WriteFileAtomic(path, []byte("y"), 0644) // fail
	chaosFS.ReadFile(path)                           // fail

	stats := chaosFS.Stats()

	if got, want := stats.WriteFails, int64(2); got != want {
		t.Errorf("WriteFails=%d, want=%d", got, want)
	}

	if got, want := stats.ReadFails, int64(1); got != want {
		t.Errorf("ReadFails=%d, want=%d", got, want)
	}
}

// TestChaos_TotalFaults verifies TotalFaults() sums all fault types.
func TestChaos_TotalFaults(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate:  1.0,
		RemoveFailRate: 1.0,
	})
	chaosFS.SetMode(ChaosModeInject)

	dir := t.TempDir()

	chaosFS.WriteFileAtomic(filepath.Join(dir, "a.txt"), []byte("x"), 0644)
	chaosFS.Remove(filepath.Join(dir, "b.txt"))

	if got, want := chaosFS.TotalFaults(), int64(2); got != want {
		t.Errorf("TotalFaults=%d, want=%d", got, want)
	}
}

// TestChaos_StatsNotCountedWhenDisabled verifies faults aren't counted
// when chaos is disabled (because no faults are injected).
func TestChaos_StatsNotCountedWhenDisabled(t *testing.T) {
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate: 1.0,
	})
	// NOT enabled

	dir := t.TempDir()
	chaosFS.WriteFileAtomic(filepath.Join(dir, "test.txt"), []byte("x"), 0644)

	if got, want := chaosFS.Stats().WriteFails, int64(0); got != want {
		t.Errorf("WriteFails=%d, want=%d (should not count when disabled)", got, want)
	}
}

// -----------------------------------------------------------------------------
// chaosFile Tests - "Does the File wrapper intercept Read/Write?"
// -----------------------------------------------------------------------------

// TestChaosFile_InterceptsRead verifies chaosFile injects faults on Read().
func TestChaosFile_InterceptsRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	// Create file
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello world"), 0644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		ReadFailRate: 1.0, // 100% on file.Read()
	})
	chaosFS.SetMode(ChaosModeInject)

	// Open succeeds (OpenFailRate is 0)
	f, err := chaosFS.Open(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Open err=%v, want=%v", got, want)
	}
	defer f.Close()

	// Read should fail with a real OS error
	buf := make([]byte, 100)
	_, err = f.Read(buf)

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Read err should be *os.PathError, got %T", err)
	}
}

// TestChaosFile_InterceptsWrite verifies chaosFile injects faults on Write().
func TestChaosFile_InterceptsWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		WriteFailRate: 1.0, // 100% on file.Write()
	})
	chaosFS.SetMode(ChaosModeInject)

	// Create succeeds (OpenFailRate is 0)
	f, err := chaosFS.Create(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Create err=%v, want=%v", got, want)
	}
	defer f.Close()

	// Write should fail with a real OS error
	_, err = f.Write([]byte("hello"))

	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("Write err should be *os.PathError, got %T", err)
	}
}

// TestChaosFile_PassesThroughFd verifies chaosFile.Fd() returns real fd.
// This is important for locking which needs the real file descriptor.
func TestChaosFile_PassesThroughFd(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, DefaultChaosConfig())

	// Create with real FS
	realF, _ := realFS.Create(path)
	realFd := realF.Fd()
	realF.Close()

	// Open with chaos FS
	chaosF, _ := chaosFS.Open(path)
	chaosFd := chaosF.Fd()
	chaosF.Close()

	// Both should return valid (non-zero) file descriptors
	if got, want := realFd != 0, true; got != want {
		t.Fatalf("realFd=%d, want non-zero", realFd)
	}

	if got, want := chaosFd != 0, true; got != want {
		t.Fatalf("chaosFd=%d, want non-zero", chaosFd)
	}
}

// TestChaosFile_PassesThroughSeek verifies chaosFile.Seek() works.
func TestChaosFile_PassesThroughSeek(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello world"), 0644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{}) // No faults
	chaosFS.SetMode(ChaosModeInject)

	f, _ := chaosFS.Open(path)
	defer f.Close()

	// Seek to position 6
	pos, err := f.Seek(6, io.SeekStart)

	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("Seek err=%v, want=%v", got, want)
	}

	if got, want := pos, int64(6); got != want {
		t.Fatalf("Seek pos=%d, want=%d", got, want)
	}

	// Read from position 6 should give "world"
	buf := make([]byte, 5)
	n, _ := f.Read(buf)

	if got, want := string(buf[:n]), "world"; got != want {
		t.Fatalf("Read after Seek=%q, want=%q", got, want)
	}
}

// -----------------------------------------------------------------------------
// State-Aware Error Tests - "Are errors logically consistent?"
// -----------------------------------------------------------------------------

// TestChaos_EIO_IsSticky verifies that once a path gets EIO (bad sector),
// it stays broken across all operations.
func TestChaos_EIO_IsSticky(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Find a seed that produces EIO on first write attempt
	var chaosFS *Chaos
	for seed := range int64(1000) {
		chaosFS = NewChaos(realFS, seed, ChaosConfig{WriteFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		err := chaosFS.WriteFileAtomic(path, []byte("x"), 0644)
		if errors.Is(err, syscall.EIO) {
			break
		}

		chaosFS.ResetPathState(path)
	}

	// Verify path is now in EIO state
	if got, want := chaosFS.PathState(path), PathIOError; got != want {
		t.Fatalf("PathState=%v, want=%v", got, want)
	}

	// All subsequent operations should fail with EIO
	_, err := chaosFS.ReadFile(path)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Errorf("ReadFile: got %v, want EIO", err)
	}

	_, err = chaosFS.Open(path)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Errorf("Open: got %v, want EIO", err)
	}

	_, err = chaosFS.Stat(path)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Errorf("Stat: got %v, want EIO", err)
	}

	err = chaosFS.Remove(path)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Errorf("Remove: got %v, want EIO", err)
	}
}

// TestChaos_EROFS_IsSticky verifies that once a path gets EROFS (read-only fs),
// writes fail but reads still work.
func TestChaos_EROFS_IsSticky(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Find a seed that produces EROFS on first write attempt
	var chaosFS *Chaos
	for seed := range int64(1000) {
		chaosFS = NewChaos(realFS, seed, ChaosConfig{WriteFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		err := chaosFS.WriteFileAtomic(path, []byte("x"), 0644)
		if errors.Is(err, syscall.EROFS) {
			break
		}

		chaosFS.ResetPathState(path)
	}

	// Verify path is now in ReadOnly state
	if got, want := chaosFS.PathState(path), PathReadOnly; got != want {
		t.Fatalf("PathState=%v, want=%v", got, want)
	}

	// Writes should fail with EROFS
	err := chaosFS.WriteFileAtomic(path, []byte("x"), 0644)
	if got, want := errors.Is(err, syscall.EROFS), true; got != want {
		t.Errorf("WriteFileAtomic: got %v, want EROFS", err)
	}

	// But reads should still work! (read-only doesn't affect reads)
	chaosFS.SetMode(ChaosModeStickyOnly) // Disable fault rates while still applying sticky state

	data, err := chaosFS.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Errorf("ReadFile err=%v, want nil (reads work on RO fs)", err)
	}

	if got, want := string(data), "hello"; got != want {
		t.Errorf("ReadFile data=%q, want=%q", got, want)
	}
}

// TestChaos_EACCES_IsSemiSticky verifies that EACCES stays in most calls
// but can occasionally "recover" (simulating permission changes).
//
// Behavior:
// - While in PathNoPermission state, returns EACCES ~80% of the time
// - ~20% of the time: clears state (permission "fixed") and proceeds.
func TestChaos_EACCES_IsSemiSticky(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	var (
		chaosFS *Chaos
		denied  int
		opened  int
		found   bool
	)

	// Pick a seed that exercises both branches (deny then recover).
	for seed := range int64(1000) {
		denied = 0
		opened = 0

		chaosFS = NewChaos(realFS, seed, ChaosConfig{})
		chaosFS.SetMode(ChaosModeInject)
		chaosFS.setState(path, PathNoPermission)

		for range 200 {
			f, err := chaosFS.Open(path)
			if err == nil {
				opened++
				_ = f.Close()
			} else if errors.Is(err, syscall.EACCES) {
				denied++
			} else {
				t.Fatalf("Open: got %v, want nil or EACCES", err)
			}

			if chaosFS.PathState(path) == PathNormal {
				break
			}
		}

		if chaosFS.PathState(path) == PathNormal && denied > 0 && opened > 0 {
			found = true

			break
		}
	}

	// If we didn't find a seed that exercises both branches, something's wrong
	// with the semi-sticky logic or the RNG.
	if !found {
		t.Fatalf("expected PathNoPermission to deny then recover (denied=%d opened=%d state=%v)", denied, opened, chaosFS.PathState(path))
	}
}

// TestChaos_TransientErrors_DontStick verifies that transient errors like
// ENOSPC and EMFILE don't create sticky state.
func TestChaos_TransientErrors_DontStick(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Find a seed that produces ENOSPC
	var chaosFS *Chaos
	for seed := range int64(1000) {
		chaosFS = NewChaos(realFS, seed, ChaosConfig{WriteFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		err := chaosFS.WriteFileAtomic(path, []byte("x"), 0644)
		if errors.Is(err, syscall.ENOSPC) {
			break
		}

		chaosFS.ResetPathState(path)
	}

	// ENOSPC should NOT create sticky state
	if got, want := chaosFS.PathState(path), PathNormal; got != want {
		t.Errorf("PathState=%v after ENOSPC, want=%v (transient)", got, want)
	}
}

// TestChaos_ENOENT_OnlyWhenFileDoesntExist verifies that ENOENT is only
// returned when the file actually doesn't exist on the real filesystem.
func TestChaos_ENOENT_OnlyWhenFileDoesntExist(t *testing.T) {
	dir := t.TempDir()
	existingPath := filepath.Join(dir, "exists.txt")
	missingPath := filepath.Join(dir, "missing.txt")

	realFS := NewReal()
	realFS.WriteFileAtomic(existingPath, []byte("hello"), 0644)

	// For existing file: should NEVER get ENOENT
	for seed := range int64(100) {
		chaosFS := NewChaos(realFS, seed, ChaosConfig{OpenFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.Open(existingPath)
		if errors.Is(err, syscall.ENOENT) {
			t.Fatalf("seed=%d: got ENOENT for existing file!", seed)
		}
	}

	// For missing file: CAN get ENOENT
	gotENOENT := false

	for seed := range int64(100) {
		chaosFS := NewChaos(realFS, seed, ChaosConfig{OpenFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.Open(missingPath)
		if errors.Is(err, syscall.ENOENT) {
			gotENOENT = true

			break
		}
	}

	if got, want := gotENOENT, true; got != want {
		t.Errorf("never got ENOENT for missing file")
	}
}

// TestChaos_ReadErrors_NoENOENT verifies that read operations on open files
// never return ENOENT (you already opened it, it exists!).
func TestChaos_ReadErrors_NoENOENT(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// ReadFile on existing file should never get ENOENT
	for seed := range int64(100) {
		chaosFS := NewChaos(realFS, seed, ChaosConfig{ReadFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.ReadFile(path)
		if errors.Is(err, syscall.ENOENT) {
			t.Fatalf("seed=%d: got ENOENT on read of existing file!", seed)
		}
	}
}

// TestChaos_StateConsistency_AcrossOperations verifies that once a path
// has a sticky error, all operations on that path respect it.
func TestChaos_StateConsistency_AcrossOperations(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Find seed that produces EIO
	var chaosFS *Chaos
	for seed := range int64(1000) {
		chaosFS = NewChaos(realFS, seed, ChaosConfig{StatFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.Stat(path)
		if errors.Is(err, syscall.EIO) {
			break
		}

		chaosFS.ResetPathState(path)
	}

	// Now disable random failures - only sticky state should affect operations
	chaosFS.SetMode(ChaosModeStickyOnly)

	// All operations should still fail with EIO due to sticky state
	ops := []struct {
		name string
		fn   func() error
	}{
		{"Open", func() error {
			_, err := chaosFS.Open(path)

			return err
		}},
		{"Create", func() error {
			_, err := chaosFS.Create(path)

			return err
		}},
		{"ReadFile", func() error {
			_, err := chaosFS.ReadFile(path)

			return err
		}},
		{"WriteFileAtomic", func() error { return chaosFS.WriteFileAtomic(path, []byte("x"), 0644) }},
		{"Stat", func() error {
			_, err := chaosFS.Stat(path)

			return err
		}},
		{"Exists", func() error {
			_, err := chaosFS.Exists(path)

			return err
		}},
		{"Remove", func() error { return chaosFS.Remove(path) }},
	}

	for _, op := range ops {
		err := op.fn()
		if got, want := errors.Is(err, syscall.EIO), true; got != want {
			t.Errorf("%s: got %v, want EIO", op.name, err)
		}
	}
}

// TestChaos_ResetPathState_ClearsStickyError verifies that ResetPathState
// clears sticky errors and allows operations to succeed again.
func TestChaos_ResetPathState_ClearsStickyError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, []byte("hello"), 0644)

	// Find seed that produces EIO
	var chaosFS *Chaos
	for seed := range int64(1000) {
		chaosFS = NewChaos(realFS, seed, ChaosConfig{ReadFailRate: 1.0})
		chaosFS.SetMode(ChaosModeInject)

		_, err := chaosFS.ReadFile(path)
		if errors.Is(err, syscall.EIO) {
			break
		}

		chaosFS.ResetPathState(path)
	}

	// Verify stuck in EIO
	if got, want := chaosFS.PathState(path), PathIOError; got != want {
		t.Fatalf("PathState=%v, want=%v", got, want)
	}

	// Reset and disable chaos
	chaosFS.ResetPathState(path)
	chaosFS.SetMode(ChaosModeStickyOnly)

	// Should work now
	data, err := chaosFS.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Errorf("ReadFile after reset err=%v, want nil", err)
	}

	if got, want := string(data), "hello"; got != want {
		t.Errorf("ReadFile data=%q, want=%q", got, want)
	}
}

// TestChaos_ResetAllPathStates_ClearsEverything verifies that ResetAllPathStates
// clears all sticky errors.
func TestChaos_ResetAllPathStates_ClearsEverything(t *testing.T) {
	dir := t.TempDir()
	realFS := NewReal()

	// Create multiple files and give them all EIO
	paths := []string{
		filepath.Join(dir, "a.txt"),
		filepath.Join(dir, "b.txt"),
		filepath.Join(dir, "c.txt"),
	}
	for _, p := range paths {
		realFS.WriteFileAtomic(p, []byte("test"), 0644)
	}

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{})
	chaosFS.SetMode(ChaosModeInject)

	// Manually set all paths to EIO state by using internal method
	for _, p := range paths {
		chaosFS.setState(p, PathIOError)
	}

	// Verify all are stuck
	for _, p := range paths {
		if got, want := chaosFS.PathState(p), PathIOError; got != want {
			t.Errorf("PathState(%s)=%v, want=%v", p, got, want)
		}
	}

	// Reset all
	chaosFS.ResetAllPathStates()

	// Verify all cleared
	for _, p := range paths {
		if got, want := chaosFS.PathState(p), PathNormal; got != want {
			t.Errorf("after reset: PathState(%s)=%v, want=%v", p, got, want)
		}
	}
}

// TestChaos_Rename_ChecksBothPaths verifies that Rename checks sticky state
// of both source and destination paths.
func TestChaos_Rename_ChecksBothPaths(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.txt")
	dst := filepath.Join(dir, "dst.txt")
	realFS := NewReal()
	realFS.WriteFileAtomic(src, []byte("hello"), 0644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{})
	chaosFS.SetMode(ChaosModeInject)

	// Set destination to EIO
	chaosFS.setState(dst, PathIOError)

	// Rename should fail because destination has EIO
	err := chaosFS.Rename(src, dst)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Errorf("Rename with dst EIO: got %v, want EIO", err)
	}

	// Reset dst, set src to EIO
	chaosFS.ResetPathState(dst)
	chaosFS.setState(src, PathIOError)

	// Rename should fail because source has EIO
	err = chaosFS.Rename(src, dst)
	if got, want := errors.Is(err, syscall.EIO), true; got != want {
		t.Errorf("Rename with src EIO: got %v, want EIO", err)
	}
}

// TestChaos_StateConcurrency verifies that path state tracking is safe
// for concurrent access.
func TestChaos_StateConcurrency(t *testing.T) {
	dir := t.TempDir()
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{WriteFailRate: 0.5})
	chaosFS.SetMode(ChaosModeInject)

	// Create some files
	for i := range 10 {
		path := filepath.Join(dir, "file"+string(rune('0'+i))+".txt")
		realFS.WriteFileAtomic(path, []byte("test"), 0644)
	}

	// Hammer from multiple goroutines
	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)

		go func(id int) {
			defer wg.Done()

			path := filepath.Join(dir, "file"+string(rune('0'+id))+".txt")
			for j := range 100 {
				chaosFS.WriteFileAtomic(path, []byte("x"), 0644)
				chaosFS.ReadFile(path)
				chaosFS.PathState(path)

				if j%10 == 0 {
					chaosFS.ResetPathState(path)
				}
			}
		}(i)
	}

	wg.Wait()
	// Test passes if no race/panic
}

// -----------------------------------------------------------------------------
// Statistical Rate Tests - "Do fault rates match configuration?"
// -----------------------------------------------------------------------------

// TestChaos_FaultRates_Statistical verifies that configured fault rates
// produce approximately the expected failure frequency.
//
// Each fault type is tested with 50% rate over 1000 iterations.
// We expect ~500 failures with reasonable tolerance (40-60%).
//
// NOTE: We reset path state between iterations because some errors are
// "sticky" (like EIO for bad sectors). Without reset, a single EIO would
// cause all subsequent operations to fail, skewing the statistics.
func TestChaos_FaultRates_Statistical(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping statistical test in short mode")
	}

	const (
		iterations = 1000
		rate       = 0.5
		minRate    = 0.40 // 40%
		maxRate    = 0.60 // 60%
	)

	t.Run("WriteFailRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		realFS := NewReal()
		chaosFS := NewChaos(realFS, 12345, ChaosConfig{WriteFailRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		failures := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state

			err := chaosFS.WriteFileAtomic(path, []byte("x"), 0644)
			if err != nil {
				failures++
			}
		}

		actual := float64(failures) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})

	t.Run("ReadFailRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		realFS := NewReal()
		realFS.WriteFileAtomic(path, []byte("hello"), 0644)

		chaosFS := NewChaos(realFS, 12345, ChaosConfig{ReadFailRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		failures := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state

			_, err := chaosFS.ReadFile(path)
			if err != nil {
				failures++
			}
		}

		actual := float64(failures) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})

	t.Run("OpenFailRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		realFS := NewReal()
		realFS.WriteFileAtomic(path, []byte("hello"), 0644)

		chaosFS := NewChaos(realFS, 12345, ChaosConfig{OpenFailRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		failures := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state

			f, err := chaosFS.Open(path)
			if err != nil {
				failures++
			} else {
				f.Close()
			}
		}

		actual := float64(failures) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})

	t.Run("RemoveFailRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		realFS := NewReal()
		chaosFS := NewChaos(realFS, 12345, ChaosConfig{RemoveFailRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		failures := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state
			// Create file so we can test non-ENOENT failures
			realFS.WriteFileAtomic(path, []byte("x"), 0644)

			err := chaosFS.Remove(path)
			if err != nil {
				failures++
			}
		}

		actual := float64(failures) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})

	t.Run("StatFailRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		realFS := NewReal()
		realFS.WriteFileAtomic(path, []byte("hello"), 0644)

		chaosFS := NewChaos(realFS, 12345, ChaosConfig{StatFailRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		failures := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state

			_, err := chaosFS.Stat(path)
			if err != nil {
				failures++
			}
		}

		actual := float64(failures) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})

	t.Run("PartialReadRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		content := []byte("hello world this is a test message")
		realFS := NewReal()
		realFS.WriteFileAtomic(path, content, 0644)

		chaosFS := NewChaos(realFS, 12345, ChaosConfig{PartialReadRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		partials := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state

			data, err := chaosFS.ReadFile(path)
			if err == nil && len(data) < len(content) {
				partials++
			}
		}

		actual := float64(partials) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})

	t.Run("PartialWriteRate", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "test.txt")
		content := []byte("hello world this is a test message")
		realFS := NewReal()
		chaosFS := NewChaos(realFS, 12345, ChaosConfig{PartialWriteRate: rate})
		chaosFS.SetMode(ChaosModeInject)

		partials := 0

		for range iterations {
			chaosFS.ResetPathState(path) // Reset sticky state

			err := chaosFS.WriteFileAtomic(path, content, 0644)
			if err != nil {
				// Check if partial data was written
				data, readErr := realFS.ReadFile(path)
				if readErr == nil && len(data) > 0 && len(data) < len(content) {
					partials++
				}
			}

			realFS.Remove(path) // Clean up for next iteration
		}

		actual := float64(partials) / float64(iterations)
		if got, want := actual >= minRate && actual <= maxRate, true; got != want {
			t.Errorf("rate=%.1f%%, want between %.0f%% and %.0f%%", actual*100, minRate*100, maxRate*100)
		}
	})
}

// -----------------------------------------------------------------------------
// Partial Read/Write Tests
// -----------------------------------------------------------------------------

// TestChaos_PartialReadReturnsSubset verifies partial reads return a
// valid subset of the original data (prefix), not garbage.
func TestChaos_PartialReadReturnsSubset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a test")
	realFS := NewReal()
	realFS.WriteFileAtomic(path, content, 0644)

	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		PartialReadRate: 1.0, // 100% partial reads
	})
	chaosFS.SetMode(ChaosModeInject)

	data, err := chaosFS.ReadFile(path)

	// Should succeed (not fail)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("err=%v, want=%v", got, want)
	}

	// Should be a prefix of original
	if got, want := bytes.HasPrefix(content, data), true; got != want {
		t.Fatalf("partial read should be prefix\noriginal: %q\ngot: %q", content, data)
	}

	// Should be shorter than original
	if got, want := len(data) < len(content), true; got != want {
		t.Fatalf("len(data)=%d, want less than %d", len(data), len(content))
	}
}

// TestChaos_PartialWriteLeavesPartialFile verifies partial writes actually
// write truncated data (simulating crash mid-write).
func TestChaos_PartialWriteLeavesPartialFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")

	content := []byte("hello world this is a test")
	realFS := NewReal()
	chaosFS := NewChaos(realFS, 12345, ChaosConfig{
		PartialWriteRate: 1.0, // 100% partial writes
	})
	chaosFS.SetMode(ChaosModeInject)

	err := chaosFS.WriteFileAtomic(path, content, 0644)

	// Should fail with a real OS error (ENOSPC, EIO, etc.)
	var pathErr *os.PathError
	if got, want := errors.As(err, &pathErr), true; got != want {
		t.Fatalf("err should be *os.PathError, got %T (%v)", err, err)
	}

	// File should exist with partial content
	data, err := realFS.ReadFile(path)
	if got, want := err, error(nil); !errors.Is(got, want) {
		t.Fatalf("ReadFile err=%v, want=%v", got, want)
	}

	// Should be a prefix of original
	if got, want := bytes.HasPrefix(content, data), true; got != want {
		t.Fatalf("partial write should be prefix\noriginal: %q\ngot: %q", content, data)
	}

	// Should be shorter than original
	if got, want := len(data) < len(content), true; got != want {
		t.Fatalf("len(data)=%d, want less than %d", len(data), len(content))
	}
}
