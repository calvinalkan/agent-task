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
	"errors"
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
