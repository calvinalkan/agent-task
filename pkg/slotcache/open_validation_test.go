// Open validation: unit tests for Open() error handling
//
// Oracle: expected error types (ErrIncompatible, ErrCorrupt)
// Technique: table-driven unit tests
//
// These tests verify that Open() correctly rejects files when reopened
// with incompatible options (different KeySize, IndexSize, UserVersion, etc.)
// and returns appropriate error types.
//
// Failures here mean: "Open accepted incompatible options or returned wrong error"

package slotcache_test

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

func Test_Open_Returns_ErrIncompatible_When_Reopening_With_Mismatched_Options(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		mutate    func(slotcache.Options) slotcache.Options
		wantError error
	}{
		{
			name: "UserVersion",
			mutate: func(opts slotcache.Options) slotcache.Options {
				opts.UserVersion++

				return opts
			},
			wantError: slotcache.ErrIncompatible,
		},
		{
			name: "KeySize",
			mutate: func(opts slotcache.Options) slotcache.Options {
				opts.KeySize++

				return opts
			},
			wantError: slotcache.ErrIncompatible,
		},
		{
			name: "IndexSize",
			mutate: func(opts slotcache.Options) slotcache.Options {
				opts.IndexSize++

				return opts
			},
			wantError: slotcache.ErrIncompatible,
		},
		{
			name: "SlotCapacity",
			mutate: func(opts slotcache.Options) slotcache.Options {
				opts.SlotCapacity++

				return opts
			},
			wantError: slotcache.ErrIncompatible,
		},
		{
			name: "OrderedKeysFalseToTrue",
			mutate: func(opts slotcache.Options) slotcache.Options {
				opts.OrderedKeys = true

				return opts
			},
			wantError: slotcache.ErrIncompatible,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tmpDir := t.TempDir()
			path := filepath.Join(tmpDir, "open_compat.slc")

			base := slotcache.Options{
				Path:         path,
				KeySize:      8,
				IndexSize:    4,
				UserVersion:  1,
				SlotCapacity: 64,
				OrderedKeys:  false,
			}

			// Create file with base options.
			c, err := slotcache.Open(base)
			if err != nil {
				t.Fatalf("Open(base) failed: %v", err)
			}

			closeErr := c.Close()
			if closeErr != nil {
				t.Fatalf("Close(base) failed: %v", closeErr)
			}

			// Reopen with mutated options.
			mutated := tc.mutate(base)
			_, err = slotcache.Open(mutated)

			if tc.wantError == nil {
				if err != nil {
					t.Fatalf("Open(mutated) unexpected error: %v", err)
				}

				return
			}

			if !errors.Is(err, tc.wantError) {
				t.Fatalf("Open(mutated) error mismatch: got=%v want=%v", err, tc.wantError)
			}
		})
	}
}

func Test_Open_Returns_ErrIncompatible_When_OrderedKeys_Changes_TrueToFalse(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_compat_ordered.slc")

	base := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(base)
	if err != nil {
		t.Fatalf("Open(base) failed: %v", err)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close(base) failed: %v", closeErr)
	}

	mutated := base
	mutated.OrderedKeys = false

	_, err = slotcache.Open(mutated)
	if !errors.Is(err, slotcache.ErrIncompatible) {
		t.Fatalf("Open(mutated) error mismatch: got=%v want=%v", err, slotcache.ErrIncompatible)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_SlotCapacity_Exceeds_BucketSizingLimit(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_overflow.slc")

	// slot_capacity * 2 would overflow uint64.
	// maxUint64 / 2 + 1 = 0x8000000000000000
	const overflowCapacity = (^uint64(0) >> 1) + 1

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: overflowCapacity,
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(overflow capacity) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_KeySize_Exceeds_Uint32Max(t *testing.T) {
	t.Parallel()

	// This test only makes sense on 64-bit systems where int can exceed uint32.
	// On 32-bit systems, int max = uint32 max, so this condition can never occur.
	const maxUint32 = int(^uint32(0))
	if maxUint32 == int(^uint(0)>>1) {
		t.Skip("skipping on 32-bit systems where int cannot exceed uint32")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_keysize_overflow.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      maxUint32 + 1, // exceeds uint32
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(keysize overflow) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_IndexSize_Exceeds_Uint32Max(t *testing.T) {
	t.Parallel()

	// This test only makes sense on 64-bit systems where int can exceed uint32.
	const maxUint32 = int(^uint32(0))
	if maxUint32 == int(^uint(0)>>1) {
		t.Skip("skipping on 32-bit systems where int cannot exceed uint32")
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_indexsize_overflow.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    maxUint32 + 1, // exceeds uint32
		UserVersion:  1,
		SlotCapacity: 64,
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(indexsize overflow) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_Derived_SlotSize_Overflows_Uint32(t *testing.T) {
	t.Parallel()

	// This test only makes sense on 64-bit systems where int can represent uint32 max.
	//
	// Note: this is different from the existing "KeySize exceeds uint32" tests.
	// Here KeySize/IndexSize are *within* uint32, but the derived slot_size implied by
	// the SLC1 formula cannot fit in a u32 and must be rejected.
	const maxUint32 = int(^uint32(0))
	if maxUint32 == int(^uint(0)>>1) {
		t.Skip("skipping on 32-bit systems where int cannot represent uint32 max")
	}

	tmpDir := t.TempDir()

	cases := []struct {
		name      string
		keySize   int
		indexSize int
	}{
		{
			name:      "KeySizeMaxUint32",
			keySize:   maxUint32,
			indexSize: 0,
		},
		{
			name:      "IndexSizeMaxUint32",
			keySize:   1,
			indexSize: maxUint32,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(tmpDir, "open_slotsize_overflow_"+tc.name+".slc")

			opts := slotcache.Options{
				Path:         path,
				KeySize:      tc.keySize,
				IndexSize:    tc.indexSize,
				UserVersion:  1,
				SlotCapacity: 1,
			}

			c, err := slotcache.Open(opts)
			if err == nil {
				_ = c.Close()
			}

			if !errors.Is(err, slotcache.ErrInvalidInput) {
				t.Fatalf("Open(slot_size overflow) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
			}
		})
	}
}

func Test_Open_Returns_ErrInvalidInput_When_FileLayout_Exceeds_Int64Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_filesize_overflow.slc")

	// Create a configuration where slot_capacity * slot_size would overflow int64.
	// slot_size for key_size=8, index_size=8 is: align8(8 + 8 + 0 + 8 + 8) = 32 bytes
	// To overflow int64 (max ~9.2e18), we need capacity > maxInt64 / slot_size
	// But we also have the bucket sizing limit (slot_capacity <= maxUint64/2).
	// So we need to find values where the file layout overflows int64.
	//
	// With slot_capacity = maxUint64/2, slot_size = 32:
	// slots_section = (maxUint64/2) * 32 = 16 * maxUint64 which overflows.
	//
	// Let's use a smaller but still overflowing configuration:
	// maxInt64 / 32 ≈ 2.88e17, so capacity > 2.88e17 with slot_size=32 would overflow.
	// bucket_sizing_limit = maxUint64/2 ≈ 9.2e18, so we can use capacity = 3e17.

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    8,
		UserVersion:  1,
		SlotCapacity: 300_000_000_000_000_000, // 3e17 - with slot_size 32, exceeds int64
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(file layout overflow) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_KeySize_Exceeds_MaxKeySize(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_keysize_cap.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      513, // max is 512
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 1,
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(keysize cap) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_IndexSize_Exceeds_MaxIndexSize(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_indexsize_cap.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    (1 << 20) + 1, // max is 1 MiB
		UserVersion:  1,
		SlotCapacity: 1,
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(indexsize cap) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_SlotCapacity_Exceeds_MaxSlotCapacity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_slotcap_cap.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 100_000_001, // max is 100,000,000
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(slotcapacity cap) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_WritebackMode_Is_Unknown(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_writeback_unknown.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 1,
		Writeback:    slotcache.WritebackMode(123),
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(unknown writeback) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

func Test_Open_Returns_ErrInvalidInput_When_FileSize_Exceeds_MaxCacheFileSize(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_filesize_cap.slc")

	// A configuration that would require a cache file > 1 TiB:
	// IndexSize=1 MiB, KeySize=512 -> slot_size is a bit over 1 MiB.
	// With SlotCapacity=2,000,000 the slots section alone exceeds 1 TiB.
	opts := slotcache.Options{
		Path:         path,
		KeySize:      512,
		IndexSize:    1 << 20, // 1 MiB
		UserVersion:  1,
		SlotCapacity: 2_000_000,
	}

	_, err := slotcache.Open(opts)
	if !errors.Is(err, slotcache.ErrInvalidInput) {
		t.Fatalf("Open(file size cap) error mismatch: got=%v want=%v", err, slotcache.ErrInvalidInput)
	}
}

const (
	// Additional header offsets used by tests.
	offBucketCount = 0x048
)

func mutateHeaderAndFixCRC(tb testing.TB, path string, mutate func([]byte)) {
	tb.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		tb.Fatalf("open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	hdr := make([]byte, slcHeaderSize)

	n, err := f.ReadAt(hdr, 0)
	if err != nil {
		tb.Fatalf("read header: %v", err)
	}

	if n != slcHeaderSize {
		tb.Fatalf("read header size mismatch: got=%d want=%d", n, slcHeaderSize)
	}

	mutate(hdr)

	// Recompute header CRC32-C with generation and crc fields zeroed.
	tmp := make([]byte, slcHeaderSize)
	copy(tmp, hdr)

	for i := offGeneration; i < offGeneration+8; i++ {
		tmp[i] = 0
	}

	for i := offHeaderCRC32C; i < offHeaderCRC32C+4; i++ {
		tmp[i] = 0
	}

	crc := crc32.Checksum(tmp, crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(hdr[offHeaderCRC32C:offHeaderCRC32C+4], crc)

	_, err = f.WriteAt(hdr, 0)
	if err != nil {
		tb.Fatalf("write header: %v", err)
	}

	_ = f.Sync()
}

func Test_Open_Returns_ErrIncompatible_When_BucketCount_Is_Not_Greater_Than_SlotCapacity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_bucketcount_too_small.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close(create) failed: %v", closeErr)
	}

	// Make the file look like a foreign-but-spec-valid SLC1 file by setting
	// bucket_count <= slot_capacity (still a power of two). This is not safe for
	// this implementation, so Open() must reject it as incompatible.
	mutateHeaderAndFixCRC(t, path, func(hdr []byte) {
		binary.LittleEndian.PutUint64(hdr[offBucketCount:offBucketCount+8], opts.SlotCapacity)
	})

	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrIncompatible) {
		t.Fatalf("Open(patched) error mismatch: got=%v want=%v", reopenErr, slotcache.ErrIncompatible)
	}
}
