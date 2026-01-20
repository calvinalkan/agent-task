package slotcache_test

// Scan input validation tests
//
// These tests verify that scan-style APIs reject clearly unreasonable inputs
// (Offset/Limit caps, etc.) with ErrInvalidInput.
//
// This is primarily a safety measure: it avoids integer-overflow footguns and
// bounds worst-case allocations/looping in paths that are not heavily fuzzed at
// extreme values.

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

func Test_Scan_Returns_ErrInvalidInput_When_Offset_Exceeds_Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_offset_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	_, scanErr := c.Scan(slotcache.ScanOptions{Offset: 100_000_001, Limit: 0})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("Scan(offset cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

func Test_Scan_Returns_ErrInvalidInput_When_Limit_Exceeds_Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scan_limit_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    4,
		UserVersion:  1,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	_, scanErr := c.Scan(slotcache.ScanOptions{Offset: 0, Limit: 100_000_001})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("Scan(limit cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

func Test_ScanMatch_Returns_ErrInvalidInput_When_PrefixBits_Exceed_KeyCapacity(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanmatch_prefixbits_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 8,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	// Bits is intentionally huge. The implementation must reject it as invalid
	// without overflowing internal arithmetic.
	_, scanErr := c.ScanMatch(slotcache.Prefix{Offset: 0, Bits: 1 << 30, Bytes: nil}, slotcache.ScanOptions{Offset: 0, Limit: 0})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("ScanMatch(prefix bits cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}

func Test_ScanRange_Returns_ErrInvalidInput_When_Offset_Exceeds_Max(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "scanrange_offset_cap.slc")

	c, err := slotcache.Open(slotcache.Options{
		Path:         path,
		KeySize:      8,
		IndexSize:    0,
		UserVersion:  1,
		SlotCapacity: 8,
		OrderedKeys:  true,
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = c.Close() }()

	_, scanErr := c.ScanRange(nil, nil, slotcache.ScanOptions{Offset: 100_000_001, Limit: 0})
	if !errors.Is(scanErr, slotcache.ErrInvalidInput) {
		t.Fatalf("ScanRange(offset cap) error mismatch: got=%v want=%v", scanErr, slotcache.ErrInvalidInput)
	}
}
