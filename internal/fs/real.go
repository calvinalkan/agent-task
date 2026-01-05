package fs

import (
	"bytes"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/natefinch/atomic"
)

// Real implements [FS] using the real filesystem.
//
// All methods are pure passthroughs to the [os] package with identical
// behavior and error semantics. The only exceptions are [Real.Exists] which
// wraps [os.Stat], [Real.WriteFileAtomic] which uses atomic file writes,
// and [Real.Lock] which provides file locking.
type Real struct{}

// NewReal returns a new [Real] filesystem.
func NewReal() *Real {
	return &Real{}
}

// --- File Operations ---

// A passthrough wrapper for [os.Open].
func (r *Real) Open(path string) (File, error) {
	return os.Open(path)
}

// A passthrough wrapper for [os.Create].
func (r *Real) Create(path string) (File, error) {
	return os.Create(path)
}

// A passthrough wrapper for [os.OpenFile].
func (r *Real) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(path, flag, perm)
}

// --- Convenience Methods ---

// A passthrough wrapper for [os.ReadFile].
func (r *Real) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

func (r *Real) WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	return atomic.WriteFile(path, bytes.NewReader(data))
}

// --- Directory Operations ---

// A passthrough wrapper for [os.ReadDir].
func (r *Real) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

// A passthrough wrapper for [os.MkdirAll].
func (r *Real) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// --- Metadata ---

// A passthrough wrapper for [os.Stat].
func (r *Real) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

// Exists checks if a file exists using [os.Stat].
// Returns (true, nil) if the file exists, (false, nil) if it does not,
// or (false, err) for other errors.
func (r *Real) Exists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}

	if os.IsNotExist(err) {
		return false, nil
	}

	return false, err
}

// --- Mutations ---

// A passthrough wrapper for [os.Remove].
func (r *Real) Remove(path string) error {
	return os.Remove(path)
}

// A passthrough wrapper for [os.RemoveAll].
func (r *Real) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// A passthrough wrapper for [os.Rename].
func (r *Real) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// --- Locking ---

const (
	lockTimeout = 2 * time.Second
	lockPerms   = 0o644
	dirPerms    = 0o755
)

// realLock holds an exclusive file lock.
type realLock struct {
	path string
	file *os.File
}

func (l *realLock) Close() error {
	if l.file != nil {
		_ = os.Remove(l.path)
		_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
		err := l.file.Close()
		l.file = nil

		return err
	}

	return nil
}

func (r *Real) Lock(path string) (Locker, error) {
	// Put lock files in .locks subdirectory to avoid changing parent dir mtime.
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	locksDir := filepath.Join(dir, ".locks")
	lockPath := filepath.Join(locksDir, base+".lock")

	deadline := time.Now().Add(lockTimeout)

	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, os.ErrDeadlineExceeded
		}

		// Ensure locks directory exists.
		if err := os.MkdirAll(locksDir, dirPerms); err != nil {
			return nil, err
		}

		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, lockPerms)
		if err != nil {
			return nil, err
		}

		// Get inode of the file we opened.
		var openStat syscall.Stat_t
		if err := syscall.Fstat(int(file.Fd()), &openStat); err != nil {
			file.Close()

			return nil, err
		}

		fd := int(file.Fd())
		done := make(chan error, 1)

		go func() {
			done <- syscall.Flock(fd, syscall.LOCK_EX)
		}()

		select {
		case err := <-done:
			if err != nil {
				file.Close()

				return nil, err
			}

			// Verify the file at the path still has the same inode.
			var pathStat syscall.Stat_t
			if err := syscall.Stat(lockPath, &pathStat); err != nil || pathStat.Ino != openStat.Ino {
				// File was deleted/replaced, retry.
				syscall.Flock(fd, syscall.LOCK_UN)
				file.Close()

				continue
			}

			return &realLock{path: lockPath, file: file}, nil

		case <-time.After(remaining):
			file.Close()

			return nil, os.ErrDeadlineExceeded
		}
	}
}

// Compile-time interface check.
var _ FS = (*Real)(nil)
