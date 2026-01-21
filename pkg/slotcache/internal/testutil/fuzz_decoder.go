package testutil

import (
	"encoding/binary"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// FuzzDecoder interprets fuzz bytes as a deterministic stream of choices.
//
// IMPORTANT: The decoder must be deterministic *for a given input* so Go's fuzzer
// can minimize failing inputs.
//
// FuzzDecoder provides low-level byte/value reading and key/index generation.
// For operation generation, use OpGenerator which wraps FuzzDecoder and provides
// configurable operation selection via the canonical OpGenerator protocol.
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

// NextKey generates a key (sometimes invalid, sometimes reused, sometimes new).
//
// This is exported so non-harness fuzz tests can reuse the same key-generation
// distribution as the behavior harness.
func (decoder *FuzzDecoder) NextKey(previouslySeenKeys [][]byte) []byte {
	return decoder.genKey(decoder.options.KeySize, previouslySeenKeys)
}

// NextIndex generates an index (sometimes invalid length).
func (decoder *FuzzDecoder) NextIndex() []byte {
	return decoder.genIndex(decoder.options.IndexSize)
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

// NextUint64 reads the next uint64 value (little-endian, 8 bytes).
// If the stream ends, missing bytes are treated as 0.
func (decoder *FuzzDecoder) NextUint64() uint64 {
	var raw [8]byte
	for index := range raw {
		raw[index] = decoder.NextByte()
	}

	return binary.LittleEndian.Uint64(raw[:])
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

// nextBool returns a boolean derived from the next byte.
func (decoder *FuzzDecoder) nextBool() bool {
	return (decoder.NextByte() & 0x01) == 1
}

// genKey generates a key with a mix of invalid, reused, and new keys.
func (decoder *FuzzDecoder) genKey(keySize int, previouslySeenKeys [][]byte) []byte {
	// Key generation tries to balance:
	//  - invalid inputs (exercise ErrInvalidInput paths)
	//  - key reuse (exercise update/delete paths)
	//  - new keys (exercise slot allocation, bucket probe chains, ErrFull)
	mode := decoder.NextByte()

	// ~15% invalid (nil or wrong length).
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

	// Reuse rate depends on how many keys we have seen.
	// Early on, prefer new keys to build up state; later, reuse more often.
	reuseThreshold := byte(160) // ~48% reuse
	if len(previouslySeenKeys) < 4 {
		reuseThreshold = 96 // ~23% reuse
	}

	if len(previouslySeenKeys) > 32 {
		reuseThreshold = 208 // ~66% reuse
	}

	if len(previouslySeenKeys) > 0 && mode < reuseThreshold {
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

// nextOrderedKey generates a monotonically increasing key for ordered mode.
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

// nextNonMonotonicOrderedKey generates a key that is intentionally out of order.
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

// genIndex generates an index with a mix of invalid and valid lengths.
func (decoder *FuzzDecoder) genIndex(indexSize int) []byte {
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
