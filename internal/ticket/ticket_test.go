package ticket

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestNextSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{"", "a"},
		{"a", "b"},
		{"b", "c"},
		{"y", "z"},
		{"z", "za"},
		{"za", "zb"},
		{"zz", "zza"},
		{"zzz", "zzza"},
		{"zza", "zzb"},
		{"zzb", "zzc"},
		{"zzy", "zzz"},
	}

	for _, testCase := range tests {
		t.Run(testCase.input+"->"+testCase.want, func(t *testing.T) {
			t.Parallel()

			got := nextSuffix(testCase.input)
			if got != testCase.want {
				t.Errorf("nextSuffix(%q) = %q, want %q", testCase.input, got, testCase.want)
			}
		})
	}
}

func TestGenerateUniqueIDNoCollision(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, dirPerms)
	if err != nil {
		t.Fatalf("failed to create ticket dir: %v", err)
	}

	ticketID, err := GenerateUniqueID(ticketDir)
	if err != nil {
		t.Fatalf("GenerateUniqueID failed: %v", err)
	}

	// ID should be base ID with no suffix (7 chars from base32 encoding)
	if len(ticketID) != 7 {
		t.Errorf("expected base ID with 7 chars, got %q (len=%d)", ticketID, len(ticketID))
	}
}

func TestGenerateUniqueIDWithCollisions(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	ticketDir := filepath.Join(tmpDir, ".tickets")

	err := os.MkdirAll(ticketDir, dirPerms)
	if err != nil {
		t.Fatalf("failed to create ticket dir: %v", err)
	}

	// Generate first ID
	baseID, err := GenerateUniqueID(ticketDir)
	if err != nil {
		t.Fatalf("GenerateUniqueID failed: %v", err)
	}

	// Create a file with that ID to simulate collision
	err = os.WriteFile(filepath.Join(ticketDir, baseID+".md"), []byte("test"), filePerms)
	if err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	// The next unique ID generated in the same second should have suffix
	// But since we can't control time, we manually create the collision scenario
	// by pre-creating files with predictable IDs

	// For now, just verify the function returns unique IDs
	id2, err := GenerateUniqueID(ticketDir)
	if err != nil {
		t.Fatalf("GenerateUniqueID failed on second call: %v", err)
	}

	if id2 == baseID {
		t.Errorf("second ID should be different from first, got same: %q", id2)
	}
}

func TestIDsSortLexicographically(t *testing.T) {
	t.Parallel()

	// Test that suffixed IDs sort correctly
	ids := []string{
		"d5czj08",
		"d5czj08a",
		"d5czj08b",
		"d5czj08z",
		"d5czj08za",
		"d5czj08zb",
		"d5czj08zz",
		"d5czj08zza",
	}

	// Shuffle and sort to verify
	shuffled := make([]string, len(ids))
	copy(shuffled, ids)
	shuffled[0], shuffled[3] = shuffled[3], shuffled[0]
	shuffled[1], shuffled[5] = shuffled[5], shuffled[1]

	slices.Sort(shuffled)

	for i, id := range shuffled {
		if id != ids[i] {
			t.Errorf("position %d: got %q, want %q", i, id, ids[i])
		}
	}
}
