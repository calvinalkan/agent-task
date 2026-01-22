// Package slotcache provides a high-performance, mnap-based slot cache.
//
// slotcache is a throwaway cache optimized for fast reads and filtering.
// It is not a durable database - on corruption or version mismatch, delete
// and rebuild from your source of truth.
//
// # Basic Usage
//
//	cache, err := slotcache.Open(slotcache.Options{
//	    Path:         "/tmp/my.cache",
//	    KeySize:      16,
//	    IndexSize:    8,
//	    SlotCapacity: 10000,
//	})
//	if err != nil {
//	    // handle [ErrCorrupt]/[ErrIncompatible] by deleting and recreating
//	}
//	defer cache.Close()
//
//	// Read
//	entry, found, err := cache.Get(key)
//
//	// Write
//	w, err := cache.BeginWrite()
//	w.Put(key, revision, indexBytes)
//	w.Commit()
//	w.Close()
//
// # Concurrency
//
// slotcache uses a multi-reader, single-writer model:
//   - Read operations on [Cache] are safe for concurrent use
//   - Only one [Writer] may be active at a time (across all processes)
//   - Write operations on [Writer] are NOT thread-safe
//
// # Error Handling
//
// Errors fall into two categories:
//
// Rebuild errors ([ErrCorrupt], [ErrIncompatible]): Delete the cache file
// and recreate it from your source of truth.
//
// Transient errors ([ErrBusy]): Retry after a short delay.
package slotcache
