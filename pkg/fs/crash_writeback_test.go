package fs_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"sort"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

func Test_Crash_Writeback_Persists_File_Data_When_File_Sync_Is_Missing(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 1,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "data.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "data.txt", testContentNew, 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	got := mustReadFile(t, crash, "data.txt")
	if got != testContentNew {
		t.Fatalf("ReadFile(\"data.txt\")=%q, want %q", got, testContentNew)
	}
}

func Test_Crash_Writeback_Persists_File_Create_When_Dir_Sync_Is_Missing(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 2,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "unsynced.txt", "hello", 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	got := mustReadFile(t, crash, "unsynced.txt")
	if got != "hello" {
		t.Fatalf("ReadFile(\"unsynced.txt\")=%q, want %q", got, "hello")
	}
}

func Test_Crash_Writeback_Keeps_Old_Data_When_File_Weight_Is_KeepOld(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 3,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepOld: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "data.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "data.txt", testContentNew, 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	got := mustReadFile(t, crash, "data.txt")
	if got != testContentOld {
		t.Fatalf("ReadFile(\"data.txt\")=%q, want %q", got, testContentOld)
	}
}

func Test_Crash_Writeback_Keeps_Prefix_Data_When_File_Weight_Is_KeepPrefix(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 4,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepPrefix: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	oldData := "old-contents-000"
	newData := "new-contents-111"

	writeFile(t, crash, "data.txt", oldData, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "data.txt", newData, 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	got := mustReadFile(t, crash, "data.txt")
	if len(got) != len(oldData) {
		t.Fatalf("ReadFile(\"data.txt\"): got len=%d, want len=%d", len(got), len(oldData))
	}

	prefixLen := 0
	for prefixLen < len(got) && prefixLen < len(newData) && got[prefixLen] == newData[prefixLen] {
		prefixLen++
	}

	if got[prefixLen:] != oldData[prefixLen:] {
		t.Fatalf("ReadFile(\"data.txt\")=%q, want prefix of %q with suffix of %q", got, newData, oldData)
	}
}

func Test_Crash_Writeback_Drops_Create_When_Dir_Weight_Is_KeepOld(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 5,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "unsynced.txt", "hello", 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, "unsynced.txt")
}

func Test_Crash_Writeback_Retains_Deleted_Entry_When_Dir_Weight_Is_KeepOld(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 11,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "deleted.txt", testContentData, 0o644, true)
	syncDir(t, crash, ".")

	err := crash.Remove("deleted.txt")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "deleted.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"deleted.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Removes_Deleted_Entry_When_Dir_Weight_Is_KeepNew(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 12,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "deleted.txt", testContentData, 0o644, true)
	syncDir(t, crash, ".")

	err := crash.Remove("deleted.txt")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, "deleted.txt")
}

func Test_Crash_Writeback_Uses_Old_Entry_When_Name_Is_Replaced(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 13,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "swap.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "swap.tmp", testContentNew, 0o644, false)

	err := crash.Rename("swap.tmp", "swap.txt")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "swap.txt"), testContentOld; got != want {
		t.Fatalf("ReadFile(\"swap.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Uses_New_Entry_When_Name_Is_Replaced(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 14,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "swap.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "swap.tmp", testContentNew, 0o644, false)

	err := crash.Rename("swap.tmp", "swap.txt")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "swap.txt"), testContentNew; got != want {
		t.Fatalf("ReadFile(\"swap.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Uses_New_Name_When_Rename_Is_Unsynced(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 6,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "old.txt", testContentData, 0o644, true)
	syncDir(t, crash, ".")

	err := crash.Rename("old.txt", "new.txt")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, "old.txt")

	if got, want := mustReadFile(t, crash, "new.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"new.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Uses_Old_Name_When_Rename_Is_Unsynced(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 7,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "old.txt", testContentData, 0o644, true)
	syncDir(t, crash, ".")

	err := crash.Rename("old.txt", "new.txt")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, "new.txt")

	if got, want := mustReadFile(t, crash, "old.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"old.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Uses_New_Name_When_Dir_Rename_Is_Unsynced(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 15,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	writeFile(t, crash, "old/data.txt", testContentData, 0o644, true)
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, testContentOld)

	if got, want := mustReadFile(t, crash, "new/data.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"new/data.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Does_Not_Produce_Both_Names_For_Dir_Rename_Within_Renamed_Directory(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 9,
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	err := crash.MkdirAll("old/sub", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entries durable.
	syncDir(t, crash, ".")
	syncDir(t, crash, testContentOld)

	err = crash.Rename("old/sub", "old/sub2")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	// This seed should keep the new parent directory name.
	requireNotExists(t, crash, testContentOld)

	entries, err := crash.ReadDir(testContentNew)
	if err != nil {
		t.Fatalf("ReadDir(\"new\"): %v", err)
	}

	haveSub := false
	haveSub2 := false

	for _, entry := range entries {
		switch entry.Name() {
		case "sub":
			haveSub = true
		case "sub2":
			haveSub2 = true
		}
	}

	if haveSub && haveSub2 {
		t.Fatal("ReadDir(\"new\") contains both \"sub\" and \"sub2\", want at most one")
	}
}

func Test_Crash_Writeback_Preserves_Durable_File_Within_Renamed_Directory_When_Rename_Is_Kept_New(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 5,
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	writeFile(t, crash, "old/data.txt", testContentData, 0o644, true)
	syncDir(t, crash, testContentOld)

	err = crash.Remove("old/data.txt")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	requireNotExists(t, crash, "old/data.txt")

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, testContentOld)

	if got, want := mustReadFile(t, crash, "new/data.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"new/data.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Uses_Old_Name_When_Dir_Rename_Is_Unsynced(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 16,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	writeFile(t, crash, "old/data.txt", testContentData, 0o644, true)
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, testContentNew)

	if got, want := mustReadFile(t, crash, "old/data.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"old/data.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Writeback_Does_Not_Resurrect_Replaced_Directory_Subtree(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 13,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	err := crash.MkdirAll("dst", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entry durable.
	syncDir(t, crash, ".")

	err = crash.MkdirAll("src", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "src/live.txt", "live", 0o644, false)

	writeFile(t, crash, "dst/stale.txt", "stale", 0o644, true)
	// Make the stale file name durable.
	syncDir(t, crash, "dst")

	err = crash.RemoveAll("dst")
	if err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	// Intentionally do not sync the parent; the removal is not durable.

	err = crash.Rename("src", "dst")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Intentionally do not sync the parent; writeback chooses whether the
	// replacement rename persists.

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "dst/live.txt"), "live"; got != want {
		t.Fatalf("ReadFile(\"dst/live.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "dst/stale.txt")
}

func Test_Crash_Writeback_Does_Not_Leak_New_Subtree_When_Replaced_Directory_Entry_Is_Kept_Old(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 14,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	err := crash.MkdirAll("dst", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entry durable.
	syncDir(t, crash, ".")

	err = crash.MkdirAll("src", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "src/live.txt", "live", 0o644, false)

	writeFile(t, crash, "dst/stale.txt", "stale", 0o644, true)
	// Make the stale file name durable.
	syncDir(t, crash, "dst")

	err = crash.RemoveAll("dst")
	if err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	// Intentionally do not sync the parent; the removal is not durable.

	err = crash.Rename("src", "dst")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Intentionally do not sync the parent; writeback chooses whether the
	// replacement rename persists.

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "dst/stale.txt"), "stale"; got != want {
		t.Fatalf("ReadFile(\"dst/stale.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "dst/live.txt")
}

func Test_Crash_Writeback_Disables_Writeback_When_All_Weights_Are_Zero(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 17,
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "stable.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "stable.txt", testContentNew, 0o644, false)
	writeFile(t, crash, "unsynced.txt", "hello", 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "stable.txt"), testContentOld; got != want {
		t.Fatalf("ReadFile(\"stable.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "unsynced.txt")
}

func Test_Crash_Writeback_Falls_Back_To_KeepOld_For_Files_When_FileWeights_All_Zero(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 101,
			// All file weights are zero => fall back to strict (KeepOld-only) file model.
			FileWeights: fs.CrashWritebackFileWeights{},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	writeFile(t, crash, "unsynced.txt", "hello", 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	// Entry may persist due to dir writeback, but data should not because file writeback
	// is in strict KeepOld-only mode and there is no old durable content.
	if got := mustReadFile(t, crash, "unsynced.txt"); got != "" {
		t.Fatalf("ReadFile(\"unsynced.txt\")=%q, want empty (file writeback KeepOld fallback)", got)
	}
}

func Test_Crash_Writeback_Mixes_Dir_Entries_When_Weights_Are_Equal(t *testing.T) {
	t.Parallel()

	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 8,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	paths := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"}
	for _, path := range paths {
		writeFile(t, crash, path, path, 0o644, false)
	}

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	kept := 0

	for _, path := range paths {
		exists, err := crash.Exists(path)
		if err != nil {
			t.Fatalf("Exists(%q): %v", path, err)
		}

		if exists {
			kept++
		}
	}

	if kept == 0 || kept == len(paths) {
		t.Fatalf("writeback kept %d/%d entries, want mixed outcomes", kept, len(paths))
	}
}

func Test_Crash_Writeback_Uses_Weighted_File_Outcomes_When_Seed_Is_Fixed(t *testing.T) {
	t.Parallel()

	seed := int64(9)
	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: seed,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepOld: 1,
				KeepNew: 3,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	paths := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"}
	for _, path := range paths {
		writeFile(t, crash, path, "old-"+path, 0o644, true)
	}

	syncDir(t, crash, ".")

	for _, path := range paths {
		writeFile(t, crash, path, "new-"+path, 0o644, false)
	}

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	rolls := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
	keepOldThreshold := 1.0 / 4.0

	for _, path := range paths {
		roll := rolls.Float64()

		want := "new-" + path
		if roll < keepOldThreshold {
			want = "old-" + path
		}

		got := mustReadFile(t, crash, path)
		if got != want {
			t.Fatalf("ReadFile(\"%s\")=%q, want %q", path, got, want)
		}
	}
}

func Test_Crash_Writeback_Uses_Weighted_Dir_Entries_When_Seed_Is_Fixed(t *testing.T) {
	t.Parallel()

	seed := int64(10)
	config := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: seed,
			FileWeights: fs.CrashWritebackFileWeights{
				KeepNew: 1,
			},
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepOld: 1,
				KeepNew: 3,
			},
		},
	}
	crash := mustNewCrash(t, &config)

	paths := []string{"a.txt", "b.txt", "c.txt", "d.txt", "e.txt", "f.txt"}
	sort.Strings(paths)

	for _, path := range paths {
		writeFile(t, crash, path, "data-"+path, 0o644, false)
	}

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	rolls := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
	keepNewThreshold := 3.0 / 4.0

	for _, path := range paths {
		roll := rolls.Float64()
		if roll < keepNewThreshold {
			if got, want := mustReadFile(t, crash, path), "data-"+path; got != want {
				t.Fatalf("ReadFile(\"%s\")=%q, want %q", path, got, want)
			}

			continue
		}

		requireNotExists(t, crash, path)
	}
}

func Test_Crash_Writeback_SimulateCrash_Rejects_Symlinks_In_Live_Tree(t *testing.T) {
	t.Parallel()

	config := &fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			Seed: 1,
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: 1,
			},
		},
	}

	capture := &captureTempDir{t: t}

	crash, err := fs.NewCrash(capture, fs.NewReal(), config)
	if err != nil {
		t.Fatalf("fs.NewCrash: %v", err)
	}

	writeFile(t, crash, "target.txt", testContentData, 0o644, false)
	mustSymlink(t, capture.dir, "target.txt", "link.txt")

	err = crash.SimulateCrash()
	if err == nil {
		t.Fatal("SimulateCrash: want error")
	}

	if !errors.Is(err, fs.ErrCrashFS) {
		t.Fatalf("SimulateCrash err=%v, want errors.Is(..., fs.ErrCrashFS)=true", err)
	}

	if !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("SimulateCrash err=%v, want contains \"symlink\"", err)
	}
}

func Fuzz_Crash_Writeback_File_When_Data_Is_Dirty(f *testing.F) {
	f.Add(int64(1), []byte(testContentOld), []byte(testContentNew), uint8(1), uint8(0), uint8(0))
	f.Add(int64(2), []byte(testContentOld), []byte(testContentNew), uint8(0), uint8(1), uint8(0))
	f.Add(int64(3), []byte(testContentOld), []byte(testContentNew), uint8(0), uint8(0), uint8(1))

	f.Fuzz(func(t *testing.T, seed int64, oldData, newData []byte, wOld, wNew, wPrefix uint8) {
		// Keep fuzz inputs small so we don't create huge temp files.
		oldData = limitFuzzBytes(oldData, 128)
		newData = limitFuzzBytes(newData, 128)
		// Ensure non-empty payloads so prefix checks are meaningful.
		if len(oldData) == 0 {
			oldData = []byte(testContentOld)
		}

		if len(newData) == 0 {
			newData = []byte(testContentNew)
		}

		// Normalize weights so at least one outcome is enabled.
		weights := normalizeFuzzWeights(wOld, wNew, wPrefix)

		// Configure writeback to vary file contents while keeping dir entries.
		config := fs.CrashConfig{
			Writeback: fs.CrashWritebackConfig{
				Seed: seed,
				FileWeights: fs.CrashWritebackFileWeights{
					KeepOld:    weights[0],
					KeepNew:    weights[1],
					KeepPrefix: weights[2],
				},
				DirEntryWeights: fs.CrashWritebackDirEntryWeights{
					KeepNew: 1,
				},
			},
		}
		crash := mustNewCrash(t, &config)

		// Make the old contents durable.
		writeFile(t, crash, "data.txt", string(oldData), 0o644, true)
		syncDir(t, crash, ".")

		// Overwrite without sync so the file is dirty at crash time.
		writeFile(t, crash, "data.txt", string(newData), 0o644, false)

		// fs.Crash and assert the file matches an allowed writeback outcome.
		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("fs.Crash: %v", err)
		}

		got := []byte(mustReadFile(t, crash, "data.txt"))
		if !isWritebackFileOutcome(got, oldData, newData) {
			t.Fatalf("ReadFile(\"data.txt\")=%q, want old/new/prefix", got)
		}
	})
}

func Fuzz_Crash_Writeback_Dir_Entries_When_Dir_Sync_Is_Missing(f *testing.F) {
	f.Add(int64(1), uint8(3), uint8(1), uint8(1))
	f.Add(int64(2), uint8(5), uint8(0), uint8(1))
	f.Add(int64(3), uint8(4), uint8(1), uint8(0))

	f.Fuzz(func(t *testing.T, seed int64, count, wOld, wNew uint8) {
		// Normalize weights so at least one entry outcome is enabled.
		weights := normalizeFuzzWeights(wOld, wNew)

		// Configure writeback to vary dir entries while keeping file contents.
		config := fs.CrashConfig{
			Writeback: fs.CrashWritebackConfig{
				Seed: seed,
				FileWeights: fs.CrashWritebackFileWeights{
					KeepNew: 1,
				},
				DirEntryWeights: fs.CrashWritebackDirEntryWeights{
					KeepOld: weights[0],
					KeepNew: weights[1],
				},
			},
		}
		crash := mustNewCrash(t, &config)

		// Create a bounded set of unsynced files so dir entries are dirty.
		fileCount := int(count%6) + 1

		paths := make([]string, 0, fileCount)
		for i := range fileCount {
			paths = append(paths, fmt.Sprintf("file-%02d.txt", i))
		}

		for _, path := range paths {
			writeFile(t, crash, path, "data-"+path, 0o644, false)
		}

		// fs.Crash and ensure each entry is either present with correct data or absent.
		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("fs.Crash: %v", err)
		}

		for _, path := range paths {
			exists, err := crash.Exists(path)
			if err != nil {
				t.Fatalf("Exists(%q): %v", path, err)
			}

			if !exists {
				requireNotExists(t, crash, path)

				continue
			}

			if got, want := mustReadFile(t, crash, path), "data-"+path; got != want {
				t.Fatalf("ReadFile(\"%s\")=%q, want %q", path, got, want)
			}
		}
	})
}

func limitFuzzBytes(data []byte, maxLen int) []byte {
	// Clamp fuzz payload sizes to keep runtime fast and predictable.
	if len(data) > maxLen {
		return data[:maxLen]
	}

	return data
}

func normalizeFuzzWeights(weights ...uint8) []float64 {
	// Map fuzz bytes to small non-negative weights and ensure at least one
	// outcome is enabled (default to the first weight when all are zero).
	result := make([]float64, len(weights))
	seen := false

	for i, weight := range weights {
		result[i] = float64(weight % 5)
		if result[i] > 0 {
			seen = true
		}
	}

	if !seen && len(result) > 0 {
		result[0] = 1
	}

	return result
}

func isWritebackFileOutcome(got, oldData, newData []byte) bool {
	// Accept old, new, or a prefix-of-new + suffix-of-old outcome.
	if bytes.Equal(got, oldData) || bytes.Equal(got, newData) {
		return true
	}

	if len(got) != len(oldData) {
		return false
	}

	prefixLen := 0
	for prefixLen < len(got) && prefixLen < len(newData) && got[prefixLen] == newData[prefixLen] {
		prefixLen++
	}

	return bytes.Equal(got[prefixLen:], oldData[prefixLen:])
}
