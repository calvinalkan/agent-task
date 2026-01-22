package testutil

import "github.com/calvinalkan/agent-task/pkg/slotcache"

// interestingCaps are capacity values that stress edge cases.
var interestingCaps = []uint64{1, 2, 3, 4, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128}

// OptionsToSeed produces bytes that DeriveFuzzOptions will decode to the given options.
// This is the inverse of DeriveFuzzOptions - useful for creating seed bytes from
// known configurations.
func OptionsToSeed(opts slotcache.Options) []byte {
	var buf []byte

	// KeySize: keySize = 1 + (byte % 32), so byte = keySize - 1
	// IndexSize: indexSize = byte % 33, so byte = indexSize
	buf = append(buf, byte(opts.KeySize-1), byte(opts.IndexSize))

	// SlotCapacity: check if it's in interestingCaps
	capIndex := -1

	for i, c := range interestingCaps {
		if c == opts.SlotCapacity {
			capIndex = i

			break
		}
	}

	// OrderedKeys: flags & 0x01 != 0
	var flags byte
	if opts.OrderedKeys {
		flags = 1
	}

	if capIndex >= 0 {
		// Use interesting caps path: capSelector % 4 == 0, then index into array
		buf = append(buf, 0, byte(capIndex), flags) // capSelector = 0, so 0 % 4 == 0
	} else {
		// Use direct path: capSelector % 4 != 0, then slotCap = 1 + (byte % 128)
		buf = append(buf, 1, byte(opts.SlotCapacity-1), flags) // capSelector = 1, so 1 % 4 != 0
	}

	return buf
}

// OptionsFromSeed deterministically derives a small-but-interesting option set
// from fuzz bytes and returns the remaining bytes.
//
// This keeps resource usage bounded (small mmaps) while exercising alignment,
// padding, ordered/unordered mode, and very small capacities.
func OptionsFromSeed(fuzzBytes []byte, path string) (slotcache.Options, []byte) {
	stream := NewByteStream(fuzzBytes)

	// KeySize: [1..32] (hits padding cases like 1,2,3,7,9,15,16,17,...)
	keySize := 1 + int(stream.NextByte()%32)

	// IndexSize: [0..32] (includes the important IndexSize==0 case)
	indexSize := int(stream.NextByte() % 33)

	// SlotCapacity: favor small/edge values to stress full tables and probe chains.
	capSelector := stream.NextByte()

	var slotCap uint64
	if capSelector%4 == 0 {
		slotCap = interestingCaps[int(stream.NextByte())%len(interestingCaps)]
	} else {
		slotCap = 1 + uint64(stream.NextByte()%128) // [1..128]
	}

	flags := stream.NextByte()
	ordered := (flags & 0x01) != 0

	opts := slotcache.Options{
		Path:         path,
		KeySize:      keySize,
		IndexSize:    indexSize,
		UserVersion:  1,
		SlotCapacity: slotCap,
		OrderedKeys:  ordered,
		Writeback:    slotcache.WritebackNone, // keep behavior-oracle deterministic
	}

	return opts, stream.Rest()
}
