package slotcache

import (
	"bytes"
	"errors"
	"testing"
)

func Test_ComputeSlotSize_Returns_Correct_Size_When_Given_Key_And_Index_Sizes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		keySize   uint32
		indexSize uint32
		want      uint32
	}{
		// keySize=16: keyPad = (8-16%8)%8 = 0, unaligned = 8+16+0+8+0 = 32, aligned = 32
		{keySize: 16, indexSize: 0, want: 32},
		// keySize=16, indexSize=8: unaligned = 8+16+0+8+8 = 40, aligned = 40
		{keySize: 16, indexSize: 8, want: 40},
		// keySize=7: keyPad = (8-7%8)%8 = 1, unaligned = 8+7+1+8+0 = 24, aligned = 24
		{keySize: 7, indexSize: 0, want: 24},
		// keySize=5: keyPad = (8-5%8)%8 = 3, unaligned = 8+5+3+8+0 = 24, aligned = 24
		{keySize: 5, indexSize: 0, want: 24},
		// keySize=8: keyPad = (8-8%8)%8 = 0, unaligned = 8+8+0+8+0 = 24, aligned = 24
		{keySize: 8, indexSize: 0, want: 24},
		// keySize=1: keyPad = (8-1%8)%8 = 7, unaligned = 8+1+7+8+0 = 24, aligned = 24
		{keySize: 1, indexSize: 0, want: 24},
		// keySize=16, indexSize=3: unaligned = 8+16+0+8+3 = 35, aligned = 40
		{keySize: 16, indexSize: 3, want: 40},
	}

	for _, tt := range tests {
		got := computeSlotSize(tt.keySize, tt.indexSize)
		if got != tt.want {
			t.Errorf("computeSlotSize(%d, %d) = %d, want %d", tt.keySize, tt.indexSize, got, tt.want)
		}
	}
}

func Test_EncodeDecodeSlot_Roundtrips_Correctly_When_Given_Various_Inputs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		keySize   uint32
		indexSize uint32
		key       []byte
		isLive    bool
		revision  int64
		index     []byte
	}{
		{
			name:      "basic live slot",
			keySize:   16,
			indexSize: 0,
			key:       []byte("0123456789abcdef"),
			isLive:    true,
			revision:  12345,
			index:     nil,
		},
		{
			name:      "dead slot",
			keySize:   16,
			indexSize: 0,
			key:       []byte("deaddeaddeaddead"),
			isLive:    false,
			revision:  0,
			index:     nil,
		},
		{
			name:      "with index data",
			keySize:   8,
			indexSize: 4,
			key:       []byte("testkey!"),
			isLive:    true,
			revision:  -9999,
			index:     []byte{0xDE, 0xAD, 0xBE, 0xEF},
		},
		{
			name:      "odd key size with padding",
			keySize:   7,
			indexSize: 0,
			key:       []byte("oddkey!"),
			isLive:    true,
			revision:  42,
			index:     nil,
		},
		{
			name:      "negative revision",
			keySize:   16,
			indexSize: 8,
			key:       []byte("negativerevisio!"),
			isLive:    true,
			revision:  -1,
			index:     []byte("indexdat"),
		},
		{
			name:      "max revision",
			keySize:   16,
			indexSize: 0,
			key:       []byte("maxrevisionkey!!"),
			isLive:    true,
			revision:  9223372036854775807, // int64 max
			index:     nil,
		},
		{
			name:      "min revision",
			keySize:   16,
			indexSize: 0,
			key:       []byte("minrevisionkey!!"),
			isLive:    true,
			revision:  -9223372036854775808, // int64 min
			index:     nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Encode
			buf := encodeSlot(tt.key, tt.isLive, tt.revision, tt.index, tt.keySize, tt.indexSize)

			// Verify size
			expectedSize := computeSlotSize(tt.keySize, tt.indexSize)
			if uint32(len(buf)) != expectedSize {
				t.Errorf("encoded slot size = %d, want %d", len(buf), expectedSize)
			}

			// Decode
			got := decodeSlot(buf, tt.keySize, tt.indexSize)

			// Verify fields
			if !bytes.Equal(got.key, tt.key) {
				t.Errorf("key = %v, want %v", got.key, tt.key)
			}

			if got.isLive != tt.isLive {
				t.Errorf("isLive = %v, want %v", got.isLive, tt.isLive)
			}

			if got.revision != tt.revision {
				t.Errorf("revision = %d, want %d", got.revision, tt.revision)
			}

			if tt.indexSize > 0 {
				if !bytes.Equal(got.index, tt.index) {
					t.Errorf("index = %v, want %v", got.index, tt.index)
				}
			}
		})
	}
}

func Test_EncodeSlot_Sets_Meta_Bit_Correctly_When_Slot_Is_Live_Or_Dead(t *testing.T) {
	t.Parallel()

	key := []byte("0123456789abcdef")

	// Live slot should have bit 0 set
	liveBuf := encodeSlot(key, true, 0, nil, 16, 0)
	if liveBuf[0] != 1 || liveBuf[1] != 0 { // Little-endian: meta=1 means byte[0]=1, byte[1..7]=0
		t.Errorf("live slot meta bytes: got %v, want [1 0 0 0 0 0 0 0]", liveBuf[0:8])
	}

	// Dead slot should have meta=0
	deadBuf := encodeSlot(key, false, 0, nil, 16, 0)
	for i := range 8 {
		if deadBuf[i] != 0 {
			t.Errorf("dead slot meta byte[%d] = %d, want 0", i, deadBuf[i])
		}
	}
}

func Test_EncodeSlot_Aligns_Revision_To_EightBytes_When_KeySize_Varies(t *testing.T) {
	t.Parallel()

	// Test that revision is correctly aligned for various key sizes.
	testCases := []struct {
		keySize        uint32
		expectedOffset uint32
	}{
		{keySize: 1, expectedOffset: 16},
		{keySize: 7, expectedOffset: 16},
		{keySize: 8, expectedOffset: 16},
		{keySize: 9, expectedOffset: 24},
		{keySize: 15, expectedOffset: 24},
		{keySize: 16, expectedOffset: 24},
	}

	for _, tc := range testCases {
		key := make([]byte, tc.keySize)
		for i := range key {
			key[i] = 'X'
		}

		revision := int64(0x0102030405060708) // Distinctive pattern

		buf := encodeSlot(key, true, revision, nil, tc.keySize, 0)

		// Check that revision is at the expected offset (little-endian)
		gotRevision := int64(buf[tc.expectedOffset]) |
			int64(buf[tc.expectedOffset+1])<<8 |
			int64(buf[tc.expectedOffset+2])<<16 |
			int64(buf[tc.expectedOffset+3])<<24 |
			int64(buf[tc.expectedOffset+4])<<32 |
			int64(buf[tc.expectedOffset+5])<<40 |
			int64(buf[tc.expectedOffset+6])<<48 |
			int64(buf[tc.expectedOffset+7])<<56

		if gotRevision != revision {
			t.Errorf("keySize=%d: revision at offset %d = 0x%016x, want 0x%016x",
				tc.keySize, tc.expectedOffset, gotRevision, revision)
		}

		// Also verify offset is 8-byte aligned
		if tc.expectedOffset%8 != 0 {
			t.Errorf("keySize=%d: revision offset %d is not 8-byte aligned", tc.keySize, tc.expectedOffset)
		}
	}
}

func Test_HeaderCRC_Validates_Correctly_When_Header_Is_Fresh_Or_Corrupted(t *testing.T) {
	t.Parallel()

	h := newHeader(16, 8, 1000, 1, false)
	buf := encodeHeader(&h)

	// Validate CRC
	if !validateHeaderCRC(buf) {
		t.Error("validateHeaderCRC returned false for freshly encoded header")
	}

	// Corrupt a byte and verify CRC fails
	buf[offKeySize]++
	if validateHeaderCRC(buf) {
		t.Error("validateHeaderCRC returned true for corrupted header")
	}
}

func Test_HeaderCRC_Remains_Unchanged_When_Generation_Changes(t *testing.T) {
	t.Parallel()

	h := newHeader(16, 8, 1000, 1, false)

	// Encode with generation=0
	h.Generation = 0
	buf1 := encodeHeader(&h)
	crc1 := computeHeaderCRC(buf1)

	// Encode with generation=42
	h.Generation = 42
	buf2 := encodeHeader(&h)
	crc2 := computeHeaderCRC(buf2)

	// CRCs should be the same since generation is zeroed before computing
	if crc1 != crc2 {
		t.Errorf("CRC changed with generation: crc1=%d, crc2=%d", crc1, crc2)
	}
}

func Test_ComputeBucketCount_Returns_Power_Of_Two_When_Given_Capacity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		slotCapacity uint64
		want         uint64
	}{
		{slotCapacity: 100, want: 256},   // 100*2=200, nextPow2=256
		{slotCapacity: 1000, want: 2048}, // 1000*2=2000, nextPow2=2048
		{slotCapacity: 1, want: 2},       // 1*2=2, nextPow2=2, minimum bucket count
		{slotCapacity: 0, want: 2},       // edge case, minimum bucket count
		{slotCapacity: 64, want: 128},    // 64*2=128, nextPow2=128
		{slotCapacity: 65, want: 256},    // 65*2=130, nextPow2=256
	}

	for _, tt := range tests {
		got := computeBucketCount(tt.slotCapacity)
		if got != tt.want {
			t.Errorf("computeBucketCount(%d) = %d, want %d", tt.slotCapacity, got, tt.want)
		}
	}
}

func Test_ComputeBucketCount_Returns_Zero_When_Overflow(t *testing.T) {
	t.Parallel()

	// slot_capacity * 2 would overflow uint64
	// maxUint64 / 2 + 1 = 9223372036854775808
	const overflowCapacity = (^uint64(0) >> 1) + 1 // maxUint64/2 + 1

	got := computeBucketCount(overflowCapacity)
	if got != 0 {
		t.Errorf("computeBucketCount(%d) = %d, want 0 (overflow)", overflowCapacity, got)
	}
}

func Test_ComputeBucketCountChecked_Returns_Error_When_Overflow(t *testing.T) {
	t.Parallel()

	// slot_capacity * 2 would overflow uint64
	const overflowCapacity = (^uint64(0) >> 1) + 1 // maxUint64/2 + 1

	_, err := computeBucketCountChecked(overflowCapacity)
	if err == nil {
		t.Error("computeBucketCountChecked should return error for overflow capacity")
	}

	if !errors.Is(err, ErrInvalidInput) {
		t.Errorf("computeBucketCountChecked error should wrap ErrInvalidInput, got %v", err)
	}
}

func Test_ComputeBucketCountChecked_Returns_Valid_Count_When_No_Overflow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		slotCapacity uint64
		want         uint64
	}{
		{slotCapacity: 0, want: 2},
		{slotCapacity: 1, want: 2},
		{slotCapacity: 64, want: 128},
		{slotCapacity: 100, want: 256},
	}

	for _, tt := range tests {
		got, err := computeBucketCountChecked(tt.slotCapacity)
		if err != nil {
			t.Errorf("computeBucketCountChecked(%d) returned error: %v", tt.slotCapacity, err)
		}

		if got != tt.want {
			t.Errorf("computeBucketCountChecked(%d) = %d, want %d", tt.slotCapacity, got, tt.want)
		}
	}
}

func Test_NextPow2_Returns_Smallest_Power_Of_Two_When_Given_Input(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input uint64
		want  uint64
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{5, 8},
		{7, 8},
		{8, 8},
		{9, 16},
		{100, 128},
		{1000, 1024},
	}

	for _, tt := range tests {
		got := nextPow2(tt.input)
		if got != tt.want {
			t.Errorf("nextPow2(%d) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func Test_HasReservedBytesSet_Returns_True_When_Reserved_Tail_Bytes_Are_Nonzero(t *testing.T) {
	t.Parallel()

	// Clean header
	h := newHeader(16, 0, 100, 1, false)

	buf := encodeHeader(&h)
	if hasReservedBytesSet(buf) {
		t.Error("hasReservedBytesSet returned true for clean header")
	}

	// Set a reserved tail byte (0x0C0-0x0FF region)
	buf[offReservedTailStart] = 0xFF
	if !hasReservedBytesSet(buf) {
		t.Error("hasReservedBytesSet returned false when reserved tail byte is set")
	}

	// Verify that user header bytes (0x078-0x0BF) are NOT checked by hasReservedBytesSet
	buf2 := encodeHeader(&h)
	buf2[offUserFlags] = 0xFF // Set a user flags byte

	buf2[offUserData] = 0xFF // Set a user data byte
	if hasReservedBytesSet(buf2) {
		t.Error("hasReservedBytesSet incorrectly flagged user header bytes as reserved")
	}
}

func Test_MsyncRange_Returns_Nil_When_Range_Is_Invalid(t *testing.T) {
	t.Parallel()

	data := make([]byte, 4096)

	// Empty length
	err := msyncRange(data, 0, 0)
	if err != nil {
		t.Errorf("msyncRange with length=0 should return nil, got %v", err)
	}

	// Negative length
	err = msyncRange(data, 0, -1)
	if err != nil {
		t.Errorf("msyncRange with negative length should return nil, got %v", err)
	}

	// Offset beyond data
	err = msyncRange(data, 5000, 100)
	if err != nil {
		t.Errorf("msyncRange with offset beyond data should return nil, got %v", err)
	}

	// Negative offset
	err = msyncRange(data, -1, 100)
	if err != nil {
		t.Errorf("msyncRange with negative offset should return nil, got %v", err)
	}
}

func Test_MsyncRange_Handles_Page_Alignment_When_Range_Spans_Pages(t *testing.T) {
	t.Parallel()

	// This test verifies that msyncRange doesn't panic when given
	// non-page-aligned inputs. It doesn't verify the actual msync call
	// succeeds (that depends on the data being mmap'd), but it verifies
	// the alignment logic works.

	// Page size check
	if pageSize <= 0 {
		t.Fatalf("pageSize should be positive, got %d", pageSize)
	}

	// Create a buffer large enough to span multiple pages
	size := pageSize * 3
	data := make([]byte, size)

	// Test various offsets and lengths (these won't actually sync since
	// the data isn't mmap'd, but they test the clamping/alignment logic)
	testCases := []struct {
		offset int
		length int
	}{
		{0, 1},               // Start of first page, 1 byte
		{0, pageSize},        // Exactly first page
		{pageSize / 2, 100},  // Middle of first page
		{pageSize - 1, 2},    // Spans page boundary
		{pageSize, pageSize}, // Second page
		{0, size},            // Entire buffer
		{100, size},          // Extends beyond buffer (should clamp)
	}

	for _, tc := range testCases {
		// These will likely fail with ENOMEM or similar since data isn't
		// actually mmap'd, but they shouldn't panic. We're testing the
		// alignment logic, not the actual syscall.
		_ = msyncRange(data, tc.offset, tc.length)
	}
}
