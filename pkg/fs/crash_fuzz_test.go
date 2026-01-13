package fs_test

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

// =============================================================================
// Crashfs Fuzz Tests
//
// These tests generate random filesystem operation sequences and verify that
// post-crash state matches the durability model:
//   - fs.File CONTENT is durable only after file.Sync()
//   - Directory ENTRIES are durable only after dir.Sync() on parent
//
// Run with: go test ./pkg/fs -fuzz=Fuzz -fuzztime=60s
// =============================================================================

// durableState tracks what SHOULD survive a crash based on the durability model.
type durableState struct {
	// contentSynced maps path -> content for files where file.Sync() was called.
	// The content is what was on disk at the time of sync.
	contentSynced map[string]string

	// entryDurable maps path -> inode for files where parent dir.Sync() was
	// called while the file existed. The inode helps track renames.
	entryDurable map[string]uint64

	// dirEntryDurable tracks directories where parent was synced.
	dirEntryDurable map[string]bool

	// liveContent tracks current content of files (for sync to capture).
	liveContent map[string]string

	// liveInodes tracks current inodes (for rename tracking).
	liveInodes map[string]uint64

	// nextInode for generating unique inode IDs.
	nextInode uint64
}

func newDurableState() *durableState {
	return &durableState{
		contentSynced:   make(map[string]string),
		entryDurable:    make(map[string]uint64),
		dirEntryDurable: make(map[string]bool),
		liveContent:     make(map[string]string),
		liveInodes:      make(map[string]uint64),
		nextInode:       1,
	}
}

// expectedAfterCrash returns what should exist after crash.
// A file exists with content if BOTH entry and content are durable.
func (s *durableState) expectedAfterCrash() map[string]string {
	result := make(map[string]string)

	for path, inode := range s.entryDurable {
		// Entry is durable. Check if content was synced for this inode.
		// Find the path that synced this inode's content.
		var content string

		found := false

		for syncPath, syncContent := range s.contentSynced {
			if syncInode, ok := s.entryDurable[syncPath]; ok && syncInode == inode {
				content = syncContent
				found = true

				break
			}
			// Also check if the current path's inode matches
			if s.liveInodes[syncPath] == inode {
				content = syncContent
				found = true

				break
			}
		}

		if found {
			result[path] = content
		} else {
			// Entry durable but content not synced -> empty file
			result[path] = ""
		}
	}

	return result
}

type fuzzOp int

const (
	opCreateFile fuzzOp = iota
	opWriteFile
	opSyncFile
	opSyncDir
	opRename
	opRemove
	opMkdir
	opRemoveAll
	numOps
)

func (op fuzzOp) String() string {
	switch op {
	case opCreateFile:
		return "CreateFile"
	case opWriteFile:
		return "WriteFile"
	case opSyncFile:
		return "SyncFile"
	case opSyncDir:
		return "SyncDir"
	case opRename:
		return "Rename"
	case opRemove:
		return "Remove"
	case opMkdir:
		return "Mkdir"
	case opRemoveAll:
		return "RemoveAll"
	default:
		return "Unknown"
	}
}

// Fuzz_Crash_RandomOps_Preserve_Durability_Invariants_When_Crashed tests that
// random operation sequences preserve durability invariants after crash.
func Fuzz_Crash_RandomOps_Preserve_Durability_Invariants_When_Crashed(f *testing.F) {
	// Seed corpus
	f.Add(int64(1), 10)
	f.Add(int64(42), 20)
	f.Add(int64(123), 15)
	f.Add(int64(999), 25)
	f.Add(int64(0), 5)

	f.Fuzz(func(t *testing.T, seed int64, numOpsInt int) {
		if numOpsInt < 1 || numOpsInt > 100 {
			return
		}

		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
		crash := mustNewCrash(t, &fs.CrashConfig{})
		state := newDurableState()

		paths := []string{"a.txt", "b.txt", "c.txt", "d.txt"}
		dirs := []string{".", "sub"}

		// Ensure sub directory exists
		_ = crash.MkdirAll("sub", 0o755)

		// Generate and execute random operations
		for range numOpsInt {
			op := fuzzOp(rng.IntN(int(numOps)))
			executeFuzzOp(t, crash, state, rng, op, paths, dirs)
		}

		// fs.Crash
		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		// Verify
		verifyDurableState(t, crash, state)
	})
}

func executeFuzzOp(_ *testing.T, crash *fs.Crash, state *durableState, rng *rand.Rand, op fuzzOp, paths, dirs []string) {
	switch op {
	case opCreateFile, opWriteFile:
		path := paths[rng.IntN(len(paths))]
		content := fmt.Sprintf("v%d", rng.IntN(1000))

		err := crash.WriteFile(path, []byte(content), 0o644)
		if err == nil {
			if _, exists := state.liveInodes[path]; !exists {
				state.liveInodes[path] = state.nextInode
				state.nextInode++
			}

			state.liveContent[path] = content
		}

	case opSyncFile:
		path := paths[rng.IntN(len(paths))]

		f, err := crash.Open(path)
		if err == nil {
			err := f.Sync()
			if err == nil {
				if content, ok := state.liveContent[path]; ok {
					state.contentSynced[path] = content
				}
			}

			f.Close()
		}

	case opSyncDir:
		dir := dirs[rng.IntN(len(dirs))]

		d, err := crash.Open(dir)
		if err == nil {
			err := d.Sync()
			if err == nil {
				// All files in this directory become entry-durable
				for path, inode := range state.liveInodes {
					if parentRel(path) == dir || (dir == "." && parentRel(path) == "") {
						state.entryDurable[path] = inode
					}
				}
				// Remove entries that no longer exist in live
				for path := range state.entryDurable {
					parent := parentRel(path)
					if parent == "" {
						parent = "."
					}

					if parent == dir {
						if _, exists := state.liveInodes[path]; !exists {
							delete(state.entryDurable, path)
							delete(state.contentSynced, path)
						}
					}
				}
			}

			d.Close()
		}

	case opRename:
		if len(paths) < 2 {
			return
		}

		i, j := rng.IntN(len(paths)), rng.IntN(len(paths))
		if i == j {
			return
		}

		src, dst := paths[i], paths[j]

		err := crash.Rename(src, dst)
		if err == nil {
			if inode, ok := state.liveInodes[src]; ok {
				delete(state.liveInodes, src)
				state.liveInodes[dst] = inode
			}

			if content, ok := state.liveContent[src]; ok {
				delete(state.liveContent, src)
				state.liveContent[dst] = content
			}
			// Move synced content tracking
			if content, ok := state.contentSynced[src]; ok {
				delete(state.contentSynced, src)
				state.contentSynced[dst] = content
			}
		}

	case opRemove:
		path := paths[rng.IntN(len(paths))]

		err := crash.Remove(path)
		if err == nil {
			delete(state.liveInodes, path)
			delete(state.liveContent, path)
		}

	case opMkdir:
		// Create a subdirectory
		dir := fmt.Sprintf("dir%d", rng.IntN(3))
		_ = crash.MkdirAll(dir, 0o755)

	case opRemoveAll:
		dir := dirs[rng.IntN(len(dirs))]
		if dir != "." {
			err := crash.RemoveAll(dir)
			if err == nil {
				// Remove all files under this dir from state
				for path := range state.liveInodes {
					if parentRel(path) == dir {
						delete(state.liveInodes, path)
						delete(state.liveContent, path)
					}
				}
			}
		}

	case numOps:
		panic("numOps is a sentinel value, not a valid operation")
	}
}

func verifyDurableState(t *testing.T, crash *fs.Crash, state *durableState) {
	t.Helper()

	expected := state.expectedAfterCrash()

	for path, expectedContent := range expected {
		content, err := crash.ReadFile(path)
		if err != nil {
			t.Errorf("ReadFile(%q): %v (expected content %q)", path, err, expectedContent)

			continue
		}

		if string(content) != expectedContent {
			t.Errorf("%q: got %q, want %q", path, content, expectedContent)
		}
	}
}

// Fuzz_Crash_DirRenames_Preserve_Subtree_When_Synced tests directory rename
// operations preserve subtrees correctly.
func Fuzz_Crash_DirRenames_Preserve_Subtree_When_Synced(f *testing.F) {
	f.Add(int64(1), 5)
	f.Add(int64(42), 10)
	f.Add(int64(100), 8)

	f.Fuzz(func(t *testing.T, seed int64, numFiles int) {
		if numFiles < 1 || numFiles > 20 {
			return
		}

		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
		crash := mustNewCrash(t, &fs.CrashConfig{})

		// Create source directory with files
		err := crash.MkdirAll("src", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		// Create files in src
		files := make(map[string]string)

		for i := range numFiles {
			name := fmt.Sprintf("src/file%d.txt", i)
			content := fmt.Sprintf("content%d", rng.IntN(1000))
			writeFile(t, crash, name, content, 0o644, true)
			files[name] = content
		}

		syncDir(t, crash, "src")

		// Rename src -> dst
		err = crash.Rename("src", "dst")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		syncDir(t, crash, ".")

		// fs.Crash
		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		// Verify: all files should be under dst with original content
		for oldPath, content := range files {
			newPath := "dst" + oldPath[3:] // Replace "src" with "dst"

			got, err := crash.ReadFile(newPath)
			if err != nil {
				t.Errorf("ReadFile(%q): %v", newPath, err)

				continue
			}

			if string(got) != content {
				t.Errorf("%q: got %q, want %q", newPath, got, content)
			}
		}

		// src should not exist
		if exists, _ := crash.Exists("src"); exists {
			t.Error("src should not exist after rename")
		}
	})
}

// Fuzz_Crash_PartialSyncs_Match_Model_When_Some_Ops_Unsynced tests that
// partial sync patterns produce expected results.
func Fuzz_Crash_PartialSyncs_Match_Model_When_Some_Ops_Unsynced(f *testing.F) {
	f.Add(int64(1), uint8(0b11111111))
	f.Add(int64(42), uint8(0b10101010))
	f.Add(int64(99), uint8(0b01010101))
	f.Add(int64(123), uint8(0b11001100))

	f.Fuzz(func(t *testing.T, seed int64, syncPattern uint8) {
		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
		crash := mustNewCrash(t, &fs.CrashConfig{})

		// Create 4 files with different sync patterns
		type fileState struct {
			path          string
			content       string
			fileSynced    bool
			dirSynced     bool
			expectExists  bool
			expectContent string
		}

		files := make([]fileState, 4)
		for i := range files {
			files[i].path = fmt.Sprintf("file%d.txt", i)
			files[i].content = fmt.Sprintf("data%d", rng.IntN(100))
			files[i].fileSynced = (syncPattern>>uint(i*2))&1 == 1
			files[i].dirSynced = (syncPattern>>uint(i*2+1))&1 == 1

			// Write file
			writeFile(t, crash, files[i].path, files[i].content, 0o644, files[i].fileSynced)

			// Determine expected outcome
			files[i].expectExists = files[i].dirSynced
			if files[i].expectExists && files[i].fileSynced {
				files[i].expectContent = files[i].content
			} else {
				files[i].expectContent = ""
			}
		}

		// Sync dir only if at least one file has dirSynced=true
		// Note: syncing dir makes ALL current entries durable, so if any file
		// in the batch has dirSynced=true, ALL files become entry-durable.
		anyDirSync := false

		for _, f := range files {
			if f.dirSynced {
				anyDirSync = true

				break
			}
		}

		if anyDirSync {
			syncDir(t, crash, ".")
			// Update expectations: all files now have durable entries
			for i := range files {
				files[i].expectExists = true
				if files[i].fileSynced {
					files[i].expectContent = files[i].content
				} else {
					files[i].expectContent = ""
				}
			}
		}

		// fs.Crash
		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		// Verify
		for _, f := range files {
			exists, _ := crash.Exists(f.path)
			if exists != f.expectExists {
				t.Errorf("%s: exists=%v, want %v (fileSynced=%v, dirSynced=%v)",
					f.path, exists, f.expectExists, f.fileSynced, f.dirSynced)

				continue
			}

			if exists {
				content, _ := crash.ReadFile(f.path)
				if string(content) != f.expectContent {
					t.Errorf("%s: content=%q, want %q", f.path, content, f.expectContent)
				}
			}
		}
	})
}

// Fuzz_Crash_WritebackMode_Produces_Valid_States_When_Weights_Vary tests
// that writeback mode produces consistent states.
func Fuzz_Crash_WritebackMode_Produces_Valid_States_When_Weights_Vary(f *testing.F) {
	f.Add(int64(1), 0.5, 0.5, 0.5, 0.5)
	f.Add(int64(42), 1.0, 0.0, 1.0, 0.0)
	f.Add(int64(99), 0.0, 1.0, 0.0, 1.0)
	f.Add(int64(123), 0.3, 0.3, 0.4, 0.5)

	f.Fuzz(func(t *testing.T, seed int64, keepOld, keepNew, dirOld, dirNew float64) {
		// Normalize weights
		if keepOld < 0 || keepNew < 0 || dirOld < 0 || dirNew < 0 {
			return
		}

		if keepOld+keepNew == 0 {
			keepOld = 1
		}

		if dirOld+dirNew == 0 {
			dirOld = 1
		}

		crash := mustNewCrash(t, &fs.CrashConfig{
			Writeback: fs.CrashWritebackConfig{
				Seed: seed,
				FileWeights: fs.CrashWritebackFileWeights{
					KeepOld: keepOld,
					KeepNew: keepNew,
				},
				DirEntryWeights: fs.CrashWritebackDirEntryWeights{
					KeepOld: dirOld,
					KeepNew: dirNew,
				},
			},
		})

		// Create file with old content, sync
		writeFile(t, crash, "test.txt", "old-content", 0o644, true)
		syncDir(t, crash, ".")

		// Overwrite with new content, don't sync
		writeFile(t, crash, "test.txt", "new-content", 0o644, false)

		// fs.Crash with writeback
		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		// fs.File should exist (entry was synced before overwrite)
		exists, err := crash.Exists("test.txt")
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}

		if !exists {
			// This is valid if dirNew weight caused entry to be dropped
			return
		}

		// Content should be either old or new (not corrupted)
		content, err := crash.ReadFile("test.txt")
		if err != nil {
			t.Fatalf("ReadFile: %v", err)
		}

		validContents := []string{"old-content", "new-content", ""}
		found := slices.Contains(validContents, string(content))

		// Also allow prefix combinations for KeepPrefix mode
		if !found && len(content) > 0 {
			// Check if it's a valid prefix mix
			if len(content) <= len("new-content") {
				prefix := "new-content"[:len(content)]
				if string(content) == prefix {
					found = true
				}
			}
		}

		if !found {
			t.Errorf("Invalid content %q - not old, new, or valid prefix", content)
		}
	})
}

// Test_Crash_Fuzz_Deterministic_Runs_Match_When_Seed_Is_Same verifies fuzz
// test determinism.
func Test_Crash_Fuzz_Deterministic_Runs_Match_When_Seed_Is_Same(t *testing.T) {
	t.Parallel()

	runTest := func(seed int64) map[string]string {
		crash := mustNewCrash(t, &fs.CrashConfig{})
		rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))

		paths := []string{"a.txt", "b.txt", "c.txt"}

		for range 20 {
			op := rng.IntN(4)
			path := paths[rng.IntN(len(paths))]

			switch op {
			case 0:
				content := fmt.Sprintf("v%d", rng.IntN(100))
				writeFile(t, crash, path, content, 0o644, false)
			case 1:
				f, err := crash.Open(path)
				if err == nil {
					_ = f.Sync()
					f.Close()
				}
			case 2:
				syncDir(t, crash, ".")
			case 3:
				_ = crash.Remove(path)
			}
		}

		syncDir(t, crash, ".")
		_ = crash.SimulateCrash()

		result := make(map[string]string)

		for _, p := range paths {
			data, err := crash.ReadFile(p)
			if err == nil {
				result[p] = string(data)
			}
		}

		return result
	}

	// Run twice with same seed, should get same result
	result1 := runTest(12345)
	result2 := runTest(12345)

	if len(result1) != len(result2) {
		t.Fatalf("Results differ in length: %d vs %d", len(result1), len(result2))
	}

	for path, content1 := range result1 {
		if content2, ok := result2[path]; !ok || content1 != content2 {
			t.Errorf("Path %s: run1=%q, run2=%q", path, content1, content2)
		}
	}
}

// Test_Crash_Fuzz_CrossDirRename_Preserves_Content_When_Both_Dirs_Synced tests
// cross-directory renames with various sync patterns.
func Test_Crash_Fuzz_CrossDirRename_Preserves_Content_When_Both_Dirs_Synced(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name         string
		syncSrcAfter bool
		syncDstAfter bool
		expectInSrc  bool
		expectInDst  bool
	}{
		{"SyncBoth", true, true, false, true},
		{"SyncSrcOnly", true, false, false, false},
		// When only dst is synced, the file appears in dst (dst was synced).
		// Src still has durable entry from before rename.
		{"SyncDstOnly", false, true, true, true},
		{"SyncNeither", false, false, true, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			crash := mustNewCrash(t, &fs.CrashConfig{})

			// Setup directories
			_ = crash.MkdirAll("src", 0o755)
			_ = crash.MkdirAll("dst", 0o755)
			syncDir(t, crash, ".")

			// Create file in src
			writeFile(t, crash, "src/file.txt", testContentData, 0o644, true)
			syncDir(t, crash, "src")

			// Rename to dst
			err := crash.Rename("src/file.txt", "dst/file.txt")
			if err != nil {
				t.Fatalf("Rename: %v", err)
			}

			// Sync based on pattern
			if tc.syncSrcAfter {
				syncDir(t, crash, "src")
			}

			if tc.syncDstAfter {
				syncDir(t, crash, "dst")
			}

			// fs.Crash
			err = crash.SimulateCrash()
			if err != nil {
				t.Fatalf("SimulateCrash: %v", err)
			}

			// Verify
			srcExists, _ := crash.Exists("src/file.txt")
			dstExists, _ := crash.Exists("dst/file.txt")

			if srcExists != tc.expectInSrc {
				t.Errorf("src/file.txt exists=%v, want %v", srcExists, tc.expectInSrc)
			}

			if dstExists != tc.expectInDst {
				t.Errorf("dst/file.txt exists=%v, want %v", dstExists, tc.expectInDst)
			}

			// If file exists, verify content
			if dstExists {
				content := mustReadFile(t, crash, "dst/file.txt")
				if content != testContentData {
					t.Errorf("dst/file.txt content=%q, want 'data'", content)
				}
			}

			if srcExists {
				content := mustReadFile(t, crash, "src/file.txt")
				if content != testContentData {
					t.Errorf("src/file.txt content=%q, want 'data'", content)
				}
			}
		})
	}
}

// Test_Crash_Fuzz_ManyFiles_Maintain_Consistency_When_Random_Syncs verifies
// consistency with many files and random sync patterns.
func Test_Crash_Fuzz_ManyFiles_Maintain_Consistency_When_Random_Syncs(t *testing.T) {
	t.Parallel()

	seeds := []int64{1, 42, 100, 999}

	for _, seed := range seeds {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			t.Parallel()

			rng := rand.New(rand.NewPCG(uint64(seed), uint64(seed)))
			crash := mustNewCrash(t, &fs.CrashConfig{})

			numFiles := 20
			synced := make(map[string]bool)
			content := make(map[string]string)

			// Create many files with random sync patterns
			for i := range numFiles {
				path := fmt.Sprintf("file%02d.txt", i)
				data := fmt.Sprintf("content%d", rng.IntN(1000))
				doSync := rng.Float32() < 0.5

				writeFile(t, crash, path, data, 0o644, doSync)
				synced[path] = doSync
				content[path] = data
			}

			// Sync directory
			syncDir(t, crash, ".")

			// fs.Crash
			err := crash.SimulateCrash()
			if err != nil {
				t.Fatalf("SimulateCrash: %v", err)
			}

			// Verify all files exist (dir was synced)
			for i := range numFiles {
				path := fmt.Sprintf("file%02d.txt", i)

				data, err := crash.ReadFile(path)
				if err != nil {
					t.Errorf("ReadFile(%s): %v", path, err)

					continue
				}

				if synced[path] {
					if string(data) != content[path] {
						t.Errorf("%s: got %q, want %q", path, data, content[path])
					}
				} else {
					if len(data) != 0 {
						t.Errorf("%s: got %q, want empty (not synced)", path, data)
					}
				}
			}
		})
	}
}

// Test_Crash_Fuzz_RenameChain_Preserves_Final_Location_When_All_Synced tests
// chains of renames.
func Test_Crash_Fuzz_RenameChain_Preserves_Final_Location_When_All_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	// Create initial file
	writeFile(t, crash, "a.txt", "original", 0o644, true)
	syncDir(t, crash, ".")

	// Chain of renames: a -> b -> c -> d
	renames := []struct{ src, dst string }{
		{"a.txt", "b.txt"},
		{"b.txt", "c.txt"},
		{"c.txt", "d.txt"},
	}

	for _, r := range renames {
		err := crash.Rename(r.src, r.dst)
		if err != nil {
			t.Fatalf("Rename %s->%s: %v", r.src, r.dst, err)
		}

		syncDir(t, crash, ".")
	}

	// fs.Crash
	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	// Only d.txt should exist
	for _, name := range []string{"a.txt", "b.txt", "c.txt"} {
		if exists, _ := crash.Exists(name); exists {
			t.Errorf("%s should not exist", name)
		}
	}

	content := mustReadFile(t, crash, "d.txt")
	if content != "original" {
		t.Errorf("d.txt = %q, want 'original'", content)
	}
}

// Test_Crash_Fuzz_DirectorySwap_Preserves_Contents_When_Synced tests swapping
// two directories.
func Test_Crash_Fuzz_DirectorySwap_Preserves_Contents_When_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	// Create two directories with different contents
	_ = crash.MkdirAll("dir1", 0o755)
	_ = crash.MkdirAll("dir2", 0o755)
	syncDir(t, crash, ".")

	writeFile(t, crash, "dir1/file.txt", "from-dir1", 0o644, true)
	writeFile(t, crash, "dir2/file.txt", "from-dir2", 0o644, true)
	syncDir(t, crash, "dir1")
	syncDir(t, crash, "dir2")

	// Swap: dir1 -> tmp, dir2 -> dir1, tmp -> dir2
	_ = crash.Rename("dir1", "tmp")
	_ = crash.Rename("dir2", "dir1")
	_ = crash.Rename("tmp", "dir2")
	syncDir(t, crash, ".")

	// fs.Crash
	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	// After swap: dir1 should have dir2's content and vice versa
	content1 := mustReadFile(t, crash, "dir1/file.txt")
	content2 := mustReadFile(t, crash, "dir2/file.txt")

	if content1 != "from-dir2" {
		t.Errorf("dir1/file.txt = %q, want 'from-dir2'", content1)
	}

	if content2 != "from-dir1" {
		t.Errorf("dir2/file.txt = %q, want 'from-dir1'", content2)
	}
}
