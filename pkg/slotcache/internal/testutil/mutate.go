package testutil

// MutateBytes applies a bounded sequence of simple deterministic mutations to src.
//
// This is used for "near-valid" corruption fuzzing: start from a valid file,
// then apply small edits so Open() gets past the header more often.
func MutateBytes(src []byte, stream *ByteStream) []byte {
	mut := append([]byte(nil), src...)

	// 1..8 mutation steps.
	steps := 1 + int(stream.NextByte()%8)

	for range steps {
		if len(mut) == 0 {
			// Ensure we can still grow from empty.
			mut = append(mut, 0)
		}

		op := stream.NextByte() % 6

		switch op {
		case 0: // flip bits in-place
			off := int(stream.NextUint32()) % len(mut)
			n := 1 + int(stream.NextByte()%32)
			end := min(off+n, len(mut))

			mask := byte(1 << (stream.NextByte() % 8))
			for i := off; i < end; i++ {
				mut[i] ^= mask
			}

		case 1: // overwrite a range
			off := int(stream.NextUint32()) % len(mut)
			n := 1 + int(stream.NextByte()%64)

			end := min(off+n, len(mut))
			for i := off; i < end; i++ {
				mut[i] = stream.NextByte()
			}

		case 2: // truncate to a smaller length
			newLen := int(stream.NextUint32()) % (len(mut) + 1)
			mut = mut[:newLen]

		case 3: // append some bytes (bounded growth)
			add := 1 + int(stream.NextByte()%128)
			for range add {
				mut = append(mut, stream.NextByte())
			}

		case 4: // insert a short run at an arbitrary position
			off := int(stream.NextUint32()) % (len(mut) + 1)
			add := 1 + int(stream.NextByte()%32)

			insert := make([]byte, 0, add)
			for range add {
				insert = append(insert, stream.NextByte())
			}

			mut = append(mut[:off], append(insert, mut[off:]...)...)

		case 5: // duplicate a short range somewhere else
			if len(mut) < 2 {
				continue
			}

			from := int(stream.NextUint32()) % len(mut)
			to := int(stream.NextUint32()) % (len(mut) + 1)
			ln := 1 + int(stream.NextByte()%32)
			end := min(from+ln, len(mut))
			chunk := append([]byte(nil), mut[from:end]...)
			mut = append(mut[:to], append(chunk, mut[to:]...)...)
		}

		// Keep mutated blobs bounded so one input can't force huge allocations.
		if len(mut) > 1<<20 { // 1 MiB
			mut = mut[:1<<20]
		}
	}

	return mut
}
