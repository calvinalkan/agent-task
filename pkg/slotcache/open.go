package slotcache

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"os"
	"path/filepath"
	"syscall"
)

// WritebackMode controls durability guarantees for [Writer.Commit].
type WritebackMode int

const (
	// WritebackNone provides no durability guarantees.
	//
	// Changes are visible to other processes immediately but may be lost
	// on power failure. This is the default and fastest mode.
	WritebackNone WritebackMode = iota

	// WritebackSync ensures changes are durable before Commit returns.
	//
	// After a crash, the cache is either in its previous state or detected
	// as corrupt (triggering [ErrCorrupt] on next [Open]).
	WritebackSync
)

// Options configures opening or creating a cache file.
type Options struct {
	// Path is the filesystem path to the cache file.
	//
	// Required. A lock file may also be created at Path+".lock".
	Path string

	// KeySize is the fixed size in bytes for all keys.
	//
	// Must be >= 1. All keys must have exactly this length.
	KeySize int

	// IndexSize is the fixed size in bytes for index data per entry.
	//
	// May be 0 if no index data is needed.
	IndexSize int

	// UserVersion is a caller-defined version for schema compatibility.
	//
	// If the persisted value doesn't match, [Open] returns [ErrIncompatible].
	// Increment this when your index byte encoding changes.
	UserVersion uint64

	// SlotCapacity is the maximum number of entries the cache can hold.
	//
	// Must be >= 1. Fixed at creation time.
	// When exhausted, [Writer.Commit] returns [ErrFull].
	SlotCapacity uint64

	// OrderedKeys enables ordered-keys mode.
	//
	// When enabled:
	//   - New inserts must be in non-decreasing key order
	//   - [Cache.ScanRange] becomes available
	//   - Commits that violate ordering return [ErrOutOfOrderInsert]
	//
	// Fixed at creation time.
	OrderedKeys bool

	// Writeback controls durability guarantees for commit.
	//
	// Default is [WritebackNone].
	Writeback WritebackMode

	// DisableLocking disables interprocess writer locking.
	//
	// When true, no lock file is used. The caller MUST provide equivalent
	// external synchronization.
	//
	// Use only when slotcache is embedded inside another component that
	// already coordinates access.
	DisableLocking bool
}

// Open opens or creates a cache file at the given path.
//
// If the file does not exist, it is created with the specified options.
// If it exists, the options must match the file's configuration.
//
// The returned Cache must be closed with [Cache.Close] when no longer needed.
//
// Possible errors:
//   - [ErrInvalidInput]: invalid options (path, key_size, index_size, slot_capacity, writeback)
//   - [ErrIncompatible]: file format mismatch (magic, version, flags, config)
//   - [ErrCorrupt]: file is corrupted (bad header, invalid counters, CRC mismatch)
//   - [ErrBusy]: writer is active (odd generation) or lock contention
//   - [ErrInvalidated]: cache was previously invalidated
//   - syscall errors: file I/O failures (open, stat, read, mmap, mkdir, etc.)
func Open(opts Options) (*Cache, error) {
	// 64-bit required: The cross-process seqlock uses atomic 64-bit load/store
	// on the generation counter. On 32-bit platforms, atomic 64-bit ops may not
	// be available or may require alignment guarantees we can't ensure via mmap.
	if !is64Bit {
		return nil, errors.New("slotcache requires 64-bit architecture")
	}

	// Little-endian required: The file format stores all integers as little-endian.
	// For most fields we use encoding/binary which handles conversion explicitly.
	// However, atomic ops (generation, slot meta, revision) use native CPU byte
	// order via unsafe pointer casts - there's no atomic little-endian load/store.
	// On big-endian CPUs, these would misinterpret the file data.
	if !isLittleEndian {
		return nil, errors.New("slotcache requires little-endian CPU (x86_64, arm64)")
	}

	// Validate options.
	if opts.Path == "" {
		return nil, fmt.Errorf("path is required: %w", ErrInvalidInput)
	}

	if opts.KeySize < 1 {
		return nil, fmt.Errorf("key_size must be >= 1, got %d: %w", opts.KeySize, ErrInvalidInput)
	}

	if opts.KeySize > maxKeySizeBytes {
		return nil, fmt.Errorf("key_size %d exceeds max %d: %w", opts.KeySize, maxKeySizeBytes, ErrInvalidInput)
	}

	if opts.IndexSize < 0 {
		return nil, fmt.Errorf("index_size must be >= 0, got %d: %w", opts.IndexSize, ErrInvalidInput)
	}

	if opts.IndexSize > maxIndexSizeBytes {
		return nil, fmt.Errorf("index_size %d exceeds max %d: %w", opts.IndexSize, maxIndexSizeBytes, ErrInvalidInput)
	}

	// Validate KeySize and IndexSize fit in uint32 (on-disk format constraint).
	_, err := intToUint32Checked(opts.KeySize)
	if err != nil {
		return nil, fmt.Errorf("key_size %d: %w", opts.KeySize, err)
	}

	_, err = intToUint32Checked(opts.IndexSize)
	if err != nil {
		return nil, fmt.Errorf("index_size %d: %w", opts.IndexSize, err)
	}

	switch opts.Writeback {
	case WritebackNone, WritebackSync:
		// ok
	default:
		return nil, fmt.Errorf("unknown writeback mode %d: %w", opts.Writeback, ErrInvalidInput)
	}

	if opts.SlotCapacity < 1 {
		return nil, fmt.Errorf("slot_capacity must be >= 1, got %d: %w", opts.SlotCapacity, ErrInvalidInput)
	}

	if opts.SlotCapacity > maxSlotCapacity {
		return nil, fmt.Errorf("slot_capacity %d exceeds max %d: %w", opts.SlotCapacity, maxSlotCapacity, ErrInvalidInput)
	}

	// Format constraint: slot_capacity must fit the slot_plus1 encoding.
	const maxSlotCapacitySpec = uint64(0xFFFFFFFFFFFFFFFE)
	if opts.SlotCapacity > maxSlotCapacitySpec {
		return nil, fmt.Errorf("slot_capacity %d exceeds maximum %d: %w", opts.SlotCapacity, maxSlotCapacitySpec, ErrInvalidInput)
	}

	// Bucket sizing uses slot_capacity * 2. Reject capacities that would overflow.
	const maxBucketSizingCapacity = ^uint64(0) >> 1 // maxUint64 / 2
	if opts.SlotCapacity > maxBucketSizingCapacity {
		return nil, fmt.Errorf("slot_capacity %d exceeds bucket sizing limit %d: %w", opts.SlotCapacity, maxBucketSizingCapacity, ErrInvalidInput)
	}

	// Validate computed file layout fits in int64 (required for mmap/ftruncate).
	layoutErr := validateFileLayoutFitsInt64(opts)
	if layoutErr != nil {
		return nil, layoutErr
	}

	// Try to open existing file.
	fd, err := syscall.Open(opts.Path, syscall.O_RDWR, 0)
	if err != nil {
		if !errors.Is(err, syscall.ENOENT) {
			return nil, fmt.Errorf("open file: %w", err)
		}

		// File doesn't exist. With locking enabled, serialize creation under the writer lock.
		if opts.DisableLocking {
			return createNewCache(opts)
		}

		return openCreateOrInitWithWriterLock(opts)
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

		if opts.DisableLocking {
			return initializeEmptyFile(opts)
		}

		return openCreateOrInitWithWriterLock(opts)
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

// openCreateOrInitWithWriterLock serializes cache creation and 0-byte initialization
// under the writer lock when locking is enabled.
//
// This prevents concurrent processes from racing on temp+rename creation or in-place
// initialization, which could otherwise result in different processes operating on
// different inodes for the same path.
//
// Possible errors:
//   - [ErrBusy]: lock contention
//   - [ErrCorrupt]: file too small or unreadable header
//   - [ErrIncompatible]: header format mismatch
//   - [ErrInvalidated]: cache was invalidated
//   - syscall errors: file I/O failures
func openCreateOrInitWithWriterLock(opts Options) (*Cache, error) {
	lock, err := tryAquireWriteLock(opts.Path)
	if err != nil {
		return nil, err
	}
	defer releaseWriteLock(lock)

	fd, openErr := syscall.Open(opts.Path, syscall.O_RDWR, 0)
	if openErr != nil {
		if errors.Is(openErr, syscall.ENOENT) {
			// Still missing under the lock: create new file.
			return createNewCache(opts)
		}

		return nil, fmt.Errorf("open file: %w", openErr)
	}

	var stat syscall.Stat_t

	statErr := syscall.Fstat(fd, &stat)
	if statErr != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("stat file: %w", statErr)
	}

	size := stat.Size
	if size == 0 {
		// 0-byte file under lock: initialize in place.
		_ = syscall.Close(fd)

		return initializeEmptyFile(opts)
	}

	// File is non-empty under lock. Proceed with the normal open/validate path.
	if size < slc1HeaderSize {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("file size %d is less than header size %d: %w", size, slc1HeaderSize, ErrCorrupt)
	}

	headerBuf := make([]byte, slc1HeaderSize)

	n, readErr := syscall.Pread(fd, headerBuf, 0)
	if readErr != nil || n != slc1HeaderSize {
		_ = syscall.Close(fd)

		return nil, ErrCorrupt
	}

	c, validateErr := validateAndOpenExisting(fd, headerBuf, size, opts)
	if validateErr != nil {
		_ = syscall.Close(fd)

		return nil, validateErr
	}

	return c, nil
}

// createNewCache creates a new cache file using temp + rename.
//
// Possible errors:
//   - syscall errors: mkdir, create, ftruncate, write, fsync, rename, open
func createNewCache(opts Options) (*Cache, error) {
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
	// KeySize and IndexSize already validated in Open() to fit in uint32.
	keySize32, _ := intToUint32Checked(opts.KeySize)
	indexSize32, _ := intToUint32Checked(opts.IndexSize)

	header, headerErr := newHeader(
		keySize32,
		indexSize32,
		opts.SlotCapacity,
		opts.UserVersion,
		opts.OrderedKeys,
	)
	if headerErr != nil {
		_ = syscall.Close(fd)
		_ = syscall.Unlink(tmpPath)

		return nil, headerErr
	}

	fileSize, fileSizeErr := uint64ToInt64Checked(header.BucketsOffset + header.BucketCount*16)
	if fileSizeErr != nil {
		_ = syscall.Close(fd)
		_ = syscall.Unlink(tmpPath)

		return nil, fmt.Errorf("file size overflow: %w", fileSizeErr)
	}

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
//
// Possible errors:
//   - syscall errors: open, ftruncate, write, fsync
func initializeEmptyFile(opts Options) (*Cache, error) {
	fd, openErr := syscall.Open(opts.Path, syscall.O_RDWR, 0)
	if openErr != nil {
		return nil, fmt.Errorf("open empty file: %w", openErr)
	}

	// KeySize and IndexSize already validated in Open() to fit in uint32.
	keySize32, _ := intToUint32Checked(opts.KeySize)
	indexSize32, _ := intToUint32Checked(opts.IndexSize)

	header, headerErr := newHeader(
		keySize32,
		indexSize32,
		opts.SlotCapacity,
		opts.UserVersion,
		opts.OrderedKeys,
	)
	if headerErr != nil {
		_ = syscall.Close(fd)

		return nil, headerErr
	}

	fileSize, fileSizeErr := uint64ToInt64Checked(header.BucketsOffset + header.BucketCount*16)
	if fileSizeErr != nil {
		_ = syscall.Close(fd)

		return nil, fmt.Errorf("file size overflow: %w", fileSizeErr)
	}

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

// validateAndOpenExisting validates header and opens an existing file.
//
// Possible errors:
//   - [ErrCorrupt]: invalid file size, CRC mismatch, structural invariant violations
//   - [ErrIncompatible]: magic/version/format mismatch, config mismatch (key_size, etc.)
//   - [ErrBusy]: writer active (odd generation) or lock contention
//   - [ErrInvalidated]: cache was previously invalidated
//   - [ErrInvalidInput]: file size exceeds limits
//   - syscall errors: pread failures
func validateAndOpenExisting(fd int, headerBuf []byte, size int64, opts Options) (*Cache, error) {
	// Safety checks: ensure the mapping size is within implementation limits and
	// representable as a Go []byte length (int). This is required for syscall.Mmap.
	if size < 0 {
		return nil, fmt.Errorf("negative file size %d: %w", size, ErrCorrupt)
	}

	if uint64(size) > maxCacheFileSizeBytes {
		return nil, fmt.Errorf("file size %d exceeds max cache file size %d: %w", size, maxCacheFileSizeBytes, ErrInvalidInput)
	}

	if size > int64(maxInt) {
		return nil, fmt.Errorf("file size %d exceeds max int %d: %w", size, maxInt, ErrInvalidInput)
	}

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

	// Check state field is a known value (0=normal, 1=invalidated).
	// Unknown state values are rejected as incompatible format.
	state := binary.LittleEndian.Uint32(headerBuf[offState:])
	if state != stateNormal && state != stateInvalidated {
		return nil, fmt.Errorf("unknown state value %d: %w", state, ErrIncompatible)
	}

	// Check reserved tail bytes (0x0C0-0x0FF) are zero.
	// Note: User header bytes (0x078-0x0BF) are caller-owned and not checked.
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
		lock, lockErr := tryAquireWriteLock(opts.Path)
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
			releaseWriteLock(lock)

			return nil, ErrCorrupt
		}

		freshGen := binary.LittleEndian.Uint64(fresh[offGeneration:])

		releaseWriteLock(lock)

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
		err := handleCRCFailure(fd, generation, opts)
		if errors.Is(err, ErrCorrupt) {
			return nil, fmt.Errorf("header CRC mismatch: %w", ErrCorrupt)
		}

		return nil, err
	}

	// After CRC validation on a stable even snapshot, check if cache is invalidated.
	// We re-read state here because headerBuf may have been refreshed during odd-gen handling.
	state = binary.LittleEndian.Uint32(headerBuf[offState:])
	if state == stateInvalidated {
		return nil, ErrInvalidated
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
	expectedSlotSize, slotErr := computeSlotSize(keySize, indexSize)
	if slotErr != nil {
		return nil, fmt.Errorf("compute slot size: %w", slotErr)
	}

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

	expectedMinSize, err := uint64ToInt64Checked(bucketsOffset + bucketCount*16)
	if err != nil {
		return nil, fmt.Errorf("computed file size: %w", ErrCorrupt)
	}

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

	// bucket_count must be large enough that the hash table always has at least
	// one EMPTY bucket. This implementation requires bucket_count > slot_capacity
	// so the table cannot become completely full even if slot_highwater reaches
	// slot_capacity.
	if bucketCount <= slotCapacity {
		return nil, fmt.Errorf("bucket_count %d must be > slot_capacity %d: %w", bucketCount, slotCapacity, ErrIncompatible)
	}

	if bucketUsed+bucketTombstones >= bucketCount {
		return nil, fmt.Errorf("bucket_used (%d) + bucket_tombstones (%d) >= bucket_count (%d): %w", bucketUsed, bucketTombstones, bucketCount, ErrCorrupt)
	}

	if bucketUsed != liveCount {
		return nil, fmt.Errorf("bucket_used %d != live_count %d: %w", bucketUsed, liveCount, ErrCorrupt)
	}

	// Optional: sample-check a small number of buckets for out-of-range slot IDs.
	// This is a cheap O(1) check that fails-fast on common corruptions without scanning
	// the full bucket table. Per spec: "Implementations MAY sample-check a small number
	// of buckets for out-of-range slot IDs."
	err = sampleBucketsForCorruption(fd, bucketsOffset, bucketCount, slotHighwater)
	if err != nil {
		return nil, err
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

// validateFileLayoutFitsInt64 checks that the computed file layout for the given
// options is representable and within implementation limits.
//
// This is primarily used to fail fast on unsafe configurations before attempting
// to create/truncate/mmap a file.
//
// Possible errors:
//   - [ErrInvalidInput]: overflow or size limit exceeded
func validateFileLayoutFitsInt64(opts Options) error {
	// Convert sizes (already validated to fit in uint32).
	keySize32, _ := intToUint32Checked(opts.KeySize)
	indexSize32, _ := intToUint32Checked(opts.IndexSize)

	slotSize32, err := computeSlotSize(keySize32, indexSize32)
	if err != nil {
		return err
	}

	slotSize := uint64(slotSize32)

	bucketCount, err := computeBucketCount(opts.SlotCapacity)
	if err != nil {
		return err
	}

	// Check slots section: header_size + slot_capacity * slot_size
	slotsSection := opts.SlotCapacity * slotSize
	if slotSize > 0 && slotsSection/slotSize != opts.SlotCapacity {
		return fmt.Errorf("slots section size overflows uint64: %w", ErrInvalidInput)
	}

	bucketsOffset := uint64(slc1HeaderSize) + slotsSection
	if bucketsOffset < uint64(slc1HeaderSize) {
		return fmt.Errorf("buckets offset overflows uint64: %w", ErrInvalidInput)
	}

	// Check buckets section: bucket_count * 16
	bucketsSection := bucketCount * 16
	if bucketsSection/16 != bucketCount {
		return fmt.Errorf("buckets section size overflows uint64: %w", ErrInvalidInput)
	}

	// Check total file size
	fileSize := bucketsOffset + bucketsSection
	if fileSize < bucketsOffset {
		return fmt.Errorf("file size overflows uint64: %w", ErrInvalidInput)
	}

	if fileSize > maxCacheFileSizeBytes {
		return fmt.Errorf("file size %d exceeds max cache file size %d: %w", fileSize, maxCacheFileSizeBytes, ErrInvalidInput)
	}

	// Must fit in int64 for ftruncate/stat and friends.
	_, err = uint64ToInt64Checked(fileSize)
	if err != nil {
		return fmt.Errorf("file size %d: %w", fileSize, err)
	}

	// Must fit in int for mmap length and Go slice sizing.
	_, err = uint64ToIntChecked(fileSize)
	if err != nil {
		return fmt.Errorf("file size %d: %w", fileSize, err)
	}

	return nil
}

// handleCRCFailure is called when header CRC validation fails.
//
// It attempts to distinguish between real corruption and transient state due to
// a concurrent writer by re-reading the generation counter.
//
// Why this matters: A reader may observe a CRC mismatch if it reads the header
// while a writer is mid-commit (torn read where generation appears even but other
// fields have been partially updated). Returning ErrCorrupt in this case is wrong;
// ErrBusy is the correct response so the caller can retry.
//
// Possible errors:
//   - [ErrCorrupt]: generation unchanged and even, or still odd after lock acquired
//   - [ErrBusy]: generation changed, or writer is active
func handleCRCFailure(fd int, originalGen uint64, opts Options) error {
	// Re-read just the generation field to check if a writer became active.
	genBuf := make([]byte, 8)

	n, err := syscall.Pread(fd, genBuf, offGeneration)
	if err != nil || n != 8 {
		return ErrCorrupt
	}

	currentGen := binary.LittleEndian.Uint64(genBuf)

	// If generation changed, the header we read overlapped with a commit.
	if currentGen != originalGen {
		return ErrBusy
	}

	// Generation is same. If it's odd now, a writer was active during our read.
	if currentGen%2 == 1 {
		if opts.DisableLocking {
			return ErrBusy
		}

		// With locking, check if writer is still active.
		lock, lockErr := tryAquireWriteLock(opts.Path)
		if lockErr != nil {
			if errors.Is(lockErr, ErrBusy) {
				return ErrBusy
			}

			return lockErr
		}

		// Lock acquired - no active writer. Re-read generation under lock.
		n, readErr := syscall.Pread(fd, genBuf, offGeneration)
		if readErr != nil || n != 8 {
			releaseWriteLock(lock)

			return ErrCorrupt
		}

		freshGen := binary.LittleEndian.Uint64(genBuf)

		releaseWriteLock(lock)

		if freshGen%2 == 1 {
			// Still odd with no active writer - crashed writer.
			return ErrCorrupt
		}

		// Writer finished between our reads - should retry Open.
		return ErrBusy
	}

	// Generation is same even value - real corruption.
	return ErrCorrupt
}

// bucketSampleCount is the number of buckets to sample during Open validation.
// Small enough to be O(1) regardless of cache size, large enough to catch common
// corruptions with high probability.
const bucketSampleCount = 8

// sampleBucketsForCorruption performs a spot-check of bucket entries to detect
// obvious corruptions without scanning the entire bucket table.
//
// This is a cheap O(1) check that samples evenly-distributed buckets and verifies
// that FULL entries reference valid slot IDs (< slotHighwater). If a bucket
// references an out-of-range slot ID, the file is corrupt.
//
// Why sample instead of full scan: The spec allows O(1) validation at open time.
// Sampling catches random corruptions (bit flips, truncation) with high probability
// while keeping open time constant regardless of cache size.
//
// Possible errors:
//   - [ErrCorrupt]: bucket references out-of-range slot ID, or read failure
func sampleBucketsForCorruption(fd int, bucketsOffset, bucketCount, slotHighwater uint64) error {
	if bucketCount == 0 {
		return nil
	}

	// Calculate step size to distribute samples evenly across the bucket table.
	// For small bucket counts, we may sample fewer than bucketSampleCount buckets.
	step := bucketCount / bucketSampleCount
	if step == 0 {
		step = 1
	}

	// Each bucket is 16 bytes: hash64 (8) + slot_plus1 (8).
	bucketBuf := make([]byte, 16)

	for i := uint64(0); i < bucketCount; i += step {
		offsetU64 := bucketsOffset + i*16

		offset, err := uint64ToInt64Checked(offsetU64)
		if err != nil {
			// Should never happen: file layout was already validated.
			return fmt.Errorf("bucket offset: %w", ErrCorrupt)
		}

		n, err := syscall.Pread(fd, bucketBuf, offset)
		if err != nil || n != 16 {
			return fmt.Errorf("failed to read bucket %d: %w", i, ErrCorrupt)
		}

		slotPlusOne := binary.LittleEndian.Uint64(bucketBuf[8:])

		// Skip EMPTY (0) and TOMBSTONE (0xFFFFFFFFFFFFFFFF) buckets.
		if slotPlusOne == 0 || slotPlusOne == ^uint64(0) {
			continue
		}

		// FULL bucket: verify slot_id is in range.
		slotID := slotPlusOne - 1
		if slotID >= slotHighwater {
			return fmt.Errorf("bucket %d references out-of-range slot_id %d (highwater=%d): %w",
				i, slotID, slotHighwater, ErrCorrupt)
		}
	}

	return nil
}

// mmapAndCreateCache mmaps the file and creates a Cache instance.
//
// The fd is consumed: on success it's owned by the Cache, on error it's closed.
//
// Possible errors:
//   - syscall errors: fstat, mmap
func mmapAndCreateCache(fd int, size int64, header *slc1Header, opts Options) (*Cache, error) {
	// Get file identity for registry entry.
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

	// Track this inode as active, but no activeWriter by default.
	fileRegistryEntry := getOrCreateRegistryEntry(identity)

	return &Cache{
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
		registryEntry:  fileRegistryEntry,
		isClosed:       false,
		disableLocking: opts.DisableLocking,
		path:           opts.Path,
		writeback:      opts.Writeback,
	}, nil
}

// isLittleEndian is true if the CPU uses little-endian byte order.
// Computed once at package init time.
var isLittleEndian = func() bool {
	var buf [2]byte
	buf[0] = 0x01

	return binary.NativeEndian.Uint16(buf[:]) == 0x01
}()

// is64Bit is true if the architecture has 64-bit pointers.
// Required for atomic 64-bit operations across processes.
var is64Bit = bits.UintSize == 64
