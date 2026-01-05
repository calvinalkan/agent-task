package fs

import (
	"errors"
	iofs "io/fs"
	"sync"
)

// InjectedError marks an error as intentionally injected by [Chaos].
//
// It wraps the underlying error so errors.Is/As continue to work.
//
// Note: For errno-style errors, [Chaos] returns a plain *fs.PathError with a
// syscall.Errno in PathError.Err so os.IsNotExist/os.IsPermission keep working.
// Those injected *fs.PathError values are tracked separately so IsInjected can
// still distinguish injected vs real OS errors in tests.
//
// All methods panic if the receiver or Err is nil.
type InjectedError struct {
	Err error
}

// Error returns the underlying error's message. Panics if e or e.Err is nil.
func (e *InjectedError) Error() string {
	return e.Err.Error()
}

// Unwrap returns the underlying error. Panics if e is nil.
func (e *InjectedError) Unwrap() error {
	return e.Err
}

// Timeout reports whether the underlying error is a timeout.
// Panics if e is nil.
func (e *InjectedError) Timeout() bool {
	t, ok := e.Err.(timeout)

	return ok && t.Timeout()
}

// IsInjected reports whether err (or any wrapped error) was injected by [Chaos].
// Returns false if err is nil.
func IsInjected(err error) bool {
	if err == nil {
		return false
	}

	var injected *InjectedError
	if errors.As(err, &injected) {
		return true
	}

	var pathErr *iofs.PathError
	if errors.As(err, &pathErr) {
		_, ok := injectedPathErrors.Load(pathErr)

		return ok
	}

	return false
}

// --- Private api ---

type timeout interface {
	Timeout() bool
}

var injectedPathErrors sync.Map // map[*fs.PathError]struct{}

// markInjectedPathError registers a PathError as injected. Panics if err is nil.
func markInjectedPathError(err *iofs.PathError) {
	injectedPathErrors.Store(err, struct{}{})
}

// inject wraps err in an InjectedError. Panics if err is nil.
// If err is already injected, returns it unchanged.
func inject(err error) error {
	if IsInjected(err) {
		return err
	}

	return &InjectedError{Err: err}
}
