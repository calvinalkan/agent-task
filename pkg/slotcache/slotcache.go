package slotcache

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
)

// Compile-time interface satisfaction checks.
var (
	_ Cache  = (*cache)(nil)
	_ Writer = (*writer)(nil)
)

// Default load factor for bucket sizing.
const defaultLoadFactor = 0.5

// fileIdentity uniquely identifies a file by device and inode.
type fileIdentity struct {
	dev uint64
	ino uint64
}

// fileRegistryEntry tracks per-file state for in-process coordination.
type fileRegistryEntry struct {
	mu           sync.RWMutex // protects mmap reads vs writes
	writerActive bool         // in-process writer guard
}

// globalRegistry maps file identities to their entries.
var globalRegistry sync.Map // map[fileIdentity]*fileRegistryEntry

// getFileIdentity returns the device and inode for a file.
func getFileIdentity(fd int) (fileIdentity, error) {
	var stat syscall.Stat_t

	err := syscall.Fstat(fd, &stat)
	if err != nil {
		return fileIdentity{}, err
	}

	return fileIdentity{dev: stat.Dev, ino: stat.Ino}, nil
}

// getOrCreateRegistryEntry gets or creates a registry entry for the given identity.
func getOrCreateRegistryEntry(id fileIdentity) *fileRegistryEntry {
	if val, ok := globalRegistry.Load(id); ok {
		if entry, typeOk := val.(*fileRegistryEntry); typeOk {
			return entry
		}
	}

	entry := &fileRegistryEntry{}
	actual, _ := globalRegistry.LoadOrStore(id, entry)

	if resultEntry, typeOk := actual.(*fileRegistryEntry); typeOk {
		return resultEntry
	}

	// Fallback: should never happen if we're consistent.
	return entry
}

// cache is the concrete implementation of Cache.
type cache struct {
	mu sync.Mutex // protects cache-level state (closed, activeWriter)

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

	// File identity for registry coordination
	identity fileIdentity
	registry *fileRegistryEntry

	// State
	isClosed       bool
	activeWriter   *writer
	disableLocking bool
	path           string
	writeback      WritebackMode
}

// Open creates or opens a cache file with the given options.
func Open(opts Options) (Cache, error) {
	// Validate options.
	if opts.Path == "" {
		return nil, ErrInvalidInput
	}

	if opts.KeySize < 1 {
		return nil, ErrInvalidInput
	}

	if opts.IndexSize < 0 {
		return nil, ErrInvalidInput
	}

	if opts.SlotCapacity < 1 {
		return nil, ErrInvalidInput
	}

	const maxSlotCapacity = uint64(0xFFFFFFFFFFFFFFFE)
	if opts.SlotCapacity > maxSlotCapacity {
		return nil, ErrInvalidInput
	}

	// Try to open existing file.
	fd, err := syscall.Open(opts.Path, syscall.O_RDWR, 0)
	if err != nil {
		if !errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("open file: %w", err)
		}
		// File doesn't exist - create it.
		return createNewCache(opts)
	}

	// File exists - check size and validate.
	var stat syscall.Stat_t

	statErr := syscall.Fstat(fd, &stat)
	if statErr != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("stat file: %w", statErr)
	}

	size := stat.Size
	if size == 0 {
		// Empty file - initialize in place.
		_ = syscall.Close(fd)

		return initializeEmptyFile(opts)
	}

	if size < slc1HeaderSize {
		_ = syscall.Close(fd)

		return nil, ErrCorrupt
	}

	// Read and validate header.
	headerBuf := make([]byte, slc1HeaderSize)

	n, err := syscall.Pread(fd, headerBuf, 0)
	if err != nil || n != slc1HeaderSize {
		_ = syscall.Close(fd)

		return nil, ErrCorrupt
	}

	c, err := validateAndOpenExisting(fd, headerBuf, size, opts)
	if err != nil {
		_ = syscall.Close(fd)

		return nil, err
	}

	return c, nil
}

// createNewCache creates a new cache file using temp + rename.
func createNewCache(opts Options) (Cache, error) {
	dir := filepath.Dir(opts.Path)
	if dir == "" {
		dir = "."
	}

	// Create parent directories if needed.
	mkdirErr := os.MkdirAll(dir, 0o750)
	if mkdirErr != nil {
		return nil, fmt.Errorf("create directory: %w", mkdirErr)
	}

	// Create temp file with random suffix.
	randBytes := make([]byte, 8)
	_, _ = rand.Read(randBytes) // Ignore error; best-effort randomness.
	tmpPath := fmt.Sprintf("%s.tmp.%x", opts.Path, randBytes)

	fd, createErr := syscall.Open(tmpPath, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL, 0o600)
	if createErr != nil {
		return nil, fmt.Errorf("create temp file: %w", createErr)
	}

	// Calculate file size.
	header := newHeader(
		safeIntToUint32(opts.KeySize),
		safeIntToUint32(opts.IndexSize),
		opts.SlotCapacity,
		opts.UserVersion,
		defaultLoadFactor,
		opts.OrderedKeys,
	)
	fileSize := safeUint64ToInt64(header.BucketsOffset + header.BucketCount*16)

	// Truncate to full size (sparse file).
	truncErr := syscall.Ftruncate(fd, fileSize)
	if truncErr != nil {
		_ = syscall.Close(fd)
		_ = syscall.Unlink(tmpPath)

		return nil, fmt.Errorf("ftruncate: %w", truncErr)
	}

	// Write header.
	headerBuf := encodeHeader(&header)

	_, writeErr := syscall.Pwrite(fd, headerBuf, 0)
	if writeErr != nil {
		_ = syscall.Close(fd)
		_ = syscall.Unlink(tmpPath)

		return nil, fmt.Errorf("write header: %w", writeErr)
	}

	// Sync header.
	syncErr := syscall.Fsync(fd)
	if syncErr != nil {
		_ = syscall.Close(fd)
		_ = syscall.Unlink(tmpPath)

		return nil, fmt.Errorf("fsync: %w", syncErr)
	}

	_ = syscall.Close(fd)

	// Atomic rename.
	renameErr := syscall.Rename(tmpPath, opts.Path)
	if renameErr != nil {
		_ = syscall.Unlink(tmpPath)

		return nil, fmt.Errorf("rename: %w", renameErr)
	}

	// Now open the renamed file.
	fd, openErr := syscall.Open(opts.Path, syscall.O_RDWR, 0)
	if openErr != nil {
		return nil, fmt.Errorf("open after rename: %w", openErr)
	}

	return mmapAndCreateCache(fd, fileSize, &header, opts)
}

// initializeEmptyFile initializes a 0-byte file in place.
func initializeEmptyFile(opts Options) (Cache, error) {
	fd, openErr := syscall.Open(opts.Path, syscall.O_RDWR, 0)
	if openErr != nil {
		return nil, fmt.Errorf("open empty file: %w", openErr)
	}

	header := newHeader(
		safeIntToUint32(opts.KeySize),
		safeIntToUint32(opts.IndexSize),
		opts.SlotCapacity,
		opts.UserVersion,
		defaultLoadFactor,
		opts.OrderedKeys,
	)
	fileSize := safeUint64ToInt64(header.BucketsOffset + header.BucketCount*16)

	truncErr := syscall.Ftruncate(fd, fileSize)
	if truncErr != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("ftruncate: %w", truncErr)
	}

	headerBuf := encodeHeader(&header)

	_, writeErr := syscall.Pwrite(fd, headerBuf, 0)
	if writeErr != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("write header: %w", writeErr)
	}

	syncErr := syscall.Fsync(fd)
	if syncErr != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("fsync: %w", syncErr)
	}

	return mmapAndCreateCache(fd, fileSize, &header, opts)
}

// validateAndOpenExisting validates header and opens existing file.
func validateAndOpenExisting(fd int, headerBuf []byte, size int64, opts Options) (*cache, error) {
	// Check magic.
	if !bytes.Equal(headerBuf[offMagic:offMagic+4], []byte("SLC1")) {
		return nil, ErrIncompatible
	}

	// Check version.
	version := binary.LittleEndian.Uint32(headerBuf[offVersion:])
	if version != slc1Version {
		return nil, ErrIncompatible
	}

	// Check header size.
	headerSize := binary.LittleEndian.Uint32(headerBuf[offHeaderSize:])
	if headerSize != slc1HeaderSize {
		return nil, ErrIncompatible
	}

	// Check hash algorithm.
	hashAlg := binary.LittleEndian.Uint32(headerBuf[offHashAlg:])
	if hashAlg != slc1HashAlgFNV1a64 {
		return nil, ErrIncompatible
	}

	// Check for unknown flags.
	flags := binary.LittleEndian.Uint32(headerBuf[offFlags:])
	if flags&^slc1FlagOrderedKeys != 0 {
		return nil, ErrIncompatible
	}

	// Check reserved bytes.
	reservedU32 := binary.LittleEndian.Uint32(headerBuf[offReservedU32:])
	if reservedU32 != 0 {
		return nil, ErrIncompatible
	}

	if hasReservedBytesSet(headerBuf) {
		return nil, ErrIncompatible
	}

	// If generation is odd, a writer is in progress or a previous writer crashed.
	//
	// IMPORTANT: we must handle this *before* CRC/invariant validation, because during an
	// in-progress commit the header may be temporarily inconsistent (CRC mismatch, counters
	// out of sync). In that case, Open must return ErrBusy (writer active) rather than
	// misclassifying transient state as ErrCorrupt.
	generation := binary.LittleEndian.Uint64(headerBuf[offGeneration:])
	if generation%2 == 1 {
		if opts.DisableLocking {
			// Without locking, we can't distinguish active writer vs crashed writer.
			return nil, ErrBusy
		}

		// With locking enabled, attempt to acquire the writer lock non-blocking.
		// - If the lock is busy: an active writer is present -> ErrBusy.
		// - If we can acquire the lock: no active writer -> likely crashed writer.
		lockFile, lockErr := tryAcquireWriterLock(opts.Path)
		if lockErr != nil {
			// We treat lock contention as busy. Unexpected lock errors are returned as-is.
			if errors.Is(lockErr, ErrBusy) {
				return nil, ErrBusy
			}

			return nil, lockErr
		}

		// We acquired the lock. Re-read the header under exclusive access to avoid
		// races with a writer that finished between our initial read and lock acquisition.
		// If generation is still odd, treat as crashed/incomplete commit.
		fresh := make([]byte, slc1HeaderSize)

		n, readErr := syscall.Pread(fd, fresh, 0)
		if readErr != nil || n != slc1HeaderSize {
			releaseWriterLock(lockFile)

			return nil, ErrCorrupt
		}

		freshGen := binary.LittleEndian.Uint64(fresh[offGeneration:])

		releaseWriterLock(lockFile)

		if freshGen%2 == 1 {
			return nil, ErrCorrupt
		}

		// Writer finished; use the fresh stable header for validation.
		headerBuf = fresh
		generation = freshGen
	}

	// Validate CRC (only after we have a stable even generation snapshot).
	if !validateHeaderCRC(headerBuf) {
		return nil, ErrCorrupt
	}

	// Read config fields.
	keySize := binary.LittleEndian.Uint32(headerBuf[offKeySize:])
	indexSize := binary.LittleEndian.Uint32(headerBuf[offIndexSize:])
	slotSize := binary.LittleEndian.Uint32(headerBuf[offSlotSize:])
	slotCapacity := binary.LittleEndian.Uint64(headerBuf[offSlotCapacity:])
	userVersion := binary.LittleEndian.Uint64(headerBuf[offUserVersion:])
	bucketCount := binary.LittleEndian.Uint64(headerBuf[offBucketCount:])
	slotsOffset := binary.LittleEndian.Uint64(headerBuf[offSlotsOffset:])
	bucketsOffset := binary.LittleEndian.Uint64(headerBuf[offBucketsOffset:])
	slotHighwater := binary.LittleEndian.Uint64(headerBuf[offSlotHighwater:])
	liveCount := binary.LittleEndian.Uint64(headerBuf[offLiveCount:])
	bucketUsed := binary.LittleEndian.Uint64(headerBuf[offBucketUsed:])
	bucketTombstones := binary.LittleEndian.Uint64(headerBuf[offBucketTombstones:])
	orderedKeys := (flags & slc1FlagOrderedKeys) != 0

	// Check config compatibility.
	if int(keySize) != opts.KeySize {
		return nil, ErrIncompatible
	}

	if int(indexSize) != opts.IndexSize {
		return nil, ErrIncompatible
	}

	if userVersion != opts.UserVersion {
		return nil, ErrIncompatible
	}

	if slotCapacity != opts.SlotCapacity {
		return nil, ErrIncompatible
	}

	if orderedKeys != opts.OrderedKeys {
		return nil, ErrIncompatible
	}

	// Validate derived slot size.
	expectedSlotSize := computeSlotSize(keySize, indexSize)
	if slotSize != expectedSlotSize {
		return nil, ErrIncompatible
	}

	// Structural integrity checks.
	if slotsOffset != slc1HeaderSize {
		return nil, ErrCorrupt
	}

	expectedBucketsOffset := slotsOffset + slotCapacity*uint64(slotSize)
	if bucketsOffset != expectedBucketsOffset {
		return nil, ErrCorrupt
	}

	expectedMinSize := safeUint64ToInt64(bucketsOffset + bucketCount*16)
	if size < expectedMinSize {
		return nil, ErrCorrupt
	}

	if slotHighwater > slotCapacity {
		return nil, ErrCorrupt
	}

	if liveCount > slotHighwater {
		return nil, ErrCorrupt
	}

	// bucket_count must be power of two >= 2.
	if bucketCount < 2 || (bucketCount&(bucketCount-1)) != 0 {
		return nil, ErrCorrupt
	}

	if bucketUsed+bucketTombstones >= bucketCount {
		return nil, ErrCorrupt
	}

	if bucketUsed != liveCount {
		return nil, ErrCorrupt
	}

	// Check generation.
	if generation%2 == 1 {
		// Odd generation - check if we can prove crashed writer.
		if !opts.DisableLocking {
			// Try to acquire lock.
			lockFile, err := tryAcquireWriterLock(opts.Path)
			if err == nil {
				// Lock acquired - crashed writer.
				releaseWriterLock(lockFile)

				return nil, ErrCorrupt
			}
			// Lock busy - active writer.
			return nil, ErrBusy
		}
		// Locking disabled - can't distinguish.
		return nil, ErrBusy
	}

	// Build header struct for mmapAndCreateCache.
	header := slc1Header{
		KeySize:       keySize,
		IndexSize:     indexSize,
		SlotSize:      slotSize,
		SlotCapacity:  slotCapacity,
		UserVersion:   userVersion,
		BucketCount:   bucketCount,
		SlotsOffset:   slotsOffset,
		BucketsOffset: bucketsOffset,
		Flags:         flags,
	}

	return mmapAndCreateCache(fd, size, &header, opts)
}

// mmapAndCreateCache mmaps the file and creates a cache instance.
func mmapAndCreateCache(fd int, size int64, header *slc1Header, opts Options) (*cache, error) {
	// Get file identity for registry.
	identity, err := getFileIdentity(fd)
	if err != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("get file identity: %w", err)
	}

	// mmap the file.
	data, err := syscall.Mmap(fd, 0, int(size), syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("mmap: %w", err)
	}

	registry := getOrCreateRegistryEntry(identity)

	return &cache{
		fd:             fd,
		data:           data,
		fileSize:       size,
		keySize:        header.KeySize,
		indexSize:      header.IndexSize,
		slotSize:       header.SlotSize,
		slotCapacity:   header.SlotCapacity,
		userVersion:    header.UserVersion,
		slotsOffset:    header.SlotsOffset,
		bucketsOffset:  header.BucketsOffset,
		bucketCount:    header.BucketCount,
		orderedKeys:    (header.Flags & slc1FlagOrderedKeys) != 0,
		identity:       identity,
		registry:       registry,
		isClosed:       false,
		disableLocking: opts.DisableLocking,
		path:           opts.Path,
		writeback:      opts.Writeback,
	}, nil
}

// Close closes the cache handle.
func (c *cache) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isClosed {
		return nil
	}

	if c.activeWriter != nil && !c.activeWriter.isClosed {
		return ErrBusy
	}

	c.isClosed = true

	if c.data != nil {
		_ = syscall.Munmap(c.data)
		c.data = nil
	}

	if c.fd != 0 {
		_ = syscall.Close(c.fd)
		c.fd = 0
	}

	return nil
}

// Len returns the number of live entries in the cache.
func (c *cache) Len() (int, error) {
	c.mu.Lock()

	if c.isClosed {
		c.mu.Unlock()

		return 0, ErrClosed
	}

	c.mu.Unlock()

	const maxRetries = 10
	for range maxRetries {
		c.registry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registry.mu.RUnlock()

			continue
		}

		count := c.readLiveCount()
		g2 := c.readGeneration()
		c.registry.mu.RUnlock()

		if g1 == g2 {
			return safeUint64ToInt(count), nil
		}
	}

	return 0, ErrBusy
}

// Get retrieves an entry by exact key.
func (c *cache) Get(key []byte) (Entry, bool, error) {
	c.mu.Lock()

	if c.isClosed {
		c.mu.Unlock()

		return Entry{}, false, ErrClosed
	}

	c.mu.Unlock()

	if len(key) != int(c.keySize) {
		return Entry{}, false, ErrInvalidInput
	}

	const maxRetries = 10
	for range maxRetries {
		c.registry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registry.mu.RUnlock()

			continue
		}

		entry, found, err := c.lookupKey(key)
		g2 := c.readGeneration()
		c.registry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		if err != nil {
			return Entry{}, false, err
		}

		return entry, found, nil
	}

	return Entry{}, false, ErrBusy
}

// Scan returns all live entries in insertion (slot) order.
func (c *cache) Scan(opts ScanOptions) ([]Entry, error) {
	c.mu.Lock()

	if c.isClosed {
		c.mu.Unlock()

		return nil, ErrClosed
	}

	c.mu.Unlock()

	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, ErrInvalidInput
	}

	return c.collectEntries(opts, func(_ []byte) bool { return true })
}

// ScanPrefix returns live entries matching the given byte prefix at offset 0.
func (c *cache) ScanPrefix(prefix []byte, opts ScanOptions) ([]Entry, error) {
	return c.ScanMatch(Prefix{Offset: 0, Bits: 0, Bytes: prefix}, opts)
}

// ScanMatch returns all live entries whose keys match the given prefix spec.
func (c *cache) ScanMatch(spec Prefix, opts ScanOptions) ([]Entry, error) {
	c.mu.Lock()

	if c.isClosed {
		c.mu.Unlock()

		return nil, ErrClosed
	}

	c.mu.Unlock()

	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, ErrInvalidInput
	}

	validationErr := c.validatePrefixSpec(spec)
	if validationErr != nil {
		return nil, validationErr
	}

	return c.collectEntries(opts, func(key []byte) bool {
		return keyMatchesPrefix(key, spec)
	})
}

// ScanRange returns all live entries in the half-open key range start <= key < end.
func (c *cache) ScanRange(start, end []byte, opts ScanOptions) ([]Entry, error) {
	c.mu.Lock()

	if c.isClosed {
		c.mu.Unlock()

		return nil, ErrClosed
	}

	c.mu.Unlock()

	if !c.orderedKeys {
		return nil, ErrUnordered
	}

	if opts.Offset < 0 || opts.Limit < 0 {
		return nil, ErrInvalidInput
	}

	startPadded, endPadded, err := c.normalizeRangeBounds(start, end)
	if err != nil {
		return nil, err
	}

	return c.collectEntries(opts, func(key []byte) bool {
		if startPadded != nil && bytes.Compare(key, startPadded) < 0 {
			return false
		}

		if endPadded != nil && bytes.Compare(key, endPadded) >= 0 {
			return false
		}

		return true
	})
}

// BeginWrite starts a new write session.
func (c *cache) BeginWrite() (Writer, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isClosed {
		return nil, ErrClosed
	}

	// Check in-process writer guard.
	c.registry.mu.Lock()

	if c.registry.writerActive {
		c.registry.mu.Unlock()

		return nil, ErrBusy
	}

	c.registry.writerActive = true
	c.registry.mu.Unlock()

	// Acquire cross-process lock if enabled.
	var lockFile *os.File

	if !c.disableLocking {
		var err error

		lockFile, err = acquireWriterLock(c.path)
		if err != nil {
			c.registry.mu.Lock()
			c.registry.writerActive = false
			c.registry.mu.Unlock()

			return nil, err
		}
	}

	wr := &writer{
		cache:       c,
		bufferedOps: nil,
		isClosed:    false,
		lockFile:    lockFile,
	}
	c.activeWriter = wr

	return wr, nil
}

// readGeneration reads the generation counter atomically.
func (c *cache) readGeneration() uint64 {
	return binary.LittleEndian.Uint64(c.data[offGeneration:])
}

// readLiveCount reads the live_count from header.
func (c *cache) readLiveCount() uint64 {
	return binary.LittleEndian.Uint64(c.data[offLiveCount:])
}

// readSlotHighwater reads slot_highwater from header.
func (c *cache) readSlotHighwater() uint64 {
	return binary.LittleEndian.Uint64(c.data[offSlotHighwater:])
}

// lookupKey finds a key in the bucket index and returns the entry.
// Must be called with registry.mu.RLock held.
func (c *cache) lookupKey(key []byte) (Entry, bool, error) {
	hash := fnv1a64(key)
	mask := c.bucketCount - 1
	startIdx := hash & mask
	highwater := c.readSlotHighwater()

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
			return Entry{}, false, ErrCorrupt
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
		meta := binary.LittleEndian.Uint64(c.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			// Slot is tombstoned but bucket points to it - corruption.
			return Entry{}, false, ErrCorrupt
		}

		// Read entry data.
		keyPad := (8 - (c.keySize % 8)) % 8
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		revision := getInt64LE(c.data[revOffset : revOffset+8])

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

	// Probed all buckets - no EMPTY found - corruption.
	return Entry{}, false, ErrCorrupt
}

// collectEntries collects entries matching the predicate with seqlock retry.
func (c *cache) collectEntries(opts ScanOptions, match func([]byte) bool) ([]Entry, error) {
	const maxRetries = 10

	for range maxRetries {
		c.registry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registry.mu.RUnlock()

			continue
		}

		entries, err := c.doCollect(opts, match)
		g2 := c.readGeneration()
		c.registry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		return entries, err
	}

	return nil, ErrBusy
}

// doCollect performs the actual slot scan.
// Must be called with registry.mu.RLock held.
func (c *cache) doCollect(opts ScanOptions, match func([]byte) bool) ([]Entry, error) {
	highwater := c.readSlotHighwater()
	entries := make([]Entry, 0)

	for slotID := range highwater {
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)

		meta := binary.LittleEndian.Uint64(c.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]
		if !match(key) {
			continue
		}

		keyPad := (8 - (c.keySize % 8)) % 8
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		revision := getInt64LE(c.data[revOffset : revOffset+8])

		var index []byte

		if c.indexSize > 0 {
			idxOffset := revOffset + 8
			index = make([]byte, c.indexSize)
			copy(index, c.data[idxOffset:idxOffset+uint64(c.indexSize)])
		}

		// Create borrowed entry for filter.
		borrowed := Entry{
			Key:      key,
			Revision: revision,
			Index:    index,
		}

		if opts.Filter != nil && !opts.Filter(borrowed) {
			continue
		}

		// Create owned copies for result.
		keyCopy := make([]byte, c.keySize)
		copy(keyCopy, key)

		var indexCopy []byte
		if c.indexSize > 0 {
			indexCopy = make([]byte, c.indexSize)
			copy(indexCopy, c.data[revOffset+8:revOffset+8+uint64(c.indexSize)])
		}

		entries = append(entries, Entry{
			Key:      keyCopy,
			Revision: revision,
			Index:    indexCopy,
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

func (c *cache) validatePrefixSpec(spec Prefix) error {
	if spec.Offset < 0 || spec.Offset >= int(c.keySize) {
		return ErrInvalidInput
	}

	if spec.Bits < 0 {
		return ErrInvalidInput
	}

	if spec.Bits == 0 {
		if len(spec.Bytes) == 0 {
			return ErrInvalidInput
		}

		if spec.Offset+len(spec.Bytes) > int(c.keySize) {
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

	if spec.Offset+needBytes > int(c.keySize) {
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

	if len(bound) == 0 || len(bound) > int(c.keySize) {
		return nil, ErrInvalidInput
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
