package testutil_test

import (
	"path/filepath"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/slotcache"
	"github.com/calvinalkan/agent-task/pkg/slotcache/internal/testutil"
)

func Test_OptionProfiles_Returns_Five_Profiles_When_Called(t *testing.T) {
	t.Parallel()

	profiles := testutil.OptionProfiles()
	if len(profiles) != 5 {
		t.Errorf("expected 5 profiles, got %d", len(profiles))
	}
}

func Test_OptionProfiles_Returns_Empty_Path_When_Called(t *testing.T) {
	t.Parallel()

	for _, profile := range testutil.OptionProfiles() {
		if profile.Options.Path != "" {
			t.Errorf("profile %q has non-empty Path: %q", profile.Name, profile.Options.Path)
		}
	}
}

func Test_OptionProfiles_Returns_Valid_KeySize_When_Called(t *testing.T) {
	t.Parallel()

	// KeySize must be >= 1 (per spec, max is 512 but we use small values)
	for _, profile := range testutil.OptionProfiles() {
		if profile.Options.KeySize < 1 || profile.Options.KeySize > 512 {
			t.Errorf("profile %q has invalid KeySize: %d", profile.Name, profile.Options.KeySize)
		}
	}
}

func Test_OptionProfiles_Returns_Valid_IndexSize_When_Called(t *testing.T) {
	t.Parallel()

	// IndexSize must be >= 0
	for _, profile := range testutil.OptionProfiles() {
		if profile.Options.IndexSize < 0 {
			t.Errorf("profile %q has invalid IndexSize: %d", profile.Name, profile.Options.IndexSize)
		}
	}
}

func Test_OptionProfiles_Returns_Valid_SlotCapacity_When_Called(t *testing.T) {
	t.Parallel()

	for _, profile := range testutil.OptionProfiles() {
		if profile.Options.SlotCapacity < 1 {
			t.Errorf("profile %q has invalid SlotCapacity: %d", profile.Name, profile.Options.SlotCapacity)
		}
	}
}

func Test_OptionProfiles_Returns_Unique_Names_When_Called(t *testing.T) {
	t.Parallel()

	seen := make(map[string]bool)
	for _, profile := range testutil.OptionProfiles() {
		if seen[profile.Name] {
			t.Errorf("duplicate profile name: %q", profile.Name)
		}

		seen[profile.Name] = true
	}
}

func Test_OptionsProfile_WithPath_Sets_Path_When_Called(t *testing.T) {
	t.Parallel()

	profile := testutil.OptionProfiles()[0]
	path := "/some/test/path.slc"
	opts := profile.WithPath(path)

	if opts.Path != path {
		t.Errorf("WithPath did not set path: got %q, want %q", opts.Path, path)
	}

	// Original profile should be unchanged
	if profile.Options.Path != "" {
		t.Error("WithPath mutated original profile")
	}
}

func Test_ProfileByIndex_Wraps_Around_When_Index_Exceeds_Count(t *testing.T) {
	t.Parallel()

	profiles := testutil.OptionProfiles()

	for i := range len(profiles) * 3 {
		got := testutil.ProfileByIndex(i)

		want := profiles[i%len(profiles)]
		if got.Name != want.Name {
			t.Errorf("ProfileByIndex(%d): got %q, want %q", i, got.Name, want.Name)
		}
	}
}

func Test_ProfileByByte_Returns_Valid_Profile_When_Any_Byte_Value(t *testing.T) {
	t.Parallel()

	// Ensure all byte values produce a valid profile
	for b := range 256 {
		profile := testutil.ProfileByByte(byte(b))
		if profile.Name == "" {
			t.Errorf("ProfileByByte(%d) returned empty name", b)
		}
	}
}

func Test_OptionProfiles_Creates_Valid_Cache_When_Path_Provided(t *testing.T) {
	t.Parallel()

	// Verify each profile produces valid options that can open a cache
	for _, profile := range testutil.OptionProfiles() {
		t.Run(profile.Name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "test.slc")
			opts := profile.WithPath(path)

			cache, err := slotcache.Open(opts)
			if err != nil {
				t.Fatalf("failed to open cache: %v", err)
			}
			defer cache.Close()
		})
	}
}

func Test_OptionProfiles_Includes_Ordered_Profile_When_Called(t *testing.T) {
	t.Parallel()

	found := false

	for _, profile := range testutil.OptionProfiles() {
		if profile.Options.OrderedKeys {
			found = true

			break
		}
	}

	if !found {
		t.Error("no profile with OrderedKeys=true found")
	}
}

func Test_OptionProfiles_Includes_Minimal_Capacity_When_Called(t *testing.T) {
	t.Parallel()

	foundOne := false
	foundTwo := false

	for _, profile := range testutil.OptionProfiles() {
		if profile.Options.SlotCapacity == 1 {
			foundOne = true
		}

		if profile.Options.SlotCapacity == 2 {
			foundTwo = true
		}
	}

	if !foundOne {
		t.Error("no profile with SlotCapacity=1 found")
	}

	if !foundTwo {
		t.Error("no profile with SlotCapacity=2 found")
	}
}
