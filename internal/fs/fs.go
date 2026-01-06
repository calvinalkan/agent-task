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
//	fsys := fs.NewReal()
//	f, err := fsys.Open("config.json")
//	if err != nil {
//	    return err
//	}
//	defer f.Close()
//
//	// Works with all stdlib io functions:
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
// Implementations must be safe for concurrent use by multiple goroutines.
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

// FS defines filesystem operations for reading, writing, and managing files.
//
// Two implementations are provided:
//   - [Real]: production use, wraps [os] package
//   - [Chaos]: testing use, injects random failures
//
// All methods mirror their [os] package equivalents but can be intercepted
// for testing with fault injection.
//
// Paths use OS semantics (like the os package and path/filepath), not the slash-separated
// paths used by the standard library io/fs package.
//
// Some implementations expose additional capabilities beyond [FS] (for example
// [Real.WriteFileAtomic]).
//
// This interface intentionally does not include a WriteFile helper. A naive
// "write file" convenience typically spans multiple syscalls (open/truncate,
// write, close) and is not atomic or durable: errors or crashes can leave a
// partially written or empty file, and it doesn't let callers control
// durability via [File.Sync]. Prefer explicit [FS.OpenFile] + [File.Sync], or
// an atomic replace helper like [Real.WriteFileAtomic].
//
// Implementations must be safe for concurrent use by multiple goroutines.
type FS interface {
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

	// ReadFile reads an entire file into memory. See [os.ReadFile].
	// For large files, prefer [FS.Open] with streaming reads.
	ReadFile(path string) ([]byte, error)

	// ReadDir reads a directory and returns its entries. See [os.ReadDir].
	// Entries are sorted by name.
	ReadDir(path string) ([]os.DirEntry, error)

	// MkdirAll creates a directory and all parents. See [os.MkdirAll].
	// No error if the directory already exists.
	MkdirAll(path string, perm os.FileMode) error

	// Stat returns file info. See [os.Stat].
	// Returns [os.ErrNotExist] if file doesn't exist.
	Stat(path string) (os.FileInfo, error)

	// Exists reports whether a file or directory exists.
	// Returns (false, nil) if not found, (false, err) on other errors.
	Exists(path string) (bool, error)

	// Remove deletes a file or empty directory. See [os.Remove].
	// For recursive deletion, use [FS.RemoveAll].
	Remove(path string) error

	// RemoveAll deletes a path and any children. See [os.RemoveAll].
	// No error if path doesn't exist.
	RemoveAll(path string) error

	// Rename moves/renames a file or directory. See [os.Rename].
	// Atomic on the same filesystem.
	Rename(oldpath, newpath string) error
}

// Compile-time interface checks.
var _ File = (*os.File)(nil)
