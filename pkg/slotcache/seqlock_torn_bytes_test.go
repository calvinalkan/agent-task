package slotcache_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// Seqlock torn-bytes regression test.
//
// # Overview
//
// slotcache relies on an on-disk seqlock (the header `generation` counter) to
// provide correct-or-retry reads while a writer commits.
//
// This test is intentionally *adversarial*: it spawns a helper process that
// mutates the mmapped cache file *byte-by-byte* to simulate the kind of
// inconsistent snapshots that can happen under concurrent modification:
//   - "torn" multi-byte stores (from the reader's perspective)
//   - observing partially-updated slot revision/index bytes
//
// The helper is NOT a conforming writer; it does not follow the commit protocol.
// The goal is deterministic regression safety: if the read-side seqlock logic is
// weakened (e.g. non-atomic generation handling, missing stability checks, or
// insufficient retry/ErrBusy behavior), this test should fail quickly.
//
// # Expected behavior
//
// While bytes are being torn:
//   - Cache.Get() may return ErrBusy (transient) and should be retried.
//   - If Cache.Get() returns an entry, it MUST be exactly one of two stable
//     states (A or B). It MUST NOT return a mixed/torn revision or index.
//
// # Notes
//
// We cannot reliably force real CPU-level tearing on all architectures.
// Instead, we simulate what tearing looks like to a reader by writing the bytes
// one-at-a-time with small delays between bytes.
func Test_Seqlock_CrossProcess_Get_Does_Not_Return_Mixed_Revision_Or_Index_When_Bytes_Torn(t *testing.T) {
	t.Parallel()

	if os.Getenv("TK_SLOTCACHE_TORN_HELPER") == "1" {
		runTornBytesHelper(t)

		return
	}

	duration := *flagConcurrencyStress
	if testing.Short() {
		duration = 250 * time.Millisecond
	}

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "tornbytes.slc")
	stopPath := filepath.Join(tmpDir, "STOP")
	readyPath := filepath.Join(tmpDir, "READY")

	opts := slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 64,
		// Keep locking enabled for creation.
	}

	// Create initial file with a valid entry so Get() is always a bucket lookup
	// followed by reading slot bytes.
	c0, err := slotcache.Open(opts)
	if err != nil {
		t.Fatalf("Open(create) failed: %v", err)
	}

	w0, err := c0.BeginWrite()
	if err != nil {
		t.Fatalf("BeginWrite(create) failed: %v", err)
	}

	_ = w0.Put(seqlockKey, seqlockRevA, seqlockIndexA)
	_ = w0.Commit()
	_ = w0.Close()
	_ = c0.Close()

	ctx := t.Context()

	timeoutCtx, cancel := context.WithTimeout(ctx, duration+3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, os.Args[0],
		"-test.run=^Test_Seqlock_CrossProcess_Get_Does_Not_Return_Mixed_Revision_Or_Index_When_Bytes_Torn$", "-test.v")

	cmd.Env = append(os.Environ(),
		"TK_SLOTCACHE_TORN_HELPER=1",
		"TK_SLOTCACHE_PATH="+path,
		"TK_SLOTCACHE_STOP="+stopPath,
		"TK_SLOTCACHE_READY="+readyPath,
		// Disable locking in the reader to avoid Open() taking the writer lock and
		// classifying odd generation as ErrCorrupt (crashed writer). This test is
		// specifically about read-side seqlock correctness under an active writer.
		"TK_SLOTCACHE_DISABLE_LOCKING=1",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	startErr := cmd.Start()
	if startErr != nil {
		t.Fatalf("start helper: %v", startErr)
	}

	waitForFile(t, readyPath, 2*time.Second)

	readerOpts := opts
	readerOpts.DisableLocking = true

	cache, err := slotcache.Open(readerOpts)
	if err != nil {
		// Under concurrent torn-byte writes, Open may observe the file mid-commit and
		// return ErrBusy. Treat that as transient and retry for a short time.
		if errors.Is(err, slotcache.ErrBusy) {
			deadline := time.Now().Add(500 * time.Millisecond)
			for time.Now().Before(deadline) {
				cache, err = slotcache.Open(readerOpts)
				if err == nil {
					break
				}

				if !errors.Is(err, slotcache.ErrBusy) {
					break
				}

				time.Sleep(5 * time.Millisecond)
			}
		}

		if err != nil {
			t.Fatalf("Open(reader) failed: %v", err)
		}
	}

	defer func() { _ = cache.Close() }()

	readerCtx, readerCancel := context.WithTimeout(ctx, duration)
	defer readerCancel()

	nReaders := max(2, runtime.GOMAXPROCS(0))

	errCh := make(chan error, 1)

	var wg sync.WaitGroup
	wg.Add(nReaders)

	for range nReaders {
		go func() {
			defer wg.Done()

			for readerCtx.Err() == nil {
				entry, found, getErr := cache.Get(seqlockKey)
				if getErr != nil {
					if errors.Is(getErr, slotcache.ErrBusy) {
						continue
					}

					sendErr(errCh, fmt.Errorf("Get returned unexpected error: %w", getErr))

					return
				}

				if !found {
					continue
				}

				if !bytes.Equal(entry.Key, seqlockKey) {
					sendErr(errCh, fmt.Errorf("Get returned wrong key: got=%x want=%x", entry.Key, seqlockKey))

					return
				}

				// Must be exactly one of the two stable states.
				if entry.Revision != seqlockRevA && entry.Revision != seqlockRevB {
					sendErr(errCh, fmt.Errorf("Get observed mixed/torn revision: got=0x%016X", uint64(entry.Revision)))

					return
				}

				if !bytes.Equal(entry.Index, seqlockIndexA) && !bytes.Equal(entry.Index, seqlockIndexB) {
					sendErr(errCh, fmt.Errorf("Get observed mixed/torn index: got=%x", entry.Index))

					return
				}
			}
		}()
	}

	wg.Wait()

	// Stop helper and wait for clean exit.
	touchFile(t, stopPath)

	waitErr := cmd.Wait()

	if timeoutCtx.Err() == context.DeadlineExceeded {
		t.Fatal("helper timed out")
	}

	if waitErr != nil {
		t.Fatalf("helper failed: %v", waitErr)
	}

	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

func runTornBytesHelper(t *testing.T) {
	t.Helper()

	path := os.Getenv("TK_SLOTCACHE_PATH")
	stopPath := os.Getenv("TK_SLOTCACHE_STOP")
	readyPath := os.Getenv("TK_SLOTCACHE_READY")

	if path == "" || stopPath == "" || readyPath == "" {
		t.Fatal("missing helper env vars")
	}

	// mmap the file so writes are visible to the reader process.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("helper open file: %v", err)
	}

	defer func() { _ = f.Close() }()

	st, err := f.Stat()
	if err != nil {
		t.Fatalf("helper stat file: %v", err)
	}

	sz := int(st.Size())

	data, err := syscall.Mmap(int(f.Fd()), 0, sz, syscall.PROT_READ|syscall.PROT_WRITE, syscall.MAP_SHARED)
	if err != nil {
		t.Fatalf("helper mmap: %v", err)
	}

	defer func() { _ = syscall.Munmap(data) }()

	// Compute offsets for slot 0 with keySize=8, indexSize=4.
	//
	// IMPORTANT: This is hard-coded to the test's chosen options. If the on-disk
	// format changes, update these offsets.
	//
	// Slot layout (see slotcache spec 002-format.md):
	//   meta(8) + key(8) + key_pad(0) + revision(8) + index(4) + trailing_pad
	const (
		slotsOffset = slcHeaderSize
		slotID0     = 0
	)

	const slotSize = 32 // computeSlotSize(8,4) = align8(8+8+0+8+4) = 32

	const (
		slotOffset = slotsOffset + slotID0*slotSize
		revOffset  = slotOffset + 16
		idxOffset  = slotOffset + 24
	)

	// Stable states.
	//
	// We use an odd generation to indicate "writer in progress" and two even
	// generations to indicate stable snapshots.
	var (
		genA   uint64 = 100
		genOdd uint64 = 101
		genB   uint64 = 200
	)

	touchFile(t, readyPath)

	// Mutate in a loop until STOP exists.
	//
	// This helper intentionally performs a *slow-motion commit* that conforms to
	// the seqlock protocol:
	//   1) publish odd generation (writer in progress)
	//   2) tear-write slot fields
	//   3) publish even generation (stable)
	//
	// Readers must treat the odd generation as unstable and retry/ErrBusy.
	for !fileExists(stopPath) {
		// Commit A -> B.
		writeUint64Torn(data, offGeneration, genOdd)
		writeInt64Torn(data, revOffset, seqlockRevB)
		writeBytesTorn(data, idxOffset, seqlockIndexB)
		writeUint64Torn(data, offGeneration, genB)

		// Commit B -> A.
		writeUint64Torn(data, offGeneration, genOdd)
		writeInt64Torn(data, revOffset, seqlockRevA)
		writeBytesTorn(data, idxOffset, seqlockIndexA)
		writeUint64Torn(data, offGeneration, genA)
	}
}

func writeUint64Torn(data []byte, off int, v uint64) {
	var buf [8]byte
	binary.LittleEndian.PutUint64(buf[:], v)

	for i := range 8 {
		data[off+i] = buf[i]

		time.Sleep(50 * time.Microsecond)
	}
}

func writeInt64Torn(data []byte, off int, v int64) {
	writeUint64Torn(data, off, uint64(v))
}

func writeBytesTorn(data []byte, off int, b []byte) {
	for i := range b {
		data[off+i] = b[i]

		time.Sleep(50 * time.Microsecond)
	}
}
