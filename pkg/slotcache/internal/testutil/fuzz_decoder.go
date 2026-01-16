package testutil

import (
	"encoding/binary"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// FuzzDecoder interprets fuzz bytes as a deterministic stream of choices.
//
// IMPORTANT: The decoder must be deterministic *for a given input* so Go's fuzzer
// can minimize failing inputs.
type FuzzDecoder struct {
	rawBytes []byte
	cursor   int

	options slotcache.Options

	orderedCounter uint32
}

// NewFuzzDecoder constructs a decoder for fuzz inputs.
func NewFuzzDecoder(fuzzBytes []byte, options slotcache.Options) *FuzzDecoder {
	return &FuzzDecoder{
		rawBytes:       fuzzBytes,
		cursor:         0,
		options:        options,
		orderedCounter: 0,
	}
}

// HasMore reports whether more fuzz bytes remain.
func (decoder *FuzzDecoder) HasMore() bool {
	return decoder.cursor < len(decoder.rawBytes)
}

// NextByte returns the next byte in the stream (0 if exhausted).
func (decoder *FuzzDecoder) NextByte() byte {
	if decoder.cursor >= len(decoder.rawBytes) {
		return 0
	}

	value := decoder.rawBytes[decoder.cursor]
	decoder.cursor++

	return value
}

// NextInt64 reads the next int64 value (little-endian, 8 bytes).
// If the stream ends, missing bytes are treated as 0.
func (decoder *FuzzDecoder) NextInt64() int64 {
	var raw [8]byte
	for index := range raw {
		raw[index] = decoder.NextByte()
	}

	return getInt64LE(raw[:])
}

// getInt64LE reads an int64 from buf in little-endian byte order.
// This avoids uint64->int64 conversion that binary.LittleEndian.Uint64 returns.
func getInt64LE(buf []byte) int64 {
	// Bounds check hint: if buf[7] is valid, buf[0..6] are too.
	// Lets the compiler eliminate redundant bounds checks below.
	_ = buf[7]

	return int64(buf[0]) |
		int64(buf[1])<<8 |
		int64(buf[2])<<16 |
		int64(buf[3])<<24 |
		int64(buf[4])<<32 |
		int64(buf[5])<<40 |
		int64(buf[6])<<48 |
		int64(buf[7])<<56
}

// NextBytes reads exactly length bytes from the stream.
// If the stream ends, remaining bytes are filled with 0.
func (decoder *FuzzDecoder) NextBytes(length int) []byte {
	if length <= 0 {
		return []byte{}
	}

	output := make([]byte, length)
	for index := range length {
		output[index] = decoder.NextByte()
	}

	return output
}

// NextPrefix reads a variable-length prefix with length in [1..keySize].
func (decoder *FuzzDecoder) NextPrefix(keySize int) []byte {
	length := 1 + int(decoder.NextByte())%keySize

	return decoder.NextBytes(length)
}

// NextOp chooses the next operation based on the current harness state.
//
// It ONLY chooses operations that are valid *from the harness perspective*
// (e.g. it will not emit Writer.Put unless a writer is active), so failures are
// meaningful implementation/model issues, not harness panics.
func (decoder *FuzzDecoder) NextOp(testHarness *Harness, previouslySeenKeys [][]byte) Operation {
	writerIsActive := testHarness.Model.Writer != nil && testHarness.Real.Writer != nil

	// Mirror the property-test behavior: give Close/Reopen a chance even when a writer is active.
	// This is important because Close/Reopen are specified to return ErrBusy while a writer is active.
	roulette := decoder.NextByte()

	// ~5% Reopen
	if roulette < 13 {
		return OpReopen{}
	}

	// ~5% Close
	if roulette >= 13 && roulette < 26 {
		return OpClose{}
	}

	if !writerIsActive {
		choice := int(decoder.NextByte() % 7)

		switch choice {
		case 1:
			return OpGet{Key: decoder.nextKey(testHarness.Options.KeySize, previouslySeenKeys)}
		case 2:
			return OpScan{
				Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
				Options: decoder.nextScanOpts(),
			}
		case 3:
			return OpScanPrefix{
				Prefix:  decoder.derivePrefixFromKeys(testHarness.Options.KeySize, previouslySeenKeys),
				Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
				Options: decoder.nextScanOpts(),
			}
		case 4:
			return OpScanMatch{
				Spec:    decoder.nextPrefixSpec(testHarness.Options.KeySize, previouslySeenKeys),
				Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
				Options: decoder.nextScanOpts(),
			}
		case 5:
			return OpScanRange{
				Start:   decoder.nextRangeBound(testHarness.Options.KeySize, previouslySeenKeys),
				End:     decoder.nextRangeBound(testHarness.Options.KeySize, previouslySeenKeys),
				Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
				Options: decoder.nextScanOpts(),
			}
		case 6:
			return OpBeginWrite{}
		default:
			return OpLen{}
		}
	}

	// Writer is active.
	choice := int(decoder.NextByte() % 10)

	switch choice {
	case 0:
		return OpPut{
			Key:      decoder.nextKey(testHarness.Options.KeySize, previouslySeenKeys),
			Revision: decoder.NextInt64(),
			Index:    decoder.nextIndex(testHarness.Options.IndexSize),
		}
	case 1:
		return OpDelete{Key: decoder.nextKey(testHarness.Options.KeySize, previouslySeenKeys)}
	case 2:
		return OpCommit{}
	case 3:
		return OpWriterClose{}
	case 5:
		return OpGet{Key: decoder.nextKey(testHarness.Options.KeySize, previouslySeenKeys)}
	case 6:
		return OpScan{
			Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
			Options: decoder.nextScanOpts(),
		}
	case 7:
		return OpScanPrefix{
			Prefix:  decoder.derivePrefixFromKeys(testHarness.Options.KeySize, previouslySeenKeys),
			Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
			Options: decoder.nextScanOpts(),
		}
	case 8:
		return OpScanMatch{
			Spec:    decoder.nextPrefixSpec(testHarness.Options.KeySize, previouslySeenKeys),
			Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
			Options: decoder.nextScanOpts(),
		}
	case 9:
		return OpScanRange{
			Start:   decoder.nextRangeBound(testHarness.Options.KeySize, previouslySeenKeys),
			End:     decoder.nextRangeBound(testHarness.Options.KeySize, previouslySeenKeys),
			Filter:  decoder.nextFilterSpec(testHarness.Options.KeySize, testHarness.Options.IndexSize, previouslySeenKeys),
			Options: decoder.nextScanOpts(),
		}
	default:
		return OpLen{}
	}
}

func (decoder *FuzzDecoder) nextBool() bool {
	return (decoder.NextByte() & 0x01) == 1
}

func (decoder *FuzzDecoder) nextKey(keySize int, previouslySeenKeys [][]byte) []byte {
	// Match the property test distribution:
	// - 15% invalid (nil or wrong length)
	// - 60% reuse an existing key
	// - otherwise generate a new key
	mode := decoder.NextByte()

	// ~15%
	if mode < 38 {
		if decoder.nextBool() {
			return nil
		}

		// Wrong length.
		wrongLength := int(decoder.NextByte()) % (keySize + 2)
		if wrongLength == keySize {
			wrongLength = keySize + 1
		}

		return decoder.NextBytes(wrongLength)
	}

	// ~60% (when we have seen keys)
	if len(previouslySeenKeys) > 0 && mode < 192 {
		selectedIndex := int(decoder.NextByte()) % len(previouslySeenKeys)
		selectedKey := previouslySeenKeys[selectedIndex]

		return append([]byte(nil), selectedKey...)
	}

	// New valid key.
	if decoder.options.OrderedKeys {
		// In ordered mode, most new keys are monotonic so commits can make progress,
		// but some are intentionally non-monotonic to exercise ErrOutOfOrderInsert.
		if mode < 240 {
			return decoder.nextOrderedKey(keySize)
		}

		return decoder.nextNonMonotonicOrderedKey(keySize)
	}

	return decoder.NextBytes(keySize)
}

func (decoder *FuzzDecoder) nextOrderedKey(keySize int) []byte {
	if keySize <= 0 {
		return []byte{}
	}

	decoder.orderedCounter++

	key := make([]byte, keySize)

	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], decoder.orderedCounter)

	if keySize >= 4 {
		copy(key[:4], tmp[:])

		return key
	}

	copy(key, tmp[4-keySize:])

	return key
}

func (decoder *FuzzDecoder) nextNonMonotonicOrderedKey(keySize int) []byte {
	if keySize <= 0 {
		return []byte{}
	}

	// If we haven't generated any ordered keys yet, fall back to monotonic.
	if decoder.orderedCounter == 0 {
		return decoder.nextOrderedKey(keySize)
	}

	// Pick a counter value smaller than the current monotonic counter.
	delta := min(uint32(decoder.NextByte()%16)+1, decoder.orderedCounter)

	base := decoder.orderedCounter - delta

	key := make([]byte, keySize)

	var tmp [4]byte
	binary.BigEndian.PutUint32(tmp[:], base)

	if keySize < 4 {
		copy(key, tmp[4-keySize:])

		return key
	}

	copy(key[:4], tmp[:])

	// Ensure the key is different from the monotonic key for this base counter.
	// (Monotonic keys have a zero suffix.)
	if keySize > 4 {
		key[keySize-1] = decoder.NextByte() | 1
	}

	return key
}

func (decoder *FuzzDecoder) nextIndex(indexSize int) []byte {
	// Match property test distribution: 10% invalid length.
	mode := decoder.NextByte()

	// ~10%
	if mode < 26 {
		wrongLength := int(decoder.NextByte()) % (indexSize + 2)
		if wrongLength == indexSize {
			wrongLength = indexSize + 1
		}

		return decoder.NextBytes(wrongLength)
	}

	return decoder.NextBytes(indexSize)
}

func (decoder *FuzzDecoder) derivePrefixFromKeys(keySize int, previouslySeenKeys [][]byte) []byte {
	// Match property test distribution: 20% invalid.
	mode := decoder.NextByte()

	// ~20%
	if mode < 52 {
		invalidMode := int(decoder.NextByte()) % 3

		switch invalidMode {
		case 1:
			return []byte{}
		case 2:
			return make([]byte, keySize+1)
		default:
			return nil
		}
	}

	// Prefer deriving a prefix from an existing key.
	if len(previouslySeenKeys) > 0 {
		selectedIndex := int(decoder.NextByte()) % len(previouslySeenKeys)
		selectedKey := previouslySeenKeys[selectedIndex]

		prefixLength := 1 + (int(decoder.NextByte()) % keySize) // 1..keySize

		return append([]byte(nil), selectedKey[:prefixLength]...)
	}

	prefixLength := 1 + (int(decoder.NextByte()) % keySize)

	return decoder.NextBytes(prefixLength)
}

func (decoder *FuzzDecoder) nextPrefixSpec(keySize int, previouslySeenKeys [][]byte) slotcache.Prefix {
	// Match the ScanPrefix distribution: 20% invalid.
	mode := decoder.NextByte()

	// ~20%
	if mode < 52 {
		invalidMode := int(decoder.NextByte()) % 5

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

	keyOffset := int(decoder.NextByte()) % keySize
	maxPrefixBytes := keySize - keyOffset

	// 50% byte-aligned, 50% bit-granular.
	if decoder.nextBool() {
		prefixLen := 1 + (int(decoder.NextByte()) % maxPrefixBytes)
		prefixBytes := decoder.derivePrefixBytes(prefixLen, previouslySeenKeys, keyOffset)

		return slotcache.Prefix{Offset: keyOffset, Bits: 0, Bytes: prefixBytes}
	}

	maxBits := maxPrefixBytes * 8
	prefixBits := 1 + (int(decoder.NextByte()) % maxBits)
	needBytes := (prefixBits + 7) / 8
	prefixBytes := decoder.derivePrefixBytes(needBytes, previouslySeenKeys, keyOffset)

	return slotcache.Prefix{Offset: keyOffset, Bits: prefixBits, Bytes: prefixBytes}
}

func (decoder *FuzzDecoder) derivePrefixBytes(length int, previouslySeenKeys [][]byte, keyOffset int) []byte {
	// Prefer deriving from an existing key for better semantic coverage.
	if len(previouslySeenKeys) > 0 && decoder.NextByte() < 192 {
		selectedIndex := int(decoder.NextByte()) % len(previouslySeenKeys)
		selectedKey := previouslySeenKeys[selectedIndex]

		if keyOffset+length <= len(selectedKey) {
			return append([]byte(nil), selectedKey[keyOffset:keyOffset+length]...)
		}
	}

	return decoder.NextBytes(length)
}

func (decoder *FuzzDecoder) nextRangeBound(keySize int, previouslySeenKeys [][]byte) []byte {
	mode := decoder.NextByte()

	// ~10% invalid bounds.
	if mode < 26 {
		if decoder.nextBool() {
			return []byte{}
		}

		return make([]byte, keySize+1)
	}

	// ~30% nil (unbounded)
	if mode < 77 {
		return nil
	}

	// Prefer deriving a bound from an existing key.
	if len(previouslySeenKeys) > 0 && mode < 200 {
		selectedIndex := int(decoder.NextByte()) % len(previouslySeenKeys)
		selectedKey := previouslySeenKeys[selectedIndex]

		length := 1 + (int(decoder.NextByte()) % keySize)

		return append([]byte(nil), selectedKey[:length]...)
	}

	length := 1 + (int(decoder.NextByte()) % keySize)

	return decoder.NextBytes(length)
}

func (decoder *FuzzDecoder) nextFilterSpec(keySize, indexSize int, previouslySeenKeys [][]byte) *FilterSpec {
	// ~30% of scans get a filter
	if decoder.NextByte()%10 >= 3 {
		return nil
	}

	kind := FilterKind(decoder.NextByte() % 5)

	switch kind {
	case FilterNone:
		return &FilterSpec{Kind: FilterNone}

	case FilterRevisionMask:
		masks := []int64{1, 3, 7, 15}
		mask := masks[int(decoder.NextByte())%len(masks)]
		want := int64(decoder.NextByte()) & mask

		return &FilterSpec{Kind: FilterRevisionMask, Mask: mask, Want: want}

	case FilterIndexByteEq:
		if indexSize <= 0 {
			return &FilterSpec{Kind: FilterAll}
		}

		offset := int(decoder.NextByte()) % indexSize
		b := decoder.NextByte()

		return &FilterSpec{Kind: FilterIndexByteEq, Offset: offset, Byte: b}

	case FilterKeyPrefixEq:
		maxPrefixLen := min(keySize, 4)

		if maxPrefixLen <= 0 {
			return &FilterSpec{Kind: FilterAll}
		}

		prefixLen := 1 + int(decoder.NextByte())%maxPrefixLen

		// Prefer deriving from a real known key
		if len(previouslySeenKeys) > 0 && decoder.NextByte() < 200 {
			k := previouslySeenKeys[int(decoder.NextByte())%len(previouslySeenKeys)]

			return &FilterSpec{Kind: FilterKeyPrefixEq, Prefix: append([]byte(nil), k[:prefixLen]...)}
		}

		return &FilterSpec{Kind: FilterKeyPrefixEq, Prefix: decoder.NextBytes(prefixLen)}

	default:
		return &FilterSpec{Kind: FilterAll}
	}
}

func (decoder *FuzzDecoder) nextScanOpts() slotcache.ScanOptions {
	// Match property test distribution: 10% invalid.
	mode := decoder.NextByte()

	// ~10%
	if mode < 26 {
		if decoder.nextBool() {
			return slotcache.ScanOptions{Reverse: false, Offset: -1, Limit: 0}
		}

		return slotcache.ScanOptions{Reverse: false, Offset: 0, Limit: -1}
	}

	// Keep these small; large offsets clamp to empty result.
	offset := int(decoder.NextByte() % 5)
	limit := int(decoder.NextByte() % 4) // 0..3 (0 means unlimited)

	return slotcache.ScanOptions{
		Reverse: decoder.nextBool(),
		Offset:  offset,
		Limit:   limit,
	}
}
