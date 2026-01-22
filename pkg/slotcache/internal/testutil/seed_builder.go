// This file provides a builder for constructing seeds compatible with the
// canonical OpGenerator protocol (CanonicalOpGenConfig + BehaviorOpSet).
//
// The OpGenerator consumes bytes differently than a simple byte stream:
//   - Phased generation affects probability thresholds
//   - Different byte ranges trigger different operations
//   - Key/index generation has different invalid-rate thresholds
//
// This builder encapsulates the protocol details so seeds are robust against
// minor implementation changes in OpGenerator.

package testutil

// BehaviorSeedBuilder helps construct behavior fuzz seeds with a fluent API.
// It encapsulates the canonical OpGenerator protocol to reduce brittleness.
//
// The builder tracks writer state to select appropriate operation bytes.
// All emitted bytes follow the canonical OpGenerator protocol.
type BehaviorSeedBuilder struct {
	data         []byte
	keySize      int
	indexSize    int
	writerActive bool
}

// NewBehaviorSeedBuilder creates a builder for behavior fuzz seeds.
func NewBehaviorSeedBuilder(keySize, indexSize int) *BehaviorSeedBuilder {
	return &BehaviorSeedBuilder{
		data:      make([]byte, 0, 256),
		keySize:   keySize,
		indexSize: indexSize,
	}
}

// Build returns the constructed seed bytes.
func (b *BehaviorSeedBuilder) Build() []byte {
	return append([]byte(nil), b.data...)
}

// BeginWrite emits a BeginWrite operation.
// In reader mode: choice < beginWriteRate (varies by phase, but ~20-50%)
// For Fill phase (typical at start): beginWriteRate=50, so choice < 50 works
// We use choice=0x00 to reliably hit BeginWrite.
func (b *BehaviorSeedBuilder) BeginWrite() *BehaviorSeedBuilder {
	if b.writerActive {
		return b // Already have a writer
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x00) // choice=0 → BeginWrite in reader mode
	b.writerActive = true

	return b
}

// Put emits a Put operation with the given key, revision, and index.
// In writer mode: Put takes up a large portion of the choice space.
// After subtracting deleteRate, commitRate, writerCloseRate, most of the
// remaining space goes to Put. We use choice that falls into Put range.
//
// For canonical config in Fill phase:
//   - deleteRate=15, commitRate=8, writerCloseRate=5
//   - delete: [0, 15), commit: [15, 23), close: [23, 28), put: [28, 85), reads: [85, 100)
//
// We use choice=0x28 (40) which falls into Put range.
func (b *BehaviorSeedBuilder) Put(key []byte, revision int64, index []byte) *BehaviorSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x28) // choice=40 → Put in writer mode
	b.addValidKey(key)
	b.addRevision(revision)
	b.addValidIndex(index)

	return b
}

// Delete emits a Delete operation.
// In writer mode: deleteRate=15 in canonical config, so choice < 15 → Delete.
// We use choice=0x05.
func (b *BehaviorSeedBuilder) Delete(key []byte) *BehaviorSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x05) // choice=5 → Delete in writer mode
	b.addValidKey(key)

	return b
}

// Commit emits a Commit operation.
// In writer mode for Fill phase:
//   - delete: [0, 15), commit: [15, 23), ...
//
// We use choice=0x10 (16) which falls into Commit range.
func (b *BehaviorSeedBuilder) Commit() *BehaviorSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x10) // choice=16 → Commit in writer mode (Fill phase)
	b.writerActive = false

	return b
}

// WriterClose emits a Writer.Close operation (discards uncommitted).
// In writer mode for Fill phase:
//   - delete: [0, 15), commit: [15, 23), close: [23, 28), ...
//
// We use choice=0x18 (24) which falls into WriterClose range.
func (b *BehaviorSeedBuilder) WriterClose() *BehaviorSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x18) // choice=24 → WriterClose in writer mode
	b.writerActive = false

	return b
}

// Get emits a Get operation.
// In reader mode (after beginWriteRate):
//   - choice distribution: getThreshold = beginWriteRate + scale(10)
//
// For Fill phase with beginWriteRate=50, scale(10) = 10*50/80 = 6.25 ≈ 6
// So Get is in range [50, 56) → we use choice=0x32 (50).
func (b *BehaviorSeedBuilder) Get(key []byte) *BehaviorSeedBuilder {
	if b.writerActive {
		// In writer mode, Get is in the read ops (last ~15%)
		b.addOpGenRoulette()
		b.data = append(b.data, 0x58, 0x00) // choice=88 → read ops, then 0 → Get
		b.addValidKey(key)

		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x32) // choice=50 → Get in reader mode (Fill phase)
	b.addValidKey(key)

	return b
}

// Scan emits a Scan operation (no filter).
// In reader mode (Fill phase):
//   - getThreshold=56, scanThreshold=56+scale(20)=56+12.5≈68
//   - Scan is in range [56, 68) → choice=0x38 (56)
func (b *BehaviorSeedBuilder) Scan() *BehaviorSeedBuilder {
	if b.writerActive {
		return b // Skip if writer active
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x38) // choice=56 → Scan in reader mode
	b.addNoFilter()
	b.addValidScanOpts(0, 0, false)

	return b
}

// ScanWithFilter emits a Scan with a filter.
// Parameters: filterKind, offset, limit, reverse.
func (b *BehaviorSeedBuilder) ScanWithFilter(filterKind filterKind, offset, limit int, reverse bool) *BehaviorSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x38) // Scan
	b.addFilter(filterKind)
	b.addValidScanOpts(offset, limit, reverse)

	return b
}

// ScanPrefix emits a ScanPrefix operation.
// In reader mode (Fill phase):
//   - scanThreshold=68, prefixThreshold=68+scale(15)=68+9≈77
//   - ScanPrefix is in range [68, 77) → choice=0x44 (68)
func (b *BehaviorSeedBuilder) ScanPrefix(prefix []byte) *BehaviorSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x44) // choice=68 → ScanPrefix in reader mode
	b.addValidPrefix(prefix)
	b.addNoFilter()
	b.addValidScanOpts(0, 0, false)

	return b
}

// Len emits a Len operation.
// In reader mode (Fill phase with beginWriteRate=50):
//   - remaining = 100 - 50 = 50
//   - getThreshold = 50 + scale(10) = 50 + 6 = 56
//   - scanThreshold = 56 + scale(20) = 56 + 12 = 68
//   - prefixThreshold = 68 + scale(15) = 68 + 9 = 77
//   - matchThreshold = 77 + scale(15) = 77 + 9 = 86
//   - rangeThreshold = 86 + scale(10) = 86 + 6 = 92
//   - userHeaderThreshold = 92 + scale(5) = 92 + 3 = 95
//   - Len is in range [95, 100) → choice=0x60 (96)
func (b *BehaviorSeedBuilder) Len() *BehaviorSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x60) // choice=96 → Len in reader mode (Fill phase)

	return b
}

// UserHeader emits a UserHeader (Cache.UserHeader()) operation.
// In reader mode (Fill phase with beginWriteRate=50):
//   - userHeaderThreshold = 92 + scale(5) = 92 + 3 = 95
//   - UserHeader is in range [92, 95) → choice=0x5C (92)
func (b *BehaviorSeedBuilder) UserHeader() *BehaviorSeedBuilder {
	if b.writerActive {
		return b // Skip if writer active (UserHeader is a reader-only op in generation)
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x5C) // choice=92 → UserHeader in reader mode (Fill phase)

	return b
}

// SetUserHeaderFlags emits a Writer.SetUserHeaderFlags operation.
// In writer mode for Fill phase:
//   - deleteThreshold=15, commitThreshold=23, closeThreshold=28
//   - setFlagsThreshold=28+3=31
//   - SetUserHeaderFlags is in range [28, 31) → choice=0x1C (28)
func (b *BehaviorSeedBuilder) SetUserHeaderFlags(flags uint64) *BehaviorSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x1C) // choice=28 → SetUserHeaderFlags in writer mode
	b.addUint64(flags)

	return b
}

// SetUserHeaderData emits a Writer.SetUserHeaderData operation.
// In writer mode for Fill phase:
//   - setFlagsThreshold=31, setDataThreshold=31+2=33
//   - SetUserHeaderData is in range [31, 33) → choice=0x1F (31)
func (b *BehaviorSeedBuilder) SetUserHeaderData(data [64]byte) *BehaviorSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x1F) // choice=31 → SetUserHeaderData in writer mode
	// nextUserData() reads 64 bytes from the decoder
	b.data = append(b.data, data[:]...)

	return b
}

// Close emits a Cache.Close operation.
// OpGenerator: roulette in [reopenThreshold, closeThreshold) → Close
// reopenThreshold ≈ 8, closeThreshold ≈ 16
// We use roulette=0x0A (10).
func (b *BehaviorSeedBuilder) Close() *BehaviorSeedBuilder {
	b.data = append(b.data, 0x0A) // roulette=10 → Close

	return b
}

// Reopen emits a Reopen operation.
// OpGenerator: roulette < reopenThreshold (~3% = ~8) → Reopen
// We use roulette=0x00.
func (b *BehaviorSeedBuilder) Reopen() *BehaviorSeedBuilder {
	b.data = append(b.data, 0x00) // roulette=0 → Reopen

	return b
}

// Invalidate emits an Invalidate operation.
// OpGenerator: roulette in [closeThreshold, invalidateThreshold) → Invalidate
// Canonical config uses InvalidateRate=2%, so roulette=0x11 (17) works.
func (b *BehaviorSeedBuilder) Invalidate() *BehaviorSeedBuilder {
	if b.writerActive {
		return b // Invalidate returns ErrBusy when writer is active
	}

	b.data = append(b.data, 0x11) // roulette=17 → Invalidate

	return b
}

// --- Private helper methods (placed after all exported methods) ---

// addOpGenRoulette adds the "roulette" byte that OpGenerator checks first.
// To skip Close/Reopen and proceed to choice-based selection, use a value
// that's >= closeThreshold (approximately 15 out of 256, or ~6%).
// We use 0x80 (128) to safely be in the "proceed to choice" range.
func (b *BehaviorSeedBuilder) addOpGenRoulette() {
	b.data = append(b.data, 0x80)
}

// addValidKey adds bytes that produce a valid key of the correct length.
// OpGenerator key generation:
//   - First byte (mode) < invalidThreshold (~5% = ~13) → invalid key
//   - mode >= invalidThreshold → valid key generation
//
// We use mode=0xFF to ensure we always get valid key generation.
// For new keys (not reusing seen keys), we also need bytes for the key itself.
func (b *BehaviorSeedBuilder) addValidKey(key []byte) {
	// mode byte: high value ensures valid key (not in invalid range)
	// For "new valid key" generation, OpGenerator reads keySize bytes
	keyPadded := padOrTruncate(key, b.keySize)
	b.data = append(b.data, 0xFF)
	b.data = append(b.data, keyPadded...)
}

// addValidIndex adds bytes that produce a valid index of the correct length.
// OpGenerator index generation:
//   - First byte (mode) < invalidThreshold (~5% = ~13) → invalid index
//   - mode >= invalidThreshold → valid index
//
// We use mode=0xFF to ensure valid index generation.
func (b *BehaviorSeedBuilder) addValidIndex(index []byte) {
	indexPadded := padOrTruncate(index, b.indexSize)
	b.data = append(b.data, 0xFF)
	b.data = append(b.data, indexPadded...)
}

// addRevision adds an int64 revision value in little-endian format.
func (b *BehaviorSeedBuilder) addRevision(rev int64) {
	b.data = append(b.data,
		byte(rev),
		byte(rev>>8),
		byte(rev>>16),
		byte(rev>>24),
		byte(rev>>32),
		byte(rev>>40),
		byte(rev>>48),
		byte(rev>>56),
	)
}

// addUint64 adds a uint64 value in little-endian format.
// Used for SetUserHeaderFlags which reads via NextUint64().
func (b *BehaviorSeedBuilder) addUint64(v uint64) {
	b.data = append(b.data,
		byte(v),
		byte(v>>8),
		byte(v>>16),
		byte(v>>24),
		byte(v>>32),
		byte(v>>40),
		byte(v>>48),
		byte(v>>56),
	)
}

// addNoFilter adds bytes that result in no filter being applied.
// OpGenerator: nextFilterSpec uses byte % 10 >= 3 for no filter.
// We use 0x03 (3 % 10 = 3 >= 3 → no filter).
func (b *BehaviorSeedBuilder) addNoFilter() {
	b.data = append(b.data, 0x03)
}

// addFilter adds bytes that result in a filter being applied.
// OpGenerator: byte % 10 < 3 for filter, then kind = byte % 5.
func (b *BehaviorSeedBuilder) addFilter(kind filterKind) {
	// First byte: must be < 3 (mod 10) to get a filter
	// 0x00 % 10 = 0 < 3 → get filter
	// Add filter-specific bytes based on kind
	switch kind {
	case filterRevisionMask, filterIndexByteEq:
		// filterRevisionMask: mask selector: 0 → mask=1, want: 0 → even revisions
		// filterIndexByteEq: offset=0, byte=0
		b.data = append(b.data, 0x00, byte(kind), 0x00, 0x00)
	case filterKeyPrefixEq:
		b.data = append(b.data, 0x00, byte(kind), 0x01, 0xFF, 0x00, 0x00) // prefixLen=2, random prefix
	default:
		// FilterAll, FilterNone: just the kind byte
		b.data = append(b.data, 0x00, byte(kind))
	}
}

// addValidScanOpts adds bytes for valid scan options.
// OpGenerator: mode >= invalidThreshold (~5% = ~13) → valid opts.
func (b *BehaviorSeedBuilder) addValidScanOpts(offset, limit int, reverse bool) {
	reverseByte := byte(0)
	if reverse {
		reverseByte = 1
	}

	b.data = append(b.data, 0x80, byte(offset%5), byte(limit%4), reverseByte)
}

// addValidPrefix adds bytes for a valid prefix (derivePrefixFromKeys).
// OpGenerator: mode >= 52 → valid prefix.
func (b *BehaviorSeedBuilder) addValidPrefix(prefix []byte) {
	// For derivePrefixFromKeys when no keys seen: generates random prefix
	// prefixLen = 1 + (byte % keySize)
	prefixLen := min(max(len(prefix), 1), b.keySize)
	// byte that gives us desired prefixLen, then the prefix bytes
	b.data = append(b.data, 0xFF, byte(prefixLen-1))
	b.data = append(b.data, prefix[:prefixLen]...)
}

// padOrTruncate ensures data is exactly length bytes.
func padOrTruncate(data []byte, length int) []byte {
	if len(data) == length {
		return data
	}

	result := make([]byte, length)
	copy(result, data)

	return result
}

// -----------------------------------------------------------------------------
// OrderedSeedBuilder — for ordered-keys mode
// -----------------------------------------------------------------------------

// OrderedSeedBuilder helps construct ordered-keys behavior fuzz seeds.
// It encapsulates the canonical OpGenerator protocol for ordered key generation.
//
// In ordered-keys mode, OpGenerator's key generation uses an internal monotonic
// counter and consumes fuzz bytes differently than unordered mode.
type OrderedSeedBuilder struct {
	data         []byte
	keySize      int
	indexSize    int
	writerActive bool
}

// NewOrderedSeedBuilder creates a builder for ordered-keys behavior fuzz seeds.
func NewOrderedSeedBuilder(keySize, indexSize int) *OrderedSeedBuilder {
	return &OrderedSeedBuilder{
		data:      make([]byte, 0, 256),
		keySize:   keySize,
		indexSize: indexSize,
	}
}

// Build returns the constructed seed bytes.
func (b *OrderedSeedBuilder) Build() []byte {
	return append([]byte(nil), b.data...)
}

// BeginWrite emits a BeginWrite operation.
func (b *OrderedSeedBuilder) BeginWrite() *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x00) // choice=0 → BeginWrite in reader mode
	b.writerActive = true

	return b
}

// PutMonotonic emits a Writer.Put that generates a NEW monotonic ordered key.
//
// Ordered keys: the key bytes are produced internally by OpGenerator; we do not
// encode explicit key bytes into the seed.
func (b *OrderedSeedBuilder) PutMonotonic(revision int64, index []byte) *OrderedSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// choice=40 → Put in writer mode (Fill phase)
	//
	// Key generation mode:
	//  - >= invalidThreshold (~12) → valid
	//  - >= reuseThreshold (96 while seen<4) → NEW key (not reuse)
	//  - < nonMonoThreshold (~248) → monotonic
	b.data = append(b.data, 0x28, 0x80)

	b.addRevision(revision)
	b.addValidIndex(index)

	return b
}

// PutNonMonotonicNew emits a Writer.Put that generates a NEW non-monotonic key.
//
// This is intended to trigger ErrOutOfOrderInsert when appended after a committed
// tail key.
func (b *OrderedSeedBuilder) PutNonMonotonicNew(revision int64, index []byte) *OrderedSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// Put.
	//
	// Key generation mode:
	//  - >= reuseThreshold → NEW key
	//  - >= nonMonoThreshold → non-monotonic
	//
	// nextNonMonotonicOrderedKey consumes:
	//  - delta byte (delta = (b%16)+1)
	//  - suffix byte (only when keySize>4)
	//
	// Use deltaByte=1 → delta=2. With only two monotonic keys generated so far,
	// this produces base=0 (a NEW key) which is smaller than the tail.
	b.data = append(b.data, 0x28, 0xFF, 0x01, 0x00)

	b.addRevision(revision)
	b.addValidIndex(index)

	return b
}

// Commit emits a Writer.Commit operation.
func (b *OrderedSeedBuilder) Commit() *OrderedSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x10) // choice=16 → Commit in writer mode (Fill phase)
	b.writerActive = false

	return b
}

// ScanRangeAll emits a ScanRange operation with nil start/end bounds.
func (b *OrderedSeedBuilder) ScanRangeAll() *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// choice=86 → ScanRange in reader mode (Fill phase)
	//
	// Start bound: mode=26 → nil (unbounded), not invalid.
	// End bound: mode=26 → nil (unbounded), not invalid.
	// Filter: nil (byte%10 >= 3)
	// ScanOptions: valid, small values.
	//   mode=0xFF (valid), offset=0, limit=0 (unlimited), reverse=0
	b.data = append(b.data, 0x56, 0x1A, 0x1A, 0x03, 0xFF, 0x00, 0x00, 0x00)

	return b
}

// ScanRangeBounded emits a ScanRange operation with explicit start/end bounds.
// The bounds are padded/truncated to keySize.
func (b *OrderedSeedBuilder) ScanRangeBounded(start, end []byte) *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// choice=86 → ScanRange in reader mode (Fill phase)
	// Start bound: mode >= 200 → generate from decoder bytes
	// nextRangeBound when mode >= 200:
	//   length := 1 + (NextByte() % keySize)
	//   return NextBytes(length)
	// We use mode=0xFF (255) to hit this path, then length byte = keySize-1 to get full key
	b.data = append(b.data,
		0x56,
		0xFF,              // mode=255 → generate from bytes
		byte(b.keySize-1), // length byte → keySize
	)
	b.data = append(b.data, padOrTruncate(start, b.keySize)...)

	// End bound: same approach
	b.data = append(b.data, 0xFF, byte(b.keySize-1))
	b.data = append(b.data, padOrTruncate(end, b.keySize)...)

	// Filter: nil (byte%10 >= 3)
	// ScanOptions: valid, offset=0, limit=0, reverse=false
	b.data = append(b.data, 0x03, 0xFF, 0x00, 0x00, 0x00)

	return b
}

// ScanRangeReverse emits a ScanRange operation with reverse=true.
func (b *OrderedSeedBuilder) ScanRangeReverse() *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// choice=86 → ScanRange in reader mode (Fill phase)
	// Start/end: nil (unbounded)
	// Filter: nil
	// ScanOptions: valid, offset=0, limit=0, reverse=true
	b.data = append(b.data, 0x56, 0x1A, 0x1A, 0x03, 0xFF, 0x00, 0x00, 0x01)

	return b
}

// ScanPrefix emits a ScanPrefix operation (uses range scan optimization in ordered mode).
func (b *OrderedSeedBuilder) ScanPrefix(prefix []byte) *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// Prefix: mode >= 52 → valid prefix from bytes
	// prefixLen = 1 + (byte % keySize)
	prefixLen := min(max(len(prefix), 1), b.keySize)
	start := len(b.data)
	b.data = append(b.data, make([]byte, 1+2+prefixLen+5)...)

	b.data[start] = 0x44 // choice=68 → ScanPrefix in reader mode (Fill phase)
	b.data[start+1] = 0xFF
	b.data[start+2] = byte(prefixLen - 1)
	copy(b.data[start+3:], prefix[:prefixLen])

	// Filter: nil (byte%10 >= 3)
	// ScanOptions: valid, offset=0, limit=0, reverse=false
	copy(b.data[start+3+prefixLen:], []byte{0x03, 0xFF, 0x00, 0x00, 0x00})

	return b
}

// Scan emits a Scan operation (no filter).
func (b *OrderedSeedBuilder) Scan() *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data,
		0x38,                   // choice=56 → Scan in reader mode
		0x03,                   // no filter
		0xFF, 0x00, 0x00, 0x00, // valid scan opts
	)

	return b
}

// ScanWithFilter emits a Scan with a filter.
func (b *OrderedSeedBuilder) ScanWithFilter(filterKind filterKind) *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x38) // Scan

	// Add filter bytes based on kind
	switch filterKind {
	case filterRevisionMask, filterIndexByteEq:
		b.data = append(b.data, 0x00, byte(filterKind), 0x00, 0x00)
	case filterKeyPrefixEq:
		b.data = append(b.data, 0x00, byte(filterKind), 0x01, 0xFF, 0x00, 0x00)
	default:
		b.data = append(b.data, 0x00, byte(filterKind))
	}

	b.data = append(b.data, 0xFF, 0x00, 0x00, 0x00) // valid scan opts

	return b
}

// ScanRangeWithFilter emits a ScanRange with a filter.
func (b *OrderedSeedBuilder) ScanRangeWithFilter(filterKind filterKind) *OrderedSeedBuilder {
	if b.writerActive {
		return b
	}

	b.addOpGenRoulette()
	b.data = append(b.data, 0x56, 0x1A, 0x1A) // ScanRange, nil bounds

	// Add filter bytes
	switch filterKind {
	case filterRevisionMask, filterIndexByteEq:
		b.data = append(b.data, 0x00, byte(filterKind), 0x00, 0x00)
	case filterKeyPrefixEq:
		b.data = append(b.data, 0x00, byte(filterKind), 0x01, 0xFF, 0x00, 0x00)
	default:
		b.data = append(b.data, 0x00, byte(filterKind))
	}

	b.data = append(b.data, 0xFF, 0x00, 0x00, 0x00) // valid scan opts

	return b
}

// Delete emits a Writer.Delete operation.
// In ordered mode, generates a monotonic key (which should match a previously inserted key
// when the test properly tracks seen keys in the harness).
func (b *OrderedSeedBuilder) Delete() *OrderedSeedBuilder {
	if !b.writerActive {
		return b
	}

	b.addOpGenRoulette()

	// genKey with mode >= reuseThreshold and seen=nil falls through to new key generation.
	// For ordered mode with mode < nonMonoThreshold, calls nextOrderedKey.
	// nextOrderedKey just increments internal counter - no extra bytes needed.
	// We use mode=0xC0 (192) which is:
	//   - >= invalidThreshold (~12) → not invalid
	//   - >= reuseThreshold (96 when seen<4) → new key (not reuse)
	//   - < nonMonoThreshold (~248) → monotonic ordered key
	b.data = append(b.data, 0x05, 0xC0) // choice=5 → Delete in writer mode

	return b
}

func (b *OrderedSeedBuilder) addOpGenRoulette() {
	b.data = append(b.data, 0x80)
}

func (b *OrderedSeedBuilder) addValidIndex(index []byte) {
	indexPadded := padOrTruncate(index, b.indexSize)
	b.data = append(b.data, 0xFF)
	b.data = append(b.data, indexPadded...)
}

func (b *OrderedSeedBuilder) addRevision(rev int64) {
	b.data = append(b.data,
		byte(rev),
		byte(rev>>8),
		byte(rev>>16),
		byte(rev>>24),
		byte(rev>>32),
		byte(rev>>40),
		byte(rev>>48),
		byte(rev>>56),
	)
}
