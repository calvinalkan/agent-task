package fmcache

import (
	"errors"
	"fmt"
)

var (
	// ErrClosed is returned when operating on a closed cache.
	ErrClosed = errors.New("fmcache: closed")

	// ErrInvalidKey is returned when a key is invalid (empty, contains NUL, too long, etc.).
	//
	// Implementations may return an error that wraps ErrInvalidKey to provide more detail.
	ErrInvalidKey = errors.New("fmcache: invalid key")

	// ErrIndexSizeMismatch is returned when encoding produces a different index size
	// than previous entries. All entries must have the same index size.
	ErrIndexSizeMismatch = errors.New("fmcache: index size mismatch")

	// ErrInvalidFilterOpts is returned when filtering methods are called with
	// negative Offset or Limit.
	ErrInvalidFilterOpts = errors.New("fmcache: invalid filter opts")

	// ErrOffsetOutOfBounds is returned when filtering methods are called with an
	// Offset beyond the number of matches.
	// Treat as a paging error.
	ErrOffsetOutOfBounds = errors.New("fmcache: offset out of bounds")
)

// InvalidKeyError provides details about why a key is invalid.
//
// Use errors.Is(err, ErrInvalidKey) to match this error.
type InvalidKeyError struct {
	Reason string
}

func (e InvalidKeyError) Error() string {
	if e.Reason == "" {
		return ErrInvalidKey.Error()
	}

	return fmt.Sprintf("%s: %s", ErrInvalidKey.Error(), e.Reason)
}

func (InvalidKeyError) Unwrap() error { return ErrInvalidKey }
