package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/natefinch/atomic"
)

// LockTimeout is the timeout for acquiring a file lock.
const LockTimeout = 5 * time.Second

// Lock errors.
var (
	errLockTimeout  = errors.New("lock timeout")
	errLockFileOpen = errors.New("failed to open lock file")
)

// fileLock represents a lock on a file.
type fileLock struct {
	path string
	file *os.File
}

// acquireLockWithTimeout tries to acquire an exclusive lock on the given path.
// Uses a separate .lock file to avoid issues with the main file.
func acquireLockWithTimeout(path string, timeout time.Duration) (*fileLock, error) {
	lockPath := path + ".lock"

	// Open or create lock file
	file, openErr := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, filePerms) //nolint:gosec // path is from caller
	if openErr != nil {
		return nil, fmt.Errorf("%w: %w", errLockFileOpen, openErr)
	}

	// Try to acquire lock with timeout
	deadline := time.Now().Add(timeout)

	const retryInterval = 10 * time.Millisecond

	for {
		// Try non-blocking exclusive lock
		flockErr := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if flockErr == nil {
			return &fileLock{path: lockPath, file: file}, nil
		}

		// Check if we've exceeded the timeout
		if time.Now().After(deadline) {
			_ = file.Close()

			return nil, fmt.Errorf("%w: %s", errLockTimeout, path)
		}

		// Wait before retrying
		time.Sleep(retryInterval)
	}
}

// acquireLock tries to acquire an exclusive lock with the default timeout.
func acquireLock(path string) (*fileLock, error) {
	return acquireLockWithTimeout(path, LockTimeout)
}

// release releases the lock.
func (l *fileLock) release() {
	if l.file != nil {
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		_ = l.file.Close()
	}
}

// WithTicketLock provides atomic access to a ticket file with file locking.
// The function handler receives the current file content and returns the new content.
// If handler returns nil content, no write is performed (read-only operation).
// If handler returns an error, no write is performed and the error is returned.
// The lock is always released when this function returns.
func WithTicketLock(path string, handler func(content []byte) ([]byte, error)) error {
	lock, lockErr := acquireLock(path)
	if lockErr != nil {
		return fmt.Errorf("acquiring lock: %w", lockErr)
	}

	defer lock.release()

	content, readErr := os.ReadFile(path) //nolint:gosec // path is validated by caller
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
}
