package slotcache_test

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

func Test_WriterCommit_Returns_ErrCorrupt_When_No_Empty_Buckets_Are_Available(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "writer_no_empty_buckets.slc")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 64,
	}

	// Create a valid file with one live slot so we can point buckets at slot 0.
	c, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w, err := c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	putErr := w.Put([]byte("key00000"), 1, nil)
	if putErr != nil {
		_ = w.Close()
		_ = c.Close()

		t.Fatalf("Put failed: %v", putErr)
	}

	createCommitErr := w.Commit()
	if createCommitErr != nil {
		_ = w.Close()
		_ = c.Close()

		t.Fatalf("Commit(create) failed: %v", createCommitErr)
	}

	_ = w.Close()
	_ = c.Close()

	// Corrupt the bucket table by making every bucket FULL and pointing to slot 0.
	// Keep the header counters unchanged so Open() succeeds (lightweight validation).
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}

	bucketCount := binary.LittleEndian.Uint64(data[offBucketCount : offBucketCount+8])
	bucketsOffset := binary.LittleEndian.Uint64(data[offBucketsOffset : offBucketsOffset+8])

	// Sanity: the file should have at least one allocated slot.
	highwater := binary.LittleEndian.Uint64(data[offSlotHighwater : offSlotHighwater+8])
	if highwater != 1 {
		t.Fatalf("expected slot_highwater=1 after initial commit, got %d", highwater)
	}

	for i := range bucketCount {
		bucketOff := bucketsOffset + i*16
		off := int(bucketOff)

		binary.LittleEndian.PutUint64(data[off:off+8], 0)    // hash (ignored here)
		binary.LittleEndian.PutUint64(data[off+8:off+16], 1) // slot_plus1 -> slot 0
	}

	writeErr := os.WriteFile(path, data, 0o600)
	if writeErr != nil {
		t.Fatalf("write file: %v", writeErr)
	}

	// Reopen (should succeed) and attempt a new insert.
	c, err = slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(corrupt buckets) failed: %v", err)
	}

	defer func() { _ = c.Close() }()

	before := mustReadFile(t, path)
	beforeHighwater := binary.LittleEndian.Uint64(before[offSlotHighwater : offSlotHighwater+8])
	beforeLive := binary.LittleEndian.Uint64(before[offLiveCount : offLiveCount+8])

	w, err = c.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite failed: %v", err)
	}

	_ = w.Put([]byte("key00001"), 2, nil)

	commitErr := w.Commit()
	_ = w.Close()

	if !errors.Is(commitErr, slotcache.ErrCorrupt) {
		t.Fatalf("Commit() error mismatch: got=%v want=%v", commitErr, slotcache.ErrCorrupt)
	}

	// Even though the file is corrupt, the writer must not have partially
	// published header counters (slot_highwater/live_count).
	after := mustReadFile(t, path)
	afterHighwater := binary.LittleEndian.Uint64(after[offSlotHighwater : offSlotHighwater+8])
	afterLive := binary.LittleEndian.Uint64(after[offLiveCount : offLiveCount+8])

	if afterHighwater != beforeHighwater {
		t.Fatalf("slot_highwater changed on failed commit: before=%d after=%d", beforeHighwater, afterHighwater)
	}

	if afterLive != beforeLive {
		t.Fatalf("live_count changed on failed commit: before=%d after=%d", beforeLive, afterLive)
	}
}

func mustReadFile(tb testing.TB, path string) []byte {
	tb.Helper()

	b, err := os.ReadFile(path)
	if err != nil {
		tb.Fatalf("read file: %v", err)
	}

	if len(b) < slcHeaderSize {
		tb.Fatalf("file too small: got %d bytes", len(b))
	}

	return b
}
