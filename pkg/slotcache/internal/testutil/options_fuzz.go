package testutil

import "github.com/calvinalkan/agent-task/pkg/slotcache"

// DeriveFuzzOptions deterministically derives a small-but-interesting option set
// from fuzz bytes and returns the remaining bytes.
//
// This keeps resource usage bounded (small mmaps) while exercising alignment,
// padding, ordered/unordered mode, and very small capacities.
func DeriveFuzzOptions(fuzzBytes []byte, path string) (slotcache.Options, []byte) {
	stream := NewByteStream(fuzzBytes)

	// KeySize: [1..32] (hits padding cases like 1,2,3,7,9,15,16,17,...)
	keySize := 1 + int(stream.NextByte()%32)

	// IndexSize: [0..32] (includes the important IndexSize==0 case)
	indexSize := int(stream.NextByte() % 33)

	// SlotCapacity: favor small/edge values to stress full tables and probe chains.
	interestingCaps := []uint64{1, 2, 3, 4, 7, 8, 9, 15, 16, 17, 31, 32, 33, 63, 64, 65, 127, 128}
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
