package fs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

func Test_Locker_LockWithTimeout_Returns_ErrWouldBlock_When_Path_Is_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	lock1, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}
	defer lock1.Close()

	_, err = locker.LockWithTimeout(path, 50*time.Millisecond)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("LockWithTimeout(%q): err=%v, want %v", path, err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("LockWithTimeout(%q): err=%q, want substring %q", path, err.Error(), "timed out")
	}
}

func Test_Locker_LockWithTimeout_Succeeds_When_Lock_Is_Released_Before_Timeout(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	lock1, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}

	attempted := make(chan struct{}, 1)
	origFlock := locker.flock
	locker.flock = func(fd int, how int) error {
		if (how&syscall.LOCK_NB) != 0 && (how&syscall.LOCK_EX) != 0 {
			select {
			case attempted <- struct{}{}:
			default:
			}
		}
		return origFlock(fd, how)
	}

	acquired := make(chan struct{})
	errCh := make(chan error, 1)

	var lock2 *Lock
	go func() {
		var err error
		lock2, err = locker.LockWithTimeout(path, 500*time.Millisecond)
		if err != nil {
			errCh <- err
			return
		}

		close(acquired)
	}()

	select {
	case <-attempted:
	case err := <-errCh:
		_ = lock1.Close()
		t.Fatalf("LockWithTimeout(%q): %v", path, err)
	case <-time.After(1 * time.Second):
		_ = lock1.Close()
		t.Fatalf("LockWithTimeout(%q) did not attempt acquisition", path)
	}

	select {
	case <-acquired:
		if lock2 != nil {
			_ = lock2.Close()
		}
		_ = lock1.Close()
		t.Fatalf("LockWithTimeout(%q) acquired while Lock held: want blocked", path)
	default:
	}

	if err := lock1.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	select {
	case <-acquired:
		if err := lock2.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case err := <-errCh:
		t.Fatalf("LockWithTimeout(%q): %v", path, err)
	case <-time.After(1 * time.Second):
		t.Fatalf("LockWithTimeout(%q) did not acquire after release", path)
	}
}

func Test_Locker_LockWithTimeout_Returns_Error_When_Timeout_Is_Non_Positive(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	for _, timeout := range []time.Duration{0, -1 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			_, err := locker.LockWithTimeout(path, timeout)
			if !errors.Is(err, ErrInvalidTimeout) {
				t.Fatalf("LockWithTimeout(%q, %s): err=%v, want %v", path, timeout, err, ErrInvalidTimeout)
			}
		})
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

func Test_Locker_RLock_Can_Lock_A_ReadOnly_File(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	if err := os.WriteFile(path, []byte("x"), 0o444); err != nil {
		t.Fatalf("setup WriteFile(%q): %v", path, err)
	}

	lock, err := locker.RLock(path)
	if err != nil {
		t.Fatalf("RLock(%q): %v", path, err)
	}
	defer lock.Close()
}

func Test_Locker_Lock_Blocks_Until_RLock_Is_Released(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	r, err := locker.RLock(path)
	if err != nil {
		t.Fatalf("RLock(%q): %v", path, err)
	}

	attempted := make(chan struct{}, 1)
	origFlock := locker.flock
	locker.flock = func(fd int, how int) error {
		if how == syscall.LOCK_EX {
			select {
			case attempted <- struct{}{}:
			default:
			}
		}
		return origFlock(fd, how)
	}

	acquired := make(chan struct{})
	errCh := make(chan error, 1)

	var w *Lock
	go func() {
		var err error
		w, err = locker.Lock(path)
		if err != nil {
			errCh <- err
			return
		}

		close(acquired)
	}()

	select {
	case <-attempted:
	case err := <-errCh:
		_ = r.Close()
		t.Fatalf("Lock(%q) while RLock held: %v", path, err)
	case <-time.After(1 * time.Second):
		_ = r.Close()
		t.Fatalf("Lock(%q) did not attempt acquisition", path)
	}

	select {
	case <-acquired:
		if w != nil {
			_ = w.Close()
		}
		_ = r.Close()
		t.Fatalf("Lock(%q) acquired while RLock held: want blocked", path)
	case err := <-errCh:
		_ = r.Close()
		t.Fatalf("Lock(%q) while RLock held: %v", path, err)
	case <-time.After(50 * time.Millisecond):
		// Still blocked (expected).
	}

	if err := r.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	select {
	case <-acquired:
		if err := w.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case err := <-errCh:
		t.Fatalf("Lock(%q) after release: %v", path, err)
	case <-time.After(1 * time.Second):
		t.Fatalf("Lock(%q) did not acquire after RLock release", path)
	}
}

func Test_Locker_RLock_Blocks_Until_Lock_Is_Released(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	w, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}

	attempted := make(chan struct{}, 1)
	origFlock := locker.flock
	locker.flock = func(fd int, how int) error {
		if how == syscall.LOCK_SH {
			select {
			case attempted <- struct{}{}:
			default:
			}
		}
		return origFlock(fd, how)
	}

	acquired := make(chan struct{})
	errCh := make(chan error, 1)

	var r *Lock
	go func() {
		var err error
		r, err = locker.RLock(path)
		if err != nil {
			errCh <- err
			return
		}

		close(acquired)
	}()

	select {
	case <-attempted:
	case err := <-errCh:
		_ = w.Close()
		t.Fatalf("RLock(%q) while Lock held: %v", path, err)
	case <-time.After(1 * time.Second):
		_ = w.Close()
		t.Fatalf("RLock(%q) did not attempt acquisition", path)
	}

	select {
	case <-acquired:
		if r != nil {
			_ = r.Close()
		}
		_ = w.Close()
		t.Fatalf("RLock(%q) acquired while Lock held: want blocked", path)
	case err := <-errCh:
		_ = w.Close()
		t.Fatalf("RLock(%q) while Lock held: %v", path, err)
	case <-time.After(50 * time.Millisecond):
		// Still blocked (expected).
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	select {
	case <-acquired:
		if err := r.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case err := <-errCh:
		t.Fatalf("RLock(%q) after release: %v", path, err)
	case <-time.After(1 * time.Second):
		t.Fatalf("RLock(%q) did not acquire after Lock release", path)
	}
}

func Test_Locker_LockWithTimeout_Returns_ErrWouldBlock_When_Path_Is_Read_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	r, err := locker.RLock(path)
	if err != nil {
		t.Fatalf("RLock(%q): %v", path, err)
	}
	defer r.Close()

	_, err = locker.LockWithTimeout(path, 50*time.Millisecond)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("LockWithTimeout(%q) while read-locked: err=%v, want %v", path, err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("LockWithTimeout(%q) while read-locked: err=%q, want substring %q", path, err.Error(), "timed out")
	}
}

func Test_Locker_RLockWithTimeout_Returns_ErrWouldBlock_When_Path_Is_Exclusively_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	w, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}
	defer w.Close()

	_, err = locker.RLockWithTimeout(path, 50*time.Millisecond)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("RLockWithTimeout(%q) while locked: err=%v, want %v", path, err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("RLockWithTimeout(%q) while locked: err=%q, want substring %q", path, err.Error(), "timed out")
	}
}

func Test_Locker_RLockWithTimeout_Succeeds_When_Lock_Is_Released_Before_Timeout(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	lock1, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}

	attempted := make(chan struct{}, 1)
	origFlock := locker.flock
	locker.flock = func(fd int, how int) error {
		if (how&syscall.LOCK_NB) != 0 && (how&syscall.LOCK_SH) != 0 {
			select {
			case attempted <- struct{}{}:
			default:
			}
		}
		return origFlock(fd, how)
	}

	acquired := make(chan struct{})
	errCh := make(chan error, 1)

	var lock2 *Lock
	go func() {
		var err error
		lock2, err = locker.RLockWithTimeout(path, 500*time.Millisecond)
		if err != nil {
			errCh <- err
			return
		}

		close(acquired)
	}()

	select {
	case <-attempted:
	case err := <-errCh:
		_ = lock1.Close()
		t.Fatalf("RLockWithTimeout(%q): %v", path, err)
	case <-time.After(1 * time.Second):
		_ = lock1.Close()
		t.Fatalf("RLockWithTimeout(%q) did not attempt acquisition", path)
	}

	select {
	case <-acquired:
		if lock2 != nil {
			_ = lock2.Close()
		}
		_ = lock1.Close()
		t.Fatalf("RLockWithTimeout(%q) acquired while Lock held: want blocked", path)
	default:
	}

	if err := lock1.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	select {
	case <-acquired:
		if err := lock2.Close(); err != nil {
			t.Fatalf("Close(): %v", err)
		}
	case err := <-errCh:
		t.Fatalf("RLockWithTimeout(%q): %v", path, err)
	case <-time.After(1 * time.Second):
		t.Fatalf("RLockWithTimeout(%q) did not acquire after release", path)
	}
}

func Test_Locker_TryRLock_Succeeds_When_Path_Is_Read_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	r1, err := locker.RLock(path)
	if err != nil {
		t.Fatalf("RLock(%q): %v", path, err)
	}
	defer r1.Close()

	r2, err := locker.TryRLock(path)
	if err != nil {
		t.Fatalf("TryRLock(%q) while read-locked: %v", path, err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func Test_Locker_TryRLock_Returns_ErrWouldBlock_When_Path_Is_Exclusively_Locked(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	w, err := locker.Lock(path)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path, err)
	}
	t.Cleanup(func() { _ = w.Close() })

	r, err := locker.TryRLock(path)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("TryRLock(%q) while exclusively locked: err=%v, want %v", path, err, ErrWouldBlock)
	}
	if r != nil {
		_ = r.Close()
		t.Fatalf("TryRLock(%q) while exclusively locked: want lock=nil, got non-nil", path)
	}

	if err := w.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	r2, err := locker.TryRLock(path)
	if err != nil {
		t.Fatalf("TryRLock(%q) after release: %v", path, err)
	}
	if err := r2.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func Test_Locker_Creates_Parent_Directories_For_Locks(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())

	root := t.TempDir()
	dir := filepath.Join(root, "a", "b")
	path := filepath.Join(dir, "lock")

	if _, err := os.Stat(dir); err == nil {
		t.Fatalf("setup: %q unexpectedly exists", dir)
	}

	lock, err := locker.TryLock(path)
	if err != nil {
		t.Fatalf("TryLock(%q): %v", path, err)
	}
	defer lock.Close()

	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected parent dir %q to exist after TryLock: %v", dir, err)
	}
}

func Test_Locker_Locks_Do_Not_Interfere_Across_Paths(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	dir := t.TempDir()
	path1 := filepath.Join(dir, "lock1")
	path2 := filepath.Join(dir, "lock2")

	l1, err := locker.Lock(path1)
	if err != nil {
		t.Fatalf("Lock(%q): %v", path1, err)
	}
	t.Cleanup(func() { _ = l1.Close() })

	l2, err := locker.TryLock(path2)
	if err != nil {
		t.Fatalf("TryLock(%q) while holding %q: %v", path2, path1, err)
	}
	if err := l2.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}
}

func Test_Locker_Can_Reacquire_After_Close(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	for i := range 3 {
		l, err := locker.Lock(path)
		if err != nil {
			t.Fatalf("Lock(%q) #%d: %v", path, i, err)
		}
		if err := l.Close(); err != nil {
			t.Fatalf("Close() #%d: %v", i, err)
		}

		r, err := locker.RLock(path)
		if err != nil {
			t.Fatalf("RLock(%q) #%d: %v", path, i, err)
		}
		if err := r.Close(); err != nil {
			t.Fatalf("Close() shared #%d: %v", i, err)
		}
	}
}

func Test_Locker_RLockWithTimeout_Returns_Error_When_Timeout_Is_Non_Positive(t *testing.T) {
	t.Parallel()

	locker := NewLocker(NewReal())
	path := filepath.Join(t.TempDir(), "lock")

	for _, timeout := range []time.Duration{0, -1 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			_, err := locker.RLockWithTimeout(path, timeout)
			if !errors.Is(err, ErrInvalidTimeout) {
				t.Fatalf("RLockWithTimeout(%q, %s): err=%v, want %v", path, timeout, err, ErrInvalidTimeout)
			}
		})
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

func Test_Locker_TryLock_Returns_ErrWouldBlock_When_Flock_WouldBlock(t *testing.T) {
	// Verifies we normalize kernel "would block" errors (EAGAIN/EWOULDBLOCK) to
	// ErrWouldBlock for TryLock callers.

	tests := []struct {
		name string
		err  error
	}{
		{name: "EWOULDBLOCK", err: syscall.EWOULDBLOCK},
		{name: "EAGAIN", err: syscall.EAGAIN},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			locker := NewLocker(stubLockFS{
				openFile: func(string, int, os.FileMode) (File, error) {
					return &stubLockFile{fd: 123}, nil
				},
			})
			locker.flock = func(int, int) error { return tt.err }

			lock, err := locker.TryLock("lock")
			if !errors.Is(err, ErrWouldBlock) {
				t.Fatalf("TryLock(): err=%v, want %v", err, ErrWouldBlock)
			}
			if lock != nil {
				_ = lock.Close()
				t.Fatalf("TryLock(): want lock=nil, got non-nil")
			}
		})
	}
}

func Test_Locker_TryLock_Returns_Error_When_Flock_Returns_NonWouldBlock(t *testing.T) {
	// Verifies we propagate non-wouldblock errors from flock (and add context).

	locker := NewLocker(stubLockFS{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &stubLockFile{fd: 123}, nil
		},
	})
	locker.flock = func(int, int) error { return syscall.EBADF }

	lock, err := locker.TryLock("lock")
	if !errors.Is(err, syscall.EBADF) {
		t.Fatalf("TryLock(): err=%v, want %v", err, syscall.EBADF)
	}
	if !strings.Contains(err.Error(), "flock") {
		t.Fatalf("TryLock(): err=%q, want substring %q", err.Error(), "flock")
	}
	if lock != nil {
		_ = lock.Close()
		t.Fatalf("TryLock(): want lock=nil, got non-nil")
	}
}

func Test_Locker_TryLock_Returns_ErrWouldBlock_When_LockFile_Was_Replaced_During_Acquire(t *testing.T) {
	// Verifies TryLock reports "would block" if the inode check detects the file
	// at path changed during acquisition (because TryLock has no retry budget).

	openInfo := &syscall.Stat_t{Dev: 1, Ino: 1}
	pathInfo := &syscall.Stat_t{Dev: 1, Ino: 2} // mismatch

	locker := NewLocker(stubLockFS{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &stubLockFile{
				fd: 123,
				stat: func() (os.FileInfo, error) {
					return stubFileInfo{sys: openInfo}, nil
				},
			}, nil
		},
		stat: func(string) (os.FileInfo, error) {
			return stubFileInfo{sys: pathInfo}, nil
		},
	})
	locker.flock = func(int, int) error { return nil }

	_, err := locker.TryLock("lock")
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("TryLock(): err=%v, want %v", err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "lock file was replaced") {
		t.Fatalf("TryLock(): err=%q, want substring %q", err.Error(), "lock file was replaced")
	}
}

func Test_Locker_TryRLock_Returns_ErrWouldBlock_When_LockFile_Was_Replaced_During_Acquire(t *testing.T) {
	// Same as TryLock test, but for shared locks.

	openInfo := &syscall.Stat_t{Dev: 1, Ino: 1}
	pathInfo := &syscall.Stat_t{Dev: 1, Ino: 2} // mismatch

	locker := NewLocker(stubLockFS{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &stubLockFile{
				fd: 123,
				stat: func() (os.FileInfo, error) {
					return stubFileInfo{sys: openInfo}, nil
				},
			}, nil
		},
		stat: func(string) (os.FileInfo, error) {
			return stubFileInfo{sys: pathInfo}, nil
		},
	})
	locker.flock = func(int, int) error { return nil }

	_, err := locker.TryRLock("lock")
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("TryRLock(): err=%v, want %v", err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "lock file was replaced") {
		t.Fatalf("TryRLock(): err=%q, want substring %q", err.Error(), "lock file was replaced")
	}
}

func Test_Locker_LockWithTimeout_Returns_ErrWouldBlock_When_LockFile_Kept_Being_Replaced(t *testing.T) {
	// Verifies the inode-mismatch retry loop returns ErrWouldBlock on timeout,
	// and includes context so callers can distinguish this from a plain
	// contention timeout.

	openInfo := &syscall.Stat_t{Dev: 1, Ino: 1}
	pathInfo := &syscall.Stat_t{Dev: 1, Ino: 2} // always mismatches

	locker := NewLocker(stubLockFS{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &stubLockFile{
				fd: 123,
				stat: func() (os.FileInfo, error) {
					return stubFileInfo{sys: openInfo}, nil
				},
			}, nil
		},
		stat: func(string) (os.FileInfo, error) {
			return stubFileInfo{sys: pathInfo}, nil
		},
	})
	locker.flock = func(int, int) error { return nil }

	_, err := locker.LockWithTimeout("lock", 20*time.Millisecond)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("LockWithTimeout(): err=%v, want %v", err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "lock file was replaced") {
		t.Fatalf("LockWithTimeout(): err=%q, want substring %q", err.Error(), "lock file was replaced")
	}
}

func Test_Locker_RLockWithTimeout_Returns_ErrWouldBlock_When_LockFile_Kept_Being_Replaced(t *testing.T) {
	// Same as LockWithTimeout test, but for shared locks.

	openInfo := &syscall.Stat_t{Dev: 1, Ino: 1}
	pathInfo := &syscall.Stat_t{Dev: 1, Ino: 2} // always mismatches

	locker := NewLocker(stubLockFS{
		openFile: func(string, int, os.FileMode) (File, error) {
			return &stubLockFile{
				fd: 123,
				stat: func() (os.FileInfo, error) {
					return stubFileInfo{sys: openInfo}, nil
				},
			}, nil
		},
		stat: func(string) (os.FileInfo, error) {
			return stubFileInfo{sys: pathInfo}, nil
		},
	})
	locker.flock = func(int, int) error { return nil }

	_, err := locker.RLockWithTimeout("lock", 20*time.Millisecond)
	if !errors.Is(err, ErrWouldBlock) {
		t.Fatalf("RLockWithTimeout(): err=%v, want %v", err, ErrWouldBlock)
	}
	if !strings.Contains(err.Error(), "lock file was replaced") {
		t.Fatalf("RLockWithTimeout(): err=%q, want substring %q", err.Error(), "lock file was replaced")
	}
}

func Test_Locker_Lock_Retries_When_LockFile_Was_Replaced_During_Acquire(t *testing.T) {
	// Verifies Lock() doesn't return an error if the lock file is replaced while
	// acquiring the lock: it retries until it locks the inode currently at path.

	open1 := &syscall.Stat_t{Dev: 1, Ino: 1}
	open2 := &syscall.Stat_t{Dev: 1, Ino: 2}
	pathInfo := &syscall.Stat_t{Dev: 1, Ino: 2}

	var openCalls int

	locker := NewLocker(stubLockFS{
		openFile: func(string, int, os.FileMode) (File, error) {
			openCalls++

			switch openCalls {
			case 1:
				return &stubLockFile{
					fd: 123,
					stat: func() (os.FileInfo, error) {
						return stubFileInfo{sys: open1}, nil
					},
				}, nil
			default:
				return &stubLockFile{
					fd: 456,
					stat: func() (os.FileInfo, error) {
						return stubFileInfo{sys: open2}, nil
					},
				}, nil
			}
		},
		stat: func(string) (os.FileInfo, error) {
			return stubFileInfo{sys: pathInfo}, nil
		},
	})
	locker.flock = func(int, int) error { return nil }

	lock, err := locker.Lock("lock")
	if err != nil {
		t.Fatalf("Lock(): %v", err)
	}
	t.Cleanup(func() { _ = lock.Close() })

	if openCalls < 2 {
		t.Fatalf("Lock(): want at least 2 open attempts, got %d", openCalls)
	}
}

func Test_flockRetryEINTR_Retries_On_EINTR(t *testing.T) {
	t.Parallel()

	t.Run("retries and succeeds", func(t *testing.T) {
		const eintrCount = 3

		var calls int
		err := flockRetryEINTR(func(int, int) error {
			calls++
			if calls <= eintrCount {
				return syscall.EINTR
			}
			return nil
		}, 123, syscall.LOCK_EX)
		if err != nil {
			t.Fatalf("flockRetryEINTR(): %v", err)
		}
		if calls != eintrCount+1 {
			t.Fatalf("flockRetryEINTR(): calls=%d, want %d", calls, eintrCount+1)
		}
	})

	t.Run("stops on non-EINTR error", func(t *testing.T) {
		var calls int
		err := flockRetryEINTR(func(int, int) error {
			calls++
			return syscall.EINVAL
		}, 123, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("flockRetryEINTR(): err=%v, want %v", err, syscall.EINVAL)
		}
		if calls != 1 {
			t.Fatalf("flockRetryEINTR(): calls=%d, want 1", calls)
		}
	})
}

type stubLockFS struct {
	openFile func(path string, flag int, perm os.FileMode) (File, error)
	mkdirAll func(path string, perm os.FileMode) error
	stat     func(path string) (os.FileInfo, error)
}

func (s stubLockFS) Open(string) (File, error)       { panic("stubLockFS.Open: not implemented") }
func (s stubLockFS) Create(string) (File, error)     { panic("stubLockFS.Create: not implemented") }
func (s stubLockFS) ReadFile(string) ([]byte, error) { panic("stubLockFS.ReadFile: not implemented") }
func (s stubLockFS) ReadDir(string) ([]os.DirEntry, error) {
	panic("stubLockFS.ReadDir: not implemented")
}
func (s stubLockFS) Exists(string) (bool, error) { panic("stubLockFS.Exists: not implemented") }
func (s stubLockFS) Remove(string) error         { panic("stubLockFS.Remove: not implemented") }
func (s stubLockFS) RemoveAll(string) error      { panic("stubLockFS.RemoveAll: not implemented") }
func (s stubLockFS) Rename(string, string) error { panic("stubLockFS.Rename: not implemented") }
func (s stubLockFS) MkdirAll(path string, perm os.FileMode) error {
	if s.mkdirAll != nil {
		return s.mkdirAll(path, perm)
	}
	return nil
}
func (s stubLockFS) Stat(path string) (os.FileInfo, error) {
	if s.stat == nil {
		panic("stubLockFS.Stat: not implemented")
	}
	return s.stat(path)
}
func (s stubLockFS) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	if s.openFile == nil {
		panic("stubLockFS.OpenFile: not implemented")
	}
	return s.openFile(path, flag, perm)
}

type stubLockFile struct {
	fd   uintptr
	stat func() (os.FileInfo, error)
}

func (*stubLockFile) Read([]byte) (int, error)       { panic("stubLockFile.Read: not implemented") }
func (*stubLockFile) Write([]byte) (int, error)      { panic("stubLockFile.Write: not implemented") }
func (*stubLockFile) Seek(int64, int) (int64, error) { panic("stubLockFile.Seek: not implemented") }
func (*stubLockFile) Sync() error                    { panic("stubLockFile.Sync: not implemented") }
func (f *stubLockFile) Close() error                 { return nil }
func (f *stubLockFile) Fd() uintptr                  { return f.fd }
func (f *stubLockFile) Stat() (os.FileInfo, error) {
	if f.stat == nil {
		panic("stubLockFile.Stat: not implemented")
	}
	return f.stat()
}

type stubFileInfo struct{ sys any }

func (stubFileInfo) Name() string       { return "stub" }
func (stubFileInfo) Size() int64        { return 0 }
func (stubFileInfo) Mode() os.FileMode  { return 0 }
func (stubFileInfo) ModTime() time.Time { return time.Time{} }
func (stubFileInfo) IsDir() bool        { return false }
func (fi stubFileInfo) Sys() any        { return fi.sys }

var _ FS = (*stubLockFS)(nil)
var _ File = (*stubLockFile)(nil)
