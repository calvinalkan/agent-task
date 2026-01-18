package slotcache

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// acquireWriterLock acquires an exclusive, non-blocking lock on the lock file.
// Returns the lock file handle on success. On lock contention, returns ErrBusy.
func acquireWriterLock(cachePath string) (*os.File, error) {
	lockPath := cachePath + ".lock"

	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}

	err = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		_ = lockFile.Close()

		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, ErrBusy
		}

		return nil, fmt.Errorf("flock: %w", err)
	}

	return lockFile, nil
}

// releaseWriterLock releases the lock and closes the file.
// Does NOT delete the lock file (per spec: lock file persists).
func releaseWriterLock(lockFile *os.File) {
	if lockFile == nil {
		return
	}

	_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	_ = lockFile.Close()
}

// tryAcquireWriterLock attempts to acquire the lock non-blocking.
// Returns the lock file on success, or an error on contention/failure.
// This is used during Open to detect crashed writers.
func tryAcquireWriterLock(cachePath string) (*os.File, error) {
	return acquireWriterLock(cachePath)
}
