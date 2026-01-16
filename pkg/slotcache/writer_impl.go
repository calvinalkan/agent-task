//go:build slotcache_impl

package slotcache

import (
	"slices"
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
	cache       *cache
	bufferedOps []bufferedOp
	isClosed    bool
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
		return ErrInvalidIndex
	}

	op := bufferedOp{
		isPut:    true,
		key:      string(key),
		revision: revision,
		index:    string(index),
	}

	w.bufferedOps = append(w.bufferedOps, op)
	if w.wouldExceedCapacity() {
		w.bufferedOps = w.bufferedOps[:len(w.bufferedOps)-1]

		return ErrFull
	}

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
	w.bufferedOps = append(w.bufferedOps, bufferedOp{
		isPut: false,
		key:   keyStr,
	})

	return wasPresent, nil
}

// Commit applies all buffered operations atomically.
func (w *writer) Commit() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if w.isClosed || w.cache.isClosed {
		return ErrClosed
	}

	for _, op := range w.finalOps() {
		w.apply(op)
	}

	w.isClosed = true
	w.bufferedOps = nil
	w.cache.activeWriter = nil

	return nil
}

// Abort discards all buffered operations.
func (w *writer) Abort() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if w.isClosed {
		return ErrClosed
	}

	w.isClosed = true
	w.bufferedOps = nil
	w.cache.activeWriter = nil

	return nil
}

// Close is an alias for Abort.
func (w *writer) Close() error {
	return w.Abort()
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
