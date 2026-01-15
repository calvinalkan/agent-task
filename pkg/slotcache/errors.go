package slotcache

import "errors"

// Error classification codes.
//
// Implementations MAY wrap these errors with additional context.
// Tests and callers MUST classify errors using errors.Is.
var (
	// ErrCorrupt indicates the cache file is corrupted (rebuild-class).
	ErrCorrupt = errors.New("slotcache: corrupt")
	// ErrIncompatible indicates the cache file format is incompatible.
	ErrIncompatible = errors.New("slotcache: incompatible")

	// ErrBusy indicates a conflicting operation is in progress (operational).
	ErrBusy              = errors.New("slotcache: busy")
	ErrInvalidInput      = errors.New("slotcache: invalid input")
	ErrInvalidKey        = errors.New("slotcache: invalid key")
	ErrInvalidIndex      = errors.New("slotcache: invalid index")
	ErrInvalidPrefix     = errors.New("slotcache: invalid prefix")
	ErrInvalidScanOpts   = errors.New("slotcache: invalid scan options")
	ErrOffsetOutOfBounds = errors.New("slotcache: offset out of bounds")
	ErrFull              = errors.New("slotcache: full")
	ErrWriteback         = errors.New("slotcache: writeback")
	ErrClosed            = errors.New("slotcache: closed")
)
