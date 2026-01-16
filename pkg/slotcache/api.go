// Package slotcache provides a slot-based cache with append-only semantics.
package slotcache

// Options configure opening or creating a cache file.
//
// This is intentionally a small subset for the test harness.
// The real implementation may expose additional options.
type Options struct {
	Path string

	KeySize      int
	IndexSize    int
	UserVersion  uint64
	SlotCapacity uint64
}

// Entry is an observable live slot record returned by Get/Scan.
type Entry struct {
	Key      []byte
	Revision int64
	Index    []byte
}

// ScanOpts control Scan / ScanPrefix pagination and order.
//
// - Reverse: iterate newest-to-oldest slot order.
// - Offset: number of matching entries to skip.
// - Limit: max entries to return (0 means no limit).
type ScanOpts struct {
	Reverse bool
	Offset  int
	Limit   int
}

// Seq is the iterator type returned by Scan and ScanPrefix.
//
// It matches the shape of iter.Seq[T] so callers can use slices.Collect:
//
//	slices.Collect(iter.Seq[slotcache.Entry](seq))
//
// The slotcache package avoids depending on iter directly.
type Seq func(yield func(Entry) bool)

// Cache is the public cache handle.
type Cache interface {
	// Close closes the cache handle.
	Close() error

	// Len returns the number of live entries in the cache.
	Len() (int, error)

	// Get retrieves an entry by exact key.
	Get(key []byte) (Entry, bool, error)

	// Scan iterates over all live entries.
	Scan(opts ScanOpts) (Seq, error)

	// ScanPrefix iterates over live entries matching the given prefix.
	ScanPrefix(prefix []byte, opts ScanOpts) (Seq, error)

	// BeginWrite starts a new write session.
	BeginWrite() (Writer, error)
}

// Writer is a single-writer session returned by Cache.BeginWrite.
type Writer interface {
	// Put buffers a put operation for the given key.
	Put(key []byte, revision int64, index []byte) error

	// Delete buffers a delete operation for the given key.
	Delete(key []byte) (bool, error)

	// Commit applies all buffered operations atomically.
	Commit() error

	// Abort discards all buffered operations.
	Abort() error

	// Close is an alias for Abort.
	Close() error
}
