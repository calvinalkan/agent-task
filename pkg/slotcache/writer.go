package slotcache

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"sort"
	"sync/atomic"

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

// Writer is a buffered write session for modifying the cache.
//
// Operations are buffered in memory and applied atomically on [Writer.Commit].
// If the same key is modified multiple times, the last operation wins.
//
// Writer methods are NOT thread-safe. Always call [Writer.Close] to release resources.
//
// A Writer must be obtained via [Cache.Writer]; the zero value is not usable.
type Writer struct {
	_ [0]func() // prevent external construction

	cache       *Cache
	bufferedOps []bufferedOp
	isClosed    atomic.Bool // atomic for safe concurrent Close() during Commit()
	lock        *fs.Lock

	// User header staging fields.
	// Changes are buffered here and only applied during Commit() if dirty.
	// This ensures preflight failures (ErrFull, ErrOutOfOrderInsert) don't
	// publish partial header changes.
	pendingUserFlags uint64
	userFlagsDirty   bool
	pendingUserData  [UserDataSize]byte
	userDataDirty    bool

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

// Put stages an upsert operation.
//
// If the key exists, the entry is updated. Otherwise, a new entry is
// allocated at commit time.
//
// Possible errors: [ErrClosed], [ErrInvalidInput], [ErrBufferFull].
func (w *Writer) Put(key []byte, revision int64, index []byte) error {
	w.cache.mu.RLock()
	defer w.cache.mu.RUnlock()

	// cache.isClosed check is defensive: cache.Close() returns ErrBusy if a
	// writer is active, so this shouldn't happen in normal operation.
	if w.isClosed.Load() || w.cache.isClosed {
		return ErrClosed
	}

	if len(key) != int(w.cache.keySize) {
		return fmt.Errorf("key length %d != key_size %d: %w", len(key), w.cache.keySize, ErrInvalidInput)
	}

	if len(index) != int(w.cache.indexSize) {
		return fmt.Errorf("index length %d != index_size %d: %w", len(index), w.cache.indexSize, ErrInvalidInput)
	}

	if len(w.bufferedOps) >= maxBufferedOpsPerWriter {
		return fmt.Errorf("buffered ops (%d) reached limit (%d): %w",
			len(w.bufferedOps), maxBufferedOpsPerWriter, ErrBufferFull)
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

// Delete stages a deletion.
//
// Returns true if the key exists (considering buffered ops), false otherwise.
//
// Possible errors: [ErrClosed], [ErrInvalidInput], [ErrBufferFull].
func (w *Writer) Delete(key []byte) (bool, error) {
	w.cache.mu.RLock()
	defer w.cache.mu.RUnlock()

	// cache.isClosed check is defensive: cache.Close() returns ErrBusy if a
	// writer is active, so this shouldn't happen in normal operation.
	if w.isClosed.Load() || w.cache.isClosed {
		return false, ErrClosed
	}

	if len(key) != int(w.cache.keySize) {
		return false, fmt.Errorf("key length %d != key_size %d: %w", len(key), w.cache.keySize, ErrInvalidInput)
	}

	if len(w.bufferedOps) >= maxBufferedOpsPerWriter {
		return false, fmt.Errorf("buffered ops (%d) reached limit (%d): %w",
			len(w.bufferedOps), maxBufferedOpsPerWriter, ErrBufferFull)
	}

	keyCopy := make([]byte, len(key))
	copy(keyCopy, key)

	wasPresent, err := w.isKeyPresent(keyCopy)
	if err != nil {
		return false, err
	}

	w.bufferedOps = append(w.bufferedOps, bufferedOp{isPut: false, key: keyCopy})

	return wasPresent, nil
}

// Commit applies all buffered operations atomically.
//
// After success, changes are visible to readers. If [WritebackSync] is
// enabled, changes are also durable.
//
// After Commit, further operations return [ErrClosed].
//
// Possible errors: [ErrClosed], [ErrFull], [ErrOutOfOrderInsert], [ErrWriteback], [ErrCorrupt], [ErrInvalidated].
func (w *Writer) Commit() error {
	w.cache.mu.RLock()
	defer w.cache.mu.RUnlock()

	// =========================================================================
	// Phase 1: Preflight checks (no registry entry lock)
	// =========================================================================

	// cache.isClosed check is defensive: cache.Close() returns ErrBusy if a
	// writer is active, so this shouldn't happen in normal operation.
	if w.isClosed.Load() || w.cache.isClosed {
		return ErrClosed
	}

	// Check for invalidation before applying changes.
	state := binary.LittleEndian.Uint32(w.cache.data[offState:])
	if state == stateInvalidated {
		w.closeLocked()

		return ErrInvalidated
	}

	// Initialize dirty range tracking for WritebackSync optimization.
	w.resetDirtyTracking()

	// Compute final ops (last-wins per key).
	finalOps := w.finalOps()

	// Categorize operations based on DISK state only (not buffered ops).
	// This determines whether each Put is an update (key exists) or insert (new key).
	var updates, inserts, deletes []bufferedOp

	for _, op := range finalOps {
		_, found, err := w.findLiveSlotLocked(op.key)
		if err != nil {
			w.closeLocked()

			return err
		}

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

	// Capacity check: ensure we have room for new inserts.
	highwater := w.cache.readSlotHighwater()

	newInserts := uint64(len(inserts))
	if highwater+newInserts > w.cache.slotCapacity {
		w.closeLocked()

		return fmt.Errorf("slot_highwater (%d) + new_inserts (%d) > slot_capacity (%d): %w",
			highwater, newInserts, w.cache.slotCapacity, ErrFull)
	}

	// Ordered-keys mode: verify new inserts maintain sorted order.
	if w.cache.orderedKeys && len(inserts) > 0 {
		sort.Slice(inserts, func(i, j int) bool {
			return bytes.Compare(inserts[i].key, inserts[j].key) < 0
		})

		minNewKey := inserts[0].key

		if highwater > 0 {
			tailSlotOffset := w.cache.slotsOffset + (highwater-1)*uint64(w.cache.slotSize)
			tailKey := w.cache.data[tailSlotOffset+8 : tailSlotOffset+8+uint64(w.cache.keySize)]

			if bytes.Compare(minNewKey, tailKey) < 0 {
				w.closeLocked()

				return fmt.Errorf("new key %x < tail key %x at slot %d: %w",
					minNewKey, tailKey, highwater-1, ErrOutOfOrderInsert)
			}
		}
	}

	// =========================================================================
	// Phase 2: Publish changes (registry entry lock held)
	// =========================================================================

	var msyncFailed bool

	syncMode := w.cache.writeback == WritebackSync

	w.cache.registryEntry.mu.Lock()

	// Step 1: Publish odd generation (signals write-in-progress to readers).
	oldGen := w.cache.readGeneration()
	newOddGen := oldGen + 1
	atomicStoreUint64(w.cache.data[offGeneration:], newOddGen)

	// Step 2 (WritebackSync only): msync header to ensure odd generation is on
	// disk before data modifications. This prevents partial writes from appearing
	// committed after a crash.
	if syncMode {
		err := msyncRange(w.cache.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	// Step 3a: Apply updates (modify existing slots in place).
	for _, op := range updates {
		slotID, found, err := w.findLiveSlotLocked(op.key)
		if err != nil {
			w.cache.registryEntry.mu.Unlock()
			w.closeLocked()

			return err
		}

		if found {
			err := w.updateSlot(slotID, op.revision, op.index)
			if err != nil {
				w.cache.registryEntry.mu.Unlock()
				w.closeLocked()

				return err
			}
		}
	}

	// Step 3b: Apply deletes (tombstone slots and bucket entries).
	deleteCount := uint64(0)

	for _, op := range deletes {
		slotID, found, err := w.findLiveSlotLocked(op.key)
		if err != nil {
			w.cache.registryEntry.mu.Unlock()
			w.closeLocked()

			return err
		}

		if !found {
			continue
		}

		err = w.deleteSlot(slotID, op.key)
		if err != nil {
			w.cache.registryEntry.mu.Unlock()
			w.closeLocked()

			return err
		}

		deleteCount++
	}

	// Step 3c: Apply inserts (allocate new slots, insert into hash index).
	filledTombstones := uint64(0)
	slotID := highwater

	for _, op := range inserts {
		filled, err := w.insertSlot(slotID, op.key, op.revision, op.index)
		if err != nil {
			w.cache.registryEntry.mu.Unlock()
			w.closeLocked()

			return err
		}

		if filled {
			filledTombstones++
		}

		slotID++
	}

	// Step 3d: Update header counters.
	oldLiveCount := binary.LittleEndian.Uint64(w.cache.data[offLiveCount:])
	if oldLiveCount < deleteCount {
		w.cache.registryEntry.mu.Unlock()
		w.closeLocked()

		return fmt.Errorf("live_count underflow (old=%d deletes=%d): %w", oldLiveCount, deleteCount, ErrCorrupt)
	}

	newLiveCount := oldLiveCount - deleteCount + uint64(len(inserts))
	binary.LittleEndian.PutUint64(w.cache.data[offLiveCount:], newLiveCount)
	binary.LittleEndian.PutUint64(w.cache.data[offBucketUsed:], newLiveCount)

	newHighwater := highwater + uint64(len(inserts))
	binary.LittleEndian.PutUint64(w.cache.data[offSlotHighwater:], newHighwater)

	oldTombstones := binary.LittleEndian.Uint64(w.cache.data[offBucketTombstones:])
	newTombstones := oldTombstones + deleteCount

	if newTombstones < filledTombstones {
		w.cache.registryEntry.mu.Unlock()
		w.closeLocked()

		return fmt.Errorf("bucket_tombstones underflow (old=%d deletes=%d filled=%d): %w",
			oldTombstones, deleteCount, filledTombstones, ErrCorrupt)
	}

	newTombstones -= filledTombstones
	binary.LittleEndian.PutUint64(w.cache.data[offBucketTombstones:], newTombstones)

	// Step 3e: Rehash if tombstone ratio exceeds threshold.
	if w.cache.bucketCount > 0 && float64(newTombstones)/float64(w.cache.bucketCount) > rehashThreshold {
		w.rehashBuckets(newHighwater)
		binary.LittleEndian.PutUint64(w.cache.data[offBucketTombstones:], 0)
	}

	// Step 3f: Apply user header changes (if any were staged).
	if w.userFlagsDirty {
		binary.LittleEndian.PutUint64(w.cache.data[offUserFlags:], w.pendingUserFlags)
	}

	if w.userDataDirty {
		copy(w.cache.data[offUserData:offUserData+UserDataSize], w.pendingUserData[:])
	}

	// Step 4: Recompute header CRC.
	crc := computeHeaderCRC(w.cache.data[:slc1HeaderSize])
	binary.LittleEndian.PutUint32(w.cache.data[offHeaderCRC32C:], crc)

	// Step 5 (WritebackSync only): msync modified data before publishing.
	if syncMode {
		// Header (contains generation, counters, CRC).
		err := msyncRange(w.cache.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}

		// Dirty slot range.
		if w.minSlotOffset >= 0 {
			err := msyncRange(w.cache.data, w.minSlotOffset, w.maxSlotOffset-w.minSlotOffset)
			if err != nil {
				msyncFailed = true
			}
		}

		// Buckets: all if rehash occurred, otherwise just dirty range.
		if w.rehashOccurred {
			bucketsStart, err1 := uint64ToIntChecked(w.cache.bucketsOffset)
			bucketsLen, err2 := uint64ToIntChecked(w.cache.bucketCount * 16)

			if err1 != nil || err2 != nil {
				msyncFailed = true
			} else {
				err := msyncRange(w.cache.data, bucketsStart, bucketsLen)
				if err != nil {
					msyncFailed = true
				}
			}
		} else if w.minBucketOffset >= 0 {
			err := msyncRange(w.cache.data, w.minBucketOffset, w.maxBucketOffset-w.minBucketOffset)
			if err != nil {
				msyncFailed = true
			}
		}
	}

	// Step 6: Publish even generation (makes changes visible to readers).
	newEvenGen := newOddGen + 1
	atomicStoreUint64(w.cache.data[offGeneration:], newEvenGen)

	// Step 7 (WritebackSync only): msync header to ensure even generation is durable.
	if syncMode {
		err := msyncRange(w.cache.data, 0, slc1HeaderSize)
		if err != nil {
			msyncFailed = true
		}
	}

	w.cache.registryEntry.mu.Unlock()

	// =========================================================================
	// Phase 3: Cleanup
	// =========================================================================

	w.closeLocked()

	if msyncFailed {
		return ErrWriteback
	}

	return nil
}

// Close releases resources and discards uncommitted changes.
//
// Close is idempotent. Always call Close, even after [Writer.Commit].
func (w *Writer) Close() error {
	if w == nil {
		return ErrClosed
	}

	if w.isClosed.Load() {
		return nil
	}

	w.closeLocked()

	return nil
}

// SetUserHeaderFlags stages a change to the user header flags.
//
// The new value is published atomically on [Writer.Commit].
// If Commit fails (e.g., [ErrFull]), the change is discarded.
// Setting flags does not affect the user data bytes.
//
// Possible errors: [ErrClosed], [ErrInvalidated].
func (w *Writer) SetUserHeaderFlags(flags uint64) error {
	w.cache.mu.RLock()
	defer w.cache.mu.RUnlock()

	// cache.isClosed check is defensive: cache.Close() returns ErrBusy if a
	// writer is active, so this shouldn't happen in normal operation.
	if w.isClosed.Load() || w.cache.isClosed {
		return ErrClosed
	}

	// Check if cache is invalidated.
	state := binary.LittleEndian.Uint32(w.cache.data[offState:])
	if state == stateInvalidated {
		return ErrInvalidated
	}

	w.pendingUserFlags = flags
	w.userFlagsDirty = true

	return nil
}

// SetUserHeaderData stages a change to the user header data.
//
// The new value is published atomically on [Writer.Commit].
// If Commit fails (e.g., [ErrFull]), the change is discarded.
// Setting data does not affect the user flags.
//
// Possible errors: [ErrClosed], [ErrInvalidated].
func (w *Writer) SetUserHeaderData(data [UserDataSize]byte) error {
	w.cache.mu.RLock()
	defer w.cache.mu.RUnlock()

	// cache.isClosed check is defensive: cache.Close() returns ErrBusy if a
	// writer is active, so this shouldn't happen in normal operation.
	if w.isClosed.Load() || w.cache.isClosed {
		return ErrClosed
	}

	// Check if cache is invalidated.
	state := binary.LittleEndian.Uint32(w.cache.data[offState:])
	if state == stateInvalidated {
		return ErrInvalidated
	}

	w.pendingUserData = data
	w.userDataDirty = true

	return nil
}

// resetDirtyTracking initializes dirty range tracking for a new commit.
func (w *Writer) resetDirtyTracking() {
	w.minSlotOffset = -1
	w.maxSlotOffset = -1
	w.minBucketOffset = -1
	w.maxBucketOffset = -1
	w.rehashOccurred = false
}

// markSlotDirty expands the dirty slot range to include the given slot.
// File layout is validated at open time to fit in int, so conversions should
// not overflow. Returns an error if conversion fails (indicates corruption or
// a bug in validation logic).
func (w *Writer) markSlotDirty(slotID uint64) error {
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)
	slotEnd := slotOffset + uint64(w.cache.slotSize)

	off, err1 := uint64ToIntChecked(slotOffset)
	end, err2 := uint64ToIntChecked(slotEnd)

	if err1 != nil || err2 != nil {
		return fmt.Errorf("markSlotDirty: slot %d offset overflow (offset=%d, end=%d): %w",
			slotID, slotOffset, slotEnd, ErrCorrupt)
	}

	if w.minSlotOffset < 0 || off < w.minSlotOffset {
		w.minSlotOffset = off
	}

	if end > w.maxSlotOffset {
		w.maxSlotOffset = end
	}

	return nil
}

// markBucketDirty expands the dirty bucket range to include the given bucket.
// File layout is validated at open time to fit in int, so conversions should
// not overflow. Returns an error if conversion fails (indicates corruption or
// a bug in validation logic).
func (w *Writer) markBucketDirty(bucketIdx uint64) error {
	bucketOffset := w.cache.bucketsOffset + bucketIdx*16
	bucketEnd := bucketOffset + 16

	off, err1 := uint64ToIntChecked(bucketOffset)
	end, err2 := uint64ToIntChecked(bucketEnd)

	if err1 != nil || err2 != nil {
		return fmt.Errorf("markBucketDirty: bucket %d offset overflow (offset=%d, end=%d): %w",
			bucketIdx, bucketOffset, bucketEnd, ErrCorrupt)
	}

	if w.minBucketOffset < 0 || off < w.minBucketOffset {
		w.minBucketOffset = off
	}

	if end > w.maxBucketOffset {
		w.maxBucketOffset = end
	}

	return nil
}

// updateSlot updates an existing slot with new revision and index.
func (w *Writer) updateSlot(slotID uint64, revision int64, index []byte) error {
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)
	keyPad := (8 - (w.cache.keySize % 8)) % 8
	revOffset := slotOffset + 8 + uint64(w.cache.keySize) + uint64(keyPad)

	// Use atomic store for revision to ensure readers see complete values.
	atomicStoreInt64(w.cache.data[revOffset:], revision)

	if w.cache.indexSize > 0 {
		idxOffset := revOffset + 8
		copy(w.cache.data[idxOffset:idxOffset+uint64(w.cache.indexSize)], index)
	}

	return w.markSlotDirty(slotID)
}

// deleteSlot marks a slot as tombstoned and updates the bucket index.
func (w *Writer) deleteSlot(slotID uint64, key []byte) error {
	// Find and tombstone the bucket entry first. If the bucket entry cannot be
	// found, the file is corrupt (a live slot must be reachable via the bucket
	// probe sequence) and we must not silently proceed.
	hash := fnv1a64(key)
	mask := w.cache.bucketCount - 1
	startIdx := hash & mask

	for probeCount := range w.cache.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := w.cache.bucketsOffset + idx*16

		slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
		if slotPlusOne == 0 {
			// Hit EMPTY: key is not findable via the probe sequence.
			return fmt.Errorf("delete: bucket entry not found for slot_id %d: %w", slotID, ErrCorrupt)
		}

		if slotPlusOne == ^uint64(0) {
			// TOMBSTONE - continue.
			continue
		}

		if slotPlusOne-1 == slotID {
			// Found our bucket entry - tombstone it.
			binary.LittleEndian.PutUint64(w.cache.data[bucketOffset+8:], ^uint64(0))

			err := w.markBucketDirty(idx)
			if err != nil {
				return err
			}

			// Clear USED bit. Use atomic store to ensure readers see complete values.
			slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)
			atomicStoreUint64(w.cache.data[slotOffset:], 0)

			return w.markSlotDirty(slotID)
		}
	}

	// Probed all buckets without finding the entry.
	return fmt.Errorf("delete: bucket entry not found for slot_id %d (no EMPTY buckets): %w", slotID, ErrCorrupt)
}

// insertSlot allocates a new slot and inserts into the bucket index.
//
// Returns whether the insertion filled a TOMBSTONE bucket.
func (w *Writer) insertSlot(slotID uint64, key []byte, revision int64, index []byte) (bool, error) {
	// Find an available bucket first so we never silently "fall through" and
	// publish a slot that isn't reachable via the hash index.
	hash := fnv1a64(key)
	mask := w.cache.bucketCount - 1
	startIdx := hash & mask

	var (
		foundIdx         uint64
		foundSlotPlusOne uint64
		found            bool
	)

	for probeCount := range w.cache.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := w.cache.bucketsOffset + idx*16

		slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
		if slotPlusOne == 0 || slotPlusOne == ^uint64(0) {
			foundIdx = idx
			foundSlotPlusOne = slotPlusOne
			found = true

			break
		}
	}

	if !found {
		return false, fmt.Errorf("insert: hash table has no EMPTY/TOMBSTONE buckets: %w", ErrCorrupt)
	}

	// Write slot.
	slotOffset := w.cache.slotsOffset + slotID*uint64(w.cache.slotSize)

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

	err := w.markSlotDirty(slotID)
	if err != nil {
		return false, err
	}

	// Insert into the bucket index.
	bucketOffset := w.cache.bucketsOffset + foundIdx*16
	binary.LittleEndian.PutUint64(w.cache.data[bucketOffset:], hash)
	binary.LittleEndian.PutUint64(w.cache.data[bucketOffset+8:], slotID+1)

	err = w.markBucketDirty(foundIdx)
	if err != nil {
		return false, err
	}

	return foundSlotPlusOne == ^uint64(0), nil
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
func (w *Writer) rehashBuckets(highwater uint64) {
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

	// Mark that all buckets were touched for WritebackSync optimization.
	w.rehashOccurred = true
}

// closeLocked releases writer resources and clears the in-process writer guard.
// Acquires fileRegistryEntry.mu internally to clear activeWriter.
func (w *Writer) closeLocked() {
	w.isClosed.Store(true)
	w.bufferedOps = nil

	// Release in-process guard.
	w.cache.registryEntry.mu.Lock()
	w.cache.registryEntry.activeWriter = nil
	w.cache.registryEntry.mu.Unlock()

	// Release file lock.
	releaseWriteLock(w.lock)
	w.lock = nil
}

// finalOps returns the last operation per key, in original order.
func (w *Writer) finalOps() []bufferedOp {
	lastIndex := make(map[string]int)

	for i, op := range w.bufferedOps {
		lastIndex[string(op.key)] = i
	}

	ops := make([]bufferedOp, 0, len(lastIndex))

	for i, op := range w.bufferedOps {
		if lastIndex[string(op.key)] == i {
			ops = append(ops, op)
		}
	}

	return ops
}

// findLiveSlotLocked finds a live slot in the file.
// Used during commit when we need the actual slot ID.
// Returns an error if a corrupt bucket entry is encountered (slot ID >= highwater).
func (w *Writer) findLiveSlotLocked(key []byte) (uint64, bool, error) {
	hash := fnv1a64(key)
	mask := w.cache.bucketCount - 1
	startIdx := hash & mask
	highwater := binary.LittleEndian.Uint64(w.cache.data[offSlotHighwater:])

	for probeCount := range w.cache.bucketCount {
		idx := (startIdx + probeCount) & mask
		bucketOffset := w.cache.bucketsOffset + idx*16

		slotPlusOne := binary.LittleEndian.Uint64(w.cache.data[bucketOffset+8:])
		if slotPlusOne == 0 {
			return 0, false, nil
		}

		if slotPlusOne == ^uint64(0) {
			continue
		}

		slotID := slotPlusOne - 1
		if slotID >= highwater {
			return 0, false, fmt.Errorf("bucket %d references slot_id %d >= highwater %d: %w",
				idx, slotID, highwater, ErrCorrupt)
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
			return slotID, true, nil
		}
	}

	return 0, false, nil
}

// isKeyPresent answers whether a key is live considering buffered ops.
// Returns an error if corruption is detected while probing the hash table.
func (w *Writer) isKeyPresent(key []byte) (bool, error) {
	for i := len(w.bufferedOps) - 1; i >= 0; i-- {
		op := w.bufferedOps[i]
		if bytes.Equal(op.key, key) {
			return op.isPut, nil
		}
	}

	_, found, err := w.findLiveSlotLocked(key)

	return found, err
}
