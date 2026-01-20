// Header state tests: unit tests for invalidation, user header, generation, and reserved tail.
//
// Oracle: expected error types and state transitions per spec.
// Technique: table-driven unit tests + state machine tests.
//
// These tests verify:
// - Invalidation behavior (terminal state, cross-handle visibility, idempotence)
// - User header read/write semantics (persistence, dirty tracking, CRC protection)
// - Generation counter behavior (initial value, monotonic increase)
// - Reserved tail enforcement (0x0C0..0x0FF must be zero)
//
// Failures here mean: "invalidation/user header/generation does not match spec"

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

// Header offsets used by tests (from format.go).
// Note: slcHeaderSize, offGeneration, offHeaderCRC32C are defined in seqlock_concurrency_test.go.
const (
	hdrStateOff             = 0x074 // uint32 (slotcache-owned state)
	hdrUserFlagsOff         = 0x078 // uint64 (caller-owned)
	hdrUserDataOff          = 0x080 // [64]byte (caller-owned)
	hdrReservedTailStartOff = 0x0C0 // reserved bytes through 0x0FF (64 bytes)
)

// =============================================================================
// Invalidation tests
// =============================================================================

func Test_Invalidate_Returns_ErrInvalidated_When_Handle_Is_Used_After_Invalidation(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalidate_unusable.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open failed: %v", openErr)
	}
	defer c.Close()

	// Invalidate the cache.
	invErr := c.Invalidate()
	if invErr != nil {
		t.Fatalf("Invalidate failed: %v", invErr)
	}

	// Verify all read operations return ErrInvalidated.
	_, lenErr := c.Len()
	if !errors.Is(lenErr, slotcache.ErrInvalidated) {
		t.Errorf("Len() after invalidate: got %v, want ErrInvalidated", lenErr)
	}

	_, _, getErr := c.Get(make([]byte, 8))
	if !errors.Is(getErr, slotcache.ErrInvalidated) {
		t.Errorf("Get() after invalidate: got %v, want ErrInvalidated", getErr)
	}

	_, scanErr := c.Scan(slotcache.ScanOptions{})
	if !errors.Is(scanErr, slotcache.ErrInvalidated) {
		t.Errorf("Scan() after invalidate: got %v, want ErrInvalidated", scanErr)
	}

	_, beginErr := c.BeginWrite()
	if !errors.Is(beginErr, slotcache.ErrInvalidated) {
		t.Errorf("BeginWrite() after invalidate: got %v, want ErrInvalidated", beginErr)
	}

	_, uhErr := c.UserHeader()
	if !errors.Is(uhErr, slotcache.ErrInvalidated) {
		t.Errorf("UserHeader() after invalidate: got %v, want ErrInvalidated", uhErr)
	}

	_, genErr := c.Generation()
	if !errors.Is(genErr, slotcache.ErrInvalidated) {
		t.Errorf("Generation() after invalidate: got %v, want ErrInvalidated", genErr)
	}
}

func Test_Invalidate_Returns_ErrInvalidated_When_Read_Via_Another_Handle(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalidate_cross_handle.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Open first handle.
	c1, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(c1) failed: %v", err)
	}
	defer c1.Close()

	// Open second handle to same file.
	c2, openErr := slotcache.Open(opts)
	if openErr != nil {
		t.Fatalf("Open(c2) failed: %v", openErr)
	}
	defer c2.Close()

	// Invalidate via first handle.
	invErr := c1.Invalidate()
	if invErr != nil {
		t.Fatalf("Invalidate(c1) failed: %v", invErr)
	}

	// Second handle should now see ErrInvalidated.
	_, lenErr := c2.Len()
	if !errors.Is(lenErr, slotcache.ErrInvalidated) {
		t.Errorf("c2.Len() after c1.Invalidate(): got %v, want ErrInvalidated", lenErr)
	}

	_, beginErr := c2.BeginWrite()
	if !errors.Is(beginErr, slotcache.ErrInvalidated) {
		t.Errorf("c2.BeginWrite() after c1.Invalidate(): got %v, want ErrInvalidated", beginErr)
	}
}

func Test_Open_Returns_ErrInvalidated_When_File_Was_Previously_Invalidated(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "open_invalidated.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create and invalidate.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	invErr := c.Invalidate()
	if invErr != nil {
		t.Fatalf("Invalidate failed: %v", invErr)
	}

	closeErr := c.Close()
	if closeErr != nil {
		t.Fatalf("Close failed: %v", closeErr)
	}

	// Reopen should fail with ErrInvalidated.
	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrInvalidated) {
		t.Fatalf("Open(invalidated file) got %v, want ErrInvalidated", reopenErr)
	}
}

func Test_Invalidate_Returns_Nil_When_Called_Multiple_Times(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalidate_idempotent.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// First invalidation.
	err1 := c.Invalidate()
	if err1 != nil {
		t.Fatalf("First Invalidate failed: %v", err1)
	}

	// Second invalidation should be a no-op and return nil.
	err2 := c.Invalidate()
	if err2 != nil {
		t.Fatalf("Second Invalidate failed: %v, want nil", err2)
	}

	// Third invalidation for good measure.
	err3 := c.Invalidate()
	if err3 != nil {
		t.Fatalf("Third Invalidate failed: %v, want nil", err3)
	}
}

func Test_Invalidate_Returns_ErrBusy_When_Writer_Is_Active(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "invalidate_writer_busy.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Acquire writer but don't commit.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}
	defer w.Close()

	// Invalidate should fail with ErrBusy.
	invErr := c.Invalidate()
	if !errors.Is(invErr, slotcache.ErrBusy) {
		t.Fatalf("Invalidate with active writer: got %v, want ErrBusy", invErr)
	}
}

// =============================================================================
// User header tests
// =============================================================================

func Test_UserHeader_Returns_Zero_Values_When_Cache_Is_Newly_Created(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_default.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	uh, uhErr := c.UserHeader()
	if uhErr != nil {
		t.Fatalf("UserHeader failed: %v", uhErr)
	}

	if uh.Flags != 0 {
		t.Errorf("UserHeader.Flags = %d, want 0", uh.Flags)
	}

	var zeroData [slotcache.UserDataSize]byte
	if uh.Data != zeroData {
		t.Errorf("UserHeader.Data = %x, want all zeros", uh.Data)
	}
}

func Test_UserHeader_Returns_Committed_Values_When_Cache_Is_Reopened(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_persist.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create and set user header.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	const testFlags uint64 = 0xDEADBEEF12345678

	var testData [slotcache.UserDataSize]byte
	for i := range testData {
		testData[i] = byte(i * 3)
	}

	flagsErr := w.SetUserHeaderFlags(testFlags)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	dataErr := w.SetUserHeaderData(testData)
	if dataErr != nil {
		t.Fatalf("SetUserHeaderData failed: %v", dataErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	w.Close()
	c.Close()

	// Reopen and verify.
	c2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Open(reopen) failed: %v", reopenErr)
	}
	defer c2.Close()

	uh, uhErr := c2.UserHeader()
	if uhErr != nil {
		t.Fatalf("UserHeader failed: %v", uhErr)
	}

	if uh.Flags != testFlags {
		t.Errorf("UserHeader.Flags = 0x%x, want 0x%x", uh.Flags, testFlags)
	}

	if uh.Data != testData {
		t.Errorf("UserHeader.Data = %x, want %x", uh.Data, testData)
	}
}

func Test_UserHeader_Returns_Original_Values_When_Writer_Closed_Without_Commit(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_discard.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Get initial user header (should be zero).
	uhBefore, beforeErr := c.UserHeader()
	if beforeErr != nil {
		t.Fatalf("UserHeader(before) failed: %v", beforeErr)
	}

	// Stage header changes but close without commit.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	flagsErr := w.SetUserHeaderFlags(0xCAFEBABE)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	var testData [slotcache.UserDataSize]byte

	testData[0] = 0xFF

	dataErr := w.SetUserHeaderData(testData)
	if dataErr != nil {
		t.Fatalf("SetUserHeaderData failed: %v", dataErr)
	}

	// Close without Commit.
	w.Close()

	// User header should be unchanged.
	uhAfter, afterErr := c.UserHeader()
	if afterErr != nil {
		t.Fatalf("UserHeader(after) failed: %v", afterErr)
	}

	if uhAfter.Flags != uhBefore.Flags {
		t.Errorf("UserHeader.Flags changed after close without commit: got 0x%x, want 0x%x",
			uhAfter.Flags, uhBefore.Flags)
	}

	if uhAfter.Data != uhBefore.Data {
		t.Error("UserHeader.Data changed after close without commit")
	}
}

func Test_UserHeader_Returns_Original_Values_When_Commit_Fails_With_ErrFull(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_preflight_full.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 2, // Very small capacity
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Fill the cache.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite(fill) failed: %v", beginErr)
	}

	for i := range 2 {
		key := make([]byte, 8)
		key[0] = byte(i)

		putErr := w.Put(key, int64(i), make([]byte, 4))
		if putErr != nil {
			t.Fatalf("Put(%d) failed: %v", i, putErr)
		}
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit(fill) failed: %v", commitErr)
	}

	w.Close()

	// Get user header before preflight failure.
	uhBefore, beforeErr := c.UserHeader()
	if beforeErr != nil {
		t.Fatalf("UserHeader(before) failed: %v", beforeErr)
	}

	// Try to insert more entries (will fail with ErrFull) while also staging header changes.
	w2, beginErr2 := c.BeginWrite()
	if beginErr2 != nil {
		t.Fatalf("BeginWrite(overflow) failed: %v", beginErr2)
	}

	// Stage header changes.
	flagsErr := w2.SetUserHeaderFlags(0xDEADDEAD)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	// Try to insert (will cause ErrFull on commit).
	key := make([]byte, 8)
	key[0] = 0xFF

	putErr := w2.Put(key, 999, make([]byte, 4))
	if putErr != nil {
		t.Fatalf("Put(overflow) failed: %v", putErr)
	}

	// Commit should fail with ErrFull.
	overflowErr := w2.Commit()
	if !errors.Is(overflowErr, slotcache.ErrFull) {
		t.Fatalf("Commit(overflow) got %v, want ErrFull", overflowErr)
	}

	w2.Close()

	// User header should be unchanged (preflight failure).
	uhAfter, afterErr := c.UserHeader()
	if afterErr != nil {
		t.Fatalf("UserHeader(after) failed: %v", afterErr)
	}

	if uhAfter.Flags != uhBefore.Flags {
		t.Errorf("UserHeader.Flags changed after preflight failure: got 0x%x, want 0x%x",
			uhAfter.Flags, uhBefore.Flags)
	}
}

func Test_UserHeader_Returns_Original_Values_When_Commit_Fails_With_ErrOutOfOrderInsert(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_preflight_order.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
		OrderedKeys:  true,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Insert a key with high value.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite(first) failed: %v", beginErr)
	}

	highKey := make([]byte, 8)
	highKey[0] = 0xFF

	putErr := w.Put(highKey, 1, make([]byte, 4))
	if putErr != nil {
		t.Fatalf("Put(high) failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit(first) failed: %v", commitErr)
	}

	w.Close()

	// Get user header before preflight failure.
	uhBefore, beforeErr := c.UserHeader()
	if beforeErr != nil {
		t.Fatalf("UserHeader(before) failed: %v", beforeErr)
	}

	// Try to insert lower key (will fail with ErrOutOfOrderInsert) while staging header changes.
	w2, beginErr2 := c.BeginWrite()
	if beginErr2 != nil {
		t.Fatalf("BeginWrite(second) failed: %v", beginErr2)
	}

	// Stage header changes.
	flagsErr := w2.SetUserHeaderFlags(0xBADBAD)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	// Insert lower key.
	lowKey := make([]byte, 8)
	lowKey[0] = 0x01

	putErr2 := w2.Put(lowKey, 2, make([]byte, 4))
	if putErr2 != nil {
		t.Fatalf("Put(low) failed: %v", putErr2)
	}

	// Commit should fail with ErrOutOfOrderInsert.
	orderErr := w2.Commit()
	if !errors.Is(orderErr, slotcache.ErrOutOfOrderInsert) {
		t.Fatalf("Commit(out-of-order) got %v, want ErrOutOfOrderInsert", orderErr)
	}

	w2.Close()

	// User header should be unchanged.
	uhAfter, afterErr := c.UserHeader()
	if afterErr != nil {
		t.Fatalf("UserHeader(after) failed: %v", afterErr)
	}

	if uhAfter.Flags != uhBefore.Flags {
		t.Errorf("UserHeader.Flags changed after preflight failure: got 0x%x, want 0x%x",
			uhAfter.Flags, uhBefore.Flags)
	}
}

func Test_SetUserHeaderFlags_Preserves_Data_When_Only_Flags_Are_Set(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_flags_only.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// First set some data.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite(first) failed: %v", beginErr)
	}

	var testData [slotcache.UserDataSize]byte
	for i := range testData {
		testData[i] = byte(i + 1)
	}

	dataErr := w.SetUserHeaderData(testData)
	if dataErr != nil {
		t.Fatalf("SetUserHeaderData failed: %v", dataErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit(first) failed: %v", commitErr)
	}

	w.Close()

	// Now set only flags.
	w2, beginErr2 := c.BeginWrite()
	if beginErr2 != nil {
		t.Fatalf("BeginWrite(second) failed: %v", beginErr2)
	}

	const newFlags uint64 = 0x123456789ABCDEF0

	flagsErr := w2.SetUserHeaderFlags(newFlags)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	commitErr2 := w2.Commit()
	if commitErr2 != nil {
		t.Fatalf("Commit(second) failed: %v", commitErr2)
	}

	w2.Close()

	// Verify flags changed but data preserved.
	uh, uhErr := c.UserHeader()
	if uhErr != nil {
		t.Fatalf("UserHeader failed: %v", uhErr)
	}

	if uh.Flags != newFlags {
		t.Errorf("UserHeader.Flags = 0x%x, want 0x%x", uh.Flags, newFlags)
	}

	if uh.Data != testData {
		t.Errorf("UserHeader.Data changed when only flags were set: got %x, want %x", uh.Data, testData)
	}
}

func Test_SetUserHeaderData_Preserves_Flags_When_Only_Data_Is_Set(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_data_only.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// First set some flags.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite(first) failed: %v", beginErr)
	}

	const testFlags uint64 = 0xFEDCBA9876543210

	flagsErr := w.SetUserHeaderFlags(testFlags)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit(first) failed: %v", commitErr)
	}

	w.Close()

	// Now set only data.
	w2, beginErr2 := c.BeginWrite()
	if beginErr2 != nil {
		t.Fatalf("BeginWrite(second) failed: %v", beginErr2)
	}

	var newData [slotcache.UserDataSize]byte
	for i := range newData {
		newData[i] = byte(255 - i)
	}

	dataErr := w2.SetUserHeaderData(newData)
	if dataErr != nil {
		t.Fatalf("SetUserHeaderData failed: %v", dataErr)
	}

	commitErr2 := w2.Commit()
	if commitErr2 != nil {
		t.Fatalf("Commit(second) failed: %v", commitErr2)
	}

	w2.Close()

	// Verify data changed but flags preserved.
	uh, uhErr := c.UserHeader()
	if uhErr != nil {
		t.Fatalf("UserHeader failed: %v", uhErr)
	}

	if uh.Flags != testFlags {
		t.Errorf("UserHeader.Flags changed when only data was set: got 0x%x, want 0x%x", uh.Flags, testFlags)
	}

	if uh.Data != newData {
		t.Errorf("UserHeader.Data = %x, want %x", uh.Data, newData)
	}
}

func Test_Open_Returns_ErrCorrupt_When_User_Header_Bytes_Are_Corrupted(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "userheader_crc.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create cache with user header values.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	flagsErr := w.SetUserHeaderFlags(0x1234567890ABCDEF)
	if flagsErr != nil {
		t.Fatalf("SetUserHeaderFlags failed: %v", flagsErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	w.Close()
	c.Close()

	// Corrupt a user header byte without fixing CRC.
	f, openErr := os.OpenFile(path, os.O_RDWR, 0)
	if openErr != nil {
		t.Fatalf("OpenFile failed: %v", openErr)
	}

	// Flip a bit in user data region.
	corruptOffset := hdrUserDataOff + 10

	var buf [1]byte

	_, readErr := f.ReadAt(buf[:], int64(corruptOffset))
	if readErr != nil {
		f.Close()
		t.Fatalf("ReadAt failed: %v", readErr)
	}

	buf[0] ^= 0x01 // Flip one bit

	_, writeErr := f.WriteAt(buf[:], int64(corruptOffset))
	if writeErr != nil {
		f.Close()
		t.Fatalf("WriteAt failed: %v", writeErr)
	}

	syncErr := f.Sync()
	if syncErr != nil {
		f.Close()
		t.Fatalf("Sync failed: %v", syncErr)
	}

	f.Close()

	// Reopen should fail with ErrCorrupt (CRC mismatch).
	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrCorrupt) {
		t.Fatalf("Open(corrupted user header) got %v, want ErrCorrupt", reopenErr)
	}
}

// =============================================================================
// Generation tests
// =============================================================================

func Test_Generation_Returns_Zero_When_Cache_Is_Newly_Created(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "generation_new.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	gen, genErr := c.Generation()
	if genErr != nil {
		t.Fatalf("Generation failed: %v", genErr)
	}

	if gen != 0 {
		t.Errorf("Generation() = %d, want 0 for new file", gen)
	}
}

func Test_Generation_Returns_Increased_Value_When_Commit_Succeeds(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "generation_increase.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	genBefore, beforeErr := c.Generation()
	if beforeErr != nil {
		t.Fatalf("Generation(before) failed: %v", beforeErr)
	}

	// Perform a commit.
	w, beginErr := c.BeginWrite()
	if beginErr != nil {
		t.Fatalf("BeginWrite failed: %v", beginErr)
	}

	key := make([]byte, 8)
	key[0] = 1

	putErr := w.Put(key, 1, make([]byte, 4))
	if putErr != nil {
		t.Fatalf("Put failed: %v", putErr)
	}

	commitErr := w.Commit()
	if commitErr != nil {
		t.Fatalf("Commit failed: %v", commitErr)
	}

	w.Close()

	genAfter, afterErr := c.Generation()
	if afterErr != nil {
		t.Fatalf("Generation(after) failed: %v", afterErr)
	}

	if genAfter <= genBefore {
		t.Errorf("Generation did not increase: before=%d, after=%d", genBefore, genAfter)
	}

	// Generation increases by 2 per commit (odd during publish, even after).
	expectedAfter := genBefore + 2
	if genAfter != expectedAfter {
		t.Errorf("Generation() = %d, want %d (before + 2)", genAfter, expectedAfter)
	}
}

func Test_Generation_Returns_Monotonically_Increasing_Values_When_Multiple_Commits_Occur(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "generation_monotonic.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	var prevGen uint64

	for i := range 5 {
		gen, genErr := c.Generation()
		if genErr != nil {
			t.Fatalf("Generation(%d) failed: %v", i, genErr)
		}

		if i > 0 && gen < prevGen {
			t.Errorf("Generation decreased: i=%d, prev=%d, cur=%d", i, prevGen, gen)
		}

		prevGen = gen

		// Commit.
		w, beginErr := c.BeginWrite()
		if beginErr != nil {
			t.Fatalf("BeginWrite(%d) failed: %v", i, beginErr)
		}

		key := make([]byte, 8)
		key[0] = byte(i)

		putErr := w.Put(key, int64(i), make([]byte, 4))
		if putErr != nil {
			t.Fatalf("Put(%d) failed: %v", i, putErr)
		}

		commitErr := w.Commit()
		if commitErr != nil {
			t.Fatalf("Commit(%d) failed: %v", i, commitErr)
		}

		w.Close()
	}
}

func Test_Generation_Returns_ErrInvalidated_When_Cache_Is_Invalidated(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "generation_invalidated.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer c.Close()

	// Should work before invalidation.
	_, beforeErr := c.Generation()
	if beforeErr != nil {
		t.Fatalf("Generation(before invalidation) failed: %v", beforeErr)
	}

	// Invalidate.
	invErr := c.Invalidate()
	if invErr != nil {
		t.Fatalf("Invalidate failed: %v", invErr)
	}

	// Should fail after invalidation.
	_, afterErr := c.Generation()
	if !errors.Is(afterErr, slotcache.ErrInvalidated) {
		t.Errorf("Generation(after invalidation) got %v, want ErrInvalidated", afterErr)
	}
}

// =============================================================================
// Reserved tail tests
// =============================================================================

func Test_Open_Returns_ErrIncompatible_When_Reserved_Tail_Contains_NonZero_Bytes(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "reserved_tail_nonzero.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create a valid cache file.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	c.Close()

	// Corrupt a byte in the reserved tail region (0x0C0..0x0FF) and fix CRC.
	mutateHeaderAndFixCRCLocal(t, path, func(hdr []byte) {
		// Set a non-zero byte in reserved tail.
		hdr[hdrReservedTailStartOff+10] = 0x42
	})

	// Reopen should fail with ErrIncompatible.
	_, reopenErr := slotcache.Open(opts)
	if !errors.Is(reopenErr, slotcache.ErrIncompatible) {
		t.Fatalf("Open(reserved tail nonzero) got %v, want ErrIncompatible", reopenErr)
	}
}

func Test_Open_Returns_Success_When_User_Header_Region_Contains_NonZero_Bytes(t *testing.T) {
	t.Parallel()

	// This test verifies that user header region (0x078..0x0BF) is NOT
	// required to be zero, unlike the reserved tail (0x0C0..0x0FF).

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "user_header_nonzero.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create a valid cache file.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	c.Close()

	// Set non-zero bytes in user header region (0x078..0x0BF) and fix CRC.
	mutateHeaderAndFixCRCLocal(t, path, func(hdr []byte) {
		// Set user flags.
		binary.LittleEndian.PutUint64(hdr[hdrUserFlagsOff:], 0xDEADBEEF)
		// Set user data byte.
		hdr[hdrUserDataOff+5] = 0xAB
	})

	// Reopen should succeed (user header region is caller-owned).
	c2, reopenErr := slotcache.Open(opts)
	if reopenErr != nil {
		t.Fatalf("Open(user header nonzero) failed: %v", reopenErr)
	}
	defer c2.Close()

	// Verify the user header values we set.
	uh, uhErr := c2.UserHeader()
	if uhErr != nil {
		t.Fatalf("UserHeader failed: %v", uhErr)
	}

	if uh.Flags != 0xDEADBEEF {
		t.Errorf("UserHeader.Flags = 0x%x, want 0xDEADBEEF", uh.Flags)
	}

	if uh.Data[5] != 0xAB {
		t.Errorf("UserHeader.Data[5] = 0x%x, want 0xAB", uh.Data[5])
	}
}

// =============================================================================
// Helper functions
// =============================================================================

// mutateHeaderAndFixCRCLocal is a local version to avoid redeclaration with other test files.
func mutateHeaderAndFixCRCLocal(tb testing.TB, path string, mutate func([]byte)) {
	tb.Helper()

	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		tb.Fatalf("open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	hdr := make([]byte, slcHeaderSize)

	n, readErr := f.ReadAt(hdr, 0)
	if readErr != nil {
		tb.Fatalf("read header: %v", readErr)
	}

	if n != slcHeaderSize {
		tb.Fatalf("read header size mismatch: got=%d want=%d", n, slcHeaderSize)
	}

	mutate(hdr)

	// Recompute header CRC32-C with generation and crc fields zeroed.
	tmp := make([]byte, slcHeaderSize)
	copy(tmp, hdr)

	// Zero generation field (offset 0x040, 8 bytes).
	for i := offGeneration; i < offGeneration+8; i++ {
		tmp[i] = 0
	}

	// Zero CRC field (offset 0x070, 4 bytes).
	for i := offHeaderCRC32C; i < offHeaderCRC32C+4; i++ {
		tmp[i] = 0
	}

	crc := crc32.Checksum(tmp, crc32.MakeTable(crc32.Castagnoli))
	binary.LittleEndian.PutUint32(hdr[offHeaderCRC32C:offHeaderCRC32C+4], crc)

	_, writeErr := f.WriteAt(hdr, 0)
	if writeErr != nil {
		tb.Fatalf("write header: %v", writeErr)
	}

	_ = f.Sync()
}
