package fs

import (
	"os"
)

// Real implements [FS] using the real filesystem.
//
// All methods are pure passthroughs to the [os] package with identical
// behavior and error semantics. The only exceptions are [Real.Exists] which
// wraps [os.Stat].
type Real struct{}

// NewReal returns a new [Real] filesystem.
func NewReal() *Real {
	return &Real{}
}

// Open is a passthrough wrapper for [os.Open].
func (*Real) Open(path string) (File, error) {
	return os.Open(path)
}

// Create is a passthrough wrapper for [os.Create].
func (*Real) Create(path string) (File, error) {
	return os.Create(path)
}

// OpenFile is a passthrough wrapper for [os.OpenFile].
func (*Real) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	return os.OpenFile(path, flag, perm)
}

// ReadFile is a passthrough wrapper for [os.ReadFile].
func (*Real) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile is a passthrough wrapper for [os.WriteFile].
func (*Real) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
}

// --- Directory Operations ---

// ReadDir is a passthrough wrapper for [os.ReadDir].
func (*Real) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

// MkdirAll is a passthrough wrapper for [os.MkdirAll].
func (*Real) MkdirAll(path string, perm os.FileMode) error {
	return os.MkdirAll(path, perm)
}

// --- Metadata ---

// Stat is a passthrough wrapper for [os.Stat].
func (*Real) Stat(path string) (os.FileInfo, error) {
	return os.Stat(path)
}

// Exists checks if a file exists using [os.Stat].
// Returns (true, nil) if the file exists, (false, nil) if it does not,
// or (false, err) for other errors.
func (*Real) Exists(path string) (bool, error) {
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

// Remove is a passthrough wrapper for [os.Remove].
func (*Real) Remove(path string) error {
	return os.Remove(path)
}

// RemoveAll is a passthrough wrapper for [os.RemoveAll].
func (*Real) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

// Rename is a passthrough wrapper for [os.Rename].
func (*Real) Rename(oldpath, newpath string) error {
	return os.Rename(oldpath, newpath)
}

// Compile-time interface check.
var _ FS = (*Real)(nil)
