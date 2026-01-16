package slotcache

import (
	"bytes"
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

	h := newHeader(16, 8, 1000, 1, 0.75)
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

	h := newHeader(16, 8, 1000, 1, 0.75)

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

func Test_ComputeBucketCount_Returns_Power_Of_Two_When_Given_Capacity_And_LoadFactor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		slotCapacity uint64
		loadFactor   float64
		want         uint64
	}{
		{slotCapacity: 100, loadFactor: 0.75, want: 256},   // ceil(100/0.75)=134, nextPow2=256
		{slotCapacity: 1000, loadFactor: 0.75, want: 2048}, // ceil(1000/0.75)=1334, nextPow2=2048
		{slotCapacity: 1, loadFactor: 0.75, want: 2},       // minimum bucket count
		{slotCapacity: 0, loadFactor: 0.75, want: 2},       // edge case
	}

	for _, tt := range tests {
		got := computeBucketCount(tt.slotCapacity, tt.loadFactor)
		if got != tt.want {
			t.Errorf("computeBucketCount(%d, %f) = %d, want %d", tt.slotCapacity, tt.loadFactor, got, tt.want)
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

func Test_HasReservedBytesSet_Returns_True_When_Reserved_Bytes_Are_Nonzero(t *testing.T) {
	t.Parallel()

	// Clean header
	h := newHeader(16, 0, 100, 1, 0.75)

	buf := encodeHeader(&h)
	if hasReservedBytesSet(buf) {
		t.Error("hasReservedBytesSet returned true for clean header")
	}

	// Set a reserved byte
	buf[offReservedStart] = 0xFF
	if !hasReservedBytesSet(buf) {
		t.Error("hasReservedBytesSet returned false when reserved byte is set")
	}
}
