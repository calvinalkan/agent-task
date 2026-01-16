// Package model provides a deliberately simple, in-memory state model of
// slotcache's publicly observable behavior.
//
// The model is intentionally easy to audit: it favors clarity over performance
// and does not attempt to mirror the on-disk format in every detail.
package model

import (
	"slices"
	"strings"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Entry mirrors the observable data returned to callers.
type Entry struct {
	Key      []byte
	Revision int64
	Index    []byte
}

// SlotRecord represents a single append-only slot position in the file.
// Slot records are never removed because slot order and capacity are
// externally observable through Scan order and ErrFull behavior.
type SlotRecord struct {
	KeyString   string
	IsLive      bool
	Revision    int64
	IndexString string
}

// FileState is the committed state that persists across Close/Open cycles.
// Slots is append-only; tombstoned records remain to preserve history.
type FileState struct {
	KeySize      int
	IndexSize    int
	SlotCapacity uint64
	Slots        []SlotRecord
}

// CacheModel is an open handle against a FileState.
type CacheModel struct {
	File        *FileState
	IsClosed    bool
	ActiveWrite *WriterModel
}

// BufferedOperation is a buffered Put or Delete awaiting Commit.
type BufferedOperation struct {
	IsPut       bool
	KeyString   string
	Revision    int64
	IndexString string
}

// WriterModel buffers operations until Commit makes them visible.
// BufferedOps are ordered; only the final op per key is applied at Commit.
type WriterModel struct {
	Cache       *CacheModel
	IsClosed    bool
	BufferedOps []BufferedOperation
}

// NewFile validates options and returns an empty file state.
func NewFile(opts slotcache.Options) (*FileState, error) {
	if opts.KeySize <= 0 || opts.IndexSize < 0 || opts.SlotCapacity == 0 {
		return nil, slotcache.ErrInvalidInput
	}

	return &FileState{
		KeySize:      opts.KeySize,
		IndexSize:    opts.IndexSize,
		SlotCapacity: opts.SlotCapacity,
	}, nil
}

// Clone makes a deep copy so metamorphic tests can fork the exact same state.
// It preserves the nil vs empty slice distinction so cmp.Diff(original, clone)
// returns empty without requiring cmpopts.EquateEmpty().
func (file *FileState) Clone() *FileState {
	if file == nil {
		return nil
	}

	var slots []SlotRecord
	if file.Slots != nil {
		slots = make([]SlotRecord, len(file.Slots))
		copy(slots, file.Slots)
	}

	return &FileState{
		KeySize:      file.KeySize,
		IndexSize:    file.IndexSize,
		SlotCapacity: file.SlotCapacity,
		Slots:        slots,
	}
}

// Open returns a new cache handle backed by the provided file state.
func Open(file *FileState) *CacheModel {
	return &CacheModel{File: file}
}

// Close closes the cache handle unless a writer is still active.
func (cache *CacheModel) Close() error {
	if cache.IsClosed {
		return slotcache.ErrClosed
	}

	if cache.ActiveWrite != nil && !cache.ActiveWrite.IsClosed {
		return slotcache.ErrBusy
	}

	cache.IsClosed = true

	return nil
}

// Len returns the number of live (non-tombstoned) slots.
func (cache *CacheModel) Len() (int, error) {
	if cache.IsClosed {
		return 0, slotcache.ErrClosed
	}

	count := 0

	for _, slot := range cache.File.Slots {
		if slot.IsLive {
			count++
		}
	}

	return count, nil
}

// Get returns the current live entry for the exact key, if any.
func (cache *CacheModel) Get(key []byte) (Entry, bool, error) {
	if cache.IsClosed {
		return Entry{}, false, slotcache.ErrClosed
	}

	err := cache.validateKey(key)
	if err != nil {
		return Entry{}, false, err
	}

	idx, found := cache.findLiveSlot(string(key))
	if !found {
		return Entry{}, false, nil
	}

	return entryFrom(cache.File.Slots[idx]), true, nil
}

// Scan returns all live entries in slot order.
func (cache *CacheModel) Scan(opts slotcache.ScanOpts) ([]Entry, error) {
	return cache.collect("", opts)
}

// ScanPrefix returns live entries whose keys share the provided prefix.
func (cache *CacheModel) ScanPrefix(prefix []byte, opts slotcache.ScanOpts) ([]Entry, error) {
	if cache.IsClosed {
		return nil, slotcache.ErrClosed
	}

	err := cache.validatePrefix(prefix)
	if err != nil {
		return nil, err
	}

	return cache.collect(string(prefix), opts)
}

// BeginWrite starts a new write session. Only one writer may be active.
func (cache *CacheModel) BeginWrite() (*WriterModel, error) {
	if cache.IsClosed {
		return nil, slotcache.ErrClosed
	}

	if cache.ActiveWrite != nil && !cache.ActiveWrite.IsClosed {
		return nil, slotcache.ErrBusy
	}

	writer := &WriterModel{Cache: cache}
	cache.ActiveWrite = writer

	return writer, nil
}

func (cache *CacheModel) validateKey(key []byte) error {
	if key == nil || len(key) != cache.File.KeySize {
		return slotcache.ErrInvalidKey
	}

	return nil
}

func (cache *CacheModel) validatePrefix(prefix []byte) error {
	if len(prefix) == 0 || len(prefix) > cache.File.KeySize {
		return slotcache.ErrInvalidPrefix
	}

	return nil
}

// findLiveSlot scans from newest to oldest to respect reinsertion semantics.
func (cache *CacheModel) findLiveSlot(key string) (int, bool) {
	for i := len(cache.File.Slots) - 1; i >= 0; i-- {
		slot := cache.File.Slots[i]
		if slot.KeyString == key && slot.IsLive {
			return i, true
		}
	}

	return 0, false
}

func (cache *CacheModel) collect(prefix string, opts slotcache.ScanOpts) ([]Entry, error) {
	if cache.IsClosed {
		return nil, slotcache.ErrClosed
	}

	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, slotcache.ErrInvalidScanOpts
	}

	var entries []Entry

	for _, slot := range cache.File.Slots {
		if slot.IsLive && (prefix == "" || strings.HasPrefix(slot.KeyString, prefix)) {
			entries = append(entries, entryFrom(slot))
		}
	}

	if opts.Reverse {
		slices.Reverse(entries)
	}

	start := min(opts.Offset, len(entries))

	end := len(entries)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	return entries[start:end], nil
}

func entryFrom(slot SlotRecord) Entry {
	return Entry{
		Key:      []byte(slot.KeyString),
		Revision: slot.Revision,
		Index:    []byte(slot.IndexString),
	}
}

// Close is an alias for Abort.
func (writer *WriterModel) Close() error {
	return writer.Abort()
}

// Abort discards buffered operations without changing committed state.
func (writer *WriterModel) Abort() error {
	if writer.IsClosed {
		return slotcache.ErrClosed
	}

	writer.IsClosed = true
	writer.BufferedOps = nil
	writer.Cache.ActiveWrite = nil

	return nil
}

// Put buffers a Put operation and enforces slot capacity at enqueue time.
func (writer *WriterModel) Put(key []byte, revision int64, index []byte) error {
	if writer.IsClosed || writer.Cache.IsClosed {
		return slotcache.ErrClosed
	}

	err := writer.Cache.validateKey(key)
	if err != nil {
		return err
	}

	if len(index) != writer.Cache.File.IndexSize {
		return slotcache.ErrInvalidIndex
	}

	op := BufferedOperation{
		IsPut:       true,
		KeyString:   string(key),
		Revision:    revision,
		IndexString: string(index),
	}

	writer.BufferedOps = append(writer.BufferedOps, op)
	if writer.wouldExceedCapacity() {
		writer.BufferedOps = writer.BufferedOps[:len(writer.BufferedOps)-1]

		return slotcache.ErrFull
	}

	return nil
}

// Delete buffers a Delete operation and reports whether the key was live.
func (writer *WriterModel) Delete(key []byte) (bool, error) {
	if writer.IsClosed || writer.Cache.IsClosed {
		return false, slotcache.ErrClosed
	}

	err := writer.Cache.validateKey(key)
	if err != nil {
		return false, err
	}

	keyStr := string(key)
	wasPresent := writer.isKeyPresent(keyStr)
	writer.BufferedOps = append(writer.BufferedOps, BufferedOperation{
		IsPut:     false,
		KeyString: keyStr,
	})

	return wasPresent, nil
}

// Commit applies buffered operations and closes the writer session.
func (writer *WriterModel) Commit() error {
	if writer.IsClosed || writer.Cache.IsClosed {
		return slotcache.ErrClosed
	}

	if writer.wouldExceedCapacity() {
		panic("broken model: Put should have rejected operation that exceeds slot capacity")
	}

	for _, op := range writer.finalOps() {
		writer.apply(op)
	}

	writer.IsClosed = true
	writer.BufferedOps = nil
	writer.Cache.ActiveWrite = nil

	return nil
}

// apply mutates committed state according to append-only rules.
func (writer *WriterModel) apply(op BufferedOperation) {
	file := writer.Cache.File
	idx, live := writer.Cache.findLiveSlot(op.KeyString)

	if op.IsPut {
		if live {
			file.Slots[idx].Revision = op.Revision
			file.Slots[idx].IndexString = op.IndexString
		} else {
			file.Slots = append(file.Slots, SlotRecord{
				KeyString:   op.KeyString,
				IsLive:      true,
				Revision:    op.Revision,
				IndexString: op.IndexString,
			})
		}

		return
	}

	if live {
		file.Slots[idx].IsLive = false
	}
}

// finalOps returns the last operation per key, in original order.
func (writer *WriterModel) finalOps() []BufferedOperation {
	seen := make(map[string]bool)

	var ops []BufferedOperation

	for i := len(writer.BufferedOps) - 1; i >= 0; i-- {
		op := writer.BufferedOps[i]
		if seen[op.KeyString] {
			continue
		}

		seen[op.KeyString] = true
		ops = append(ops, op)
	}

	slices.Reverse(ops)

	return ops
}

// wouldExceedCapacity answers whether Commit would allocate too many slots.
func (writer *WriterModel) wouldExceedCapacity() bool {
	current := uint64(len(writer.Cache.File.Slots))
	needed := writer.newSlotsNeeded()

	return current+needed > writer.Cache.File.SlotCapacity
}

// newSlotsNeeded counts new slots for the final operation per key.
func (writer *WriterModel) newSlotsNeeded() uint64 {
	seen := make(map[string]bool)

	var count uint64

	for i := len(writer.BufferedOps) - 1; i >= 0; i-- {
		op := writer.BufferedOps[i]
		if seen[op.KeyString] {
			continue
		}

		seen[op.KeyString] = true
		if !op.IsPut {
			continue
		}

		if _, live := writer.Cache.findLiveSlot(op.KeyString); !live {
			count++
		}
	}

	return count
}

// isKeyPresent answers whether a key is live considering buffered ops.
func (writer *WriterModel) isKeyPresent(key string) bool {
	for i := len(writer.BufferedOps) - 1; i >= 0; i-- {
		op := writer.BufferedOps[i]
		if op.KeyString == key {
			return op.IsPut
		}
	}

	_, live := writer.Cache.findLiveSlot(key)

	return live
}
