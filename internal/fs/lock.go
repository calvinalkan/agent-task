package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var (
	// ErrWouldBlock is returned by TryLock/TryRLock when the lock is held by
	// another process, or by *WithTimeout when the acquisition timeout expires.
	ErrWouldBlock = errors.New("lock would block")

	// ErrInvalidTimeout is returned when a timeout is <= 0.
	ErrInvalidTimeout = errors.New("invalid lock timeout")

	// errInodeMismatch is an internal sentinel indicating the lock file was
	// replaced between open and flock. Callers should retry.
	errInodeMismatch = errors.New("inode mismatch")
)

// Locker provides file-based locking using flock(2).
//
// flock locks an inode (the open file), not a pathname. In most cases, callers
// should lock a dedicated, stable lock file path (for example "ticket.md.lock")
// and avoid replacing/unlinking that lock file while locks may be held.
//
// Locker has no internal mutable state beyond its dependencies. It is safe for
// concurrent use as long as the underlying [FS] implementation is safe for
// concurrent use (see [FS] docs).
type Locker struct {
	fs    FS
	flock func(fd int, how int) error
}

// NewLocker creates a Locker that uses the given filesystem for file operations.
func NewLocker(fs FS) *Locker {
	return &Locker{
		fs:    fs,
		flock: syscall.Flock,
	}
}

// Lock represents a held file lock. Call [Lock.Close] to release it.
type Lock struct {
	mu    sync.Mutex
	file  File
	flock func(fd int, how int) error
}

// Close releases the lock and closes the underlying file descriptor.
//
// Close is idempotent - calling it multiple times is safe and subsequent calls
// return nil.
//
// Note: on Unix, closing a file descriptor typically releases any flock held
// by that descriptor/process. Close attempts an explicit unlock first; if that
// fails but the close succeeds, the lock is usually still released. If Close
// returns an error, treat it as "something went wrong during cleanup" and log
// it; callers typically cannot make strong guarantees about whether the lock
// was released.
//
// If Close returns an error, the lock may or may not have been released and
// the file descriptor may or may not be closed. In practice, errors here are
// rare (kernel issues or bugs) and there is little the caller can do to
// recover. Logging the error is reasonable; retrying is unlikely to help.
func (lk *Lock) Close() error {
	lk.mu.Lock()
	defer lk.mu.Unlock()

	if lk.file == nil {
		return nil
	}

	fd := int(lk.file.Fd())

	unlockErr := flockRetryEINTR(lk.flock, fd, syscall.LOCK_UN)
	closeErr := lk.file.Close()
	lk.file = nil

	if unlockErr != nil {
		return fmt.Errorf("unlocking lock: %w", unlockErr)
	}

	if closeErr != nil {
		return fmt.Errorf("closing lock fd: %w", closeErr)
	}

	return nil
}

type lockType int

const (
	sharedLock    lockType = syscall.LOCK_SH
	exclusiveLock lockType = syscall.LOCK_EX
)

// Lock acquires an exclusive lock on the file at path, blocking until the lock
// is available.
//
// If the file or its parent directories do not exist, they are created lazily.
// The lock is held on the exact path provided - not a temporary file.
//
// This method blocks in the kernel with no timeout. It can block indefinitely
// if another process holds the lock and never releases it. Use
// [Locker.LockWithTimeout] or [Locker.TryLock] if you need cancellation or
// timeout behavior.
//
// Race conditions where the file is replaced (renamed, deleted+recreated)
// during lock acquisition are handled automatically - the lock is always
// acquired on the inode currently at path. See [Locker.inodeMatchesPath] for
// details.
func (l *Locker) Lock(path string) (*Lock, error) {
	return l.lockBlocking(path, exclusiveLock)
}

// RLock acquires a shared (read) lock on the file at path, blocking until the
// lock is available.
//
// Multiple processes can hold shared locks simultaneously, but a shared lock
// blocks exclusive locks and vice versa. Use shared locks for read-only access
// when you want to allow concurrent readers but block writers.
//
// See [Locker.Lock] for details on blocking behavior, file creation, and inode
// replacement caveats.
func (l *Locker) RLock(path string) (*Lock, error) {
	return l.lockBlocking(path, sharedLock)
}

// LockWithTimeout attempts to acquire an exclusive lock, retrying with
// exponential backoff until the timeout expires.
//
// Unlike [Locker.Lock], this method uses non-blocking flock calls internally
// and polls with sleeps (1ms to 25ms backoff). This is slightly less efficient
// than true blocking but allows for timeout/cancellation.
//
// The timeout is best-effort: because this method polls and sleeps, it may
// overshoot slightly under scheduler delay.
//
// Returns [ErrWouldBlock] if the timeout expires before the lock is acquired.
// Returns [ErrInvalidTimeout] if timeout <= 0.
func (l *Locker) LockWithTimeout(path string, timeout time.Duration) (*Lock, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be > 0", ErrInvalidTimeout)
	}

	return l.lockPolling(path, exclusiveLock, timeout)
}

// RLockWithTimeout attempts to acquire a shared lock, retrying with exponential
// backoff until the timeout expires.
//
// See [Locker.RLock] for shared lock semantics and [Locker.LockWithTimeout] for
// timeout behavior.
func (l *Locker) RLockWithTimeout(path string, timeout time.Duration) (*Lock, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be > 0", ErrInvalidTimeout)
	}

	return l.lockPolling(path, sharedLock, timeout)
}

// TryLock attempts to acquire an exclusive lock without blocking.
//
// Returns immediately with [ErrWouldBlock] if the lock is held by another
// process. Use this for opportunistic locking where you have a fallback.
func (l *Locker) TryLock(path string) (*Lock, error) {
	return l.lockPolling(path, exclusiveLock, 0)
}

// TryRLock attempts to acquire a shared lock without blocking.
//
// Returns immediately with [ErrWouldBlock] if an exclusive lock is held by
// another process. Multiple shared locks can be held simultaneously.
func (l *Locker) TryRLock(path string) (*Lock, error) {
	return l.lockPolling(path, sharedLock, 0)
}

func (l *Locker) lockBlocking(path string, lt lockType) (*Lock, error) {
	openFlag := openFlagForLockType(lt)

	for {
		file, err := l.openLockFile(path, openFlag)
		if err != nil {
			return nil, fmt.Errorf("opening lockfile: %w", err)
		}

		err = l.acquire(file, path, lt, false)
		if err == nil {
			return &Lock{file: file, flock: l.flock}, nil
		}

		_ = file.Close()

		if errors.Is(err, errInodeMismatch) {
			continue
		}

		return nil, err
	}
}

// lockPolling attempts to acquire a lock using non-blocking flock with retries.
//
//   - timeout == 0: try once (TryLock behavior)
//   - timeout > 0: retry with backoff until timeout (LockWithTimeout behavior)
func (l *Locker) lockPolling(path string, lt lockType, timeout time.Duration) (*Lock, error) {
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}

	backoff := time.Millisecond
	openFlag := openFlagForLockType(lt)

	for {
		file, err := l.openLockFile(path, openFlag)
		if err != nil {
			return nil, fmt.Errorf("opening lockfile: %w", err)
		}

		err = l.acquire(file, path, lt, true)
		if err == nil {
			return &Lock{file: file, flock: l.flock}, nil
		}

		_ = file.Close()

		retryable := errors.Is(err, ErrWouldBlock) || errors.Is(err, errInodeMismatch)
		if !retryable {
			return nil, err
		}

		if timeout == 0 {
			if errors.Is(err, errInodeMismatch) {
				return nil, fmt.Errorf("%w: lock file was replaced while acquiring lock", ErrWouldBlock)
			}

			return nil, ErrWouldBlock
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			if errors.Is(err, errInodeMismatch) {
				return nil, fmt.Errorf("%w: timed out after %s (lock file was replaced while acquiring lock)", ErrWouldBlock, timeout)
			}

			return nil, fmt.Errorf("%w: timed out after %s", ErrWouldBlock, timeout)
		}

		sleep := backoff
		if sleep > remaining {
			sleep = remaining
		}

		time.Sleep(sleep)

		if backoff < 25*time.Millisecond {
			backoff *= 2
			if backoff > 25*time.Millisecond {
				backoff = 25 * time.Millisecond
			}
		}
	}
}

// acquire attempts to flock the given file and verify the inode still matches
// path. On success, the file is locked and ready to use. On failure, the file
// is unlocked (if needed) but NOT closed - the caller must close it.
//
// Returns:
//   - nil: lock acquired successfully
//   - ErrWouldBlock: lock held by another process (only when nonBlocking=true)
//   - errInodeMismatch: file at path was replaced, caller should retry
//   - other error: something went wrong
func (l *Locker) acquire(file File, path string, lt lockType, nonBlocking bool) error {
	fd := int(file.Fd())

	flags := int(lt)
	if nonBlocking {
		flags |= syscall.LOCK_NB
	}

	if err := flockRetryEINTR(l.flock, fd, flags); err != nil {
		if isWouldBlock(err) {
			return ErrWouldBlock
		}
		return err
	}

	match, err := l.inodeMatchesPath(path, file)
	if err != nil {
		_ = flockRetryEINTR(l.flock, fd, syscall.LOCK_UN)
		if errors.Is(err, os.ErrNotExist) {
			return errInodeMismatch
		}
		return fmt.Errorf("verifying inode match: %w", err)
	}

	if !match {
		_ = flockRetryEINTR(l.flock, fd, syscall.LOCK_UN)
		return errInodeMismatch
	}

	return nil
}

const (
	lockFilePerm = 0o600
	lockDirPerm  = 0o755
)

func (l *Locker) openLockFile(path string, flag int) (File, error) {
	f, err := l.fs.OpenFile(path, flag|os.O_CREATE, lockFilePerm)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return f, err
	}

	if err := l.fs.MkdirAll(filepath.Dir(path), lockDirPerm); err != nil {
		return nil, err
	}

	return l.fs.OpenFile(path, flag|os.O_CREATE, lockFilePerm)
}

// inodeMatchesPath verifies that f (the open file descriptor we're about to
// use as the lock) still refers to the file currently at path.
//
// Why: flock locks by inode, not pathname. A pathname can be replaced while
// you’re acquiring the lock (or while you’re blocked waiting): rename,
// delete+recreate, editors writing via temp+rename, etc. Then you can end up
// with this situation:
//
//  1. A opens path → gets inode X
//  2. path is replaced → now points to inode Y
//  3. A successfully flocks inode X (still valid, but no longer “the file at path”)
//  4. B opens path → inode Y, and flocks it successfully too
//
// Without this check, both A and B believe they "locked the path", but they're
// actually coordinating on different inodes.
//
// This method compares (dev,inode) of the open fd (via File.Stat) to the
// current (dev,inode) at path (via [FS.Stat]). Callers use it immediately after
// flock; on mismatch they unlock and retry.
//
// Note: this only protects the open→lock window / waiting period. If the file
// at path is replaced after this check succeeds, the lock no longer guards the
// pathname; avoid replacing the file while holding the lock, or use a separate
// lock file/directory lock if you need that guarantee.
func (l *Locker) inodeMatchesPath(path string, f File) (bool, error) {
	openInfo, err := f.Stat()
	if err != nil {
		return false, err
	}

	openSys, ok := openInfo.Sys().(*syscall.Stat_t)
	if !ok || openSys == nil {
		return false, fmt.Errorf("file.Stat Sys=%T, want *syscall.Stat_t", openInfo.Sys())
	}

	pathInfo, err := l.fs.Stat(path)
	if err != nil {
		return false, err
	}

	pathSys, ok := pathInfo.Sys().(*syscall.Stat_t)
	if !ok || pathSys == nil {
		return false, fmt.Errorf("fs.Stat Sys=%T, want *syscall.Stat_t", pathInfo.Sys())
	}

	return openSys.Dev == pathSys.Dev && openSys.Ino == pathSys.Ino, nil
}

func isWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}

func openFlagForLockType(lt lockType) int {
	if lt == sharedLock {
		return os.O_RDONLY
	}
	return os.O_RDWR
}

// flockRetryEINTR wraps flock, retrying on EINTR.
//
// EINTR means the syscall was interrupted by a signal before it could complete.
// This is common on Unix systems - signals like SIGWINCH (terminal resize),
// SIGCHLD (child process exited), or SIGALRM (timers) can interrupt any
// blocking syscall. When this happens, the syscall didn't fail, it just needs
// to be retried.
//
// We cap retries to avoid spinning forever under pathological signal storms.
// In practice this limit should never be hit - if you're getting 10000 signals
// during a single flock call, something else is very wrong. Note that Go's
// stdlib (ignoringEINTR in the os package) retries forever without a cap.
func flockRetryEINTR(flock func(fd int, how int) error, fd int, how int) error {
	const maxEINTRRetries = 10000

	var err error
	for range maxEINTRRetries {
		err = flock(fd, how)
		if err == nil || !errors.Is(err, syscall.EINTR) {
			return err
		}
	}

	return err
}
