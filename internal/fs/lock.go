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
	// ErrWouldBlock is returned by TryLock/TryRLock when the lock is held by another
	// process.
	ErrWouldBlock = errors.New("lock would block")

	// ErrInvalidTimeout is returned when a timeout is <= 0.
	ErrInvalidTimeout = errors.New("invalid lock timeout")
)

type Locker struct{ fs FS }

func NewLocker(fs FS) *Locker {
	return &Locker{fs: fs}
}

type lockType int

const (
	sharedLock    lockType = syscall.LOCK_SH
	exclusiveLock lockType = syscall.LOCK_EX
)

type Lock struct {
	mu   sync.Mutex
	file File
}

func (lk *Lock) Close() error {
	lk.mu.Lock()
	defer lk.mu.Unlock()

	if lk.file == nil {
		// Make Close() idempotent.
		return nil
	}

	fd := int(lk.file.Fd())

	unlockErr := syscall.Flock(fd, syscall.LOCK_UN)
	closeErr := lk.file.Close()
	lk.file = nil

	if unlockErr != nil {
		return fmt.Errorf("unlocking lock: %w", unlockErr)
	}

	return fmt.Errorf("closing lock fd: %w", closeErr)
}

func (l *Locker) Lock(path string) (*Lock, error) {
	return l.lockBlocking(path, exclusiveLock)
}

func (l *Locker) RLock(path string) (*Lock, error) {
	return l.lockBlocking(path, sharedLock)
}

func (l *Locker) LockWithTimeout(path string, timeout time.Duration) (*Lock, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be > 0", ErrInvalidTimeout)
	}

	return l.lockWithTimeout(path, exclusiveLock, timeout)
}

func (l *Locker) RLockWithTimeout(path string, timeout time.Duration) (*Lock, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: timeout must be > 0", ErrInvalidTimeout)
	}

	return l.lockWithTimeout(path, sharedLock, timeout)
}

func (l *Locker) TryLock(path string) (*Lock, error) {
	return l.tryLock(path, exclusiveLock)
}

func (l *Locker) TryRLock(path string) (*Lock, error) {
	return l.tryLock(path, sharedLock)
}

func (l *Locker) lockBlocking(path string, lt lockType) (*Lock, error) {
	for {
		file, err := l.openLockFile(path)
		if err != nil {
			return nil, err
		}

		fd := int(file.Fd())
		if err := flockRetryEINTR(fd, int(lt)); err != nil {
			_ = file.Close()
			return nil, err
		}

		same, err := l.inodeMatchesPath(path, file)
		if err != nil {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = file.Close()
			return nil, err
		}

		if !same {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = file.Close()
			continue
		}

		return &Lock{file: file}, nil
	}
}

func (l *Locker) lockWithTimeout(path string, lt lockType, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	backoff := 1 * time.Millisecond

	for {
		lock, err := l.tryLock(path, lt)
		if err == nil {
			return lock, nil
		}

		if !errors.Is(err, ErrWouldBlock) {
			return nil, err
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, os.ErrDeadlineExceeded
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

func (l *Locker) tryLock(path string, lt lockType) (*Lock, error) {
	for range 3 {
		file, err := l.openLockFile(path)
		if err != nil {
			return nil, err
		}

		fd := int(file.Fd())

		err = flockRetryEINTR(fd, int(lt)|syscall.LOCK_NB)
		if err != nil {
			_ = file.Close()

			if isWouldBlock(err) {
				return nil, ErrWouldBlock
			}

			return nil, err
		}

		same, err := l.inodeMatchesPath(path, file)
		if err != nil {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = file.Close()

			return nil, err
		}

		if !same {
			_ = syscall.Flock(fd, syscall.LOCK_UN)
			_ = file.Close()

			continue
		}

		return &Lock{file: file}, nil
	}

	return nil, ErrWouldBlock
}

const (
	lockFilePerm = 0o600
	lockDirPerm  = 0o755
)

func (l *Locker) openLockFile(path string) (File, error) {
	dir := filepath.Dir(path)
	if err := l.fs.MkdirAll(dir, lockDirPerm); err != nil {
		return nil, err
	}

	return l.fs.OpenFile(path, os.O_CREATE|os.O_RDWR, lockFilePerm)
}

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

func flockRetryEINTR(fd int, how int) error {
	for {
		err := syscall.Flock(fd, how)
		if err == nil || !errors.Is(err, syscall.EINTR) {
			return err
		}
	}
}

func isWouldBlock(err error) bool {
	return errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN)
}
