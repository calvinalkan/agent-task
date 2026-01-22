package slotcache

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// Cache is a handle to an open cache file.
//
// All read methods are safe for concurrent use by multiple goroutines.
// To modify the cache, acquire a [Writer] via [Cache.Writer].
//
// If a writer commits while a read is in progress, the read retries
// automatically. If retries are exhausted, [ErrBusy] is returned.
// Scan-style methods capture a stable snapshot before returning results.
// If a stable snapshot cannot be acquired after bounded retries, they return
// [ErrBusy] and no results.
//
// A Cache must be obtained via [Open]; the zero value is not usable.
type Cache struct {
	_ [0]func() // prevent external construction

	// mu protects isClosed. See "Locking architecture" comment in lock.go.
	// RWMutex because isClosed is read frequently (every operation) but
	// written rarely (only on Close).
	mu sync.RWMutex

	fd       int    // file descriptor
	data     []byte // mmap'd file data
	fileSize int64  // total file size

	// Cached immutable config from header
	keySize       uint32
	indexSize     uint32
	slotSize      uint32
	slotCapacity  uint64
	userVersion   uint64
	slotsOffset   uint64
	bucketsOffset uint64
	bucketCount   uint64
	orderedKeys   bool

	// File identity for registry entry coordination
	identity      fileIdentity
	registryEntry *fileRegistryEntry

	disableLocking bool
	path           string
	writeback      WritebackMode

	// State
	isClosed bool
}

// Close releases all resources associated with the cache.
//
// After Close, all other methods return [ErrClosed].
// Close is idempotent; subsequent calls are no-ops.
func (c *Cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isClosed {
		return nil
	}

	// Check if this cache owns an active writer.
	c.registryEntry.mu.RLock()
	hasActiveWriter := c.registryEntry.activeWriter == c
	c.registryEntry.mu.RUnlock()

	if hasActiveWriter {
		return ErrBusy
	}

	c.isClosed = true

	if c.data != nil {
		_ = syscall.Munmap(c.data)
		c.data = nil
	}

	if c.fd >= 0 {
		_ = syscall.Close(c.fd)
		c.fd = -1
	}

	// Release our reference to the registry entry, allowing it to be
	// pruned from fileRegistry when the last handle for this file closes.
	releaseRegistryEntry(c.identity)

	return nil
}

// Len returns the number of live entries in the cache.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidated].
func (c *Cache) Len() (int, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return 0, ErrClosed
	}

	for attempt := range readMaxRetries {
		readBackoff(attempt)

		c.registryEntry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registryEntry.mu.RUnlock()

			continue
		}

		// Check for invalidation under stable generation.
		state := binary.LittleEndian.Uint32(c.data[offState:])
		if state == stateInvalidated {
			c.registryEntry.mu.RUnlock()

			return 0, ErrInvalidated
		}

		highwater, hwErr := c.safeSlotHighwater(g1)
		if hwErr != nil {
			c.registryEntry.mu.RUnlock()

			if errors.Is(hwErr, errOverlap) {
				continue
			}

			return 0, hwErr
		}

		count := c.readLiveCount()
		if count > highwater {
			invErr := c.checkInvariantViolation(g1)
			c.registryEntry.mu.RUnlock()

			if errors.Is(invErr, errOverlap) {
				continue
			}

			return 0, invErr
		}

		result, convErr := uint64ToIntChecked(count)
		if convErr != nil {
			invErr := c.checkInvariantViolation(g1)
			c.registryEntry.mu.RUnlock()

			if errors.Is(invErr, errOverlap) {
				continue
			}

			return 0, invErr
		}

		g2 := c.readGeneration()
		c.registryEntry.mu.RUnlock()

		if g1 == g2 {
			return result, nil
		}
	}

	return 0, ErrBusy
}

// Get retrieves an entry by exact key.
//
// Returns (entry, true, nil) if found, ([Entry]{}, false, nil) if not found.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput], [ErrInvalidated].
func (c *Cache) Get(key []byte) (Entry, bool, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return Entry{}, false, ErrClosed
	}

	if len(key) != int(c.keySize) {
		return Entry{}, false, fmt.Errorf("key length %d != key_size %d: %w", len(key), c.keySize, ErrInvalidInput)
	}

	for attempt := range readMaxRetries {
		readBackoff(attempt)

		c.registryEntry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registryEntry.mu.RUnlock()

			continue
		}

		// Check for invalidation under stable generation.
		state := binary.LittleEndian.Uint32(c.data[offState:])
		if state == stateInvalidated {
			c.registryEntry.mu.RUnlock()

			return Entry{}, false, ErrInvalidated
		}

		entry, found, err := c.lookupKey(key, g1)
		g2 := c.readGeneration()
		c.registryEntry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		if err != nil {
			// errOverlap means we detected an impossible invariant but generation
			// changed mid-read - treat as overlap and retry.
			if errors.Is(err, errOverlap) {
				continue
			}

			return Entry{}, false, err
		}

		return entry, found, nil
	}

	return Entry{}, false, ErrBusy
}

// Writer starts a new write session.
//
// Only one writer may be active at a time.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrInvalidated].
func (c *Cache) Writer() (*Writer, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}

	// Check for invalidation before acquiring writer.
	// Note: This reads without the seqlock since we're about to acquire
	// exclusive access anyway. A concurrent invalidation would be caught
	// by the in-process writer guard.
	state := binary.LittleEndian.Uint32(c.data[offState:])
	if state == stateInvalidated {
		return nil, ErrInvalidated
	}

	// Check in-process writer guard.
	c.registryEntry.mu.Lock()

	if c.registryEntry.activeWriter != nil {
		c.registryEntry.mu.Unlock()

		return nil, ErrBusy
	}

	c.registryEntry.activeWriter = c
	c.registryEntry.mu.Unlock()

	// Acquire cross-process lock if enabled.
	var lock *fs.Lock

	if !c.disableLocking {
		var err error

		lock, err = tryAquireWriteLock(c.path)
		if err != nil {
			c.registryEntry.mu.Lock()
			c.registryEntry.activeWriter = nil
			c.registryEntry.mu.Unlock()

			return nil, err
		}
	}

	wr := &Writer{
		cache:       c,
		bufferedOps: nil,
		lock:        lock,
		// isClosed is zero-value (false) by default for atomic.Bool
	}

	return wr, nil
}

// Invalidate marks the cache as permanently unusable.
//
// After invalidation, all operations on this handle and any future
// [Open] calls on the same file return [ErrInvalidated].
// Invalidation is atomic and durable (in WritebackSync mode).
//
// Calling Invalidate on an already-invalidated cache is a no-op.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrWriteback].
func (c *Cache) Invalidate() error {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return ErrClosed
	}

	// Check for active in-process writer.
	c.registryEntry.mu.Lock()

	if c.registryEntry.activeWriter != nil {
		c.registryEntry.mu.Unlock()

		return ErrBusy
	}

	// Acquire in-process guard (same as BeginWrite).
	c.registryEntry.activeWriter = c
	c.registryEntry.mu.Unlock()

	// Acquire cross-process lock if enabled.
	var lock *fs.Lock

	if !c.disableLocking {
		var err error

		lock, err = tryAquireWriteLock(c.path)
		if err != nil {
			// Release in-process guard on failure.
			c.registryEntry.mu.Lock()
			c.registryEntry.activeWriter = nil
			c.registryEntry.mu.Unlock()

			return err
		}
	}

	// Perform invalidation under the lock.
	c.registryEntry.mu.Lock()

	// Check if already invalidated (idempotent).
	state := binary.LittleEndian.Uint32(c.data[offState:])
	if state == stateInvalidated {
		c.registryEntry.mu.Unlock()

		// Release resources.
		releaseWriteLock(lock)
		c.registryEntry.mu.Lock()
		c.registryEntry.activeWriter = nil
		c.registryEntry.mu.Unlock()

		return nil
	}

	syncMode := c.writeback == WritebackSync

	var msyncFailed bool

	// Step 1: Publish odd generation.
	oldGen := c.readGeneration()
	newOddGen := oldGen + 1
	atomicStoreUint64(c.data[offGeneration:], newOddGen)

	// Step 2 (WritebackSync): msync header to ensure odd generation is on disk.
	if syncMode {
		err := msyncRange(c.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	// Step 3: Set state=INVALIDATED.
	binary.LittleEndian.PutUint32(c.data[offState:], stateInvalidated)

	// Step 4: Recompute header CRC.
	crc := computeHeaderCRC(c.data[:slc1HeaderSize])
	binary.LittleEndian.PutUint32(c.data[offHeaderCRC32C:], crc)

	// Step 5 (WritebackSync): msync header to ensure state + CRC are on disk.
	if syncMode {
		err := msyncRange(c.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	// Step 6: Publish even generation.
	newEvenGen := newOddGen + 1
	atomicStoreUint64(c.data[offGeneration:], newEvenGen)

	// Step 7 (WritebackSync): msync header to ensure even generation is on disk.
	if syncMode {
		err := msyncRange(c.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	c.registryEntry.mu.Unlock()

	// Release cross-process lock.
	releaseWriteLock(lock)

	// Release in-process guard.
	c.registryEntry.mu.Lock()
	c.registryEntry.activeWriter = nil
	c.registryEntry.mu.Unlock()

	// Per spec: ErrWriteback indicates changes are visible but durability
	// is not guaranteed. We still return nil for the invalidation itself
	// since the state change is visible. The caller can check durability
	// separately if needed.
	if msyncFailed {
		return ErrWriteback
	}

	return nil
}

// UserDataSize is the fixed size of the caller-owned data region in the header.
const UserDataSize = 64

// UserHeader contains caller-owned metadata stored in the cache file header.
//
// These fields are opaque to slotcache and reserved for caller use.
// Changes to user header fields are published atomically with [Writer.Commit].
type UserHeader struct {
	// Flags is a caller-owned 64-bit field for arbitrary use.
	//
	// Common uses: feature flags, schema version, cache metadata.
	Flags uint64

	// Data is a caller-owned 64-byte region for arbitrary use.
	//
	// Common uses: checksums, timestamps, small metadata blobs.
	Data [UserDataSize]byte
}

// UserHeader returns the caller-owned header metadata.
//
// The returned [UserHeader] is a snapshot; subsequent writes do not
// affect it. Use [Writer.SetUserHeaderFlags] and [Writer.SetUserHeaderData]
// to modify.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrInvalidated].
func (c *Cache) UserHeader() (UserHeader, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return UserHeader{}, ErrClosed
	}

	for attempt := range readMaxRetries {
		readBackoff(attempt)

		c.registryEntry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registryEntry.mu.RUnlock()

			continue
		}

		// Check state under stable generation.
		state := binary.LittleEndian.Uint32(c.data[offState:])
		if state == stateInvalidated {
			c.registryEntry.mu.RUnlock()

			return UserHeader{}, ErrInvalidated
		}

		// Read user header fields.
		userFlags := binary.LittleEndian.Uint64(c.data[offUserFlags:])

		var userData [UserDataSize]byte

		copy(userData[:], c.data[offUserData:offUserData+UserDataSize])

		g2 := c.readGeneration()
		c.registryEntry.mu.RUnlock()

		if g1 == g2 {
			return UserHeader{
				Flags: userFlags,
				Data:  userData,
			}, nil
		}
	}

	return UserHeader{}, ErrBusy
}

// Generation returns the current generation counter.
//
// The generation is incremented on each successful commit. Callers can
// use this for cheap change detection without reading entry data.
//
// Returns a stable even generation (never an odd in-progress value).
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrInvalidated].
func (c *Cache) Generation() (uint64, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return 0, ErrClosed
	}

	for attempt := range readMaxRetries {
		readBackoff(attempt)

		c.registryEntry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registryEntry.mu.RUnlock()

			continue
		}

		// Check state under stable generation.
		state := binary.LittleEndian.Uint32(c.data[offState:])
		if state == stateInvalidated {
			c.registryEntry.mu.RUnlock()

			return 0, ErrInvalidated
		}

		g2 := c.readGeneration()
		c.registryEntry.mu.RUnlock()

		if g1 == g2 {
			return g1, nil
		}
	}

	return 0, ErrBusy
}

// readGeneration reads the generation counter atomically.
// Uses atomic 64-bit load per spec requirement for cross-process seqlock.
func (c *Cache) readGeneration() uint64 {
	return atomicLoadUint64(c.data[offGeneration:])
}

// errOverlap is an internal sentinel indicating that an impossible invariant was
// detected but generation changed (or became odd), meaning the read overlapped with
// a concurrent write. Callers should retry. This is not exported; callers see ErrBusy
// after retry exhaustion.
var errOverlap = errors.New("slotcache: internal: read overlapped with concurrent write")

// Retry configuration for read operations under seqlock contention.
// See TECHNICAL_DECISIONS.md §8 for rationale.
const (
	// readMaxRetries is the maximum number of retry attempts for read operations
	// before returning ErrBusy.
	readMaxRetries = 10

	// readInitialBackoff is the initial sleep duration between retry attempts.
	readInitialBackoff = 50 * time.Microsecond

	// readMaxBackoff caps the exponential backoff growth.
	readMaxBackoff = 1 * time.Millisecond
)

// readBackoff waits for an exponentially increasing duration based on the
// attempt number (0-indexed).
func readBackoff(attempt int) {
	if attempt == 0 {
		return // First attempt is immediate
	}

	backoff := min(
		// Exponential: 50µs, 100µs, 200µs, ...
		readInitialBackoff<<(attempt-1), readMaxBackoff)

	time.Sleep(backoff)
}

// checkInvariantViolation is called when an impossible invariant is detected during
// a read operation. Per the spec's reader coherence rule (step 4), we must re-read
// generation to determine if the violation is due to overlap with a concurrent write
// or due to real corruption.
//
// Parameters:
//   - expectedGen: the generation (g1) we read at the start of the operation
//
// Returns:
//   - errOverlap if generation changed or is now odd (caller should retry)
//   - ErrCorrupt if generation is still the same even value (real corruption)
func (c *Cache) checkInvariantViolation(expectedGen uint64) error {
	gx := c.readGeneration()
	if gx != expectedGen || gx%2 == 1 {
		// Generation changed or is odd - read overlapped with a concurrent write.
		return errOverlap
	}
	// Generation is stable and even - this is real corruption.
	return ErrCorrupt
}

// readLiveCount reads the live_count from header.
func (c *Cache) readLiveCount() uint64 {
	return binary.LittleEndian.Uint64(c.data[offLiveCount:])
}

// readSlotHighwater reads slot_highwater from header.
func (c *Cache) readSlotHighwater() uint64 {
	return binary.LittleEndian.Uint64(c.data[offSlotHighwater:])
}

// safeSlotHighwater reads slot_highwater and validates it is safe to use as a
// loop bound / for slot offset calculations.
//
// This exists for panic-proofing: under cross-process overlap, readers may
// observe transient torn header values. We must never use such values to index
// into the mmap or to run unbounded loops.
//
// Must be called while holding registryEntry.mu.RLock.
func (c *Cache) safeSlotHighwater(expectedGen uint64) (uint64, error) {
	highwater := c.readSlotHighwater()

	// slot_highwater must never exceed slot_capacity.
	if highwater > c.slotCapacity {
		return 0, c.checkInvariantViolation(expectedGen)
	}

	slotSize := uint64(c.slotSize)

	// Compute slots byte range: [slotsOffset, slotsOffset + highwater*slotSize).
	// Guard multiplication + addition overflow and ensure it fits in the mapping.
	slotsBytes := highwater * slotSize
	if slotSize > 0 && slotsBytes/slotSize != highwater {
		return 0, c.checkInvariantViolation(expectedGen)
	}

	slotsEnd := c.slotsOffset + slotsBytes
	if slotsEnd < c.slotsOffset {
		return 0, c.checkInvariantViolation(expectedGen)
	}

	if slotsEnd > uint64(len(c.data)) {
		return 0, c.checkInvariantViolation(expectedGen)
	}

	return highwater, nil
}

// lookupKey finds a key in the bucket index and returns the entry.
// Must be called with registryEntry.mu.RLock held.
//
// The expectedGen parameter is the generation read at the start of the operation.
// When an impossible invariant is detected, we re-check generation to distinguish
// overlap (errOverlap) from real corruption (ErrCorrupt) per the spec's reader
// coherence rule.
func (c *Cache) lookupKey(key []byte, expectedGen uint64) (Entry, bool, error) {
	hash := fnv1a64(key)
	mask := c.bucketCount - 1
	startIdx := hash & mask

	highwater, hwErr := c.safeSlotHighwater(expectedGen)
	if hwErr != nil {
		return Entry{}, false, hwErr
	}

	for probeCount := range c.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := c.bucketsOffset + idx*16

		storedHash := binary.LittleEndian.Uint64(c.data[bucketOffset:])
		slotPlusOne := binary.LittleEndian.Uint64(c.data[bucketOffset+8:])

		if slotPlusOne == 0 {
			// EMPTY - key not found.
			return Entry{}, false, nil
		}

		if slotPlusOne == ^uint64(0) {
			// TOMBSTONE - continue probing.
			continue
		}

		// FULL bucket.
		slotID := slotPlusOne - 1
		if slotID >= highwater {
			// Impossible invariant: bucket references slot beyond highwater.
			// This could be overlap with concurrent write or real corruption.
			return Entry{}, false, c.checkInvariantViolation(expectedGen)
		}

		if storedHash != hash {
			continue
		}

		// Hash matches - verify key bytes.
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)
		slotKey := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		if !bytes.Equal(slotKey, key) {
			continue
		}

		// Key matches - check if live.
		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])

		// Check for reserved bits set (corruption indicator).
		// Per spec: "All other bits are reserved and MUST be zero in v1."
		if meta&slotMetaReservedMask != 0 {
			return Entry{}, false, c.checkInvariantViolation(expectedGen)
		}

		if (meta & slotMetaUsed) == 0 {
			// Impossible invariant: bucket points to tombstoned slot.
			// This could be overlap with concurrent write or real corruption.
			return Entry{}, false, c.checkInvariantViolation(expectedGen)
		}

		// Read entry data.
		keyPad := (8 - (c.keySize % 8)) % 8
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		// Use atomic load for revision to avoid torn reads during concurrent writes.
		revision := atomicLoadInt64(c.data[revOffset:])

		var index []byte

		if c.indexSize > 0 {
			idxOffset := revOffset + 8
			index = make([]byte, c.indexSize)
			copy(index, c.data[idxOffset:idxOffset+uint64(c.indexSize)])
		}

		keyCopy := make([]byte, c.keySize)
		copy(keyCopy, slotKey)

		return Entry{
			Key:      keyCopy,
			Revision: revision,
			Index:    index,
		}, true, nil
	}

	// Impossible invariant: probed all buckets without finding EMPTY.
	// Hash table should never be completely full (bucket_used + bucket_tombstones < bucket_count).
	// This could be overlap with concurrent write or real corruption.
	return Entry{}, false, c.checkInvariantViolation(expectedGen)
}
