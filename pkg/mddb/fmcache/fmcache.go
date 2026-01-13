package fmcache

import "iter"

// Schema defines how values are encoded to bytes and decoded back.
type Schema[T any] struct {
	// Encode serializes a value to index bytes (fixed-size) and data bytes (variable-size).
	// Index bytes enable fast filtering without decoding the full value.
	Encode func(*T) (index, data []byte, err error)

	// Decode reconstructs a value from index and data bytes.
	Decode func(index, data []byte) (T, error)
}

// Cache is the typed wrapper around ByteCache.
//
// NOTE: This file intentionally contains a no-op stub implementation.
// The real implementation will be added via TDD and must match the spec model.
type Cache[T any] struct{}

// Open opens a cache file. Creates it if it doesn't exist.
func Open[T any](_ string, _ Schema[T]) (*Cache[T], error) {
	panic("not implemented")
}

// Close closes the cache.
func (*Cache[T]) Close() error {
	panic("not implemented")
}

// Len returns the number of entries.
func (*Cache[T]) Len() (int, error) {
	panic("not implemented")
}

// Get retrieves an entry by key.
func (*Cache[T]) Get(_ string) (Entry[T], bool, error) {
	panic("not implemented")
}

// Put adds or updates an entry.
func (*Cache[T]) Put(_ string, _ int64, _ T) error {
	panic("not implemented")
}

// Delete removes an entry. Returns true if it existed.
func (*Cache[T]) Delete(_ string) (bool, error) {
	panic("not implemented")
}

// FilterEntries iterates entries in key order and yields those for which match returns true.
func (*Cache[T]) FilterEntries(_ FilterOpts, _ func(Entry[T]) bool) (iter.Seq[Entry[T]], error) {
	panic("not implemented")
}

// AllEntries is a convenience wrapper for FilterEntries(..., matchAll).
func (c *Cache[T]) AllEntries(opts FilterOpts) (iter.Seq[Entry[T]], error) {
	return c.FilterEntries(opts, func(Entry[T]) bool { return true })
}

// Commit writes all changes to disk.
func (*Cache[T]) Commit() error {
	panic("not implemented")
}
