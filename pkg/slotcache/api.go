// Package slotcache provides a high-performance, file-backed slot cache.
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
	// This occurs when the file was created with different options
	// (KeySize, IndexSize, UserVersion, etc.) than those provided to [Open].
	//
	// Recovery: delete and recreate the cache.
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
)

// WritebackMode controls durability guarantees for [Writer.Commit].
type WritebackMode int

const (
	// WritebackNone provides no durability guarantees.
	//
	// Changes are visible to other processes immediately but may be lost
	// on power failure. This is the default and fastest mode.
	WritebackNone WritebackMode = iota

	// WritebackSync ensures changes are durable before Commit returns.
	//
	// After a crash, the cache is either in its previous state or detected
	// as corrupt (triggering [ErrCorrupt] on next [Open]).
	WritebackSync
)

// Options configures opening or creating a cache file.
type Options struct {
	// Path is the filesystem path to the cache file.
	//
	// Required. A lock file may also be created at Path+".lock".
	Path string

	// KeySize is the fixed size in bytes for all keys.
	//
	// Must be >= 1. All keys must have exactly this length.
	KeySize int

	// IndexSize is the fixed size in bytes for index data per entry.
	//
	// May be 0 if no index data is needed.
	IndexSize int

	// UserVersion is a caller-defined version for schema compatibility.
	//
	// If the persisted value doesn't match, [Open] returns [ErrIncompatible].
	// Increment this when your index byte encoding changes.
	UserVersion uint64

	// SlotCapacity is the maximum number of entries the cache can hold.
	//
	// Must be >= 1. Fixed at creation time.
	// When exhausted, [Writer.Commit] returns [ErrFull].
	SlotCapacity uint64

	// OrderedKeys enables ordered-keys mode.
	//
	// When enabled:
	//   - New inserts must be in non-decreasing key order
	//   - [Cache.ScanRange] becomes available
	//   - Commits that violate ordering return [ErrOutOfOrderInsert]
	//
	// Fixed at creation time.
	OrderedKeys bool

	// Writeback controls durability guarantees for commit.
	//
	// Default is [WritebackNone].
	Writeback WritebackMode

	// DisableLocking disables interprocess writer locking.
	//
	// When true, no lock file is used. The caller MUST provide equivalent
	// external synchronization.
	//
	// Use only when slotcache is embedded inside another component that
	// already coordinates access.
	DisableLocking bool
}

// Entry represents an entry returned by read operations.
//
// All byte slices are copies that the caller owns and may retain.
type Entry struct {
	// Key is the entry's key bytes (length equals [Options.KeySize]).
	Key []byte

	// Revision is an opaque int64 provided by the caller during [Writer.Put].
	//
	// Typically used to store mtime or a generation number for staleness detection.
	Revision int64

	// Index is the entry's index bytes (length equals [Options.IndexSize]).
	//
	// May be nil if IndexSize is 0.
	Index []byte
}

// ScanOptions controls scan iteration behavior.
type ScanOptions struct {
	// Filter is called for each candidate entry. Only entries where Filter
	// returns true are included in results. If nil, all entries match.
	//
	// The Entry passed to Filter contains borrowed slices that are only valid
	// for the duration of the call. Do not retain references to Key or Index.
	//
	// Offset and Limit apply after filtering.
	Filter func(Entry) bool

	// Reverse iterates in descending order.
	//
	// For regular scans: newest-to-oldest insertion order.
	// For range scans: highest-to-lowest key order.
	Reverse bool

	// Offset is the number of matching entries to skip.
	//
	// Must be >= 0. If Offset exceeds matches, returns empty result.
	Offset int

	// Limit is the maximum number of entries to return.
	//
	// Must be >= 0. Zero means no limit.
	Limit int
}

// Prefix describes a bit-granular prefix match.
//
// Designed for disambiguation UIs where short IDs use encodings like base32
// (5 bits per character) that don't align to byte boundaries.
//
// # Matching Rules
//
// If Bits is 0, matches len(Bytes) complete bytes at Offset.
//
// If Bits > 0, matches exactly that many bits (MSB-first).
// Bytes must have length ceil(Bits / 8).
//
// # Example
//
//	// Match 10 bits starting at byte 6
//	Prefix{
//	    Offset:  6,
//	    Bits: 10,
//	    Bytes:      []byte{0xAB, 0xC0}, // matches 0xAB then 0b11xxxxxx
//	}
type Prefix struct {
	// Offset is the byte offset within the key where matching starts.
	Offset int

	// Bits is the number of bits to match (0 = byte-aligned).
	Bits int

	// Bytes contains the prefix pattern to match.
	Bytes []byte
}

// Cache is a read-only handle to an open cache file.
//
// All read methods are safe for concurrent use by multiple goroutines.
// To modify the cache, acquire a [Writer] via [Cache.BeginWrite].
//
// If a writer commits while a read is in progress, the read retries
// automatically. If retries are exhausted, [ErrBusy] is returned.
// Scan-style methods capture a stable snapshot before returning results.
// If a stable snapshot cannot be acquired after bounded retries, they return
// [ErrBusy] and no results.
type Cache interface {
	// Close releases all resources associated with the cache.
	//
	// After Close, all other methods return [ErrClosed].
	// Close is idempotent; subsequent calls are no-ops.
	Close() error

	// Len returns the number of live entries in the cache.
	//
	// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt].
	Len() (int, error)

	// Get retrieves an entry by exact key.
	//
	// Returns (entry, true, nil) if found, ([Entry]{}, false, nil) if not found.
	//
	// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput].
	Get(key []byte) (Entry, bool, error)

	// Scan returns all live entries in insertion order.
	//
	// Scan captures a stable snapshot before returning. If snapshot acquisition
	// fails, it returns [ErrBusy] and no results.
	//
	// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput].
	Scan(opts ScanOptions) ([]Entry, error)

	// ScanPrefix returns live entries matching the given byte prefix at offset 0.
	//
	// Equivalent to ScanMatch([Prefix]{Bytes: prefix}, opts).
	// ScanPrefix captures a stable snapshot before returning. If snapshot
	// acquisition fails, it returns [ErrBusy] and no results.
	//
	// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput].
	ScanPrefix(prefix []byte, opts ScanOptions) ([]Entry, error)

	// ScanMatch returns live entries matching a [Prefix].
	//
	// This scans all entries; it cannot use the key index for acceleration.
	// ScanMatch captures a stable snapshot before returning. If snapshot
	// acquisition fails, it returns [ErrBusy] and no results.
	//
	// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput].
	ScanMatch(spec Prefix, opts ScanOptions) ([]Entry, error)

	// ScanRange returns live entries in the half-open key range [start, end).
	//
	// Requires [Options.OrderedKeys]. Either bound may be nil (unbounded).
	// Bounds shorter than KeySize are right-padded with 0x00.
	// ScanRange captures a stable snapshot before returning. If snapshot
	// acquisition fails, it returns [ErrBusy] and no results.
	//
	// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput], [ErrUnordered].
	ScanRange(start, end []byte, opts ScanOptions) ([]Entry, error)

	// BeginWrite starts a new write session.
	//
	// Only one writer may be active at a time.
	//
	// Possible errors: [ErrClosed], [ErrBusy].
	BeginWrite() (Writer, error)
}

// Writer is a buffered write session for modifying the cache.
//
// Operations are buffered in memory and applied atomically on [Writer.Commit].
// If the same key is modified multiple times, the last operation wins.
//
// Writer methods are NOT thread-safe. Always call [Writer.Close] to release resources.
type Writer interface {
	// Put stages an upsert operation.
	//
	// If the key exists, the entry is updated. Otherwise, a new entry is
	// allocated at commit time.
	//
	// Possible errors: [ErrClosed], [ErrInvalidInput].
	Put(key []byte, revision int64, index []byte) error

	// Delete stages a deletion.
	//
	// Returns true if the key exists (considering buffered ops), false otherwise.
	//
	// Possible errors: [ErrClosed], [ErrInvalidInput].
	Delete(key []byte) (existed bool, err error)

	// Commit applies all buffered operations atomically.
	//
	// After success, changes are visible to readers. If [WritebackSync] is
	// enabled, changes are also durable.
	//
	// After Commit, further operations return [ErrClosed].
	//
	// Possible errors: [ErrClosed], [ErrFull], [ErrOutOfOrderInsert], [ErrWriteback], [ErrCorrupt].
	Commit() error

	// Close releases resources and discards uncommitted changes.
	//
	// Close is idempotent. Always call Close, even after [Writer.Commit].
	Close() error
}
