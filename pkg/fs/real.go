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

// A passthrough wrapper for [os.ReadFile].
func (r *Real) ReadFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

// WriteFile is a passthrough wrapper for [os.WriteFile].
func (r *Real) WriteFile(path string, data []byte, perm os.FileMode) error {
	return os.WriteFile(path, data, perm)
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

// Compile-time interface check.
var _ FS = (*Real)(nil)
