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
	"time"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// Compile-time interface satisfaction checks.
var (
	_ Cache  = (*cache)(nil)
	_ Writer = (*writer)(nil)
)

// errOverlap is an internal sentinel indicating that an impossible invariant was
// detected but generation changed (or became odd), meaning the read overlapped with
// a concurrent write. Callers should retry. This is not exported; callers see ErrBusy
// after retry exhaustion.
var errOverlap = errors.New("internal: read overlapped with concurrent write")

// Retry configuration for read operations under seqlock contention.
// See TECHNICAL_DECISIONS.md §8 for rationale.
const (
	// readMaxRetries is the maximum number of retry attempts for read operations
	// before returning ErrBusy.
	readMaxRetries = 10

	// readInitialBackoff is the initial sleep duration between retry attempts.
	readInitialBackoff = 50 * time.Microsecond

	// readMaxBackoff caps the exponential backoff growth.
	readMaxBackoff = 1 * time.Millisecond
)

// readBackoff waits for an exponentially increasing duration based on the
// attempt number (0-indexed). Returns the backoff duration used.
func readBackoff(attempt int) time.Duration {
	if attempt == 0 {
		return 0 // First attempt is immediate
	}

	backoff := min(
		// Exponential: 50µs, 100µs, 200µs, ...
		readInitialBackoff<<(attempt-1), readMaxBackoff)

	<-time.After(backoff)

	return backoff
}

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

// pkgLocker is the package-level file locker for cross-process writer coordination.
// Uses fs.Real for production use with proper inode verification and EINTR handling.
var pkgLocker = fs.NewLocker(fs.NewReal())

// acquireWriterLock acquires an exclusive, non-blocking lock on the lock file.
// Returns the lock on success. On lock contention, returns ErrBusy.
func acquireWriterLock(cachePath string) (*fs.Lock, error) {
	lockPath := cachePath + ".lock"

	lock, err := pkgLocker.TryLock(lockPath)
	if err != nil {
		if errors.Is(err, fs.ErrWouldBlock) {
			return nil, ErrBusy
		}

		return nil, fmt.Errorf("acquire writer lock: %w", err)
	}

	return lock, nil
}

// releaseWriterLock releases the lock. Safe to call with nil.
// Does NOT delete the lock file (per spec: lock file persists).
func releaseWriterLock(lock *fs.Lock) {
	if lock == nil {
		return
	}

	_ = lock.Close()
}

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
		return nil, fmt.Errorf("path is required: %w", ErrInvalidInput)
	}

	if opts.KeySize < 1 {
		return nil, fmt.Errorf("key_size must be >= 1, got %d: %w", opts.KeySize, ErrInvalidInput)
	}

	if opts.IndexSize < 0 {
		return nil, fmt.Errorf("index_size must be >= 0, got %d: %w", opts.IndexSize, ErrInvalidInput)
	}

	if opts.SlotCapacity < 1 {
		return nil, fmt.Errorf("slot_capacity must be >= 1, got %d: %w", opts.SlotCapacity, ErrInvalidInput)
	}

	const maxSlotCapacity = uint64(0xFFFFFFFFFFFFFFFE)
	if opts.SlotCapacity > maxSlotCapacity {
		return nil, fmt.Errorf("slot_capacity %d exceeds maximum %d: %w", opts.SlotCapacity, maxSlotCapacity, ErrInvalidInput)
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

		return nil, fmt.Errorf("file size %d is less than header size %d: %w", size, slc1HeaderSize, ErrCorrupt)
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
		return nil, fmt.Errorf("invalid magic %q, expected SLC1: %w", headerBuf[offMagic:offMagic+4], ErrIncompatible)
	}

	// Check version.
	version := binary.LittleEndian.Uint32(headerBuf[offVersion:])
	if version != slc1Version {
		return nil, fmt.Errorf("unsupported version %d, expected %d: %w", version, slc1Version, ErrIncompatible)
	}

	// Check header size.
	headerSize := binary.LittleEndian.Uint32(headerBuf[offHeaderSize:])
	if headerSize != slc1HeaderSize {
		return nil, fmt.Errorf("unsupported header_size %d, expected %d: %w", headerSize, slc1HeaderSize, ErrIncompatible)
	}

	// Check hash algorithm.
	hashAlg := binary.LittleEndian.Uint32(headerBuf[offHashAlg:])
	if hashAlg != slc1HashAlgFNV1a64 {
		return nil, fmt.Errorf("unsupported hash_alg %d, expected %d (FNV-1a): %w", hashAlg, slc1HashAlgFNV1a64, ErrIncompatible)
	}

	// Check for unknown flags.
	flags := binary.LittleEndian.Uint32(headerBuf[offFlags:])
	if flags&^slc1FlagOrderedKeys != 0 {
		return nil, fmt.Errorf("unknown flags 0x%08x: %w", flags&^slc1FlagOrderedKeys, ErrIncompatible)
	}

	// Check reserved bytes.
	reservedU32 := binary.LittleEndian.Uint32(headerBuf[offReservedU32:])
	if reservedU32 != 0 {
		return nil, fmt.Errorf("reserved_u32 is non-zero: %w", ErrIncompatible)
	}

	if hasReservedBytesSet(headerBuf) {
		return nil, fmt.Errorf("reserved bytes are non-zero: %w", ErrIncompatible)
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
		lock, lockErr := acquireWriterLock(opts.Path)
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
			releaseWriterLock(lock)

			return nil, ErrCorrupt
		}

		freshGen := binary.LittleEndian.Uint64(fresh[offGeneration:])

		releaseWriterLock(lock)

		if freshGen%2 == 1 {
			return nil, ErrCorrupt
		}

		// Writer finished; use the fresh stable header for validation.
		headerBuf = fresh
		generation = freshGen
	}

	// Validate CRC (only after we have a stable even generation snapshot).
	if !validateHeaderCRC(headerBuf) {
		// CRC mismatch could be due to a concurrent writer that started after our initial
		// read. To avoid misclassifying transient state as corruption, check if generation
		// changed or if a writer is now active.
		_, err := handleCRCFailure(fd, generation, opts)
		if errors.Is(err, ErrCorrupt) {
			return nil, fmt.Errorf("header CRC mismatch: %w", ErrCorrupt)
		}

		return nil, err
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
		return nil, fmt.Errorf("key_size mismatch: file has %d, expected %d: %w", keySize, opts.KeySize, ErrIncompatible)
	}

	if int(indexSize) != opts.IndexSize {
		return nil, fmt.Errorf("index_size mismatch: file has %d, expected %d: %w", indexSize, opts.IndexSize, ErrIncompatible)
	}

	if userVersion != opts.UserVersion {
		return nil, fmt.Errorf("user_version mismatch: file has %d, expected %d: %w", userVersion, opts.UserVersion, ErrIncompatible)
	}

	if slotCapacity != opts.SlotCapacity {
		return nil, fmt.Errorf("slot_capacity mismatch: file has %d, expected %d: %w", slotCapacity, opts.SlotCapacity, ErrIncompatible)
	}

	if orderedKeys != opts.OrderedKeys {
		return nil, fmt.Errorf("ordered_keys mismatch: file has %v, expected %v: %w", orderedKeys, opts.OrderedKeys, ErrIncompatible)
	}

	// Validate derived slot size.
	expectedSlotSize := computeSlotSize(keySize, indexSize)
	if slotSize != expectedSlotSize {
		return nil, fmt.Errorf("slot_size mismatch: file has %d, expected %d: %w", slotSize, expectedSlotSize, ErrIncompatible)
	}

	// Structural integrity checks.
	if slotsOffset != slc1HeaderSize {
		return nil, fmt.Errorf("slots_offset %d != header_size %d: %w", slotsOffset, slc1HeaderSize, ErrCorrupt)
	}

	expectedBucketsOffset := slotsOffset + slotCapacity*uint64(slotSize)
	if bucketsOffset != expectedBucketsOffset {
		return nil, fmt.Errorf("buckets_offset %d != expected %d: %w", bucketsOffset, expectedBucketsOffset, ErrCorrupt)
	}

	expectedMinSize := safeUint64ToInt64(bucketsOffset + bucketCount*16)
	if size < expectedMinSize {
		return nil, fmt.Errorf("file size %d < minimum required %d: %w", size, expectedMinSize, ErrCorrupt)
	}

	if slotHighwater > slotCapacity {
		return nil, fmt.Errorf("slot_highwater %d > slot_capacity %d: %w", slotHighwater, slotCapacity, ErrCorrupt)
	}

	if liveCount > slotHighwater {
		return nil, fmt.Errorf("live_count %d > slot_highwater %d: %w", liveCount, slotHighwater, ErrCorrupt)
	}

	// bucket_count must be power of two >= 2.
	if bucketCount < 2 || (bucketCount&(bucketCount-1)) != 0 {
		return nil, fmt.Errorf("bucket_count %d is not a power of two >= 2: %w", bucketCount, ErrCorrupt)
	}

	if bucketUsed+bucketTombstones >= bucketCount {
		return nil, fmt.Errorf("bucket_used (%d) + bucket_tombstones (%d) >= bucket_count (%d): %w", bucketUsed, bucketTombstones, bucketCount, ErrCorrupt)
	}

	if bucketUsed != liveCount {
		return nil, fmt.Errorf("bucket_used %d != live_count %d: %w", bucketUsed, liveCount, ErrCorrupt)
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

// handleCRCFailure is called when header CRC validation fails.
// It attempts to distinguish between real corruption and transient state due to
// a concurrent writer by re-reading the generation counter.
//
// Why this matters: A reader may observe a CRC mismatch if it reads the header
// while a writer is mid-commit (torn read where generation appears even but other
// fields have been partially updated). Returning ErrCorrupt in this case is wrong;
// ErrBusy is the correct response so the caller can retry.
func handleCRCFailure(fd int, originalGen uint64, opts Options) (*cache, error) {
	// Re-read just the generation field to check if a writer became active.
	genBuf := make([]byte, 8)

	n, err := syscall.Pread(fd, genBuf, offGeneration)
	if err != nil || n != 8 {
		return nil, ErrCorrupt
	}

	currentGen := binary.LittleEndian.Uint64(genBuf)

	// If generation changed, the header we read overlapped with a commit.
	if currentGen != originalGen {
		return nil, ErrBusy
	}

	// Generation is same. If it's odd now, a writer was active during our read.
	if currentGen%2 == 1 {
		if opts.DisableLocking {
			return nil, ErrBusy
		}

		// With locking, check if writer is still active.
		lock, lockErr := acquireWriterLock(opts.Path)
		if lockErr != nil {
			if errors.Is(lockErr, ErrBusy) {
				return nil, ErrBusy
			}

			return nil, lockErr
		}

		// Lock acquired - no active writer. Re-read generation under lock.
		n, readErr := syscall.Pread(fd, genBuf, offGeneration)
		if readErr != nil || n != 8 {
			releaseWriterLock(lock)

			return nil, ErrCorrupt
		}

		freshGen := binary.LittleEndian.Uint64(genBuf)

		releaseWriterLock(lock)

		if freshGen%2 == 1 {
			// Still odd with no active writer - crashed writer.
			return nil, ErrCorrupt
		}

		// Writer finished between our reads - should retry Open.
		return nil, ErrBusy
	}

	// Generation is same even value - real corruption.
	return nil, ErrCorrupt
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

	if c.fd >= 0 {
		_ = syscall.Close(c.fd)
		c.fd = -1
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

	for attempt := range readMaxRetries {
		readBackoff(attempt)

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
		return Entry{}, false, fmt.Errorf("key length %d != key_size %d: %w", len(key), c.keySize, ErrInvalidInput)
	}

	for attempt := range readMaxRetries {
		readBackoff(attempt)

		c.registry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registry.mu.RUnlock()

			continue
		}

		entry, found, err := c.lookupKey(key, g1)
		g2 := c.readGeneration()
		c.registry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		if err != nil {
			// errOverlap means we detected an impossible invariant but generation
			// changed mid-read - treat as overlap and retry.
			if errors.Is(err, errOverlap) {
				continue
			}

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

	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0, got %d: %w", opts.Offset, ErrInvalidInput)
	}

	if opts.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0, got %d: %w", opts.Limit, ErrInvalidInput)
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

	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0, got %d: %w", opts.Offset, ErrInvalidInput)
	}

	if opts.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0, got %d: %w", opts.Limit, ErrInvalidInput)
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
// For ordered-keys mode, this uses binary search to find the start position,
// then sequential scan, stopping early when keys exceed the end bound.
func (c *cache) ScanRange(start, end []byte, opts ScanOptions) ([]Entry, error) {
	c.mu.Lock()

	if c.isClosed {
		c.mu.Unlock()

		return nil, ErrClosed
	}

	c.mu.Unlock()

	if !c.orderedKeys {
		return nil, fmt.Errorf("ScanRange requires ordered_keys mode: %w", ErrUnordered)
	}

	if opts.Offset < 0 {
		return nil, fmt.Errorf("offset must be >= 0, got %d: %w", opts.Offset, ErrInvalidInput)
	}

	if opts.Limit < 0 {
		return nil, fmt.Errorf("limit must be >= 0, got %d: %w", opts.Limit, ErrInvalidInput)
	}

	startPadded, endPadded, err := c.normalizeRangeBounds(start, end)
	if err != nil {
		return nil, err
	}

	return c.collectRangeEntries(startPadded, endPadded, opts)
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
	var lock *fs.Lock

	if !c.disableLocking {
		var err error

		lock, err = acquireWriterLock(c.path)
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
		lock:        lock,
	}
	c.activeWriter = wr

	return wr, nil
}

// binarySearchSlotGE finds the first slot index where key >= target.
// Returns highwater if all keys are less than target.
// This works correctly with tombstones because they preserve their key bytes.
// Must be called with registry.mu.RLock held.
func (c *cache) binarySearchSlotGE(target []byte, highwater uint64) uint64 {
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
func (c *cache) collectRangeEntries(startPadded, endPadded []byte, opts ScanOptions) ([]Entry, error) {
	for attempt := range readMaxRetries {
		readBackoff(attempt)

		c.registry.mu.RLock()

		g1 := c.readGeneration()
		if g1%2 == 1 {
			c.registry.mu.RUnlock()

			continue
		}

		entries, err := c.doCollectRange(startPadded, endPadded, opts)
		g2 := c.readGeneration()
		c.registry.mu.RUnlock()

		if g1 != g2 {
			continue
		}

		return entries, err
	}

	return nil, ErrBusy
}

// doCollectRange performs range scan using binary search + sequential scan.
// Must be called with registry.mu.RLock held.
func (c *cache) doCollectRange(startPadded, endPadded []byte, opts ScanOptions) ([]Entry, error) {
	highwater := c.readSlotHighwater()

	if highwater == 0 {
		return []Entry{}, nil
	}

	// Binary search to find starting position.
	// binarySearchSlotGE returns the first slot with key >= startPadded.
	var startSlot uint64
	if startPadded != nil {
		startSlot = c.binarySearchSlotGE(startPadded, highwater)
	}
	// If no start bound, startSlot remains 0.

	entries := make([]Entry, 0)

	// Sequential scan from startSlot.
	for slotID := startSlot; slotID < highwater; slotID++ {
		slotOffset := c.slotsOffset + slotID*uint64(c.slotSize)
		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]

		// Early termination: if key >= end, we're done (keys are sorted).
		if endPadded != nil && bytes.Compare(key, endPadded) >= 0 {
			break
		}

		// Check if live (not tombstoned).
		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		// Read entry data.
		keyPad := (8 - (c.keySize % 8)) % 8
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		// Use atomic load for revision to avoid torn reads during concurrent writes.
		revision := atomicLoadInt64(c.data[revOffset:])

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

// readGeneration reads the generation counter atomically.
// Uses atomic 64-bit load per spec requirement for cross-process seqlock.
func (c *cache) readGeneration() uint64 {
	return atomicLoadUint64(c.data[offGeneration:])
}

// checkInvariantViolation is called when an impossible invariant is detected during
// a read operation. Per the spec's reader coherence rule (step 4), we must re-read
// generation to determine if the violation is due to overlap with a concurrent write
// or due to real corruption.
//
// Parameters:
//   - expectedGen: the generation (g1) we read at the start of the operation
//
// Returns:
//   - errOverlap if generation changed or is now odd (caller should retry)
//   - ErrCorrupt if generation is still the same even value (real corruption)
func (c *cache) checkInvariantViolation(expectedGen uint64) error {
	gx := c.readGeneration()
	if gx != expectedGen || gx%2 == 1 {
		// Generation changed or is odd - read overlapped with a concurrent write.
		return errOverlap
	}
	// Generation is stable and even - this is real corruption.
	return ErrCorrupt
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
//
// The expectedGen parameter is the generation read at the start of the operation.
// When an impossible invariant is detected, we re-check generation to distinguish
// overlap (errOverlap) from real corruption (ErrCorrupt) per the spec's reader
// coherence rule.
func (c *cache) lookupKey(key []byte, expectedGen uint64) (Entry, bool, error) {
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
			// Impossible invariant: bucket references slot beyond highwater.
			// This could be overlap with concurrent write or real corruption.
			return Entry{}, false, c.checkInvariantViolation(expectedGen)
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
		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			// Impossible invariant: bucket points to tombstoned slot.
			// This could be overlap with concurrent write or real corruption.
			return Entry{}, false, c.checkInvariantViolation(expectedGen)
		}

		// Read entry data.
		keyPad := (8 - (c.keySize % 8)) % 8
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		// Use atomic load for revision to avoid torn reads during concurrent writes.
		revision := atomicLoadInt64(c.data[revOffset:])

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

	// Impossible invariant: probed all buckets without finding EMPTY.
	// Hash table should never be completely full (bucket_used + bucket_tombstones < bucket_count).
	// This could be overlap with concurrent write or real corruption.
	return Entry{}, false, c.checkInvariantViolation(expectedGen)
}

// collectEntries collects entries matching the predicate with seqlock retry.
func (c *cache) collectEntries(opts ScanOptions, match func([]byte) bool) ([]Entry, error) {
	for attempt := range readMaxRetries {
		readBackoff(attempt)

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

		// Use atomic load for meta to avoid torn reads during concurrent writes.
		meta := atomicLoadUint64(c.data[slotOffset:])
		if (meta & slotMetaUsed) == 0 {
			continue // tombstone
		}

		key := c.data[slotOffset+8 : slotOffset+8+uint64(c.keySize)]
		if !match(key) {
			continue
		}

		keyPad := (8 - (c.keySize % 8)) % 8
		revOffset := slotOffset + 8 + uint64(c.keySize) + uint64(keyPad)
		// Use atomic load for revision to avoid torn reads during concurrent writes.
		revision := atomicLoadInt64(c.data[revOffset:])

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
	if spec.Offset < 0 {
		return fmt.Errorf("prefix offset %d must be >= 0: %w", spec.Offset, ErrInvalidInput)
	}

	if spec.Offset >= int(c.keySize) {
		return fmt.Errorf("prefix offset %d >= key_size %d: %w", spec.Offset, c.keySize, ErrInvalidInput)
	}

	if spec.Bits < 0 {
		return fmt.Errorf("prefix bits %d must be >= 0: %w", spec.Bits, ErrInvalidInput)
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

func (c *cache) normalizeRangeBounds(start, end []byte) ([]byte, []byte, error) {
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

func (c *cache) normalizeRangeBound(bound []byte, name string) ([]byte, error) {
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
