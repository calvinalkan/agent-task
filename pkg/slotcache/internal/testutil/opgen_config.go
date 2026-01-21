package testutil

import (
	"encoding/binary"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// OpGenConfig tunes the probabilities and behavior of OpGenerator.
// All rate fields are percentages (0–100).
//
// Determinism: All weighting decisions consume bytes from FuzzDecoder,
// so fuzz minimization and fixed seeds remain stable.
type OpGenConfig struct {
	// InvalidKeyRate is the percentage of keys that should be invalid
	// (nil or wrong length). Default: 15.
	InvalidKeyRate int

	// InvalidIndexRate is the percentage of index values that should be
	// invalid (wrong length). Default: 10.
	InvalidIndexRate int

	// InvalidScanOptsRate is the percentage of ScanOptions that should be
	// invalid (negative offset or limit). Default: 10.
	InvalidScanOptsRate int

	// DeleteRate is the percentage of writer-active ops that should be Delete
	// (relative to Put). Higher values stress tombstone handling. Default: 15.
	DeleteRate int

	// CommitRate is the percentage of writer-active ops that should be Commit.
	// Lower values create longer writer sessions. Default: 15.
	CommitRate int

	// WriterCloseRate is the percentage of writer-active ops that should be
	// Writer.Close (discard uncommitted changes). Default: 10.
	WriterCloseRate int

	// NonMonotonicRate is the percentage of new keys in ordered mode that
	// should be intentionally non-monotonic (to exercise ErrOutOfOrderInsert).
	// Default: 6.
	NonMonotonicRate int

	// ReopenRate is the percentage of ops that should be Reopen.
	// Applied before writer-state branching. Default: 5.
	ReopenRate int

	// CloseRate is the percentage of ops that should be Close.
	// Applied before writer-state branching. Default: 5.
	CloseRate int

	// BeginWriteRate is the percentage of reader-mode ops that should attempt
	// BeginWrite. Higher values create more write sessions. Default: 20.
	BeginWriteRate int

	// SmallScanLimitBias biases scan Limit toward small values (0-3).
	// When true (default), scans use small limits to keep tests fast.
	SmallScanLimitBias bool

	// KeyReuseMinThreshold is the minimum number of seen keys before
	// key reuse rates increase. Default: 4.
	KeyReuseMinThreshold int

	// KeyReuseMaxThreshold is the seen-key count above which reuse rate
	// is maximized. Default: 32.
	KeyReuseMaxThreshold int
}

// DefaultOpGenConfig returns a config matching the original FuzzDecoder behavior.
func DefaultOpGenConfig() OpGenConfig {
	return OpGenConfig{
		InvalidKeyRate:       15,
		InvalidIndexRate:     10,
		InvalidScanOptsRate:  10,
		DeleteRate:           15,
		CommitRate:           15,
		WriterCloseRate:      10,
		NonMonotonicRate:     6,
		ReopenRate:           5,
		CloseRate:            5,
		BeginWriteRate:       20,
		SmallScanLimitBias:   true,
		KeyReuseMinThreshold: 4,
		KeyReuseMaxThreshold: 32,
	}
}

// DeepStateOpGenConfig returns a config optimized for deeper state exploration.
// Lower invalid input rates and longer writer sessions produce more meaningful
// slot allocation and tombstone stress.
func DeepStateOpGenConfig() OpGenConfig {
	return OpGenConfig{
		InvalidKeyRate:       5,
		InvalidIndexRate:     5,
		InvalidScanOptsRate:  5,
		DeleteRate:           20, // More tombstones
		CommitRate:           10, // Longer sessions
		WriterCloseRate:      5,  // Fewer discards
		NonMonotonicRate:     3,  // Fewer order violations in ordered mode
		ReopenRate:           3,
		CloseRate:            3,
		BeginWriteRate:       30, // More eager to start writing
		SmallScanLimitBias:   true,
		KeyReuseMinThreshold: 4,
		KeyReuseMaxThreshold: 32,
	}
}

// OpGenerator wraps FuzzDecoder and applies configurable probability weights.
// It implements OpSource for use with RunBehavior.
type OpGenerator struct {
	decoder *FuzzDecoder
	config  OpGenConfig
	options slotcache.Options

	// orderedCounter tracks monotonic key generation for ordered mode.
	orderedCounter uint32
}

// NewOpGenerator creates an OpGenerator with the given config.
func NewOpGenerator(fuzzBytes []byte, opts slotcache.Options, cfg *OpGenConfig) *OpGenerator {
	return &OpGenerator{
		decoder: NewFuzzDecoder(fuzzBytes, opts),
		config:  *cfg,
		options: opts,
	}
}

// HasMore reports whether more fuzz bytes remain.
func (g *OpGenerator) HasMore() bool {
	return g.decoder.HasMore()
}

// NextOp implements OpSource. It chooses the next operation based on harness
// state and the configured probability weights.
func (g *OpGenerator) NextOp(h *Harness, seen [][]byte) Operation {
	writerActive := h.Model.Writer != nil && h.Real.Writer != nil

	// Global ops: Reopen/Close can happen anytime (even with writer active,
	// they return ErrBusy which is meaningful behavior to test).
	roulette := g.decoder.NextByte()
	reopenThreshold := byte(float64(256) * float64(g.config.ReopenRate) / 100.0)
	closeThreshold := reopenThreshold + byte(float64(256)*float64(g.config.CloseRate)/100.0)

	if roulette < reopenThreshold {
		return OpReopen{}
	}

	if roulette < closeThreshold {
		return OpClose{}
	}

	if !writerActive {
		return g.nextReaderOp(h, seen)
	}

	return g.nextWriterOp(h, seen)
}

func (g *OpGenerator) nextReaderOp(_ *Harness, seen [][]byte) Operation {
	choice := g.decoder.NextByte() % 100

	// BeginWrite has configurable rate.
	if choice < byte(g.config.BeginWriteRate) {
		return OpBeginWrite{}
	}

	// Distribute remaining ops among read operations.
	// Original distribution (when BeginWriteRate=20):
	//   Get: 10%, Scan: 20%, ScanPrefix: 15%, ScanMatch: 15%, ScanRange: 10%, Len: 10%
	// We scale these proportionally.
	remaining := 100 - g.config.BeginWriteRate
	scale := func(pct int) int { return pct * remaining / 80 }

	getThreshold := g.config.BeginWriteRate + scale(10)
	scanThreshold := getThreshold + scale(20)
	prefixThreshold := scanThreshold + scale(15)
	matchThreshold := prefixThreshold + scale(15)
	rangeThreshold := matchThreshold + scale(10)

	switch {
	case choice < byte(getThreshold):
		return OpGet{Key: g.genKey(seen)}
	case choice < byte(scanThreshold):
		return OpScan{
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	case choice < byte(prefixThreshold):
		return OpScanPrefix{
			Prefix:  g.derivePrefixFromKeys(seen),
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	case choice < byte(matchThreshold):
		return OpScanMatch{
			Spec:    g.nextPrefixSpec(seen),
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	case choice < byte(rangeThreshold):
		return OpScanRange{
			Start:   g.nextRangeBound(seen),
			End:     g.nextRangeBound(seen),
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	default:
		return OpLen{}
	}
}

func (g *OpGenerator) nextWriterOp(h *Harness, seen [][]byte) Operation {
	choice := g.decoder.NextByte() % 100

	// Calculate thresholds from config.
	// Original distribution: Put=45%, Delete=15%, Commit=15%, WriterClose=10%, reads=15%
	// We now use configurable rates for Delete/Commit/WriterClose,
	// with Put taking the remainder before reads.
	deleteThreshold := g.config.DeleteRate
	commitThreshold := deleteThreshold + g.config.CommitRate
	closeThreshold := commitThreshold + g.config.WriterCloseRate

	// Remaining percentage goes to Put (before reads).
	// Reads get ~15% as in original.
	putEnd := 100 - 15 // Put ends where reads start

	switch {
	case choice < byte(deleteThreshold):
		return OpDelete{Key: g.genKey(seen)}
	case choice < byte(commitThreshold):
		return OpCommit{}
	case choice < byte(closeThreshold):
		return OpWriterClose{}
	case choice < byte(putEnd):
		return OpPut{
			Key:      g.genKey(seen),
			Revision: g.decoder.NextInt64(),
			Index:    g.genIndex(),
		}
	default:
		// Remaining ~15% is read ops.
		return g.nextWriterReadOp(h, seen)
	}
}

func (g *OpGenerator) nextWriterReadOp(_ *Harness, seen [][]byte) Operation {
	// Distribute among read ops: Get, Scan, ScanPrefix, ScanMatch, ScanRange.
	choice := g.decoder.NextByte() % 100

	switch {
	case choice < 35:
		return OpGet{Key: g.genKey(seen)}
	case choice < 60:
		return OpScan{
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	case choice < 80:
		return OpScanPrefix{
			Prefix:  g.derivePrefixFromKeys(seen),
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	case choice < 90:
		return OpScanMatch{
			Spec:    g.nextPrefixSpec(seen),
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	default:
		return OpScanRange{
			Start:   g.nextRangeBound(seen),
			End:     g.nextRangeBound(seen),
			Filter:  g.nextFilterSpec(seen),
			Options: g.nextScanOpts(),
		}
	}
}

// genKey generates a key with configurable invalid rate.
func (g *OpGenerator) genKey(seen [][]byte) []byte {
	keySize := g.options.KeySize
	mode := g.decoder.NextByte()

	// Invalid key rate.
	invalidThreshold := byte(float64(256) * float64(g.config.InvalidKeyRate) / 100.0)
	if mode < invalidThreshold {
		if g.decoder.NextByte()&1 == 0 {
			return nil
		}
		// Wrong length.
		wrongLen := int(g.decoder.NextByte()) % (keySize + 2)
		if wrongLen == keySize {
			wrongLen = keySize + 1
		}

		return g.decoder.NextBytes(wrongLen)
	}

	// Reuse rate depends on how many keys we've seen.
	reuseThreshold := byte(160) // ~62% reuse by default
	if len(seen) < g.config.KeyReuseMinThreshold {
		reuseThreshold = 96 // ~37%
	}

	if len(seen) > g.config.KeyReuseMaxThreshold {
		reuseThreshold = 208 // ~81%
	}

	if len(seen) > 0 && mode < reuseThreshold {
		idx := int(g.decoder.NextByte()) % len(seen)

		return append([]byte(nil), seen[idx]...)
	}

	// New valid key.
	if g.options.OrderedKeys {
		nonMonoThreshold := byte(float64(256) * float64(100-g.config.NonMonotonicRate) / 100.0)
		if mode < nonMonoThreshold {
			return g.nextOrderedKey(keySize)
		}

		return g.nextNonMonotonicOrderedKey(keySize)
	}

	return g.decoder.NextBytes(keySize)
}

func (g *OpGenerator) nextOrderedKey(keySize int) []byte {
	if keySize <= 0 {
		return []byte{}
	}

	g.orderedCounter++
	key := make([]byte, keySize)

	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], g.orderedCounter)

	if keySize >= 4 {
		copy(key[:4], tmp[:])

		return key
	}

	copy(key, tmp[4-keySize:])

	return key
}

func (g *OpGenerator) nextNonMonotonicOrderedKey(keySize int) []byte {
	if keySize <= 0 {
		return []byte{}
	}

	if g.orderedCounter == 0 {
		return g.nextOrderedKey(keySize)
	}

	delta := min(uint32(g.decoder.NextByte()%16)+1, g.orderedCounter)
	base := g.orderedCounter - delta

	key := make([]byte, keySize)

	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], base)

	if keySize < 4 {
		copy(key, tmp[4-keySize:])

		return key
	}

	copy(key[:4], tmp[:])

	if keySize > 4 {
		key[keySize-1] = g.decoder.NextByte() | 1
	}

	return key
}

// genIndex generates an index with configurable invalid rate.
func (g *OpGenerator) genIndex() []byte {
	indexSize := g.options.IndexSize
	mode := g.decoder.NextByte()

	invalidThreshold := byte(float64(256) * float64(g.config.InvalidIndexRate) / 100.0)
	if mode < invalidThreshold {
		wrongLen := int(g.decoder.NextByte()) % (indexSize + 2)
		if wrongLen == indexSize {
			wrongLen = indexSize + 1
		}

		return g.decoder.NextBytes(wrongLen)
	}

	return g.decoder.NextBytes(indexSize)
}

func (g *OpGenerator) derivePrefixFromKeys(seen [][]byte) []byte {
	keySize := g.options.KeySize
	mode := g.decoder.NextByte()

	// ~20% invalid.
	if mode < 52 {
		invalidMode := int(g.decoder.NextByte()) % 3
		switch invalidMode {
		case 1:
			return []byte{}
		case 2:
			return make([]byte, keySize+1)
		default:
			return nil
		}
	}

	if len(seen) > 0 {
		idx := int(g.decoder.NextByte()) % len(seen)
		key := seen[idx]
		prefixLen := 1 + (int(g.decoder.NextByte()) % keySize)

		return append([]byte(nil), key[:prefixLen]...)
	}

	prefixLen := 1 + (int(g.decoder.NextByte()) % keySize)

	return g.decoder.NextBytes(prefixLen)
}

func (g *OpGenerator) nextPrefixSpec(seen [][]byte) slotcache.Prefix {
	keySize := g.options.KeySize
	mode := g.decoder.NextByte()

	// ~20% invalid.
	if mode < 52 {
		invalidMode := int(g.decoder.NextByte()) % 5
		switch invalidMode {
		case 0:
			return slotcache.Prefix{Offset: keySize, Bits: 0, Bytes: []byte{0x00}}
		case 2:
			return slotcache.Prefix{Offset: 0, Bits: -1, Bytes: []byte{0x00}}
		case 3:
			return slotcache.Prefix{Offset: 0, Bits: 1, Bytes: []byte{}}
		case 4:
			return slotcache.Prefix{Offset: 0, Bits: 0, Bytes: make([]byte, keySize+1)}
		default:
			return slotcache.Prefix{Offset: 0, Bits: 0, Bytes: nil}
		}
	}

	keyOffset := int(g.decoder.NextByte()) % keySize
	maxPrefixBytes := keySize - keyOffset

	// 50% byte-aligned, 50% bit-granular.
	if g.decoder.NextByte()&1 == 1 {
		prefixLen := 1 + (int(g.decoder.NextByte()) % maxPrefixBytes)
		prefixBytes := g.derivePrefixBytes(prefixLen, seen, keyOffset)

		return slotcache.Prefix{Offset: keyOffset, Bits: 0, Bytes: prefixBytes}
	}

	maxBits := maxPrefixBytes * 8
	prefixBits := 1 + (int(g.decoder.NextByte()) % maxBits)
	needBytes := (prefixBits + 7) / 8
	prefixBytes := g.derivePrefixBytes(needBytes, seen, keyOffset)

	return slotcache.Prefix{Offset: keyOffset, Bits: prefixBits, Bytes: prefixBytes}
}

func (g *OpGenerator) derivePrefixBytes(length int, seen [][]byte, keyOffset int) []byte {
	if len(seen) > 0 && g.decoder.NextByte() < 192 {
		idx := int(g.decoder.NextByte()) % len(seen)

		key := seen[idx]
		if keyOffset+length <= len(key) {
			return append([]byte(nil), key[keyOffset:keyOffset+length]...)
		}
	}

	return g.decoder.NextBytes(length)
}

func (g *OpGenerator) nextRangeBound(seen [][]byte) []byte {
	keySize := g.options.KeySize
	mode := g.decoder.NextByte()

	// ~10% invalid bounds.
	if mode < 26 {
		if g.decoder.NextByte()&1 == 0 {
			return []byte{}
		}

		return make([]byte, keySize+1)
	}

	// ~30% nil (unbounded).
	if mode < 77 {
		return nil
	}

	if len(seen) > 0 && mode < 200 {
		idx := int(g.decoder.NextByte()) % len(seen)
		key := seen[idx]
		length := 1 + (int(g.decoder.NextByte()) % keySize)

		return append([]byte(nil), key[:length]...)
	}

	length := 1 + (int(g.decoder.NextByte()) % keySize)

	return g.decoder.NextBytes(length)
}

func (g *OpGenerator) nextFilterSpec(seen [][]byte) *FilterSpec {
	// ~30% of scans get a filter.
	if g.decoder.NextByte()%10 >= 3 {
		return nil
	}

	kind := FilterKind(g.decoder.NextByte() % 5)
	keySize := g.options.KeySize
	indexSize := g.options.IndexSize

	switch kind {
	case FilterNone:
		return &FilterSpec{Kind: FilterNone}

	case FilterRevisionMask:
		masks := []int64{1, 3, 7, 15}
		mask := masks[int(g.decoder.NextByte())%len(masks)]
		want := int64(g.decoder.NextByte()) & mask

		return &FilterSpec{Kind: FilterRevisionMask, Mask: mask, Want: want}

	case FilterIndexByteEq:
		if indexSize <= 0 {
			return &FilterSpec{Kind: FilterAll}
		}

		offset := int(g.decoder.NextByte()) % indexSize
		b := g.decoder.NextByte()

		return &FilterSpec{Kind: FilterIndexByteEq, Offset: offset, Byte: b}

	case FilterKeyPrefixEq:
		maxPrefixLen := min(keySize, 4)
		if maxPrefixLen <= 0 {
			return &FilterSpec{Kind: FilterAll}
		}

		prefixLen := 1 + int(g.decoder.NextByte())%maxPrefixLen
		if len(seen) > 0 && g.decoder.NextByte() < 200 {
			k := seen[int(g.decoder.NextByte())%len(seen)]

			return &FilterSpec{Kind: FilterKeyPrefixEq, Prefix: append([]byte(nil), k[:prefixLen]...)}
		}

		return &FilterSpec{Kind: FilterKeyPrefixEq, Prefix: g.decoder.NextBytes(prefixLen)}

	default:
		return &FilterSpec{Kind: FilterAll}
	}
}

func (g *OpGenerator) nextScanOpts() slotcache.ScanOptions {
	mode := g.decoder.NextByte()

	// Invalid rate.
	invalidThreshold := byte(float64(256) * float64(g.config.InvalidScanOptsRate) / 100.0)
	if mode < invalidThreshold {
		if g.decoder.NextByte()&1 == 0 {
			return slotcache.ScanOptions{Reverse: false, Offset: -1, Limit: 0}
		}

		return slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: -1}
	}

	var offset, limit int
	if g.config.SmallScanLimitBias {
		offset = int(g.decoder.NextByte() % 5)
		limit = int(g.decoder.NextByte() % 4) // 0..3 (0 = unlimited)
	} else {
		offset = int(g.decoder.NextByte())
		limit = int(g.decoder.NextByte())
	}

	return slotcache.ScanOptions{
		Reverse: g.decoder.NextByte()&1 == 1,
		Offset:  offset,
		Limit:   limit,
	}
}
