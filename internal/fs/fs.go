// Package fs provides filesystem abstractions for testing and fault injection.
//
// The main types are:
//   - [FS]: interface for filesystem operations
//   - [File]: interface for open files (satisfied by [os.File])
//   - [Real]: production implementation using [os] package
//   - [Chaos]: testing implementation that injects random failures
//
// Example usage:
//
//	fs := fs.NewReal()
//	f, err := fs.Open("config.json")
//	if err != nil {
//	    return err
//	}
//	defer f.Close()
//
//	// Works with all stdlib io functions:
//	scanner := bufio.NewScanner(f)
//	data, _ := io.ReadAll(f)
package fs

import (
	"io"
	"os"
)

// File represents an open file descriptor.
//
// This interface is satisfied by [os.File] and can be used with all
// standard library functions that accept [io.Reader], [io.Writer],
// [io.Seeker], or [io.Closer].
//
// Example:
//
//	f, _ := fs.Open("data.txt")
//	defer f.Close()
//
//	// Use with bufio
//	scanner := bufio.NewScanner(f)
//
//	// Use with io
//	io.Copy(os.Stdout, f)
//
//	// Use with encoding/json
//	json.NewDecoder(f).Decode(&v)
type File interface {
	// Embedded interfaces from [io] package.
	// These provide Read, Write, Close, and Seek methods.
	io.ReadWriteCloser
	io.Seeker

	// Fd returns the file descriptor. See [os.File.Fd].
	// Used for low-level operations like [syscall.Flock].
	Fd() uintptr

	// Stat returns the [os.FileInfo] for this file. See [os.File.Stat].
	Stat() (os.FileInfo, error)

	// Sync commits the file's contents to disk. See [os.File.Sync].
	Sync() error
}

// Locker represents a held file lock.
// Call [Locker.Close] to release the lock.
//
// Example:
//
//	lock, err := fs.Lock("data.db")
//	if err != nil {
//	    return err // lock contention or timeout
//	}
//	defer lock.Close() // always release
//
//	// ... exclusive access to data.db ...
type Locker interface {
	io.Closer
}

// FS defines filesystem operations for reading, writing, and managing files.
//
// Two implementations are provided:
//   - [Real]: production use, wraps [os] package
//   - [Chaos]: testing use, injects random failures
//
// All methods mirror their [os] package equivalents but can be intercepted
// for testing with fault injection.
type FS interface {
	// --- File Operations ---

	// Open opens a file for reading. See [os.Open].
	// The returned [File] can be used with [bufio], [io], and other stdlib packages.
	Open(path string) (File, error)

	// Create creates or truncates a file for writing. See [os.Create].
	// The file is created with mode 0666 (before umask).
	Create(path string) (File, error)

	// OpenFile opens a file with specified flags and permissions. See [os.OpenFile].
	// Use this for fine-grained control (append, exclusive create, etc).
	//
	// Common flags: [os.O_RDONLY], [os.O_WRONLY], [os.O_RDWR],
	// [os.O_APPEND], [os.O_CREATE], [os.O_EXCL], [os.O_TRUNC].
	OpenFile(path string, flag int, perm os.FileMode) (File, error)

	// --- Convenience Methods ---

	// ReadFile reads an entire file into memory. See [os.ReadFile].
	// For large files, prefer [FS.Open] with streaming reads.
	ReadFile(path string) ([]byte, error)

	// WriteFileAtomic writes data to a file atomically.
	// Uses a temp file + rename to prevent partial writes on crash.
	// This is safer than [os.WriteFile] for critical data.
	WriteFileAtomic(path string, data []byte, perm os.FileMode) error

	// --- Directory Operations ---

	// ReadDir reads a directory and returns its entries. See [os.ReadDir].
	// Entries are sorted by name.
	ReadDir(path string) ([]os.DirEntry, error)

	// MkdirAll creates a directory and all parents. See [os.MkdirAll].
	// No error if the directory already exists.
	MkdirAll(path string, perm os.FileMode) error

	// --- Metadata ---

	// Stat returns file info. See [os.Stat].
	// Returns [os.ErrNotExist] if file doesn't exist.
	Stat(path string) (os.FileInfo, error)

	// Exists reports whether a file or directory exists.
	// Returns (false, nil) if not found, (false, err) on other errors.
	Exists(path string) (bool, error)

	// --- Mutations ---

	// Remove deletes a file or empty directory. See [os.Remove].
	// For recursive deletion, use [FS.RemoveAll].
	Remove(path string) error

	// RemoveAll deletes a path and any children. See [os.RemoveAll].
	// No error if path doesn't exist.
	RemoveAll(path string) error

	// Rename moves/renames a file or directory. See [os.Rename].
	// Atomic on the same filesystem.
	Rename(oldpath, newpath string) error

	// --- Locking ---

	// Lock acquires an exclusive file lock.
	// Blocks until the lock is acquired or returns error on timeout.
	// Call [Locker.Close] to release the lock.
	//
	// Used for coordinating access between processes.
	Lock(path string) (Locker, error)
}

// Compile-time interface checks.
var _ File = (*os.File)(nil)
