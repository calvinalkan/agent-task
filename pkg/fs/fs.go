// Package fs provides filesystem abstractions for testing and fault injection.
//
// The main types are:
//   - [FS]: interface for filesystem operations
//   - [File]: interface for open files (satisfied by [os.File])
//   - [Real]: production implementation using [os] package
//   - [Chaos]: testing implementation that injects random failures
//   - [Crash]: testing implementation that simulates crash consistency
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

// File represents an OS-backed open file descriptor.
//
// This interface is satisfied by [os.File] and can be used with all
// standard library functions that accept [io.Reader], [io.Writer],
// [io.Seeker], or [io.Closer].
//
// The intent is os-like behavior: implementations must behave like [os.File],
// including that [File.Fd] returns a valid OS file descriptor usable with
// syscalls (for example [syscall.Flock]) until the file is closed.
//
// Note: [File] includes [io.Writer] even for read-only handles. Like [os.File],
// implementations should return an error from Write when the file wasn't opened
// for writing.
//
// Implementations must be safe for concurrent use by multiple goroutines.
//
// Example:
//
//	fsys := fs.NewReal()
//	f, _ := fsys.Open("data.txt")
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

	// Chmod changes the mode of the file. See [os.File.Chmod].
	Chmod(mode os.FileMode) error
}

// FS defines filesystem operations for reading, writing, and managing files.
//
// Implementations in this package include:
//   - [Real]: production use, wraps [os] package
//   - [Chaos]: testing use, injects random failures
//   - [Crash]: testing use, simulates crash consistency
//
// All methods mirror their [os] package equivalents but can be intercepted
// for testing with fault injection.
//
// Paths use OS semantics (like the os package and path/filepath), not the slash-separated
// paths used by the standard library io/fs package.
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

	// WriteFile writes data to a file, creating it if necessary. See [os.WriteFile].
	// The file is created with the specified permissions (before umask) if it
	// doesn't exist, or truncated if it does.
	//
	// Note: WriteFile is not atomic or durable. Errors or crashes can leave a
	// partially written or empty file. For durability, use [FS.OpenFile] with
	// explicit [File.Sync] before [File.Close].
	WriteFile(path string, data []byte, perm os.FileMode) error

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
