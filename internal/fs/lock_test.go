package fs

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func Test_Locker_TryLock_Returns_ErrWouldBlock_When_Path_Is_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	lock1, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("TryLock(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = lock1.Close() })

	lock2, err := locker.TryLock(path)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("TryLock(%q) while locked: err=%v, want %v", path, err, ErrWouldBlock)
	}
	if lock2 != nil {
		_ = lock2.Close()
		t.Fatalf("TryLock(%q) while locked: want lock=nil, got non-nil", path)
	}

	if err := lock1.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	lock3, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("TryLock(%q) after release: %v", path, err)
	}
	if err := lock3.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func Test_Locker_LockWithTimeout_Returns_DeadlineExceeded_When_Path_Is_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	lock1, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}
	defer lock1.Close()

	_, err = locker.LockWithTimeout(path, 50*time.Millisecond)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		t.Fatalf("LockWithTimeout(%q): err=%v, want %v", path, err, os.ErrDeadlineExceeded)
	}
}

func Test_Locker_LockWithTimeout_Returns_Error_When_Timeout_Is_Non_Positive(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	_, err := locker.LockWithTimeout(path, 0)
	if !errors.Is(err, ErrInvalidTimeout) {
		t.Fatalf("LockWithTimeout(%q, 0): err=%v, want %v", path, err, ErrInvalidTimeout)
	}
}

func Test_Locker_RLock_Allows_Multiple_Readers_And_Blocks_Writer(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	r1, err := locker.RLock(path)
	if err != nil {
		t.Fatalf("RLock(%q): %v", path, err)
	}
	defer r1.Close()

	r2, err := locker.RLock(path)
	if err != nil {
		t.Fatalf("RLock(%q) second: %v", path, err)
	}
	defer r2.Close()

	_, err = locker.TryLock(path)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("TryLock(%q) while read-locked: err=%v, want %v", path, err, ErrWouldBlock)
	}
}

func Test_Lock_Close_Is_Idempotent(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	lock, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("TryLock(%q): %v", path, err)
	}

	if err := lock.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("Close() second: %v", err)
	}
}
