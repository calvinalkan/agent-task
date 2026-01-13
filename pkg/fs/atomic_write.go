package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
)

// ErrAtomicWriteDirSync indicates the parent directory could not be synced after rename.
//
// When returned, the new file is in place but durability is not guaranteed.
// Callers can detect this with errors.Is(err, ErrAtomicWriteDirSync).
var ErrAtomicWriteDirSync = errors.New("dir sync")

// AtomicWriter writes files atomically using rename.
type AtomicWriter struct {
	fs FS
}

// NewAtomicWriter creates an AtomicWriter that uses the given filesystem.
// Panics if fs is nil.
func NewAtomicWriter(fs FS) *AtomicWriter {
	if fs == nil {
		panic("fs is nil")
	}

	return &AtomicWriter{fs: fs}
}

// AtomicWriteOptions configures WriteFile behavior.
type AtomicWriteOptions struct {
	// SyncDir controls whether the parent directory is synced after rename.
	// Default: true.
	SyncDir bool

	// Perm specifies the file permissions. Must be non-zero.
	// The file is always explicitly chmod'd to this mode, regardless of umask.
	Perm os.FileMode
}

// Write writes data from r to path atomically and durably.
//
// It writes to a temp file in the same directory, syncs it, renames it over
// path, then syncs the parent directory (if opts.SyncDir is true).
//
// If the directory sync step fails, the returned error satisfies
// errors.Is(err, ErrDirSync).
func (w *AtomicWriter) Write(path string, reader io.Reader, opts AtomicWriteOptions) error {
	if reader == nil {
		panic("reader is nil")
	}

	if path == "" {
		return errors.New("path is empty")
	}

	if opts.Perm == 0 {
		return errors.New("opts.Perm must be non-zero")
	}

	dir, base := filepath.Split(path)
	if base == "" || base == string(os.PathSeparator) || base == "." {
		return fmt.Errorf("path is invalid: %q", path)
	}

	if dir == "" {
		dir = "."
	}

	dir = filepath.Clean(dir)

	tmpFile, tmpPath, err := createAtomicTempFile(w.fs, dir, base, opts.Perm)
	if err != nil {
		return err
	}

	cleanup := func() error {
		closeErr := closeTmpFile(tmpPath, tmpFile)
		removeErr := removeTempFile(w.fs, tmpPath)

		return errors.Join(closeErr, removeErr)
	}

	chmodErr := tmpFile.Chmod(opts.Perm)
	if chmodErr != nil {
		return errors.Join(
			fmt.Errorf("chmod temp file %q: %w", tmpPath, chmodErr),
			cleanup(),
		)
	}

	writeErr := writeAndSyncTempFile(tmpFile, tmpPath, reader)
	if writeErr != nil {
		return errors.Join(
			writeErr,
			cleanup(),
		)
	}

	renameErr := w.fs.Rename(tmpPath, path)
	if renameErr != nil {
		return errors.Join(
			fmt.Errorf("rename: %w", renameErr),
			cleanup(),
		)
	}

	cleanupErr := cleanup()

	if opts.SyncDir {
		err := fsyncDir(w.fs, dir)
		if err != nil {
			return errors.Join(err, cleanupErr)
		}
	}

	// Don't surface cleanup errors if all main operations worked.
	return nil
}

// WriteWithDefaults writes content atomically using default options.
func (w *AtomicWriter) WriteWithDefaults(path string, r io.Reader) error {
	return w.Write(path, r, w.DefaultOptions())
}

// DefaultOptions returns the default atomic write options.
func (*AtomicWriter) DefaultOptions() AtomicWriteOptions {
	return AtomicWriteOptions{
		SyncDir: true,
		Perm:    0o644,
	}
}

func writeAndSyncTempFile(file File, path string, r io.Reader) error {
	_, copyErr := io.Copy(file, r)
	if copyErr != nil {
		return fmt.Errorf("write temp file %q: %w", path, copyErr)
	}

	err := file.Sync()
	if err != nil {
		return fmt.Errorf("sync temp file %q: %w", path, err)
	}

	return nil
}

const atomicWriteMaxAttempts = 10000

var atomicWriteCounter atomic.Uint64

func createAtomicTempFile(fs FS, dir, base string, perm os.FileMode) (File, string, error) {
	for range atomicWriteMaxAttempts {
		seq := atomicWriteCounter.Add(1)
		path := filepath.Join(dir, fmt.Sprintf(".%s.tmp-%d", base, seq))

		file, err := fs.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if err == nil {
			return file, path, nil
		}

		if os.IsExist(err) {
			continue
		}

		return nil, "", fmt.Errorf("create temp file: %w", err)
	}

	return nil, "", fmt.Errorf("exhausted temp file attempts in %q", dir)
}

func fsyncDir(fs FS, dirPath string) error {
	dirFd, err := fs.Open(dirPath)
	if err != nil {
		return errors.Join(ErrAtomicWriteDirSync, fmt.Errorf("open dir %q: %w", dirPath, err))
	}

	syncErr := dirFd.Sync()
	if syncErr == nil {
		return closeDir(dirPath, dirFd)
	}

	return errors.Join(
		ErrAtomicWriteDirSync,
		fmt.Errorf("%q: %w", dirPath, syncErr),
		closeDir(dirPath, dirFd),
	)
}

func closeDir(dir string, file File) error {
	err := file.Close()
	if err == nil {
		return nil
	}

	return fmt.Errorf("close dir %q: %w", dir, err)
}

func closeTmpFile(path string, file File) error {
	err := file.Close()
	if err == nil {
		return nil
	}

	return fmt.Errorf("close temp file %q: %w", path, err)
}

func removeTempFile(fs FS, path string) error {
	err := fs.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove temp file %q: %w", path, err)
	}

	return nil
}
