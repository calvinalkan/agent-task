package testutil

import (
	"encoding/binary"
	"fmt"
	"os"
)

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

// Header state values (mirrors slotcache.stateNormal and stateInvalidated).
const (
	StateNormal      uint32 = 0
	StateInvalidated uint32 = 1
)

// offState is the byte offset of the state field in the SLC1 header.
const offState = 0x074

// ReadHeaderState reads the state field from the header of a slotcache file.
// Returns an error if the file cannot be read or is too small.
//
// This helper is intended for unit tests that need to verify the on-disk state
// after operations like Invalidate().
func ReadHeaderState(filePath string) (uint32, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("open file: %w", err)
	}

	defer func() { _ = f.Close() }()

	buf := make([]byte, 4)

	n, err := f.ReadAt(buf, offState)
	if err != nil || n < 4 {
		return 0, fmt.Errorf("read state field: %w", err)
	}

	return binary.LittleEndian.Uint32(buf), nil
}

// AssertHeaderState reads the state field from a slotcache file and returns
// an error if it doesn't match the expected value.
//
// This helper is intended for unit tests that want a concise assertion.
func AssertHeaderState(filePath string, expectedState uint32) error {
	state, err := ReadHeaderState(filePath)
	if err != nil {
		return err
	}

	if state != expectedState {
		var expectedName, actualName string

		switch expectedState {
		case StateNormal:
			expectedName = "STATE_NORMAL"
		case StateInvalidated:
			expectedName = "STATE_INVALIDATED"
		default:
			expectedName = fmt.Sprintf("unknown(%d)", expectedState)
		}

		switch state {
		case StateNormal:
			actualName = "STATE_NORMAL"
		case StateInvalidated:
			actualName = "STATE_INVALIDATED"
		default:
			actualName = fmt.Sprintf("unknown(%d)", state)
		}

		return fmt.Errorf("state mismatch: expected %s, got %s", expectedName, actualName)
	}

	return nil
}
