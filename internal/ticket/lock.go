package ticket

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/natefinch/atomic"
)

// locksDirName is the subdirectory for lock files.
// Using a subdirectory avoids modifying the parent directory's mtime,
// which would invalidate the cache on every lock acquire/release.
const locksDirName = ".locks"

// LockTimeout is the timeout for acquiring a file lock.
const LockTimeout = 2 * time.Second

// Lock errors.
var (
	errLockTimeout  = errors.New("lock timeout")
	errLockFileOpen = errors.New("failed to open lock file")
)

// WithLock executes a function while holding an exclusive lock on the given path.
// The lock is released when the function returns.
func WithLock(path string, handler func() error) error {
	lock, lockErr := acquireLock(path)
	if lockErr != nil {
		return fmt.Errorf("acquiring lock: %w", lockErr)
	}

	defer lock.release()

	return handler()
}

// WithTicketLock provides atomic access to a ticket file with file locking.
// The function handler receives the current file content and returns the new content.
// If handler returns nil content, no write is performed (read-only operation).
// If handler returns an error, no write is performed and the error is returned.
// The lock is always released when this function returns.
func WithTicketLock(path string, handler func(content []byte) ([]byte, error)) error {
	return WithLock(path, func() error {
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return fmt.Errorf("reading ticket: %w", readErr)
		}

		newContent, handleErr := handler(content)
		if handleErr != nil {
			return handleErr // check failed, no write
		}

		if newContent == nil {
			return nil // read-only operation
		}

		writeErr := atomic.WriteFile(path, strings.NewReader(string(newContent)))
		if writeErr != nil {
			return fmt.Errorf("writing ticket: %w", writeErr)
		}

		return nil
	})
}

// fileLock represents a lock on a file.
type fileLock struct {
	path string
	file *os.File
}

// release releases the lock and removes the lock file.
// Order matters: remove while holding lock, then unlock, then close.
func (l *fileLock) release() {
	if l.file != nil {
		_ = os.Remove(l.path)
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
		l.file = nil
	}
}

// acquireLockWithTimeout tries to acquire an exclusive lock on the given path.
// Uses a separate .lock file in a .locks subdirectory to avoid issues with the
// main file and to prevent modifying the parent directory's mtime.
// Handles the race between flock acquisition and lock file deletion by
// verifying the inode after acquiring the lock.
func acquireLockWithTimeout(path string, timeout time.Duration) (*fileLock, error) {
	// Put lock files in .locks subdirectory to avoid changing parent dir mtime.
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	locksDir := filepath.Join(dir, locksDirName)
	lockPath := filepath.Join(locksDir, base+".lock")

	deadline := time.Now().Add(timeout)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("%w: %s", errLockTimeout, path)
		}

		// Ensure locks directory exists.
		mkdirErr := os.MkdirAll(locksDir, dirPerms)
		if mkdirErr != nil {
			return nil, fmt.Errorf("creating locks dir: %w", mkdirErr)
		}

		file, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, filePerms)
		if openErr != nil {
			return nil, fmt.Errorf("%w: %w", errLockFileOpen, openErr)
		}

		// Get inode of the file we opened.
		var openStat syscall.Stat_t

		err := syscall.Fstat(int(file.Fd()), &openStat)
		if err != nil {
			_ = file.Close()

			return nil, fmt.Errorf("fstat lock file: %w", err)
		}

		fd := int(file.Fd())
		done := make(chan error, 1)

		go func() {
			done <- syscall.Flock(fd, syscall.LOCK_EX)
		}()

		select {
		case err := <-done:
			if err != nil {
				_ = file.Close()

				return nil, fmt.Errorf("flock: %w", err)
			}

			// Verify the file at the path still has the same inode.
			// If not, someone deleted and recreated it while we were waiting.
			var pathStat syscall.Stat_t

			statErr := syscall.Stat(lockPath, &pathStat)
			if statErr != nil || pathStat.Ino != openStat.Ino {
				// File was deleted/replaced, retry with new file.
				_ = syscall.Flock(fd, syscall.LOCK_UN)
				_ = file.Close()

				continue
			}

			return &fileLock{path: lockPath, file: file}, nil
		case <-time.After(remaining):
			_ = file.Close()

			return nil, fmt.Errorf("%w: %s", errLockTimeout, path)
		}
	}
}

// acquireLock tries to acquire an exclusive lock with the default timeout.
func acquireLock(path string) (*fileLock, error) {
	return acquireLockWithTimeout(path, LockTimeout)
}
