package slotcache

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Compile-time interface satisfaction checks.
var (
	_ Cache  = (*cache)(nil)
	_ Writer = (*writer)(nil)
)

// slotRecord represents a single slot in the cache.
type slotRecord struct {
	key      string
	isLive   bool
	revision int64
	index    string
}

// persistedSlot is the gob-serializable version of slotRecord.
type persistedSlot struct {
	Key      string
	IsLive   bool
	Revision int64
	Index    string
}

// persistedState is the gob-serializable representation of the cache file.
type persistedState struct {
	KeySize      int
	IndexSize    int
	SlotCapacity uint64
	UserVersion  uint64
	OrderedKeys  bool
	Slots        []persistedSlot
}

// fileState holds the persisted state (shared across handles for the same path).
type fileState struct {
	path         string
	keySize      int
	indexSize    int
	slotCapacity uint64
	userVersion  uint64
	orderedKeys  bool
	slots        []slotRecord
	writerActive bool // in-process writer guard (per file, not per handle)
}

// cache is the concrete implementation of Cache.
type cache struct {
	file           *fileState
	isClosed       bool
	activeWriter   *writer
	disableLocking bool
}

// globalMu protects all slotcache operations.
// This is intentionally coarse-grained for Phase 1 correctness.
var globalMu sync.Mutex

// fileRegistry maps paths to their file states (simulates file persistence).
var fileRegistry sync.Map

// saveState persists the file state to disk using gob encoding.
func saveState(path string, state *fileState) error {
	// Create parent directories if needed.
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		mkdirErr := os.MkdirAll(dir, 0o750)
		if mkdirErr != nil {
			return fmt.Errorf("create directory: %w", mkdirErr)
		}
	}

	// Write to a temp file first, then rename for atomicity.
	tmpPath := path + ".tmp"

	tmpFile, createErr := os.Create(tmpPath)
	if createErr != nil {
		return fmt.Errorf("create temp file: %w", createErr)
	}

	// Convert to persisted format.
	ps := persistedState{
		KeySize:      state.keySize,
		IndexSize:    state.indexSize,
		SlotCapacity: state.slotCapacity,
		UserVersion:  state.userVersion,
		OrderedKeys:  state.orderedKeys,
		Slots:        make([]persistedSlot, len(state.slots)),
	}

	for i, slot := range state.slots {
		ps.Slots[i] = persistedSlot{
			Key:      slot.key,
			IsLive:   slot.isLive,
			Revision: slot.revision,
			Index:    slot.index,
		}
	}

	enc := gob.NewEncoder(tmpFile)

	encodeErr := enc.Encode(ps)
	if encodeErr != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)

		return fmt.Errorf("encode state: %w", encodeErr)
	}

	closeErr := tmpFile.Close()
	if closeErr != nil {
		_ = os.Remove(tmpPath)

		return fmt.Errorf("close temp file: %w", closeErr)
	}

	// Atomic rename.
	renameErr := os.Rename(tmpPath, path)
	if renameErr != nil {
		return fmt.Errorf("rename temp file: %w", renameErr)
	}

	return nil
}

// loadState reads the file state from disk.
func loadState(path string) (*fileState, error) {
	file, openErr := os.Open(path)
	if openErr != nil {
		return nil, fmt.Errorf("open file: %w", openErr)
	}

	defer func() { _ = file.Close() }()

	var ps persistedState

	dec := gob.NewDecoder(file)

	decodeErr := dec.Decode(&ps)
	if decodeErr != nil {
		return nil, fmt.Errorf("decode state: %w", decodeErr)
	}

	state := &fileState{
		path:         path,
		keySize:      ps.KeySize,
		indexSize:    ps.IndexSize,
		slotCapacity: ps.SlotCapacity,
		userVersion:  ps.UserVersion,
		orderedKeys:  ps.OrderedKeys,
		slots:        make([]slotRecord, len(ps.Slots)),
	}

	for i, slot := range ps.Slots {
		state.slots[i] = slotRecord{
			key:      slot.Key,
			isLive:   slot.IsLive,
			revision: slot.Revision,
			index:    slot.Index,
		}
	}

	return state, nil
}

// getOrCreateFile returns the file state for a path, creating it if necessary.
// Must be called with globalMu held.
func getOrCreateFile(opts Options) (*fileState, error) {
	// Try in-memory registry first (for open handles).
	if val, ok := fileRegistry.Load(opts.Path); ok {
		existing, ok := val.(*fileState)
		if !ok {
			return nil, ErrCorrupt
		}

		// Validate compatibility.
		if existing.keySize != opts.KeySize ||
			existing.indexSize != opts.IndexSize ||
			existing.slotCapacity != opts.SlotCapacity ||
			existing.userVersion != opts.UserVersion ||
			existing.orderedKeys != opts.OrderedKeys {
			return nil, ErrIncompatible
		}

		return existing, nil
	}

	// Try loading from disk.
	_, statErr := os.Stat(opts.Path)
	if statErr == nil {
		state, err := loadState(opts.Path)
		if err != nil {
			return nil, ErrCorrupt
		}

		// Validate compatibility.
		if state.keySize != opts.KeySize ||
			state.indexSize != opts.IndexSize ||
			state.slotCapacity != opts.SlotCapacity ||
			state.userVersion != opts.UserVersion ||
			state.orderedKeys != opts.OrderedKeys {
			return nil, ErrIncompatible
		}

		state.path = opts.Path
		fileRegistry.Store(opts.Path, state)

		return state, nil
	}

	// Create new.
	state := &fileState{
		path:         opts.Path,
		keySize:      opts.KeySize,
		indexSize:    opts.IndexSize,
		slotCapacity: opts.SlotCapacity,
		userVersion:  opts.UserVersion,
		orderedKeys:  opts.OrderedKeys,
	}

	// Persist to disk.
	err := saveState(opts.Path, state)
	if err != nil {
		return nil, err
	}

	fileRegistry.Store(opts.Path, state)

	return state, nil
}

// Open creates or opens a cache file with the given options.
func Open(opts Options) (Cache, error) {
	if opts.KeySize <= 0 || opts.IndexSize < 0 || opts.SlotCapacity == 0 {
		return nil, ErrInvalidInput
	}

	globalMu.Lock()
	defer globalMu.Unlock()

	file, err := getOrCreateFile(opts)
	if err != nil {
		return nil, err
	}

	return &cache{
		file:           file,
		isClosed:       false,
		disableLocking: opts.DisableLocking,
	}, nil
}

// Close closes the cache handle.
func (c *cache) Close() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return nil
	}

	if c.activeWriter != nil && !c.activeWriter.isClosed {
		return ErrBusy
	}

	c.isClosed = true

	return nil
}

// Len returns the number of live entries in the cache.
func (c *cache) Len() (int, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return 0, ErrClosed
	}

	count := 0

	for _, slot := range c.file.slots {
		if slot.isLive {
			count++
		}
	}

	return count, nil
}

// Get retrieves an entry by exact key.
func (c *cache) Get(key []byte) (Entry, bool, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return Entry{}, false, ErrClosed
	}

	err := c.validateKey(key)
	if err != nil {
		return Entry{}, false, err
	}

	idx, found := c.findLiveSlot(string(key))
	if !found {
		return Entry{}, false, nil
	}

	slot := c.file.slots[idx]

	return Entry{
		Key:      []byte(slot.key),
		Revision: slot.revision,
		Index:    []byte(slot.index),
	}, true, nil
}

// Scan iterates over all live entries.
func (c *cache) Scan(opts ScanOptions) *Cursor {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return cursorWithError(ErrClosed)
	}

	entries, err := c.collect(opts, func(_ []byte) bool { return true })

	return cursorFromEntries(entries, err)
}

// ScanPrefix iterates over live entries matching the given byte prefix.
func (c *cache) ScanPrefix(prefix []byte, opts ScanOptions) *Cursor {
	return c.ScanMatch(Prefix{Offset: 0, Bits: 0, Bytes: prefix}, opts)
}

// ScanMatch iterates over all live entries whose keys match the given prefix spec.
func (c *cache) ScanMatch(spec Prefix, opts ScanOptions) *Cursor {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return cursorWithError(ErrClosed)
	}

	validationErr := c.validatePrefixSpec(spec)
	if validationErr != nil {
		return cursorWithError(validationErr)
	}

	entries, err := c.collect(opts, func(key []byte) bool { return keyMatchesPrefix(key, spec) })

	return cursorFromEntries(entries, err)
}

// ScanRange iterates over all live entries in the half-open key range start <= key < end.
func (c *cache) ScanRange(start, end []byte, opts ScanOptions) *Cursor {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return cursorWithError(ErrClosed)
	}

	if !c.file.orderedKeys {
		return cursorWithError(ErrUnordered)
	}

	startPadded, endPadded, err := c.normalizeRangeBounds(start, end)
	if err != nil {
		return cursorWithError(err)
	}

	entries, err := c.collect(opts, func(key []byte) bool {
		if startPadded != nil && bytes.Compare(key, startPadded) < 0 {
			return false
		}

		if endPadded != nil && bytes.Compare(key, endPadded) >= 0 {
			return false
		}

		return true
	})

	return cursorFromEntries(entries, err)
}

func cursorFromEntries(entries []Entry, err error) *Cursor {
	if err != nil {
		return cursorWithError(err)
	}

	return &Cursor{
		seq: func(yield func(Entry) bool) {
			for _, entry := range entries {
				if !yield(entry) {
					return
				}
			}
		},
		err: nil,
	}
}

func cursorWithError(err error) *Cursor {
	return &Cursor{
		seq: func(func(Entry) bool) {},
		err: err,
	}
}

// BeginWrite starts a new write session.
func (c *cache) BeginWrite() (Writer, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return nil, ErrClosed
	}

	// Check if this file already has an active writer in-process.
	// This guards against multiple Cache instances for the same path.
	if c.file.writerActive {
		return nil, ErrBusy
	}

	// Acquire cross-process lock if locking is enabled.
	var lockFile *os.File

	if !c.disableLocking {
		var err error

		lockFile, err = acquireWriterLock(c.file.path)
		if err != nil {
			return nil, err
		}
	}

	c.file.writerActive = true

	wr := &writer{
		cache:       c,
		bufferedOps: nil,
		isClosed:    false,
		lockFile:    lockFile,
	}
	c.activeWriter = wr

	return wr, nil
}

// validateKey checks if a key is valid.
func (c *cache) validateKey(key []byte) error {
	if len(key) != c.file.keySize {
		return ErrInvalidInput
	}

	return nil
}

func (c *cache) validatePrefixSpec(spec Prefix) error {
	if spec.Offset < 0 || spec.Offset >= c.file.keySize {
		return ErrInvalidInput
	}

	if spec.Bits < 0 {
		return ErrInvalidInput
	}

	if spec.Bits == 0 {
		if len(spec.Bytes) == 0 {
			return ErrInvalidInput
		}

		if spec.Offset+len(spec.Bytes) > c.file.keySize {
			return ErrInvalidInput
		}

		return nil
	}

	needBytes := (spec.Bits + 7) / 8
	if needBytes == 0 {
		return ErrInvalidInput
	}

	if len(spec.Bytes) != needBytes {
		return ErrInvalidInput
	}

	if spec.Offset+needBytes > c.file.keySize {
		return ErrInvalidInput
	}

	return nil
}

func (c *cache) normalizeRangeBounds(start, end []byte) ([]byte, []byte, error) {
	startPadded, err := c.normalizeRangeBound(start)
	if err != nil {
		return nil, nil, err
	}

	endPadded, err := c.normalizeRangeBound(end)
	if err != nil {
		return nil, nil, err
	}

	if startPadded != nil && endPadded != nil && bytes.Compare(startPadded, endPadded) > 0 {
		return nil, nil, ErrInvalidInput
	}

	return startPadded, endPadded, nil
}

func (c *cache) normalizeRangeBound(bound []byte) ([]byte, error) {
	if bound == nil {
		return nil, nil
	}

	if len(bound) == 0 || len(bound) > c.file.keySize {
		return nil, ErrInvalidInput
	}

	if len(bound) == c.file.keySize {
		return append([]byte(nil), bound...), nil
	}

	padded := make([]byte, c.file.keySize)
	copy(padded, bound)

	return padded, nil
}

// findLiveSlot scans from newest to oldest to respect reinsertion semantics.
func (c *cache) findLiveSlot(key string) (int, bool) {
	for i := len(c.file.slots) - 1; i >= 0; i-- {
		slot := c.file.slots[i]
		if slot.key == key && slot.isLive {
			return i, true
		}
	}

	return 0, false
}

// collect gathers entries matching the match predicate with pagination.
func (c *cache) collect(opts ScanOptions, match func(key []byte) bool) ([]Entry, error) {
	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, ErrInvalidInput
	}

	entries := make([]Entry, 0)

	for _, slot := range c.file.slots {
		if !slot.isLive {
			continue
		}

		keyBytes := []byte(slot.key)
		if !match(keyBytes) {
			continue
		}

		indexBytes := []byte(slot.index)

		if opts.Filter != nil {
			borrowed := Entry{
				Key:      keyBytes,
				Revision: slot.revision,
				Index:    indexBytes,
			}

			if !opts.Filter(borrowed) {
				continue
			}

			keyBytes = []byte(slot.key)
			indexBytes = []byte(slot.index)
		}

		entries = append(entries, Entry{
			Key:      keyBytes,
			Revision: slot.revision,
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
