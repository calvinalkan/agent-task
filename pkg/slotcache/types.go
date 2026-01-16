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
//
// The concrete implementation lives behind the slotcache_impl build tag.
type Cache struct{}

// Writer is a single-writer session returned by Cache.BeginWrite.
type Writer struct{}
