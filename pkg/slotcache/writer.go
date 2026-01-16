package slotcache

import (
	"os"
	"slices"
	"sort"
)

// bufferedOp represents a buffered Put or Delete operation.
type bufferedOp struct {
	isPut    bool
	key      string
	revision int64
	index    string
}

// writer is the concrete implementation of Writer.
type writer struct {
	cache          *cache
	bufferedOps    []bufferedOp
	isClosed       bool
	closedByCommit bool
	lockFile       *os.File
}

// Put buffers a put operation for the given key.
func (w *writer) Put(key []byte, revision int64, index []byte) error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return ErrClosed
	}

	err := w.cache.validateKey(key)
	if err != nil {
		return err
	}

	if len(index) != w.cache.file.indexSize {
		return ErrInvalidInput
	}

	w.bufferedOps = append(w.bufferedOps, bufferedOp{
		isPut:    true,
		key:      string(key),
		revision: revision,
		index:    string(index),
	})

	return nil
}

// Delete buffers a delete operation for the given key.
func (w *writer) Delete(key []byte) (bool, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return false, ErrClosed
	}

	err := w.cache.validateKey(key)
	if err != nil {
		return false, err
	}

	keyStr := string(key)
	wasPresent := w.isKeyPresent(keyStr)
	w.bufferedOps = append(w.bufferedOps, bufferedOp{isPut: false, key: keyStr})

	return wasPresent, nil
}

// Commit applies all buffered operations atomically.
func (w *writer) Commit() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return ErrClosed
	}

	finalOps := w.finalOps()

	if w.wouldExceedCapacity() {
		w.closeByCommit()

		return ErrFull
	}

	if w.cache.file.orderedKeys {
		err := w.applyOrdered(finalOps)
		if err != nil {
			w.closeByCommit()

			return err
		}
	} else {
		for _, op := range finalOps {
			w.apply(op)
		}
	}

	// Persist to disk.
	err := saveState(w.cache.file.path, w.cache.file)
	if err != nil {
		w.closeByCommit()

		return err
	}

	w.closeByCommit()

	return nil
}

// Close releases resources and discards uncommitted changes.
//
// Close is idempotent: calling Close multiple times (including after Commit)
// returns nil. Always call Close, even after [Writer.Commit].
func (w *writer) Close() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if w.isClosed {
		// Idempotent: return nil even if closed by Commit.
		return nil
	}

	w.isClosed = true
	w.closedByCommit = false
	w.bufferedOps = nil
	w.cache.activeWriter = nil

	// Release both guards (idempotent).
	if w.cache != nil && w.cache.file != nil {
		w.cache.file.writerActive = false
	}

	releaseWriterLock(w.lockFile)
	w.lockFile = nil

	return nil
}

func (w *writer) closeByCommit() {
	w.isClosed = true
	w.closedByCommit = true
	w.bufferedOps = nil
	w.cache.activeWriter = nil

	// Release both guards (idempotent).
	if w.cache != nil && w.cache.file != nil {
		w.cache.file.writerActive = false
	}

	releaseWriterLock(w.lockFile)
	w.lockFile = nil
}

// apply mutates committed state according to append-only rules.
func (w *writer) apply(op bufferedOp) {
	idx, live := w.cache.findLiveSlot(op.key)

	if op.isPut {
		if live {
			w.cache.file.slots[idx].revision = op.revision
			w.cache.file.slots[idx].index = op.index
		} else {
			w.cache.file.slots = append(w.cache.file.slots, slotRecord{
				key:      op.key,
				isLive:   true,
				revision: op.revision,
				index:    op.index,
			})
		}

		return
	}

	if live {
		w.cache.file.slots[idx].isLive = false
	}
}

func (w *writer) applyOrdered(finalOps []bufferedOp) error {
	var inserts []bufferedOp

	for _, op := range finalOps {
		if !op.isPut {
			continue
		}

		if _, live := w.cache.findLiveSlot(op.key); live {
			continue // update
		}

		inserts = append(inserts, op)
	}

	if len(inserts) > 0 && len(w.cache.file.slots) > 0 {
		tailKey := w.cache.file.slots[len(w.cache.file.slots)-1].key

		minNewKey := inserts[0].key
		for _, op := range inserts[1:] {
			if op.key < minNewKey {
				minNewKey = op.key
			}
		}

		if minNewKey < tailKey {
			return ErrOutOfOrderInsert
		}
	}

	sort.Slice(inserts, func(i, j int) bool {
		return inserts[i].key < inserts[j].key
	})

	for _, op := range finalOps {
		if op.isPut {
			if _, live := w.cache.findLiveSlot(op.key); !live {
				continue // insert handled later
			}
		}

		w.apply(op)
	}

	for _, op := range inserts {
		w.apply(op)
	}

	return nil
}

// finalOps returns the last operation per key, in original order.
func (w *writer) finalOps() []bufferedOp {
	seen := make(map[string]bool)

	var ops []bufferedOp

	for i := len(w.bufferedOps) - 1; i >= 0; i-- {
		op := w.bufferedOps[i]
		if seen[op.key] {
			continue
		}

		seen[op.key] = true
		ops = append(ops, op)
	}

	slices.Reverse(ops)

	return ops
}

// wouldExceedCapacity answers whether Commit would allocate too many slots.
func (w *writer) wouldExceedCapacity() bool {
	current := uint64(len(w.cache.file.slots))
	needed := w.newSlotsNeeded()

	return current+needed > w.cache.file.slotCapacity
}

// newSlotsNeeded counts new slots for the final operation per key.
func (w *writer) newSlotsNeeded() uint64 {
	seen := make(map[string]bool)

	var count uint64

	for i := len(w.bufferedOps) - 1; i >= 0; i-- {
		op := w.bufferedOps[i]
		if seen[op.key] {
			continue
		}

		seen[op.key] = true

		if !op.isPut {
			continue
		}

		if _, live := w.cache.findLiveSlot(op.key); !live {
			count++
		}
	}

	return count
}

// isKeyPresent answers whether a key is live considering buffered ops.
func (w *writer) isKeyPresent(key string) bool {
	for i := len(w.bufferedOps) - 1; i >= 0; i-- {
		op := w.bufferedOps[i]
		if op.key == key {
			return op.isPut
		}
	}

	_, live := w.cache.findLiveSlot(key)

	return live
}
