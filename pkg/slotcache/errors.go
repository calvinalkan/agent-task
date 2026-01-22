package slotcache

import "errors"

// Sentinel errors returned by slotcache operations.
//
// Callers should use [errors.Is] to check error types:
//
//	if errors.Is(err, slotcache.ErrCorrupt) {
//	    os.Remove(path)
//	    // recreate cache
//	}
var (
	// ErrCorrupt indicates the cache file is damaged.
	//
	// Recovery: delete and recreate the cache.
	ErrCorrupt = errors.New("slotcache: corrupt")

	// ErrIncompatible indicates a format or configuration mismatch.
	//
	// This occurs when the file was created with different options (KeySize,
	// IndexSize, UserVersion, SlotCapacity, OrderedKeys) than those provided
	// to [Open], or when the file format version or flags are not recognized.
	//
	// Recovery: delete and recreate the cache with matching options.
	ErrIncompatible = errors.New("slotcache: incompatible")

	// ErrBusy indicates a writer is active or lock contention occurred.
	//
	// Recovery: retry after a short delay with backoff.
	ErrBusy = errors.New("slotcache: busy")

	// ErrFull indicates the cache capacity has been exhausted.
	//
	// Deleted entries do not free capacity (append-only storage).
	//
	// Recovery: recreate the cache with a larger [Options.SlotCapacity].
	ErrFull = errors.New("slotcache: full")

	// ErrClosed indicates the [Cache] or [Writer] has already been closed.
	//
	// This is a programming error.
	ErrClosed = errors.New("slotcache: closed")

	// ErrWriteback indicates a flush failed during commit.
	//
	// Changes are visible to other processes but durability is not guaranteed.
	// Only returned when using [WritebackSync] mode.
	//
	// Recovery: if durability is required, recreate the cache.
	ErrWriteback = errors.New("slotcache: writeback failed")

	// ErrInvalidInput indicates invalid arguments were provided.
	//
	// Common causes: wrong key/index length, malformed [Prefix],
	// invalid range bounds, negative Offset/Limit.
	//
	// This is a programming error.
	ErrInvalidInput = errors.New("slotcache: invalid input")

	// ErrUnordered indicates an ordered operation was attempted on a
	// cache not created with [Options.OrderedKeys].
	//
	// Recovery: use a different API, or recreate with OrderedKeys enabled.
	ErrUnordered = errors.New("slotcache: unordered")

	// ErrOutOfOrderInsert indicates an insert violated the ordering constraint.
	//
	// In ordered-keys mode, new keys must be >= the last inserted key.
	//
	// Recovery: rebuild the cache with properly sorted data.
	ErrOutOfOrderInsert = errors.New("slotcache: out of order insert")

	// ErrOutOfOrder is a deprecated alias for [ErrOutOfOrderInsert].
	//
	// Deprecated: Use [ErrOutOfOrderInsert] instead.
	ErrOutOfOrder = ErrOutOfOrderInsert

	// ErrInvalidated indicates the cache has been explicitly invalidated.
	//
	// Once invalidated, all operations on the cache (reads, writes, new opens)
	// return this error. Invalidation is terminal and cannot be undone.
	//
	// Recovery: delete and recreate the cache.
	ErrInvalidated = errors.New("slotcache: invalidated")

	// ErrBufferFull indicates the writer's in-memory buffer is full.
	//
	// Each [Writer] can buffer a limited number of operations before commit.
	// This error is returned when that limit is reached.
	//
	// Recovery: call [Writer.Commit] to flush buffered operations, then
	// continue with a new write session via [Cache.BeginWrite].
	ErrBufferFull = errors.New("slotcache: buffer full")
)
