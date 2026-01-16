//go:build slotcache_impl

package slotcache_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
)

// FuzzSpec_OpenAndReadRobustness writes fuzz bytes as a cache file and then
// tries to Open and read it.
//
// Allowed outcomes:
// - Open returns ErrCorrupt / ErrIncompatible (or ErrBusy).
// - Open succeeds and reads either succeed or return ErrCorrupt/ErrBusy.
//
// Disallowed outcomes:
// - panic
// - infinite hang
// - Open succeeds but file fails the spec oracle.
func FuzzSpec_OpenAndReadRobustness(f *testing.F) {
	// Seed a few "interesting" shapes.
	f.Add([]byte{})
	f.Add(make([]byte, 256))
	f.Add([]byte("SLC1"))

	f.Fuzz(func(t *testing.T, fuzzBytes []byte) {
		tmpDir := t.TempDir()
		cacheFilePath := filepath.Join(tmpDir, "spec_mut_fuzz.slc")

		writeError := os.WriteFile(cacheFilePath, fuzzBytes, 0o600)
		if writeError != nil {
			t.Fatalf("WriteFile failed: %v", writeError)
		}

		options := slotcache.Options{
			Path:         cacheFilePath,
			KeySize:      8,
			IndexSize:    4,
			UserVersion:  1,
			SlotCapacity: 64,
		}

		cacheHandle, openError := slotcache.Open(options)
		if openError != nil {
			// Only allow classified errors.
			if errors.Is(openError, slotcache.ErrCorrupt) ||
				errors.Is(openError, slotcache.ErrIncompatible) ||
				errors.Is(openError, slotcache.ErrBusy) {
				return
			}

			t.Fatalf("Open returned unexpected error: %v", openError)
		}

		// If Open succeeded, validate file against the oracle.
		validationError := validateSlotcacheFileAgainstOptions(cacheFilePath, options)
		if validationError != nil {
			t.Fatalf("Open succeeded but speccheck failed: %v", validationError)
		}

		// Exercise basic reads. They must not panic.
		_, _ = cacheHandle.Len()
		_, _, _ = cacheHandle.Get(make([]byte, options.KeySize))
		_, _ = cacheHandle.Scan(slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: 0})
		_, _ = cacheHandle.ScanPrefix([]byte{0x00}, slotcache.ScanOpts{Reverse: false, Offset: 0, Limit: 0})

		_ = cacheHandle.Close()
	})
}
