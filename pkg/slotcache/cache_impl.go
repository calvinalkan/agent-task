//go:build slotcache_impl

package slotcache

import (
	"encoding/gob"
	"os"
	"path/filepath"
	"slices"
	"strings"
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
	Slots        []persistedSlot
}

// fileState holds the persisted state (shared across handles for the same path).
type fileState struct {
	path         string
	keySize      int
	indexSize    int
	slotCapacity uint64
	slots        []slotRecord
}

// cache is the concrete implementation of Cache.
type cache struct {
	file         *fileState
	isClosed     bool
	activeWriter *writer
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
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	// Write to a temp file first, then rename for atomicity.
	tmpPath := path + ".tmp"

	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}

	// Convert to persisted format.
	ps := persistedState{
		KeySize:      state.keySize,
		IndexSize:    state.indexSize,
		SlotCapacity: state.slotCapacity,
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

	enc := gob.NewEncoder(f)

	if err := enc.Encode(ps); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)

		return err
	}

	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)

		return err
	}

	// Atomic rename.
	return os.Rename(tmpPath, path)
}

// loadState reads the file state from disk.
func loadState(path string) (*fileState, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	defer func() { _ = f.Close() }()

	var ps persistedState

	dec := gob.NewDecoder(f)
	if err := dec.Decode(&ps); err != nil {
		return nil, err
	}

	state := &fileState{
		path:         path,
		keySize:      ps.KeySize,
		indexSize:    ps.IndexSize,
		slotCapacity: ps.SlotCapacity,
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
			existing.slotCapacity != opts.SlotCapacity {
			return nil, ErrIncompatible
		}

		return existing, nil
	}

	// Try loading from disk.
	if _, err := os.Stat(opts.Path); err == nil {
		state, err := loadState(opts.Path)
		if err != nil {
			return nil, ErrCorrupt
		}

		// Validate compatibility.
		if state.keySize != opts.KeySize ||
			state.indexSize != opts.IndexSize ||
			state.slotCapacity != opts.SlotCapacity {
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
	}

	// Persist to disk.
	if err := saveState(opts.Path, state); err != nil {
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
		file:     file,
		isClosed: false,
	}, nil
}

// Close closes the cache handle.
func (c *cache) Close() error {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return ErrClosed
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
func (c *cache) Scan(opts ScanOpts) (Seq, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return nil, ErrClosed
	}

	return c.collect("", opts)
}

// ScanPrefix iterates over live entries matching the given prefix.
func (c *cache) ScanPrefix(prefix []byte, opts ScanOpts) (Seq, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return nil, ErrClosed
	}

	err := c.validatePrefix(prefix)
	if err != nil {
		return nil, err
	}

	return c.collect(string(prefix), opts)
}

// BeginWrite starts a new write session.
func (c *cache) BeginWrite() (Writer, error) {
	globalMu.Lock()
	defer globalMu.Unlock()

	if c.isClosed {
		return nil, ErrClosed
	}

	if c.activeWriter != nil && !c.activeWriter.isClosed {
		return nil, ErrBusy
	}

	w := &writer{
		cache:       c,
		bufferedOps: nil,
		isClosed:    false,
	}
	c.activeWriter = w

	return w, nil
}

// validateKey checks if a key is valid.
func (c *cache) validateKey(key []byte) error {
	if key == nil || len(key) != c.file.keySize {
		return ErrInvalidKey
	}

	return nil
}

// validatePrefix checks if a prefix is valid.
func (c *cache) validatePrefix(prefix []byte) error {
	if len(prefix) == 0 || len(prefix) > c.file.keySize {
		return ErrInvalidPrefix
	}

	return nil
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

// collect gathers entries matching the prefix with pagination.
func (c *cache) collect(prefix string, opts ScanOpts) (Seq, error) {
	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, ErrInvalidScanOpts
	}

	entries := make([]Entry, 0)

	for _, slot := range c.file.slots {
		if slot.isLive && (prefix == "" || strings.HasPrefix(slot.key, prefix)) {
			entries = append(entries, Entry{
				Key:      []byte(slot.key),
				Revision: slot.revision,
				Index:    []byte(slot.index),
			})
		}
	}

	if opts.Reverse {
		slices.Reverse(entries)
	}

	start := opts.Offset
	if start > len(entries) {
		start = len(entries)
	}

	end := len(entries)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	result := entries[start:end]

	return func(yield func(Entry) bool) {
		for _, e := range result {
			if !yield(e) {
				return
			}
		}
	}, nil
}
