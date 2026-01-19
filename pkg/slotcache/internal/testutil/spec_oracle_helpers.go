package testutil

// Helpers used by spec_oracle on rare (error) paths.

func readSlotKey(fileBytes []byte, slotsOffset, slotSizeBytes, keySizeBytes, slotID uint64) []byte {
	if keySizeBytes == 0 {
		return nil
	}

	off := slotsOffset + slotID*slotSizeBytes + 8

	end := off + keySizeBytes
	if end > uint64(len(fileBytes)) {
		return nil
	}

	key := make([]byte, keySizeBytes)
	copy(key, fileBytes[off:end])

	return key
}
