package slotcache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"slices"
	"sort"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// rehashThreshold is the ratio of bucket_tombstones/bucket_count above which
// we rebuild the hash table during Commit. Per TECHNICAL_DECISIONS.md §5.
//
// Note: The benefit of rehashing is limited since slotcache doesn't resize.
// Rehashing only eliminates tombstones to reduce probe chain length during
// lookups - it doesn't reclaim slots or reduce file size. If the cache
// becomes slow due to fragmentation, rebuilding from source of truth is
// the intended solution for this "throwaway cache" design.
const rehashThreshold = 0.25

// bufferedOp represents a buffered Put or Delete operation.
type bufferedOp struct {
	isPut    bool
	key      []byte
	revision int64
	index    []byte
}

// writer is the concrete implementation of Writer.
type writer struct {
	cache          *cache
	bufferedOps    []bufferedOp
	isClosed       bool
	closedByCommit bool
	lock           *fs.Lock

	// Dirty range tracking for WritebackSync optimization.
	// Instead of msync'ing the entire file, we track which page ranges
	// were modified and only sync those. The kernel usually skips clean
	// pages, but for large caches this can reduce syscall overhead.
	//
	// Slots and buckets are tracked as byte ranges [minOffset, maxOffset).
	// A value of -1 for minSlotOffset means no slots were touched.
	minSlotOffset   int
	maxSlotOffset   int // exclusive: points past the last modified byte
	minBucketOffset int
	maxBucketOffset int  // exclusive: points past the last modified byte
	rehashOccurred  bool // if true, all buckets were rewritten
}

// Put buffers a put operation for the given key.
func (w *writer) Put(key []byte, revision int64, index []byte) error {
	w.cache.mu.Lock()
	defer w.cache.mu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return ErrClosed
	}

	if len(key) != int(w.cache.keySize) {
		return fmt.Errorf("key length %d != key_size %d: %w", len(key), w.cache.keySize, ErrInvalidInput)
	}

	if len(index) != int(w.cache.indexSize) {
		return fmt.Errorf("index length %d != index_size %d: %w", len(index), w.cache.indexSize, ErrInvalidInput)
	}

	if len(w.bufferedOps) >= maxBufferedOpsPerWriter {
		return fmt.Errorf("too many buffered ops (%d), max %d: %w", len(w.bufferedOps), maxBufferedOpsPerWriter, ErrInvalidInput)
	}

	// Copy key and index to avoid external mutation.
	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	indexCopy := make([]byte, len(index))
	copy(indexCopy, index)

	w.bufferedOps = append(w.bufferedOps, bufferedOp{
		isPut:    true,
		key:      keyCopy,
		revision: revision,
		index:    indexCopy,
	})

	return nil
}

// Delete buffers a delete operation for the given key.
func (w *writer) Delete(key []byte) (bool, error) {
	w.cache.mu.Lock()
	defer w.cache.mu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return false, ErrClosed
	}

	if len(key) != int(w.cache.keySize) {
		return false, fmt.Errorf("key length %d != key_size %d: %w", len(key), w.cache.keySize, ErrInvalidInput)
	}

	if len(w.bufferedOps) >= maxBufferedOpsPerWriter {
		return false, fmt.Errorf("too many buffered ops (%d), max %d: %w", len(w.bufferedOps), maxBufferedOpsPerWriter, ErrInvalidInput)
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	wasPresent := w.isKeyPresent(keyCopy)
	w.bufferedOps = append(w.bufferedOps, bufferedOp{isPut: false, key: keyCopy})

	return wasPresent, nil
}

// Commit applies all buffered operations atomically.
func (w *writer) Commit() error {
	w.cache.mu.Lock()
	defer w.cache.mu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return ErrClosed
	}

	// Initialize dirty range tracking for WritebackSync optimization.
	w.resetDirtyTracking()

	// Compute final ops (last-wins per key).
	finalOps := w.finalOps()

	// Categorize operations based on DISK state only (not buffered ops).
	var updates, inserts, deletes []bufferedOp

	for _, op := range finalOps {
		// Check disk state only - don't consider buffered ops.
		_, found := w.findLiveSlotLocked(op.key)
		if op.isPut {
			if found {
				updates = append(updates, op)
			} else {
				inserts = append(inserts, op)
			}
		} else {
			if found {
				deletes = append(deletes, op)
			}
			// Delete of absent key is no-op.
		}
	}

	// Preflight checks.
	highwater := w.cache.readSlotHighwater()

	newInserts := uint64(len(inserts))
	if highwater+newInserts > w.cache.slotCapacity {
		w.closeByCommit()

		return fmt.Errorf("slot_highwater (%d) + new_inserts (%d) > slot_capacity (%d): %w",
			highwater, newInserts, w.cache.slotCapacity, ErrFull)
	}

	// Ordered mode check.
	if w.cache.orderedKeys && len(inserts) > 0 {
		// Sort inserts by key.
		sort.Slice(inserts, func(i, j int) bool {
			return bytes.Compare(inserts[i].key, inserts[j].key) < 0
		})

		minNewKey := inserts[0].key

		if highwater > 0 {
			// Get tail key (even if tombstoned).
			tailSlotOffset := w.cache.slotsOffset + (highwater-1)*uint64(w.cache.slotSize)

			tailKey := w.cache.data[tailSlotOffset+8 : tailSlotOffset+8+uint64(w.cache.keySize)]
			if bytes.Compare(minNewKey, tailKey) < 0 {
				w.closeByCommit()

				return fmt.Errorf("new key %x < tail key %x at slot %d: %w",
					minNewKey, tailKey, highwater-1, ErrOutOfOrderInsert)
			}
		}
	}

	// Track msync failures for WritebackSync mode.
	var msyncFailed bool

	syncMode := w.cache.writeback == WritebackSync

	// Now apply changes under the registry lock.
	w.cache.registry.mu.Lock()

	// Step 1: Publish odd generation.
	// Uses atomic store per spec requirement for cross-process seqlock.
	oldGen := w.cache.readGeneration()
	newOddGen := oldGen + 1
	atomicStoreUint64(w.cache.data[offGeneration:], newOddGen)

	// Step 2 (WritebackSync): msync header to ensure odd generation is on disk
	// before data modifications.
	if syncMode {
		err := msyncRange(w.cache.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	// Step 3: Apply buffered ops to slots, buckets, and header counters.
	for _, op := range updates {
		slotID, found := w.findLiveSlotLocked(op.key)
		if found {
			w.updateSlot(slotID, op.revision, op.index)
		}
	}

	// Apply deletes.
	deleteCount := uint64(0)

	for _, op := range deletes {
		slotID, found := w.findLiveSlotLocked(op.key)
		if found {
			w.deleteSlot(slotID, op.key)

			deleteCount++
		}
	}

	// Apply inserts (already sorted if ordered mode).
	for _, op := range inserts {
		w.insertSlot(op.key, op.revision, op.index)
	}

	// Update header counters.
	liveCount := binary.LittleEndian.Uint64(w.cache.data[offLiveCount:])
	newLiveCount := liveCount - deleteCount + uint64(len(inserts))
	binary.LittleEndian.PutUint64(w.cache.data[offLiveCount:], newLiveCount)

	newHighwater := highwater + uint64(len(inserts))
	binary.LittleEndian.PutUint64(w.cache.data[offSlotHighwater:], newHighwater)

	// bucket_used must equal live_count.
	binary.LittleEndian.PutUint64(w.cache.data[offBucketUsed:], newLiveCount)

	// Step 3b: Check if tombstone-driven rehash is needed.
	// Per TECHNICAL_DECISIONS.md §5: rebuild when bucket_tombstones/bucket_count > 0.25.
	bucketTombstones := binary.LittleEndian.Uint64(w.cache.data[offBucketTombstones:])
	if w.cache.bucketCount > 0 && float64(bucketTombstones)/float64(w.cache.bucketCount) > rehashThreshold {
		w.rehashBuckets(newHighwater)
	}

	// Step 4: Recompute header CRC.
	crc := computeHeaderCRC(w.cache.data[:slc1HeaderSize])
	binary.LittleEndian.PutUint32(w.cache.data[offHeaderCRC32C:], crc)

	// Step 5 (WritebackSync): msync modified data (slots + buckets + header).
	// This ensures data is on disk before we publish the even generation.
	// Optimization: track dirty page ranges and only sync those instead of
	// the entire file. The kernel usually skips clean pages, but for large
	// caches this can reduce syscall overhead.
	if syncMode {
		// Always sync header (contains generation, counters, CRC).
		err := msyncRange(w.cache.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}

		// Sync dirty slot range if any slots were modified.
		if w.minSlotOffset >= 0 {
			err := msyncRange(w.cache.data, w.minSlotOffset, w.maxSlotOffset-w.minSlotOffset)
			if err != nil {
				msyncFailed = true
			}
		}

		// Sync buckets: all if rehash occurred, otherwise just dirty range.
		if w.rehashOccurred {
			bucketsStart, _ := uint64ToIntChecked(w.cache.bucketsOffset)
			bucketsLen, _ := uint64ToIntChecked(w.cache.bucketCount * 16)

			err := msyncRange(w.cache.data, bucketsStart, bucketsLen)
			if err != nil {
				msyncFailed = true
			}
		} else if w.minBucketOffset >= 0 {
			err := msyncRange(w.cache.data, w.minBucketOffset, w.maxBucketOffset-w.minBucketOffset)
			if err != nil {
				msyncFailed = true
			}
		}
	}

	// Step 6: Publish even generation.
	// Uses atomic store per spec requirement for cross-process seqlock.
	newEvenGen := newOddGen + 1
	atomicStoreUint64(w.cache.data[offGeneration:], newEvenGen)

	// Step 7 (WritebackSync): msync header to ensure even generation is on disk.
	if syncMode {
		err := msyncRange(w.cache.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	w.cache.registry.mu.Unlock()

	w.closeByCommit()

	// If any msync failed, data is visible via MAP_SHARED but durability
	// is not guaranteed. Return ErrWriteback per spec.
	if msyncFailed {
		return ErrWriteback
	}

	return nil
}

// Close releases resources and discards uncommitted changes.
func (w *writer) Close() error {
	w.cache.mu.Lock()
	defer w.cache.mu.Unlock()

	if w.isClosed {
		return nil
	}

	w.isClosed = true
	w.closedByCommit = false
	w.bufferedOps = nil
	w.cache.activeWriter = nil

	// Release in-process guard.
	w.cache.registry.mu.Lock()
	w.cache.registry.writerActive = false
	w.cache.registry.mu.Unlock()

	// Release file lock.
	releaseWriterLock(w.lock)
	w.lock = nil

	return nil
}

// resetDirtyTracking initializes dirty range tracking for a new commit.
func (w *writer) resetDirtyTracking() {
	w.minSlotOffset = -1
	w.maxSlotOffset = -1
	w.minBucketOffset = -1
	w.maxBucketOffset = -1
	w.rehashOccurred = false
}

// markSlotDirty expands the dirty slot range to include the given slot.
// File layout is validated at open time to fit in int64, so conversions are safe.
func (w *writer) markSlotDirty(slotID uint64) {
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)
	slotEnd := slotOffset + uint64(w.cache.slotSize)

	off, ok1 := uint64ToIntChecked(slotOffset)
	end, ok2 := uint64ToIntChecked(slotEnd)

	if !ok1 || !ok2 {
		return // Should never happen due to validation at open time.
	}

	if w.minSlotOffset < 0 || off < w.minSlotOffset {
		w.minSlotOffset = off
	}

	if end > w.maxSlotOffset {
		w.maxSlotOffset = end
	}
}

// markBucketDirty expands the dirty bucket range to include the given bucket.
// File layout is validated at open time to fit in int64, so conversions are safe.
func (w *writer) markBucketDirty(bucketIdx uint64) {
	bucketOffset := w.cache.bucketsOffset + bucketIdx*16
	bucketEnd := bucketOffset + 16

	off, ok1 := uint64ToIntChecked(bucketOffset)
	end, ok2 := uint64ToIntChecked(bucketEnd)

	if !ok1 || !ok2 {
		return // Should never happen due to validation at open time.
	}

	if w.minBucketOffset < 0 || off < w.minBucketOffset {
		w.minBucketOffset = off
	}

	if end > w.maxBucketOffset {
		w.maxBucketOffset = end
	}
}

// updateSlot updates an existing slot with new revision and index.
func (w *writer) updateSlot(slotID uint64, revision int64, index []byte) {
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)
	keyPad := (8 - (w.cache.keySize % 8)) % 8
	revOffset := slotOffset + 8 + uint64(w.cache.keySize) + uint64(keyPad)

	// Use atomic store for revision to ensure readers see complete values.
	atomicStoreInt64(w.cache.data[revOffset:], revision)

	if w.cache.indexSize > 0 {
		idxOffset := revOffset + 8
		copy(w.cache.data[idxOffset:idxOffset+uint64(w.cache.indexSize)], index)
	}

	w.markSlotDirty(slotID)
}

// deleteSlot marks a slot as tombstoned and updates the bucket index.
func (w *writer) deleteSlot(slotID uint64, key []byte) {
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)

	// Clear USED bit. Use atomic store to ensure readers see complete values.
	atomicStoreUint64(w.cache.data[slotOffset:], 0)

	w.markSlotDirty(slotID)

	// Find and tombstone the bucket entry.
	hash := fnv1a64(key)
	mask := w.cache.bucketCount - 1
	startIdx := hash & mask

	for probeCount := range w.cache.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := w.cache.bucketsOffset + idx*16

		slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
		if slotPlusOne == 0 {
			// EMPTY - shouldn't happen for existing key.
			break
		}

		if slotPlusOne == ^uint64(0) {
			// TOMBSTONE - continue.
			continue
		}

		if slotPlusOne-1 == slotID {
			// Found our bucket entry - tombstone it.
			binary.LittleEndian.PutUint64(w.cache.data[bucketOffset+8:], ^uint64(0))

			w.markBucketDirty(idx)

			// Update bucket_tombstones.
			tombstones := binary.LittleEndian.Uint64(w.cache.data[offBucketTombstones:])
			binary.LittleEndian.PutUint64(w.cache.data[offBucketTombstones:], tombstones+1)

			break
		}
	}
}

// insertSlot allocates a new slot and inserts into the bucket index.
func (w *writer) insertSlot(key []byte, revision int64, index []byte) {
	highwater := binary.LittleEndian.Uint64(w.cache.data[offSlotHighwater:])
	slotID := highwater
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)

	// Write slot.
	// Use atomic store for meta to ensure readers see complete values.
	atomicStoreUint64(w.cache.data[slotOffset:], slotMetaUsed) // meta = USED
	copy(w.cache.data[slotOffset+8:slotOffset+8+uint64(w.cache.keySize)], key)

	keyPad := (8 - (w.cache.keySize % 8)) % 8
	revOffset := slotOffset + 8 + uint64(w.cache.keySize) + uint64(keyPad)
	// Use atomic store for revision to ensure readers see complete values.
	atomicStoreInt64(w.cache.data[revOffset:], revision)

	if w.cache.indexSize > 0 {
		idxOffset := revOffset + 8
		copy(w.cache.data[idxOffset:idxOffset+uint64(w.cache.indexSize)], index)
	}

	// Update highwater (done in Commit after all inserts).
	binary.LittleEndian.PutUint64(w.cache.data[offSlotHighwater:], highwater+1)

	w.markSlotDirty(slotID)

	// Insert into bucket index.
	hash := fnv1a64(key)
	mask := w.cache.bucketCount - 1
	startIdx := hash & mask

	for probeCount := range w.cache.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := w.cache.bucketsOffset + idx*16

		slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
		if slotPlusOne == 0 || slotPlusOne == ^uint64(0) {
			// EMPTY or TOMBSTONE - insert here.
			binary.LittleEndian.PutUint64(w.cache.data[bucketOffset:], hash)
			binary.LittleEndian.PutUint64(w.cache.data[bucketOffset+8:], slotID+1)

			w.markBucketDirty(idx)

			// If we filled a tombstone, decrement tombstone count.
			if slotPlusOne == ^uint64(0) {
				tombstones := binary.LittleEndian.Uint64(w.cache.data[offBucketTombstones:])
				if tombstones > 0 {
					binary.LittleEndian.PutUint64(w.cache.data[offBucketTombstones:], tombstones-1)
				}
			}

			break
		}
	}
}

// rehashBuckets rebuilds the bucket index by clearing all buckets and
// re-inserting entries for all live slots. This eliminates tombstones
// and restores optimal probe chain lengths.
//
// Note: Since slotcache doesn't resize, the benefit is limited to reducing
// probe iterations during lookups. Slot tombstones remain (append-only),
// and file size is unchanged. For severe fragmentation, rebuilding the
// entire cache from source of truth is the recommended approach.
//
// Called during Commit when bucket_tombstones/bucket_count > rehashThreshold.
func (w *writer) rehashBuckets(highwater uint64) {
	// Step 1: Clear all buckets to EMPTY (slot_plus_one = 0).
	for i := range w.cache.bucketCount {
		bucketOffset := w.cache.bucketsOffset + i*16
		binary.LittleEndian.PutUint64(w.cache.data[bucketOffset:], 0)   // clear hash field
		binary.LittleEndian.PutUint64(w.cache.data[bucketOffset+8:], 0) // clear to EMPTY state
	}

	// Step 2: Re-insert bucket entries for all live slots.
	mask := w.cache.bucketCount - 1

	for slotID := range highwater {
		slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)

		// Check if slot is live (USED bit set).
		meta := binary.LittleEndian.Uint64(w.cache.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			continue // tombstoned slot, skip
		}

		// Read key and compute hash.
		key := w.cache.data[slotOffset+8 : slotOffset+8+uint64(w.cache.keySize)]
		hash := fnv1a64(key)
		startIdx := hash & mask

		// Linear probe to find an empty bucket.
		for probeCount := range w.cache.bucketCount {
			idx := (startIdx + probeCount) & mask
			bucketOffset := w.cache.bucketsOffset + idx*16

			slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
			if slotPlusOne == 0 {
				// EMPTY - insert here.
				binary.LittleEndian.PutUint64(w.cache.data[bucketOffset:], hash)
				binary.LittleEndian.PutUint64(w.cache.data[bucketOffset+8:], slotID+1)

				break
			}
			// Note: We just cleared all buckets, so we should never see tombstones
			// or existing entries. This loop will always find an empty bucket.
		}
	}

	// Step 3: Update header - reset bucket_tombstones to 0.
	binary.LittleEndian.PutUint64(w.cache.data[offBucketTombstones:], 0)

	// Mark that all buckets were touched for WritebackSync optimization.
	w.rehashOccurred = true
}
func (w *writer) closeByCommit() {
	w.isClosed = true
	w.closedByCommit = true
	w.bufferedOps = nil
	w.cache.activeWriter = nil

	// Release in-process guard.
	w.cache.registry.mu.Lock()
	w.cache.registry.writerActive = false
	w.cache.registry.mu.Unlock()

	// Release file lock.
	releaseWriterLock(w.lock)
	w.lock = nil
}

// finalOps returns the last operation per key, in original order.
func (w *writer) finalOps() []bufferedOp {
	seen := make(map[string]bool)

	var ops []bufferedOp

	for i := len(w.bufferedOps) - 1; i >= 0; i-- {
		op := w.bufferedOps[i]

		keyStr := string(op.key)
		if seen[keyStr] {
			continue
		}

		seen[keyStr] = true

		ops = append(ops, op)
	}

	slices.Reverse(ops)

	return ops
}

// findLiveSlotLocked finds a live slot in the file.
// Used during commit when we need the actual slot ID.
func (w *writer) findLiveSlotLocked(key []byte) (uint64, bool) {
	hash := fnv1a64(key)
	mask := w.cache.bucketCount - 1
	startIdx := hash & mask
	highwater := binary.LittleEndian.Uint64(w.cache.data[offSlotHighwater:])

	for probeCount := range w.cache.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := w.cache.bucketsOffset + idx*16

		slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
		if slotPlusOne == 0 {
			return 0, false
		}

		if slotPlusOne == ^uint64(0) {
			continue
		}

		slotID := slotPlusOne - 1
		if slotID >= highwater {
			continue
		}

		storedHash := binary.LittleEndian.Uint64(w.cache.data[bucketOffset:])
		if storedHash != hash {
			continue
		}

		slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)

		// Use atomic load for meta for consistency with other slot reads.
		meta := atomicLoadUint64(w.cache.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			continue
		}

		slotKey := w.cache.data[slotOffset+8 : slotOffset+8+uint64(w.cache.keySize)]
		if bytes.Equal(slotKey, key) {
			return slotID, true
		}
	}

	return 0, false
}

// isKeyPresent answers whether a key is live considering buffered ops.
func (w *writer) isKeyPresent(key []byte) bool {
	for i := len(w.bufferedOps) - 1; i >= 0; i-- {
		op := w.bufferedOps[i]
		if bytes.Equal(op.key, key) {
			return op.isPut
		}
	}

	_, found := w.findLiveSlotLocked(key)

	return found
}
