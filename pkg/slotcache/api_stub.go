//go:build !slotcache_impl

package slotcache

// This file provides a compile-time stub of the slotcache API.
//
// It is used when the real implementation has not yet been built (the
// slotcache_impl build tag is not enabled). Every method panics.

// Open opens or creates a cache file at the path specified in options.
func Open(_ Options) (*Cache, error) {
	panic("not implemented")
}

// Close closes the cache handle.
func (*Cache) Close() error {
	panic("not implemented")
}

// Len returns the number of live entries in the cache.
func (*Cache) Len() (int, error) {
	panic("not implemented")
}

// Get retrieves an entry by exact key.
func (*Cache) Get(_ []byte) (Entry, bool, error) {
	panic("not implemented")
}

// Scan iterates over all live entries.
func (*Cache) Scan(_ ScanOpts) (Seq, error) {
	panic("not implemented")
}

// ScanPrefix iterates over live entries matching the given prefix.
func (*Cache) ScanPrefix(_ []byte, _ ScanOpts) (Seq, error) {
	panic("not implemented")
}

// BeginWrite starts a new write session.
func (*Cache) BeginWrite() (*Writer, error) {
	panic("not implemented")
}

// Put buffers a put operation for the given key.
func (*Writer) Put(_ []byte, _ int64, _ []byte) error {
	panic("not implemented")
}

// Delete buffers a delete operation for the given key.
func (*Writer) Delete(_ []byte) (bool, error) {
	panic("not implemented")
}

// Commit applies all buffered operations atomically.
func (*Writer) Commit() error {
	panic("not implemented")
}

// Abort discards all buffered operations.
func (*Writer) Abort() error {
	panic("not implemented")
}

// Close is an alias for Abort.
func (*Writer) Close() error {
	panic("not implemented")
}
