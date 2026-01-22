package slotcache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
)

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

// Scan returns all live entries in insertion order.
// Scan captures a stable snapshot before returning. If snapshot acquisition
// fails, it returns [ErrBusy] and no results.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput], [ErrInvalidated].
func (c *Cache) Scan(opts ScanOptions) ([]Entry, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}

	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0, got %d: %w", opts.Offset, ErrInvalidInput)
	}

	if opts.Offset > maxScanOffset {
		return nil, fmt.Errorf("offset %d exceeds max %d: %w", opts.Offset, maxScanOffset, ErrInvalidInput)
	}

	if opts.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0, got %d: %w", opts.Limit, ErrInvalidInput)
	}

	if opts.Limit > maxScanLimit {
		return nil, fmt.Errorf("limit %d exceeds max %d: %w", opts.Limit, maxScanLimit, ErrInvalidInput)
	}

	return c.collectEntries(opts, func(_ []byte) bool { return true })
}

// ScanPrefix returns live entries matching the given byte prefix at offset 0.
//
// Equivalent to ScanMatch([Prefix]{Bytes: prefix}, opts).
// ScanPrefix captures a stable snapshot before returning. If snapshot
// acquisition fails, it returns [ErrBusy] and no results.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput], [ErrInvalidated].
func (c *Cache) ScanPrefix(prefix []byte, opts ScanOptions) ([]Entry, error) {
	return c.ScanMatch(Prefix{Offset: 0, Bits: 0, Bytes: prefix}, opts)
}

// ScanMatch returns live entries matching a [Prefix].
//
// This scans all entries O(N); it cannot use the key index for acceleration.
// ScanMatch captures a stable snapshot before returning. If snapshot
// acquisition fails, it returns [ErrBusy] and no results.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput], [ErrInvalidated].
func (c *Cache) ScanMatch(spec Prefix, opts ScanOptions) ([]Entry, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}

	// Check for invalidation early, before ErrUnordered.
	// Per spec: "All subsequent operations return ErrInvalidated" once invalidated.
	// This is a fast-path check; collectRangeEntries will also check under seqlock.
	// Note: We read c.data outside the mu lock, but that's safe because:
	// 1. We already checked isClosed above (under mu), and
	// 2. Close() sets isClosed=true before clearing c.data
	state := binary.LittleEndian.Uint32(c.data[offState:])
	if state == stateInvalidated {
		return nil, ErrInvalidated
	}

	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0, got %d: %w", opts.Offset, ErrInvalidInput)
	}

	if opts.Offset > maxScanOffset {
		return nil, fmt.Errorf("offset %d exceeds max %d: %w", opts.Offset, maxScanOffset, ErrInvalidInput)
	}

	if opts.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0, got %d: %w", opts.Limit, ErrInvalidInput)
	}

	if opts.Limit > maxScanLimit {
		return nil, fmt.Errorf("limit %d exceeds max %d: %w", opts.Limit, maxScanLimit, ErrInvalidInput)
	}

	validationErr := c.validatePrefixSpec(spec)
	if validationErr != nil {
		return nil, validationErr
	}

	// Optimization: Use binary search range scan for prefix at offset 0 in ordered-keys mode.
	if c.prefixCanUseRangeScan(spec) {
		start, end, ok := c.prefixToRange(spec)
		if ok {
			// Note: Filter is applied by collectRangeEntries internally.
			return c.collectRangeEntries(start, end, opts)
		}
		// Prefix matches all keys (all 0xFF), fall through to full scan.
	}

	// Fall back to full scan with filter for non-zero offset prefixes or unordered mode.
	return c.collectEntries(opts, func(key []byte) bool {
		return keyMatchesPrefix(key, spec)
	})
}

// ScanRange returns live entries in the half-open key range [start, end).
//
// Requires [Options.OrderedKeys]. Either bound may be nil (unbounded).
// Bounds shorter than KeySize are right-padded with 0x00.
// ScanRange captures a stable snapshot before returning. If snapshot
// acquisition fails, it returns [ErrBusy] and no results.
//
// Possible errors: [ErrClosed], [ErrBusy], [ErrCorrupt], [ErrInvalidInput], [ErrUnordered], [ErrInvalidated].
func (c *Cache) ScanRange(start, end []byte, opts ScanOptions) ([]Entry, error) {
	c.mu.RLock()
	closed := c.isClosed
	c.mu.RUnlock()

	if closed {
		return nil, ErrClosed
	}

	// Check for invalidation early, before ErrUnordered.
	// Per spec: "All subsequent operations return ErrInvalidated" once invalidated.
	// This is a fast-path check; collectRangeEntries will also check under seqlock.
	// Note: We read c.data outside the mu lock, but that's safe because:
	// 1. We already checked isClosed above (under mu), and
	// 2. Close() sets isClosed=true before clearing c.data
	state := binary.LittleEndian.Uint32(c.data[offState:])
	if state == stateInvalidated {
		return nil, ErrInvalidated
	}

	if !c.orderedKeys {
		return nil, fmt.Errorf("ScanRange requires ordered_keys mode: %w", ErrUnordered)
	}

	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0, got %d: %w", opts.Offset, ErrInvalidInput)
	}

	if opts.Offset > maxScanOffset {
		return nil, fmt.Errorf("offset %d exceeds max %d: %w", opts.Offset, maxScanOffset, ErrInvalidInput)
	}

	if opts.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0, got %d: %w", opts.Limit, ErrInvalidInput)
	}

	if opts.Limit > maxScanLimit {
		return nil, fmt.Errorf("limit %d exceeds max %d: %w", opts.Limit, maxScanLimit, ErrInvalidInput)
	}

	startPadded, endPadded, err := c.normalizeRangeBounds(start, end)
	if err != nil {
		return nil, err
	}

	return c.collectRangeEntries(startPadded, endPadded, opts)
}

// binarySearchSlotGE finds the first slot index where key >= target.
// Returns highwater if all keys are less than target.
// This works correctly with tombstones because they preserve their key bytes.
// Must be called with registryEntry.mu.RLock held.
func (c *Cache) binarySearchSlotGE(target []byte, highwater uint64) uint64 {
	low := uint64(0)
	high := highwater

	for low < high {
		mid := low + (high-low)/2
		slotOffset := c.slotsOffset + mid*uint64(c.slotSize)
		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		if bytes.Compare(key, target) < 0 {
			low = mid + 1
		} else {
			high = mid
		}
	}

	return low
}

// collectRangeEntries collects entries in the given key range with seqlock retry.
// Uses binary search optimization for ordered-keys mode.
func (c *Cache) collectRangeEntries(startPadded, endPadded []byte, opts ScanOptions) ([]Entry, error) {
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

			return nil, ErrInvalidated
		}

		entries, err := c.doCollectRange(g1, startPadded, endPadded, opts)
		g2 := c.readGeneration()
		c.registryEntry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		return entries, err
	}

	return nil, ErrBusy
}

// doCollectRange performs range scan using binary search + sequential scan.
// Must be called with registryEntry.mu.RLock held.
//
// The expectedGen parameter is the generation read at the start of the operation.
// When an impossible invariant is detected (e.g., reserved meta bits set), we re-check
// generation to distinguish overlap (errOverlap) from real corruption (ErrCorrupt).
//
// Allocation optimization: Same approach as doCollect - borrow mmap slices for
// filter callbacks, only allocate owned copies for entries that pass the filter.
//
// Early termination optimization: For scans with Limit, we stop scanning
// once we've collected Offset+Limit entries (enough to satisfy the request).
//
// Reverse iteration optimization: For reverse scans, we iterate slots in
// reverse order directly (avoiding slices.Reverse). We use binary search to
// find the last slot in range (key < end), then iterate backward to start.
func (c *Cache) doCollectRange(expectedGen uint64, startPadded, endPadded []byte, opts ScanOptions) ([]Entry, error) {
	highwater, hwErr := c.safeSlotHighwater(expectedGen)
	if hwErr != nil {
		return nil, hwErr
	}

	if highwater == 0 {
		return []Entry{}, nil
	}

	// For reverse scans, iterate backwards directly.
	if opts.Reverse {
		return c.doCollectRangeReverse(expectedGen, highwater, startPadded, endPadded, opts)
	}

	// Binary search to find starting position.
	// binarySearchSlotGE returns the first slot with key >= startPadded.
	var startSlot uint64
	if startPadded != nil {
		startSlot = c.binarySearchSlotGE(startPadded, highwater)
	}
	// If no start bound, startSlot remains 0.

	entries := make([]Entry, 0)
	keyPad := (8 - (c.keySize % 8)) % 8

	// Early termination: we only need Offset+Limit entries.
	canTerminateEarly := opts.Limit > 0

	needCount := 0
	if canTerminateEarly {
		needCount = opts.Offset + opts.Limit
	}

	// Order validation for ordered-keys mode: track previous key to verify sorted invariant.
	var prevKey []byte

	// Sequential scan from startSlot.
	for slotID := startSlot; slotID < highwater; slotID++ {
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)
		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		// Order validation: keys must be non-decreasing in ordered-keys mode.
		if prevKey != nil && bytes.Compare(key, prevKey) < 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		prevKey = key

		// Early termination: if key >= end, we're done (keys are sorted).
		if endPadded != nil && bytes.Compare(key, endPadded) >= 0 {
			break
		}

		// Skip if key < start (corruption defense: binary search may land wrong
		// if the ordered-keys invariant is violated by file corruption).
		if startPadded != nil && bytes.Compare(key, startPadded) < 0 {
			continue
		}

		// Check if live (not tombstoned).
		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])

		// Check for reserved bits set (corruption indicator).
		// Per spec: "All other bits are reserved and MUST be zero in v1."
		if meta&slotMetaReservedMask != 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		// Read entry data.
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		// Use atomic load for revision to avoid torn reads during concurrent writes.
		revision := atomicLoadInt64(c.data[revOffset:])

		// Apply filter if present, using borrowed mmap slices.
		if opts.Filter != nil {
			var borrowedIndex []byte

			if c.indexSize > 0 {
				idxOffset := revOffset + 8
				// Borrow directly from mmap - no allocation needed for filter.
				borrowedIndex = c.data[idxOffset : idxOffset+uint64(c.indexSize)]
			}

			borrowed := Entry{
				Key:      key,
				Revision: revision,
				Index:    borrowedIndex,
			}

			if !opts.Filter(borrowed) {
				continue
			}
		}

		// Create owned copies for result.
		keyCopy := make([]byte, c.keySize)
		copy(keyCopy, key)

		var indexCopy []byte

		if c.indexSize > 0 {
			idxOffset := revOffset + 8
			indexCopy = make([]byte, c.indexSize)
			copy(indexCopy, c.data[idxOffset:idxOffset+uint64(c.indexSize)])
		}

		entries = append(entries, Entry{
			Key:      keyCopy,
			Revision: revision,
			Index:    indexCopy,
		})

		// Early termination when we have enough entries.
		if canTerminateEarly && len(entries) >= needCount {
			break
		}
	}

	start := min(opts.Offset, len(entries))

	end := len(entries)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return entries[start:end], nil
}

// binarySearchSlotLT finds the last slot index where key < target.
// Returns the index of the last slot with key < target, or highwater if none found.
// This is used for reverse range scans to find the starting point.
// Must be called with registryEntry.mu.RLock held.
func (c *Cache) binarySearchSlotLT(target []byte, highwater uint64) uint64 {
	// Binary search for first slot with key >= target, then step back.
	// binarySearchSlotGE returns first slot with key >= target, or highwater if all < target.
	firstGE := c.binarySearchSlotGE(target, highwater)
	if firstGE == 0 {
		// All keys >= target, no slot with key < target.
		return highwater // Signal "no valid slot"
	}
	// Return the slot before firstGE (last slot with key < target).
	return firstGE - 1
}

// doCollectRangeReverse performs reverse range scan.
// Iterates slots in reverse order directly, from the last slot in range
// (key < end) back to the first slot in range (key >= start).
// Must be called with registryEntry.mu.RLock held.
func (c *Cache) doCollectRangeReverse(expectedGen uint64, highwater uint64, startPadded, endPadded []byte, opts ScanOptions) ([]Entry, error) {
	// Find the last slot in range.
	// For range [start, end), we want slots where start <= key < end.
	// In reverse, we start from the last slot with key < end.
	var lastSlot uint64
	if endPadded != nil {
		// Find last slot with key < endPadded.
		lastSlot = c.binarySearchSlotLT(endPadded, highwater)
		if lastSlot == highwater {
			// All keys >= end, no entries in range.
			return []Entry{}, nil
		}
	} else {
		// No end bound, start from the last slot.
		lastSlot = highwater - 1
	}

	entries := make([]Entry, 0)
	keyPad := (8 - (c.keySize % 8)) % 8

	// Early termination: we only need Offset+Limit entries.
	canTerminateEarly := opts.Limit > 0

	needCount := 0
	if canTerminateEarly {
		needCount = opts.Offset + opts.Limit
	}

	// Order validation: track previous key to verify sorted invariant.
	// When iterating backwards, keys should be non-increasing (current key <= prevKey).
	var prevKey []byte

	// Iterate from lastSlot down to 0.
	for i := lastSlot + 1; i > 0; i-- {
		slotID := i - 1
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)
		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		// Order validation: when iterating backwards, keys should be non-increasing.
		// Note: prevKey holds the key from the *higher* slot ID we saw earlier.
		if prevKey != nil && bytes.Compare(key, prevKey) > 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		prevKey = key

		// Early termination: if key < start, we're done (keys are sorted).
		if startPadded != nil && bytes.Compare(key, startPadded) < 0 {
			break
		}

		// Skip if key >= end (corruption defense: binary search may land wrong
		// if the ordered-keys invariant is violated by file corruption).
		if endPadded != nil && bytes.Compare(key, endPadded) >= 0 {
			continue
		}

		// Check if live (not tombstoned).
		meta := atomicLoadUint64(c.data[slotOffset:])

		// Check for reserved bits set (corruption indicator).
		if meta&slotMetaReservedMask != 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		// Read entry data.
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		revision := atomicLoadInt64(c.data[revOffset:])

		// Apply filter if present.
		if opts.Filter != nil {
			var borrowedIndex []byte

			if c.indexSize > 0 {
				idxOffset := revOffset + 8
				borrowedIndex = c.data[idxOffset : idxOffset+uint64(c.indexSize)]
			}

			borrowed := Entry{
				Key:      key,
				Revision: revision,
				Index:    borrowedIndex,
			}

			if !opts.Filter(borrowed) {
				continue
			}
		}

		// Create owned copies for result.
		keyCopy := make([]byte, c.keySize)
		copy(keyCopy, key)

		var indexCopy []byte

		if c.indexSize > 0 {
			idxOffset := revOffset + 8
			indexCopy = make([]byte, c.indexSize)
			copy(indexCopy, c.data[idxOffset:idxOffset+uint64(c.indexSize)])
		}

		entries = append(entries, Entry{
			Key:      keyCopy,
			Revision: revision,
			Index:    indexCopy,
		})

		// Early termination when we have enough entries.
		if canTerminateEarly && len(entries) >= needCount {
			break
		}
	}

	// No reversal needed - entries are already in reverse order.

	start := min(opts.Offset, len(entries))

	end := len(entries)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return entries[start:end], nil
}

// collectEntries collects entries matching the predicate with seqlock retry.
func (c *Cache) collectEntries(opts ScanOptions, match func([]byte) bool) ([]Entry, error) {
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

			return nil, ErrInvalidated
		}

		entries, err := c.doCollect(g1, opts, match)
		g2 := c.readGeneration()
		c.registryEntry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		return entries, err
	}

	return nil, ErrBusy
}

// doCollect performs the actual slot scan.
// Must be called with registryEntry.mu.RLock held.
//
// The expectedGen parameter is the generation read at the start of the operation.
// When an impossible invariant is detected (e.g., reserved meta bits set), we re-check
// generation to distinguish overlap (errOverlap) from real corruption (ErrCorrupt).
//
// Allocation optimization: We minimize allocations by:
// 1. Borrowing mmap slices directly for filter callbacks (API contract allows this)
// 2. Only allocating owned copies for entries that pass the filter
// 3. Skipping borrowed entry construction entirely when no filter is set.
//
// Early termination optimization: For scans with Limit, we stop scanning
// once we've collected Offset+Limit entries (enough to satisfy the request).
//
// Reverse iteration optimization: For ordered-keys mode with reverse scans,
// we iterate slots in reverse order directly (avoiding slices.Reverse).
func (c *Cache) doCollect(expectedGen uint64, opts ScanOptions, match func([]byte) bool) ([]Entry, error) {
	highwater, hwErr := c.safeSlotHighwater(expectedGen)
	if hwErr != nil {
		return nil, hwErr
	}

	// For ordered-keys mode with reverse scans, iterate backwards directly.
	// This avoids collecting all entries and then reversing.
	if opts.Reverse && c.orderedKeys {
		return c.doCollectReverse(expectedGen, highwater, opts, match)
	}

	entries := make([]Entry, 0)

	keyPad := (8 - (c.keySize % 8)) % 8

	// Early termination: for forward scans with Limit, we only need Offset+Limit entries.
	// For reverse scans in unordered mode, we need all entries since we reverse after collection.
	canTerminateEarly := !opts.Reverse && opts.Limit > 0

	needCount := 0
	if canTerminateEarly {
		needCount = opts.Offset + opts.Limit
	}

	// Order validation for ordered-keys mode: track previous key to verify sorted invariant.
	// Per spec: "For all allocated slot IDs i < j < slot_highwater, slot[i].key <= slot[j].key"
	var prevKey []byte

	for slotID := range highwater {
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)

		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])

		// Check for reserved bits set (corruption indicator).
		// Per spec: "All other bits are reserved and MUST be zero in v1."
		if meta&slotMetaReservedMask != 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		// Order validation: in ordered-keys mode, keys must be non-decreasing.
		// This check validates the on-disk sorted invariant during scans.
		if c.orderedKeys && prevKey != nil && bytes.Compare(key, prevKey) < 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		prevKey = key

		if !match(key) {
			continue
		}

		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		// Use atomic load for revision to avoid torn reads during concurrent writes.
		revision := atomicLoadInt64(c.data[revOffset:])

		// Apply filter if present, using borrowed mmap slices.
		// The API contract states filter receives borrowed slices valid only during the call.
		if opts.Filter != nil {
			var borrowedIndex []byte

			if c.indexSize > 0 {
				idxOffset := revOffset + 8
				// Borrow directly from mmap - no allocation needed for filter.
				borrowedIndex = c.data[idxOffset : idxOffset+uint64(c.indexSize)]
			}

			borrowed := Entry{
				Key:      key,
				Revision: revision,
				Index:    borrowedIndex,
			}

			if !opts.Filter(borrowed) {
				continue
			}
		}

		// Create owned copies for result.
		keyCopy := make([]byte, c.keySize)
		copy(keyCopy, key)

		var indexCopy []byte

		if c.indexSize > 0 {
			idxOffset := revOffset + 8
			indexCopy = make([]byte, c.indexSize)
			copy(indexCopy, c.data[idxOffset:idxOffset+uint64(c.indexSize)])
		}

		entries = append(entries, Entry{
			Key:      keyCopy,
			Revision: revision,
			Index:    indexCopy,
		})

		// Early termination for forward scans with Limit.
		if canTerminateEarly && len(entries) >= needCount {
			break
		}
	}

	if opts.Reverse {
		// Unordered mode: must reverse after collection.
		slices.Reverse(entries)
	}

	start := min(opts.Offset, len(entries))

	end := len(entries)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return entries[start:end], nil
}

// doCollectReverse performs reverse slot scan for ordered-keys mode.
// Iterates slots in reverse order directly, avoiding the need to collect all
// entries and then reverse. This enables early termination for Limit.
// Must be called with registryEntry.mu.RLock held.
func (c *Cache) doCollectReverse(expectedGen uint64, highwater uint64, opts ScanOptions, match func([]byte) bool) ([]Entry, error) {
	entries := make([]Entry, 0)

	keyPad := (8 - (c.keySize % 8)) % 8

	// Early termination: we only need Offset+Limit entries.
	canTerminateEarly := opts.Limit > 0

	needCount := 0
	if canTerminateEarly {
		needCount = opts.Offset + opts.Limit
	}

	// Order validation for ordered-keys mode: track previous key to verify sorted invariant.
	// When iterating backwards, keys should be non-increasing (current key <= prevKey).
	var prevKey []byte

	// Iterate from highwater-1 down to 0.
	for i := highwater; i > 0; i-- {
		slotID := i - 1
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)

		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])

		// Check for reserved bits set (corruption indicator).
		if meta&slotMetaReservedMask != 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		// Order validation: in ordered-keys mode, when iterating backwards,
		// keys should be non-increasing (current key <= previous key seen).
		// Note: prevKey holds the key from the *higher* slot ID we saw earlier.
		if prevKey != nil && bytes.Compare(key, prevKey) > 0 {
			return nil, c.checkInvariantViolation(expectedGen)
		}

		prevKey = key

		if !match(key) {
			continue
		}

		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		revision := atomicLoadInt64(c.data[revOffset:])

		// Apply filter if present.
		if opts.Filter != nil {
			var borrowedIndex []byte

			if c.indexSize > 0 {
				idxOffset := revOffset + 8
				borrowedIndex = c.data[idxOffset : idxOffset+uint64(c.indexSize)]
			}

			borrowed := Entry{
				Key:      key,
				Revision: revision,
				Index:    borrowedIndex,
			}

			if !opts.Filter(borrowed) {
				continue
			}
		}

		// Create owned copies for result.
		keyCopy := make([]byte, c.keySize)
		copy(keyCopy, key)

		var indexCopy []byte

		if c.indexSize > 0 {
			idxOffset := revOffset + 8
			indexCopy = make([]byte, c.indexSize)
			copy(indexCopy, c.data[idxOffset:idxOffset+uint64(c.indexSize)])
		}

		entries = append(entries, Entry{
			Key:      keyCopy,
			Revision: revision,
			Index:    indexCopy,
		})

		// Early termination when we have enough entries.
		if canTerminateEarly && len(entries) >= needCount {
			break
		}
	}

	// No reversal needed - entries are already in reverse order.

	start := min(opts.Offset, len(entries))

	end := len(entries)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return entries[start:end], nil
}

func (c *Cache) validatePrefixSpec(spec Prefix) error {
	if spec.Offset < 0 {
		return fmt.Errorf("prefix offset %d must be >= 0: %w", spec.Offset, ErrInvalidInput)
	}

	if spec.Offset >= int(c.keySize) {
		return fmt.Errorf("prefix offset %d >= key_size %d: %w", spec.Offset, c.keySize, ErrInvalidInput)
	}

	if spec.Bits < 0 {
		return fmt.Errorf("prefix bits %d must be >= 0: %w", spec.Bits, ErrInvalidInput)
	}

	// Hard safety cap: prevent int overflow in (Bits+7)/8 and ensure the prefix
	// can fit within the remaining key bytes.
	maxBits := (int(c.keySize) - spec.Offset) * 8
	if spec.Bits > maxBits {
		return fmt.Errorf("prefix bits %d exceeds max %d for offset %d and key_size %d: %w",
			spec.Bits, maxBits, spec.Offset, c.keySize, ErrInvalidInput)
	}

	if spec.Bits == 0 {
		if len(spec.Bytes) == 0 {
			return fmt.Errorf("prefix bytes is empty with bits=0: %w", ErrInvalidInput)
		}

		if spec.Offset+len(spec.Bytes) > int(c.keySize) {
			return fmt.Errorf("prefix offset (%d) + len(bytes) (%d) > key_size (%d): %w", spec.Offset, len(spec.Bytes), c.keySize, ErrInvalidInput)
		}

		return nil
	}

	needBytes := (spec.Bits + 7) / 8
	if needBytes == 0 {
		return fmt.Errorf("prefix bits %d requires 0 bytes (invalid): %w", spec.Bits, ErrInvalidInput)
	}

	if len(spec.Bytes) != needBytes {
		return fmt.Errorf("prefix bytes length %d != required %d for %d bits: %w", len(spec.Bytes), needBytes, spec.Bits, ErrInvalidInput)
	}

	if spec.Offset+needBytes > int(c.keySize) {
		return fmt.Errorf("prefix offset (%d) + needBytes (%d) > key_size (%d): %w", spec.Offset, needBytes, c.keySize, ErrInvalidInput)
	}

	return nil
}

func (c *Cache) normalizeRangeBounds(start, end []byte) ([]byte, []byte, error) {
	startPadded, err := c.normalizeRangeBound(start, "start")
	if err != nil {
		return nil, nil, err
	}

	endPadded, err := c.normalizeRangeBound(end, "end")
	if err != nil {
		return nil, nil, err
	}

	if startPadded != nil && endPadded != nil && bytes.Compare(startPadded, endPadded) > 0 {
		return nil, nil, fmt.Errorf("start bound > end bound: %w", ErrInvalidInput)
	}

	return startPadded, endPadded, nil
}

func (c *Cache) normalizeRangeBound(bound []byte, name string) ([]byte, error) {
	if bound == nil {
		return nil, nil
	}

	if len(bound) == 0 {
		return nil, fmt.Errorf("%s bound is empty (use nil for unbounded): %w", name, ErrInvalidInput)
	}

	if len(bound) > int(c.keySize) {
		return nil, fmt.Errorf("%s bound length %d > key_size %d: %w", name, len(bound), c.keySize, ErrInvalidInput)
	}

	if len(bound) == int(c.keySize) {
		return append([]byte(nil), bound...), nil
	}

	padded := make([]byte, c.keySize)
	copy(padded, bound)

	return padded, nil
}

func keyMatchesPrefix(key []byte, spec Prefix) bool {
	if spec.Bits == 0 {
		segment := key[spec.Offset : spec.Offset+len(spec.Bytes)]

		return bytes.Equal(segment, spec.Bytes)
	}

	needBytes := (spec.Bits + 7) / 8
	segment := key[spec.Offset : spec.Offset+needBytes]

	fullBytes := needBytes
	if rem := spec.Bits % 8; rem != 0 {
		fullBytes = needBytes - 1
	}

	if fullBytes > 0 {
		if !bytes.Equal(segment[:fullBytes], spec.Bytes[:fullBytes]) {
			return false
		}
	}

	remBits := spec.Bits % 8
	if remBits == 0 {
		return true
	}

	mask := byte(0xFF) << (8 - remBits)

	return (segment[needBytes-1] & mask) == (spec.Bytes[needBytes-1] & mask)
}

// prefixCanUseRangeScan checks whether a prefix scan can be accelerated
// using the binary search range scan path. This is possible when:
// - The cache is in ordered-keys mode
// - The prefix starts at offset 0 (prefix matches key start).
func (c *Cache) prefixCanUseRangeScan(spec Prefix) bool {
	return c.orderedKeys && spec.Offset == 0
}

// prefixToRange converts a Prefix spec to range bounds [start, end) for use
// with the range scan optimization. Both bounds are padded to keySize.
//
// Returns (start, end, true) if conversion succeeded.
// Returns (nil, nil, false) if the prefix matches all keys (all 0xFF prefix).
//
// Precondition: spec.Offset == 0 (caller must verify prefix starts at key start).
// Precondition: spec has already been validated by validatePrefixSpec.
func (c *Cache) prefixToRange(spec Prefix) ([]byte, []byte, bool) {
	keySize := int(c.keySize)

	if spec.Bits == 0 {
		// Byte-aligned prefix.
		return byteAlignedPrefixToRange(spec.Bytes, keySize)
	}

	// Bit-level prefix.
	return bitLevelPrefixToRange(spec.Bytes, spec.Bits, keySize)
}

// byteAlignedPrefixToRange converts a byte-aligned prefix to range bounds.
func byteAlignedPrefixToRange(prefix []byte, keySize int) ([]byte, []byte, bool) {
	// Start bound: prefix padded with zeros.
	start := make([]byte, keySize)
	copy(start, prefix)

	// End bound: prefix incremented, padded with zeros.
	// If prefix is all 0xFF, there's no successor - prefix matches all keys >= start.
	end := computePrefixSuccessor(prefix, keySize)

	return start, end, true
}

// bitLevelPrefixToRange converts a bit-level prefix to range bounds.
func bitLevelPrefixToRange(prefixBytes []byte, bits int, keySize int) ([]byte, []byte, bool) {
	needBytes := (bits + 7) / 8

	// Start bound: prefix bytes with unused bits zeroed, then padded with zeros.
	start := make([]byte, keySize)
	copy(start, prefixBytes)

	// Mask out unused bits in the last byte.
	remBits := bits % 8
	if remBits != 0 {
		mask := byte(0xFF) << (8 - remBits)
		start[needBytes-1] &= mask
	}

	// End bound: increment at the bit level.
	// For a 10-bit prefix matching 0b1010101111..., the successor is 0b1010110000...
	end := computeBitPrefixSuccessor(start[:needBytes], bits, keySize)

	return start, end, true
}

// computePrefixSuccessor returns the lexicographically next prefix after the given one.
// The result is padded to keySize with zeros.
// Returns nil if there is no successor (prefix is all 0xFF).
func computePrefixSuccessor(prefix []byte, keySize int) []byte {
	// Work from the end, incrementing bytes and handling carry.
	succ := make([]byte, len(prefix))
	copy(succ, prefix)

	for i := len(succ) - 1; i >= 0; i-- {
		if succ[i] < 0xFF {
			succ[i]++

			// Pad the result to keySize.
			result := make([]byte, keySize)
			copy(result, succ[:i+1])

			return result
		}

		// Byte is 0xFF, need to carry.
		succ[i] = 0x00
	}

	// All bytes were 0xFF - no successor exists.
	return nil
}

// computeBitPrefixSuccessor computes the successor of a bit-level prefix.
// bits is the number of significant bits in the prefix.
// Returns nil if there is no successor (all significant bits are 1).
func computeBitPrefixSuccessor(prefix []byte, bits int, keySize int) []byte {
	if bits == 0 {
		return nil
	}

	needBytes := (bits + 7) / 8
	remBits := bits % 8

	// Make a copy to work with.
	succ := make([]byte, needBytes)
	copy(succ, prefix)

	// Mask out unused bits in the last byte.
	if remBits != 0 {
		mask := byte(0xFF) << (8 - remBits)
		succ[needBytes-1] &= mask
	}

	// Compute the increment value for the least significant bit position.
	// For a 10-bit prefix (remBits=2), we need to add 0b01000000 = 0x40.
	// For a byte-aligned prefix (remBits=0), we add 0x01 to the last byte.
	var (
		incrementByte  int
		incrementValue byte
	)

	if remBits == 0 {
		incrementByte = needBytes - 1
		incrementValue = 0x01
	} else {
		incrementByte = needBytes - 1
		incrementValue = 0x01 << (8 - remBits)
	}

	// Add the increment and propagate carry.
	for i := incrementByte; i >= 0; i-- {
		newVal := uint16(succ[i]) + uint16(incrementValue)
		succ[i] = byte(newVal & 0xFF)

		if newVal <= 0xFF {
			// No carry, done.
			result := make([]byte, keySize)
			copy(result, succ)

			return result
		}

		// Carry to next byte.
		incrementValue = 0x01
	}

	// All significant bits were 1 - no successor exists.
	return nil
}
