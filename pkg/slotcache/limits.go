package slotcache

// Hardcoded implementation limits.
//
// These limits are intentionally generous; they exist primarily to:
//   - keep arithmetic safely away from overflow boundaries
//   - bound resource usage for configurations that the project does not fuzz/test
//   - avoid unsafe int64/int conversions (mmap length is an int)
//
// All limit violations are treated as programming/configuration errors and
// return ErrInvalidInput.
const (
	// Maximum allowed key size (bytes).
	maxKeySizeBytes = 512

	// Maximum allowed index size (bytes) per slot.
	maxIndexSizeBytes = 1 << 20 // 1 MiB

	// Maximum allowed total slot record size (bytes), including meta/revision/key
	// overhead and padding.
	maxSlotSizeBytes = 2 << 20 // 2 MiB

	// Maximum allowed slot capacity (number of slots in the slots section).
	maxSlotCapacity = uint64(100_000_000)

	// Maximum allowed cache file size (bytes).
	//
	// This is a safety guardrail, not a RAM limit. mmap does not load the entire
	// file into memory, but very large mappings are outside what we want to
	// implicitly claim support for.
	maxCacheFileSizeBytes = uint64(1) << 40 // 1 TiB

	// Maximum allowed Offset/Limit values for scan-style APIs.
	// Offset/Limit beyond this are treated as invalid input to avoid integer
	// overflow footguns and runaway allocations.
	maxScanOffset = 100_000_000

	maxScanLimit = maxScanOffset

	// Maximum number of buffered operations allowed in a single Writer session.
	// Prevents unbounded memory growth when callers stage massive batches.
	maxBufferedOpsPerWriter = 1_000_000
)
