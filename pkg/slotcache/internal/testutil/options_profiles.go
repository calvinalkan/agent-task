// options_profiles.go provides deterministic option profiles for tests.
//
// These profiles are templates without a Path field—tests inject Path per run.
// The profiles are designed to exercise edge cases:
//   - KeySize=1: minimal key (single byte)
//   - KeySize=7,9: non-power-of-two sizes (alignment/padding)
//   - KeySize=16: larger key (typical UUID size)
//   - IndexSize=0: no index bytes (purely key-based lookup)
//   - IndexSize=3: small index (non-power-of-two)
//   - SlotCapacity=1,2: extremely constrained (exercises ErrFull quickly)
//   - SlotCapacity=4,8: small but allows multiple operations
//   - OrderedKeys=true: exercises ScanRange and ordered insert semantics
//
// Why these specific profiles?
//   - They cover realistic use cases (UUID keys, small caches, ordered mode)
//   - They stress edge conditions (capacity 1 & 2, odd-sized keys/indices)
//   - They're small enough for fast deterministic tests
//   - They're diverse enough to catch alignment and boundary bugs

package testutil

import "github.com/calvinalkan/agent-task/pkg/slotcache"

// OptionsProfile represents a pre-configured set of options for testing.
// Path is intentionally empty—callers must set it before use.
type OptionsProfile struct {
	// Name identifies this profile for logging and subtest names.
	Name string

	// Options contains the slotcache configuration (Path is empty).
	Options slotcache.Options
}

// OptionProfiles returns a slice of deterministic test profiles.
// Each profile is a template—callers must set Options.Path before use.
//
// The profiles are ordered from most constrained to least constrained:
//  1. Single slot, large key (exercises ErrFull immediately)
//  2. Two slots, minimal key (exercises ErrFull quickly)
//  3. Four slots, odd-sized key (exercises padding)
//  4. Eight slots, key+index (more headroom, exercises index filtering)
//  5. Eight slots, ordered mode (exercises ScanRange and order validation)
func OptionProfiles() []OptionsProfile {
	return []OptionsProfile{
		{
			Name: "KeySize16_IndexSize0_Capacity1",
			Options: slotcache.Options{
				KeySize:      16,
				IndexSize:    0,
				SlotCapacity: 1,
				UserVersion:  1,
				Writeback:    slotcache.WritebackNone,
			},
		},
		{
			Name: "KeySize1_IndexSize0_Capacity2",
			Options: slotcache.Options{
				KeySize:      1,
				IndexSize:    0,
				SlotCapacity: 2,
				UserVersion:  1,
				Writeback:    slotcache.WritebackNone,
			},
		},
		{
			Name: "KeySize7_IndexSize0_Capacity4",
			Options: slotcache.Options{
				KeySize:      7,
				IndexSize:    0,
				SlotCapacity: 4,
				UserVersion:  1,
				Writeback:    slotcache.WritebackNone,
			},
		},
		{
			Name: "KeySize9_IndexSize3_Capacity8",
			Options: slotcache.Options{
				KeySize:      9,
				IndexSize:    3,
				SlotCapacity: 8,
				UserVersion:  1,
				Writeback:    slotcache.WritebackNone,
			},
		},
		{
			Name: "KeySize8_IndexSize4_Capacity8_Ordered",
			Options: slotcache.Options{
				KeySize:      8,
				IndexSize:    4,
				SlotCapacity: 8,
				OrderedKeys:  true,
				UserVersion:  1,
				Writeback:    slotcache.WritebackNone,
			},
		},
	}
}

// ProfileByIndex returns a profile by index, wrapping around if necessary.
// This is useful for byte-driven selection in fuzz tests.
//
// Usage:
//
//	profile := testutil.ProfileByIndex(int(fuzzByte))
//	profile.Options.Path = filepath.Join(t.TempDir(), "test.slc")
func ProfileByIndex(index int) OptionsProfile {
	profiles := OptionProfiles()

	return profiles[index%len(profiles)]
}

// ProfileByByte returns a profile selected by a seed byte.
// This is a convenience wrapper around ProfileByIndex for fuzz tests.
func ProfileByByte(b byte) OptionsProfile {
	return ProfileByIndex(int(b))
}

// WithPath returns a copy of the profile's Options with Path set.
// This is the recommended way to get usable options from a profile.
//
// Usage:
//
//	for _, profile := range testutil.OptionProfiles() {
//	    t.Run(profile.Name, func(t *testing.T) {
//	        opts := profile.WithPath(filepath.Join(t.TempDir(), "test.slc"))
//	        cache, err := slotcache.Open(opts)
//	        // ...
//	    })
//	}
func (p *OptionsProfile) WithPath(path string) slotcache.Options {
	opts := p.Options
	opts.Path = path

	return opts
}
