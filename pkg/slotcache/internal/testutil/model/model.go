// Package model provides a deliberately simple, in-memory state model of
// slotcache's publicly observable behavior.
//
// This is NOT a reference implementation of the spec. It does not implement
// the file format, hashing, or any on-disk details. Instead, it models the
// observable API behavior (Get, Put, Delete, Scan, etc.) to serve as a test
// oracle for property-based testing: the real implementation's behavior is
// compared against this model to detect discrepancies.
//
// The model is intentionally easy to audit: it favors clarity over performance
// and uses naive data structures (maps, linear scans) that are obviously correct.
package model

import (
	"bytes"
	"slices"
	"sort"

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
	UserVersion  uint64
	OrderedKeys  bool
	Slots        []SlotRecord
	UserHeader   slotcache.UserHeader // caller-owned header metadata
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
	Cache          *CacheModel
	IsClosed       bool
	ClosedByCommit bool
	BufferedOps    []BufferedOperation

	// Staged user header changes; published only on successful Commit.
	stagedFlags    uint64
	stagedData     [slotcache.UserDataSize]byte
	hasStagedFlags bool
	hasStagedData  bool
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
		UserVersion:  opts.UserVersion,
		OrderedKeys:  opts.OrderedKeys,
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
		UserVersion:  file.UserVersion,
		OrderedKeys:  file.OrderedKeys,
		Slots:        slots,
		UserHeader:   file.UserHeader, // value copy (uint64 + [64]byte)
	}
}

// Open returns a new cache handle backed by the provided file state.
func Open(file *FileState) *CacheModel {
	return &CacheModel{File: file}
}

// Close closes the cache handle unless a writer is still active.
func (cache *CacheModel) Close() error {
	if cache.IsClosed {
		return nil
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

// UserHeader returns a copy of the caller-owned header metadata.
func (cache *CacheModel) UserHeader() (slotcache.UserHeader, error) {
	if cache.IsClosed {
		return slotcache.UserHeader{}, slotcache.ErrClosed
	}

	return cache.File.UserHeader, nil
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
func (cache *CacheModel) Scan(opts slotcache.ScanOptions) ([]Entry, error) {
	return cache.collect(opts, func(_ []byte) bool { return true })
}

// ScanPrefix returns live entries whose keys share the provided byte prefix.
func (cache *CacheModel) ScanPrefix(prefix []byte, opts slotcache.ScanOptions) ([]Entry, error) {
	return cache.ScanMatch(slotcache.Prefix{Offset: 0, Bits: 0, Bytes: prefix}, opts)
}

// ScanMatch returns live entries whose keys match the provided prefix spec.
func (cache *CacheModel) ScanMatch(prefix slotcache.Prefix, opts slotcache.ScanOptions) ([]Entry, error) {
	if cache.IsClosed {
		return nil, slotcache.ErrClosed
	}

	err := cache.validatePrefixSpec(prefix)
	if err != nil {
		return nil, err
	}

	return cache.collect(opts, func(key []byte) bool { return keyMatchesPrefix(key, prefix) })
}

// ScanRange iterates over live entries in the half-open key range start <= key < end.
func (cache *CacheModel) ScanRange(start, end []byte, opts slotcache.ScanOptions) ([]Entry, error) {
	if cache.IsClosed {
		return nil, slotcache.ErrClosed
	}

	if !cache.File.OrderedKeys {
		return nil, slotcache.ErrUnordered
	}

	startPadded, endPadded, err := cache.normalizeRangeBounds(start, end)
	if err != nil {
		return nil, err
	}

	return cache.collect(opts, func(key []byte) bool {
		if startPadded != nil && bytes.Compare(key, startPadded) < 0 {
			return false
		}

		if endPadded != nil && bytes.Compare(key, endPadded) >= 0 {
			return false
		}

		return true
	})
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
	if len(key) != cache.File.KeySize {
		return slotcache.ErrInvalidInput
	}

	return nil
}

func (cache *CacheModel) validatePrefixSpec(spec slotcache.Prefix) error {
	if spec.Offset < 0 || spec.Offset >= cache.File.KeySize {
		return slotcache.ErrInvalidInput
	}

	if spec.Bits < 0 {
		return slotcache.ErrInvalidInput
	}

	if spec.Bits == 0 {
		if len(spec.Bytes) == 0 {
			return slotcache.ErrInvalidInput
		}

		if spec.Offset+len(spec.Bytes) > cache.File.KeySize {
			return slotcache.ErrInvalidInput
		}

		return nil
	}

	needBytes := (spec.Bits + 7) / 8
	if needBytes == 0 {
		return slotcache.ErrInvalidInput
	}

	if len(spec.Bytes) != needBytes {
		return slotcache.ErrInvalidInput
	}

	if spec.Offset+needBytes > cache.File.KeySize {
		return slotcache.ErrInvalidInput
	}

	return nil
}

func (cache *CacheModel) normalizeRangeBounds(start, end []byte) ([]byte, []byte, error) {
	startPadded, err := cache.normalizeRangeBound(start)
	if err != nil {
		return nil, nil, err
	}

	endPadded, err := cache.normalizeRangeBound(end)
	if err != nil {
		return nil, nil, err
	}

	if startPadded != nil && endPadded != nil && bytes.Compare(startPadded, endPadded) > 0 {
		return nil, nil, slotcache.ErrInvalidInput
	}

	return startPadded, endPadded, nil
}

func (cache *CacheModel) normalizeRangeBound(bound []byte) ([]byte, error) {
	if bound == nil {
		return nil, nil
	}

	if len(bound) == 0 || len(bound) > cache.File.KeySize {
		return nil, slotcache.ErrInvalidInput
	}

	if len(bound) == cache.File.KeySize {
		return append([]byte(nil), bound...), nil
	}

	padded := make([]byte, cache.File.KeySize)
	copy(padded, bound)

	return padded, nil
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

func (cache *CacheModel) collect(opts slotcache.ScanOptions, match func(key []byte) bool) ([]Entry, error) {
	if cache.IsClosed {
		return nil, slotcache.ErrClosed
	}

	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, slotcache.ErrInvalidInput
	}

	var entries []Entry

	for _, slot := range cache.File.Slots {
		if !slot.IsLive {
			continue
		}

		keyBytes := []byte(slot.KeyString)
		if !match(keyBytes) {
			continue
		}

		indexBytes := []byte(slot.IndexString)

		if opts.Filter != nil {
			borrowed := slotcache.Entry{
				Key:      keyBytes,
				Revision: slot.Revision,
				Index:    indexBytes,
			}

			if !opts.Filter(borrowed) {
				continue
			}

			keyBytes = []byte(slot.KeyString)
			indexBytes = []byte(slot.IndexString)
		}

		entries = append(entries, Entry{
			Key:      keyBytes,
			Revision: slot.Revision,
			Index:    indexBytes,
		})
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

// Close releases resources and discards uncommitted changes.
//
// Close is idempotent: calling Close multiple times (including after Commit)
// returns nil. Always call Close, even after [WriterModel.Commit].
func (writer *WriterModel) Close() {
	if writer.IsClosed {
		// Idempotent: no-op if already closed.
		return
	}

	writer.IsClosed = true
	writer.ClosedByCommit = false
	writer.BufferedOps = nil
	writer.Cache.ActiveWrite = nil
}

// Put buffers a Put operation.
func (writer *WriterModel) Put(key []byte, revision int64, index []byte) error {
	if writer.IsClosed || writer.Cache.IsClosed {
		return slotcache.ErrClosed
	}

	err := writer.Cache.validateKey(key)
	if err != nil {
		return err
	}

	if len(index) != writer.Cache.File.IndexSize {
		return slotcache.ErrInvalidInput
	}

	writer.BufferedOps = append(writer.BufferedOps, BufferedOperation{
		IsPut:       true,
		KeyString:   string(key),
		Revision:    revision,
		IndexString: string(index),
	})

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

// SetUserHeaderFlags stages a change to the user header flags.
//
// The new value is published atomically on [WriterModel.Commit].
// If Commit fails (e.g., ErrFull), the change is discarded.
// Setting flags does not affect the user data bytes.
func (writer *WriterModel) SetUserHeaderFlags(flags uint64) error {
	if writer.IsClosed || writer.Cache.IsClosed {
		return slotcache.ErrClosed
	}

	writer.stagedFlags = flags
	writer.hasStagedFlags = true

	return nil
}

// SetUserHeaderData stages a change to the user header data.
//
// The new value is published atomically on [WriterModel.Commit].
// If Commit fails (e.g., ErrFull), the change is discarded.
// Setting data does not affect the user flags.
func (writer *WriterModel) SetUserHeaderData(data [slotcache.UserDataSize]byte) error {
	if writer.IsClosed || writer.Cache.IsClosed {
		return slotcache.ErrClosed
	}

	writer.stagedData = data
	writer.hasStagedData = true

	return nil
}

// Commit applies buffered operations and closes the writer session.
func (writer *WriterModel) Commit() error {
	if writer.IsClosed || writer.Cache.IsClosed {
		return slotcache.ErrClosed
	}

	finalOps := writer.finalOps()
	if writer.wouldExceedCapacity() {
		writer.closeLocked()

		return slotcache.ErrFull
	}

	if writer.Cache.File.OrderedKeys {
		err := writer.applyOrdered(finalOps)
		if err != nil {
			writer.closeLocked()

			return err
		}

		writer.publishUserHeader()
		writer.closeLocked()

		return nil
	}

	for _, op := range finalOps {
		writer.apply(op)
	}

	writer.publishUserHeader()
	writer.closeLocked()

	return nil
}

// publishUserHeader applies staged user header changes to the file state.
// Called only on successful Commit. Preserves the other field when only one is updated.
func (writer *WriterModel) publishUserHeader() {
	if writer.hasStagedFlags {
		writer.Cache.File.UserHeader.Flags = writer.stagedFlags
	}

	if writer.hasStagedData {
		writer.Cache.File.UserHeader.Data = writer.stagedData
	}
}

func (writer *WriterModel) closeLocked() {
	writer.IsClosed = true
	writer.ClosedByCommit = true
	writer.BufferedOps = nil
	writer.Cache.ActiveWrite = nil
}

func (writer *WriterModel) applyOrdered(finalOps []BufferedOperation) error {
	var inserts []BufferedOperation

	for _, op := range finalOps {
		if !op.IsPut {
			continue
		}

		if _, live := writer.Cache.findLiveSlot(op.KeyString); live {
			continue // update
		}

		inserts = append(inserts, op)
	}

	if len(inserts) > 0 && len(writer.Cache.File.Slots) > 0 {
		tailKey := writer.Cache.File.Slots[len(writer.Cache.File.Slots)-1].KeyString

		minNewKey := inserts[0].KeyString
		for _, op := range inserts[1:] {
			if op.KeyString < minNewKey {
				minNewKey = op.KeyString
			}
		}

		if minNewKey < tailKey {
			return slotcache.ErrOutOfOrderInsert
		}
	}

	sort.Slice(inserts, func(i, j int) bool {
		return inserts[i].KeyString < inserts[j].KeyString
	})

	for _, op := range finalOps {
		if op.IsPut {
			if _, live := writer.Cache.findLiveSlot(op.KeyString); !live {
				continue // insert handled later
			}
		}

		writer.apply(op)
	}

	for _, op := range inserts {
		writer.apply(op)
	}

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

func keyMatchesPrefix(key []byte, spec slotcache.Prefix) bool {
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
