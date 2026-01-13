package fs_test

// =============================================================================
// fs.Crash fs.FS Tests
//
// These tests are intentionally written as behavioural tests against the
// exported filesystem surface ([fs.FS]/[fs.File] plus [fs.NewCrash]/[fs.Crash]/[Recover]).
// Even though they live in package fs, they should not reach into fs.Crash
// internals or assert on implementation details.
//
// What to assert:
//   - Prefer assertions on observable state after a simulated restart.
//     Use [fs.Crash.SimulateCrash] and then verify via fs.FS methods like
//     Stat/Exists/ReadFile/ReadDir.
//   - Make durability boundaries explicit. In this model:
//       * file contents become durable only after fs.File.Sync() on that handle
//       * directory entry updates become durable only after Sync() on an open
//         directory handle for the containing directory
//   - Helpers like writeFile(...) and syncDir(...) exist to make those
//     boundaries obvious in each test.
//
// Failpoints:
//   - Use failpoints only to validate injection, latching, and filtering.
//   - Treat the injected panic as a crash boundary (execution stops). Only
//     recognize it via errors.As(err, *fs.CrashPanicError), then call Recover()
//     and assert the post-crash filesystem state.
// =============================================================================

import (
	"errors"
	"io"
	"os"
	"reflect"
	"sort"
	"syscall"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

func Test_Crash_FileHandle_Read_Seek_Stat_And_Fd_Work(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	f, err := crash.OpenFile("file.txt", os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o666)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	defer func() {
		_ = f.Close()
	}()

	_, err = f.Write([]byte(testContentHello))
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		t.Fatalf("Seek: %v", err)
	}

	buf := make([]byte, 5)

	n, err := f.Read(buf)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if n != len(buf) {
		t.Fatalf("Read n=%d, want %d", n, len(buf))
	}

	if got, want := string(buf), testContentHello; got != want {
		t.Fatalf("Read=%q, want %q", got, want)
	}

	info, err := f.Stat()
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if info.Size() != 5 {
		t.Fatalf("Stat.Size()=%d, want %d", info.Size(), 5)
	}

	fd := f.Fd()

	var st syscall.Stat_t

	err = syscall.Fstat(int(fd), &st)
	if err != nil {
		t.Fatalf("syscall.Fstat(fd=%d): %v", int(fd), err)
	}
}

func Test_Crash_WriteFile_Writes_Live_Data_But_Not_Durable_Without_File_Sync(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.WriteFile("wf.txt", []byte(testContentHello), 0o644)
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if got, want := mustReadFile(t, crash, "wf.txt"), testContentHello; got != want {
		t.Fatalf("ReadFile(\"wf.txt\")=%q, want %q", got, want)
	}

	// Make the directory entry durable but not the file data.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "wf.txt"), ""; got != want {
		t.Fatalf("ReadFile(\"wf.txt\")=%q, want empty (no fs.File.Sync durability)", got)
	}

	info, err := crash.Stat("wf.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if !info.Mode().IsRegular() {
		t.Fatal("Stat(\"wf.txt\").Mode().IsRegular()=false, want true")
	}
}

func Test_Crash_Create_Is_Durable_When_File_And_Dir_Are_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	f, err := crash.Create("created.txt")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	_, err = f.Write([]byte(testContentHello))
	if err != nil {
		_ = f.Close()

		t.Fatalf("Write: %v", err)
	}

	err = f.Sync()
	if err != nil {
		_ = f.Close()

		t.Fatalf("Sync: %v", err)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "created.txt"), testContentHello; got != want {
		t.Fatalf("ReadFile(\"created.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Restores_File_After_Crash_When_Sync_Combinations_Vary(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		syncFile  bool
		syncDir   bool
		wantExist bool
		want      string
	}{
		{name: "NoSync", syncFile: false, syncDir: false, wantExist: false},
		{name: "FileSyncOnly", syncFile: true, syncDir: false, wantExist: false},
		{name: "DirSyncOnly", syncFile: false, syncDir: true, wantExist: true, want: ""},
		{name: "FileAndDirSync", syncFile: true, syncDir: true, wantExist: true, want: testContentHello},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			crash := mustNewCrash(t, &fs.CrashConfig{})

			writeFile(t, crash, "a.txt", testContentHello, 0o600, tc.syncFile)

			if tc.syncDir {
				syncDir(t, crash, ".")
			}

			err := crash.SimulateCrash()
			if err != nil {
				t.Fatalf("fs.Crash: %v", err)
			}

			if tc.wantExist {
				got := mustReadFile(t, crash, "a.txt")
				if got != tc.want {
					t.Fatalf("ReadFile(\"a.txt\")=%q, want %q", got, tc.want)
				}

				return
			}

			requireNotExists(t, crash, "a.txt")
		})
	}
}

func Test_Crash_Reverts_File_Content_When_Overwrite_Is_Not_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	writeFile(t, crash, "a.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	writeFile(t, crash, "a.txt", testContentNew, 0o644, false)

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "a.txt"), testContentOld; got != want {
		t.Fatalf("ReadFile(\"a.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Handles_Rename_When_Dir_Sync_Is_Missing_Or_Present(t *testing.T) {
	t.Parallel()

	t.Run("NotDurableWithoutDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "tmp", testContentHello, 0o644, true)
		syncDir(t, crash, ".")

		err := crash.Rename("tmp", "final")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("fs.Crash: %v", err)
		}

		requireNotExists(t, crash, "final")

		if got, want := mustReadFile(t, crash, "tmp"), testContentHello; got != want {
			t.Fatalf("ReadFile(\"tmp\")=%q, want %q", got, want)
		}
	})

	t.Run("DurableWithDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "tmp", testContentHello, 0o644, true)
		syncDir(t, crash, ".")

		err := crash.Rename("tmp", "final")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("fs.Crash: %v", err)
		}

		requireNotExists(t, crash, "tmp")

		if got, want := mustReadFile(t, crash, "final"), testContentHello; got != want {
			t.Fatalf("ReadFile(\"final\")=%q, want %q", got, want)
		}
	})
}

func Test_Crash_Rename_Across_Directories_Durability_Depends_On_Syncing_Both_Dirs(t *testing.T) {
	t.Parallel()

	t.Run("NoDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("a", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err = crash.MkdirAll("b", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		writeFile(t, crash, "a/file", testContentData, 0o644, true)
		syncDir(t, crash, "a")

		err = crash.Rename("a/file", "b/file")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, "b/file")

		if got, want := mustReadFile(t, crash, "a/file"), testContentData; got != want {
			t.Fatalf("ReadFile(\"a/file\")=%q, want %q", got, want)
		}
	})

	t.Run("SyncDestOnly", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("a", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err = crash.MkdirAll("b", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		writeFile(t, crash, "a/file", testContentData, 0o644, true)
		syncDir(t, crash, "a")

		err = crash.Rename("a/file", "b/file")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		syncDir(t, crash, "b")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		if got, want := mustReadFile(t, crash, "a/file"), testContentData; got != want {
			t.Fatalf("ReadFile(\"a/file\")=%q, want %q", got, want)
		}

		if got, want := mustReadFile(t, crash, "b/file"), testContentData; got != want {
			t.Fatalf("ReadFile(\"b/file\")=%q, want %q", got, want)
		}
	})

	t.Run("SyncSrcOnly", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("a", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err = crash.MkdirAll("b", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		writeFile(t, crash, "a/file", testContentData, 0o644, true)
		syncDir(t, crash, "a")

		err = crash.Rename("a/file", "b/file")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		syncDir(t, crash, "a")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, "a/file")
		requireNotExists(t, crash, "b/file")
	})

	t.Run("SyncBoth", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("a", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err = crash.MkdirAll("b", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		writeFile(t, crash, "a/file", testContentData, 0o644, true)
		syncDir(t, crash, "a")

		err = crash.Rename("a/file", "b/file")
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		syncDir(t, crash, "a")
		syncDir(t, crash, "b")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, "a/file")

		if got, want := mustReadFile(t, crash, "b/file"), testContentData; got != want {
			t.Fatalf("ReadFile(\"b/file\")=%q, want %q", got, want)
		}
	})
}

func Test_Crash_DirSync_Does_Not_Prune_Inode_Snapshots_Still_Referenced_By_Other_Durable_Names(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	writeFile(t, crash, "a/file", testContentData, 0o644, true)
	syncDir(t, crash, "a")

	err = crash.Rename("a/file", "b/file")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the destination name durable while the source name remains durable.
	syncDir(t, crash, "b")

	err = crash.Remove("b/file")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Intentionally do not sync "b"; the deletion is not durable.

	// Making the source removal durable must not prune the inode snapshot because
	// "b/file" still references it durably.
	syncDir(t, crash, "a")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "a/file")

	if got, want := mustReadFile(t, crash, "b/file"), testContentData; got != want {
		t.Fatalf("ReadFile(\"b/file\")=%q, want %q", got, want)
	}
}

func Test_Crash_FileSync_Does_Not_Snapshot_Wrong_File_After_Rename(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	f, err := crash.OpenFile("a.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	_, err = f.Write([]byte(testContentOld))
	if err != nil {
		_ = f.Close()

		t.Fatalf("Write: %v", err)
	}

	err = f.Sync()
	if err != nil {
		_ = f.Close()

		t.Fatalf("Sync: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.Rename("a.txt", "b.txt")
	if err != nil {
		_ = f.Close()

		t.Fatalf("Rename: %v", err)
	}

	// Reuse the old name for a different (unsynced) file.
	writeFile(t, crash, "a.txt", testContentNew, 0o644, false)

	// The original handle must still snapshot the original inode.
	err = f.Sync()
	if err != nil {
		_ = f.Close()

		t.Fatalf("Sync after rename: %v", err)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "a.txt"), testContentOld; got != want {
		t.Fatalf("ReadFile(\"a.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_FileSync_Does_Not_Snapshot_Wrong_File_When_Rename_Replaces_An_Existing_Path(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	writeFile(t, crash, "dst.txt", "dst-old", 0o600, true)
	syncDir(t, crash, ".")

	dst, err := crash.Open("dst.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// Create a replacement file under a different name.
	writeFile(t, crash, "tmp.txt", "tmp-new", 0o600, false)

	err = crash.Rename("tmp.txt", "dst.txt")
	if err != nil {
		_ = dst.Close()

		t.Fatalf("Rename: %v", err)
	}

	// Syncing the original handle must not snapshot the replacement file.
	err = dst.Sync()
	if err != nil {
		_ = dst.Close()

		t.Fatalf("Sync: %v", err)
	}

	err = dst.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "dst.txt"), "dst-old"; got != want {
		t.Fatalf("ReadFile(\"dst.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_FileSync_Captures_Unlinked_File_Data_When_Dir_Entry_Is_Not_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	f, err := crash.OpenFile("keep.txt", os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	_, err = f.Write([]byte(testContentOld))
	if err != nil {
		_ = f.Close()

		t.Fatalf("Write: %v", err)
	}

	err = f.Sync()
	if err != nil {
		_ = f.Close()

		t.Fatalf("Sync: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.Remove("keep.txt")
	if err != nil {
		_ = f.Close()

		t.Fatalf("Remove: %v", err)
	}

	exists, err := crash.Exists("keep.txt")
	if err != nil {
		_ = f.Close()

		t.Fatalf("Exists: %v", err)
	}

	if exists {
		_ = f.Close()

		t.Fatal("Exists(\"keep.txt\")=true, want false")
	}

	_, err = f.Seek(0, io.SeekStart)
	if err != nil {
		_ = f.Close()

		t.Fatalf("Seek: %v", err)
	}

	_, err = f.Write([]byte(testContentNew))
	if err != nil {
		_ = f.Close()

		t.Fatalf("Write: %v", err)
	}

	err = f.Sync()
	if err != nil {
		_ = f.Close()

		t.Fatalf("Sync after unlink: %v", err)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "keep.txt"), testContentNew; got != want {
		t.Fatalf("ReadFile(\"keep.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_DirHandleSync_Does_Not_Snapshot_Wrong_Directory_After_Rename(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entry itself durable.
	syncDir(t, crash, ".")

	d, err := crash.Open("dir")
	if err != nil {
		t.Fatalf("Open(\"dir\"): %v", err)
	}

	writeFile(t, crash, "dir/file.txt", testContentData, 0o644, true)

	// Rename the directory, then reuse the old name for a different directory.
	err = crash.Rename("dir", "other")
	if err != nil {
		_ = d.Close()

		t.Fatalf("Rename: %v", err)
	}

	err = crash.MkdirAll("dir", 0o755)
	if err != nil {
		_ = d.Close()

		t.Fatalf("MkdirAll: %v", err)
	}

	// Syncing the original handle must snapshot the original directory inode.
	err = d.Sync()
	if err != nil {
		_ = d.Close()

		t.Fatalf("Sync(\"dir\"): %v", err)
	}

	err = d.Close()
	if err != nil {
		t.Fatalf("Close(\"dir\"): %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "dir/file.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"dir/file.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_DirHandleSync_Tracks_Durable_Name_After_Rename_When_Parent_Is_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	d, err := crash.Open("dir")
	if err != nil {
		t.Fatalf("Open(\"dir\"): %v", err)
	}

	err = crash.Rename("dir", "other")
	if err != nil {
		_ = d.Close()

		t.Fatalf("Rename: %v", err)
	}

	syncDir(t, crash, ".")

	writeFile(t, crash, "other/file.txt", testContentData, 0o644, true)

	err = d.Sync()
	if err != nil {
		_ = d.Close()

		t.Fatalf("Sync(\"dir\"): %v", err)
	}

	err = d.Close()
	if err != nil {
		t.Fatalf("Close(\"dir\"): %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "dir")

	if got, want := mustReadFile(t, crash, "other/file.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"other/file.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_DirHandleSync_Does_Not_Snapshot_Moved_Directory_Under_Stale_Name_When_Destination_Not_Durable(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.MkdirAll("a/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entry durable in the source parent.
	syncDir(t, crash, "a")

	d, err := crash.Open("a/dir")
	if err != nil {
		t.Fatalf("Open(\"a/dir\"): %v", err)
	}

	err = crash.Rename("a/dir", "b/dir")
	if err != nil {
		_ = d.Close()

		t.Fatalf("Rename: %v", err)
	}
	// Make removal durable in the source parent; destination is still not durable.
	syncDir(t, crash, "a")

	writeFile(t, crash, "b/dir/new.txt", testContentData, 0o644, true)

	err = d.Sync()
	if err != nil {
		_ = d.Close()

		t.Fatalf("Sync(\"a/dir\"): %v", err)
	}

	err = d.Close()
	if err != nil {
		t.Fatalf("Close(\"a/dir\"): %v", err)
	}

	// Reuse the old source name and make it durable.
	err = crash.MkdirAll("a/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, "a")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "b/dir")

	info, err := crash.Stat("a/dir")
	if err != nil {
		t.Fatalf("Stat(\"a/dir\"): %v", err)
	}

	if !info.IsDir() {
		t.Fatal("Stat(\"a/dir\").IsDir()=false, want true")
	}

	requireNotExists(t, crash, "a/dir/new.txt")
}

func Test_Crash_Rename_Directory_Across_Directories_Preserves_Subtree_When_Both_Dirs_Synced(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		syncOrder []string
	}{
		{name: "SyncSrcThenDest", syncOrder: []string{"a", "b"}},
		{name: "SyncDestThenSrc", syncOrder: []string{"b", "a"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			crash := mustNewCrash(t, &fs.CrashConfig{})

			err := crash.MkdirAll("a", 0o755)
			if err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			err = crash.MkdirAll("b", 0o755)
			if err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			syncDir(t, crash, ".")

			err = crash.MkdirAll("a/dir", 0o755)
			if err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			syncDir(t, crash, "a")

			writeFile(t, crash, "a/dir/file.txt", testContentData, 0o644, true)
			syncDir(t, crash, "a/dir")

			err = crash.Rename("a/dir", "b/dir")
			if err != nil {
				t.Fatalf("Rename: %v", err)
			}

			for _, dir := range tc.syncOrder {
				syncDir(t, crash, dir)
			}

			err = crash.SimulateCrash()
			if err != nil {
				t.Fatalf("SimulateCrash: %v", err)
			}

			requireNotExists(t, crash, "a/dir")

			if got, want := mustReadFile(t, crash, "b/dir/file.txt"), testContentData; got != want {
				t.Fatalf("ReadFile(\"b/dir/file.txt\")=%q, want %q", got, want)
			}
		})
	}
}

func Test_Crash_Rename_Directory_Across_Directories_Does_Not_Confuse_Reused_Source_Name(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.MkdirAll("a/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, "a")

	writeFile(t, crash, "a/dir/file.txt", testContentData, 0o644, true)
	syncDir(t, crash, "a/dir")

	err = crash.Rename("a/dir", "b/dir")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Make removal durable in the source parent.
	syncDir(t, crash, "a")

	// Recreate the source name with a different directory.
	err = crash.MkdirAll("a/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, "a")

	// Make addition durable in the destination parent.
	syncDir(t, crash, "b")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	info, err := crash.Stat("a/dir")
	if err != nil {
		t.Fatalf("Stat(\"a/dir\"): %v", err)
	}

	if !info.IsDir() {
		t.Fatal("Stat(\"a/dir\").IsDir()=false, want true")
	}

	requireNotExists(t, crash, "a/dir/file.txt")

	if got, want := mustReadFile(t, crash, "b/dir/file.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"b/dir/file.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Rename_Directory_Across_Directories_Does_Not_Overwrite_Durable_Updates_Made_After_Move(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.MkdirAll("a/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, "a")

	writeFile(t, crash, "a/dir/data.txt", "before", 0o644, true)
	syncDir(t, crash, "a/dir")

	err = crash.Rename("a/dir", "b/dir")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make removal durable in the source parent.
	syncDir(t, crash, "a")

	// Replace the file in the moved directory with a different inode.
	writeFile(t, crash, "b/dir/tmp.txt", "after", 0o644, true)

	err = crash.Rename("b/dir/tmp.txt", "b/dir/data.txt")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the replace-rename durable within the moved directory.
	syncDir(t, crash, "b/dir")

	// Make addition durable in the destination parent.
	syncDir(t, crash, "b")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "b/dir/data.txt"), "after"; got != want {
		t.Fatalf("ReadFile(\"b/dir/data.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "b/dir/tmp.txt")
}

func Test_Crash_Rename_Directory_Across_Directories_Does_Not_Resurrect_Stale_Subtree_Under_Replaced_Subdir_When_Reattached(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.MkdirAll("a/dir/sub", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, "a")
	syncDir(t, crash, "a/dir")

	writeFile(t, crash, "a/dir/sub/old.txt", testContentOld, 0o644, true)
	syncDir(t, crash, "a/dir/sub")

	// Remove the file live without syncing the directory so it remains durable in
	// the snapshot even though the live directory becomes empty.
	err = crash.Remove("a/dir/sub/old.txt")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	requireNotExists(t, crash, "a/dir/sub/old.txt")

	err = crash.Rename("a/dir", "b/dir")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the source removal durable; the moved subtree is now orphaned.
	syncDir(t, crash, "a")

	// Replace the subdirectory with a different inode and sync the moved directory
	// itself (but not the destination parent).
	err = crash.MkdirAll("b/dir/sub-new", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.Remove("b/dir/sub")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	err = crash.Rename("b/dir/sub-new", "b/dir/sub")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	syncDir(t, crash, "b/dir")

	writeFile(t, crash, "b/dir/sub/new.txt", testContentNew, 0o644, true)
	syncDir(t, crash, "b/dir/sub")

	// Make the destination entry durable; this reattaches the orphaned subtree.
	syncDir(t, crash, "b")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "a/dir")

	if got, want := mustReadFile(t, crash, "b/dir/sub/new.txt"), testContentNew; got != want {
		t.Fatalf("ReadFile(\"b/dir/sub/new.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "b/dir/sub/old.txt")
}

func Test_Crash_Rename_Directory_Across_Directories_Durability_Depends_On_Syncing_Both_Dirs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		syncA bool
		syncB bool

		wantA bool
		wantB bool
	}{
		{name: "NoDirSync", syncA: false, syncB: false, wantA: true, wantB: false},
		{name: "SyncDestOnly", syncA: false, syncB: true, wantA: true, wantB: true},
		{name: "SyncSrcOnly", syncA: true, syncB: false, wantA: false, wantB: false},
		{name: "SyncBoth", syncA: true, syncB: true, wantA: false, wantB: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			crash := mustNewCrash(t, &fs.CrashConfig{})

			err := crash.MkdirAll("a", 0o755)
			if err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			err = crash.MkdirAll("b", 0o755)
			if err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			syncDir(t, crash, ".")

			err = crash.MkdirAll("a/dir", 0o755)
			if err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}

			syncDir(t, crash, "a")

			writeFile(t, crash, "a/dir/file.txt", testContentData, 0o644, true)
			syncDir(t, crash, "a/dir")

			err = crash.Rename("a/dir", "b/dir")
			if err != nil {
				t.Fatalf("Rename: %v", err)
			}

			if tc.syncA {
				syncDir(t, crash, "a")
			}

			if tc.syncB {
				syncDir(t, crash, "b")
			}

			err = crash.SimulateCrash()
			if err != nil {
				t.Fatalf("SimulateCrash: %v", err)
			}

			if tc.wantA {
				if got, want := mustReadFile(t, crash, "a/dir/file.txt"), testContentData; got != want {
					t.Fatalf("ReadFile(\"a/dir/file.txt\")=%q, want %q", got, want)
				}
			} else {
				requireNotExists(t, crash, "a/dir")
			}

			if tc.wantB {
				if got, want := mustReadFile(t, crash, "b/dir/file.txt"), testContentData; got != want {
					t.Fatalf("ReadFile(\"b/dir/file.txt\")=%q, want %q", got, want)
				}
			} else {
				requireNotExists(t, crash, "b/dir")
			}
		})
	}
}

func Test_Crash_DirSync_Propagates_To_All_Durable_Names_For_A_Directory_Inode(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.MkdirAll("a/dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	syncDir(t, crash, "a")

	writeFile(t, crash, "a/dir/base.txt", "base", 0o644, true)
	syncDir(t, crash, "a/dir")

	err = crash.Rename("a/dir", "b/dir")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the destination entry durable while the source entry remains durable.
	syncDir(t, crash, "b")

	writeFile(t, crash, "b/dir/new.txt", testContentNew, 0o644, true)
	syncDir(t, crash, "b/dir")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "a/dir/new.txt"), testContentNew; got != want {
		t.Fatalf("ReadFile(\"a/dir/new.txt\")=%q, want %q", got, want)
	}

	if got, want := mustReadFile(t, crash, "b/dir/new.txt"), testContentNew; got != want {
		t.Fatalf("ReadFile(\"b/dir/new.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Handles_Remove_When_Dir_Sync_Is_Missing_Or_Present(t *testing.T) {
	t.Parallel()

	t.Run("NotDurableWithoutDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "a.txt", testContentHello, 0o644, true)
		syncDir(t, crash, ".")

		err := crash.Remove("a.txt")
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("fs.Crash: %v", err)
		}

		if got, want := mustReadFile(t, crash, "a.txt"), testContentHello; got != want {
			t.Fatalf("ReadFile(\"a.txt\")=%q, want %q", got, want)
		}
	})

	t.Run("DurableWithDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "a.txt", testContentHello, 0o644, true)
		syncDir(t, crash, ".")

		err := crash.Remove("a.txt")
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("fs.Crash: %v", err)
		}

		requireNotExists(t, crash, "a.txt")
	})
}

func Test_Crash_Preserves_Durable_Data_When_Crashed_Twice(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	writeFile(t, crash, "a.txt", "durable", 0o644, true)
	syncDir(t, crash, ".")

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash(1): %v", err)
	}

	// Dir sync after restart must not "forget" the durable file's contents.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash(2): %v", err)
	}

	if got, want := mustReadFile(t, crash, "a.txt"), "durable"; got != want {
		t.Fatalf("ReadFile(\"a.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Does_Not_Restore_Dir_Contents_When_Ancestor_Dir_Is_Not_Durable(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("parent/child", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "parent/child/file.txt", testContentData, 0o644, true)
	syncDir(t, crash, "parent/child")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "parent")
	requireNotExists(t, crash, "parent/child/file.txt")
}

func Test_Crash_DirSync_Does_Not_Make_Directory_Entry_Durable_In_Parent(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	d, err := crash.Open("dir")
	if err != nil {
		t.Fatalf("Open(\"dir\"): %v", err)
	}

	syncErr := d.Sync()

	closeErr := d.Close()
	if closeErr != nil {
		t.Fatalf("Close(\"dir\"): %v", closeErr)
	}

	if errors.Is(syncErr, syscall.EINVAL) {
		t.Skip("directory fsync unsupported")
	}

	if syncErr != nil {
		t.Fatalf("Sync(\"dir\"): %v", syncErr)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "dir")
}

func Test_Crash_MkdirAll_Durability_Depends_On_Parent_Dir_Sync(t *testing.T) {
	t.Parallel()

	t.Run("NotDurableWithoutParentSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("dir", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, "dir")
	})

	t.Run("DurableWithParentSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("dir", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		info, err := crash.Stat("dir")
		if err != nil {
			t.Fatalf("Stat(\"dir\"): %v", err)
		}

		if !info.IsDir() {
			t.Fatal("Stat(\"dir\").IsDir()=false, want true")
		}
	})

	t.Run("NestedDirRequiresSyncOfEachParent", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("parent/child", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		// Only sync the root. This makes "parent" durable, but not "parent/child".
		syncDir(t, crash, ".")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		exists, err := crash.Exists("parent")
		if err != nil {
			t.Fatalf("Exists(\"parent\"): %v", err)
		}

		if !exists {
			t.Fatal("Exists(\"parent\")=false, want true")
		}

		requireNotExists(t, crash, "parent/child")
	})

	t.Run("NestedDirDurableAfterSyncingParent", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("parent/child", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")
		syncDir(t, crash, "parent")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		info, err := crash.Stat("parent/child")
		if err != nil {
			t.Fatalf("Stat(\"parent/child\"): %v", err)
		}

		if !info.IsDir() {
			t.Fatal("Stat(\"parent/child\").IsDir()=false, want true")
		}
	})
}

func Test_Crash_Rename_Directory_Durability_Depends_On_Parent_Dir_Sync(t *testing.T) {
	t.Parallel()

	t.Run("NotDurableWithoutDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll(testContentOld, 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.Rename(testContentOld, testContentNew)
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, testContentNew)

		info, err := crash.Stat(testContentOld)
		if err != nil {
			t.Fatalf("Stat(\"old\"): %v", err)
		}

		if !info.IsDir() {
			t.Fatal("Stat(\"old\").IsDir()=false, want true")
		}
	})

	t.Run("DurableWithDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll(testContentOld, 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.Rename(testContentOld, testContentNew)
		if err != nil {
			t.Fatalf("Rename: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, testContentOld)

		info, err := crash.Stat(testContentNew)
		if err != nil {
			t.Fatalf("Stat(\"new\"): %v", err)
		}

		if !info.IsDir() {
			t.Fatal("Stat(\"new\").IsDir()=false, want true")
		}
	})
}

func Test_Crash_Rename_Directory_Preserves_Durable_Subtree_When_Parent_Is_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entry durable.
	syncDir(t, crash, ".")

	writeFile(t, crash, "old/file.txt", testContentData, 0o644, true)
	// Make the file name durable in the directory.
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the rename durable in the parent.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, testContentOld)

	if got, want := mustReadFile(t, crash, "new/file.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"new/file.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Rename_Directory_Swap_Preserves_Durable_Subtrees_When_Parent_Is_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entries durable.
	syncDir(t, crash, ".")

	writeFile(t, crash, "a/a.txt", "A", 0o644, true)
	syncDir(t, crash, "a")

	writeFile(t, crash, "b/b.txt", "B", 0o644, true)
	syncDir(t, crash, "b")

	err = crash.Rename("a", "tmp")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.Rename("b", "a")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	err = crash.Rename("tmp", "b")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the swap durable.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "a/b.txt"), "B"; got != want {
		t.Fatalf("ReadFile(\"a/b.txt\")=%q, want %q", got, want)
	}

	if got, want := mustReadFile(t, crash, "b/a.txt"), "A"; got != want {
		t.Fatalf("ReadFile(\"b/a.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "a/a.txt")
	requireNotExists(t, crash, "b/b.txt")
}

func Test_Crash_Rename_Directory_Does_Not_Overwrite_Durable_Updates_Made_After_Rename(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make the directory entry durable.
	syncDir(t, crash, ".")

	writeFile(t, crash, "old/data.txt", "before", 0o644, true)
	// Make the file name durable.
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Replace the file after the directory rename.
	writeFile(t, crash, "new/tmp.txt", "after", 0o644, true)

	err = crash.Rename("new/tmp.txt", "new/data.txt")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the replace-rename durable within the renamed directory.
	syncDir(t, crash, testContentNew)

	// Make the directory rename durable in the parent.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, testContentOld)

	if got, want := mustReadFile(t, crash, "new/data.txt"), "after"; got != want {
		t.Fatalf("ReadFile(\"new/data.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "new/tmp.txt")
}

func Test_Crash_Rename_Directory_Does_Not_Resurrect_Replaced_Subtree_When_Parent_Is_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("src", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("dst", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make both directory entries durable.
	syncDir(t, crash, ".")

	writeFile(t, crash, "dst/stale.txt", "stale", 0o644, true)
	// Make the stale file durable under dst.
	syncDir(t, crash, "dst")

	err = crash.RemoveAll("dst")
	if err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	// Intentionally do not sync the parent; the removal is not durable.
	requireNotExists(t, crash, "dst")

	writeFile(t, crash, "src/live.txt", "live", 0o644, true)
	syncDir(t, crash, "src")

	err = crash.Rename("src", "dst")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	// Make the replace-rename durable in the parent.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, "src")

	if got, want := mustReadFile(t, crash, "dst/live.txt"), "live"; got != want {
		t.Fatalf("ReadFile(\"dst/live.txt\")=%q, want %q", got, want)
	}

	requireNotExists(t, crash, "dst/stale.txt")
}

func Test_Crash_Remove_Empty_Directory_Durability_Depends_On_Parent_Dir_Sync(t *testing.T) {
	t.Parallel()

	t.Run("NotDurableWithoutDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("dir", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.Remove("dir")
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		info, err := crash.Stat("dir")
		if err != nil {
			t.Fatalf("Stat(\"dir\"): %v", err)
		}

		if !info.IsDir() {
			t.Fatal("Stat(\"dir\").IsDir()=false, want true")
		}
	})

	t.Run("DurableWithDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		err := crash.MkdirAll("dir", 0o755)
		if err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.Remove("dir")
		if err != nil {
			t.Fatalf("Remove: %v", err)
		}

		syncDir(t, crash, ".")

		err = crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		requireNotExists(t, crash, "dir")
	})
}

func Test_Crash_Removes_Descendants_When_Directory_Is_Deleted(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("parent/child", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "parent/child/file.txt", testContentData, 0o644, true)
	syncDir(t, crash, "parent/child")
	syncDir(t, crash, "parent")
	syncDir(t, crash, ".")

	err = crash.RemoveAll("parent")
	if err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	requireNotExists(t, crash, "parent")
	requireNotExists(t, crash, "parent/child/file.txt")
}

func Test_Crash_ReadDir_Is_Sorted_And_Reflects_Durable_State_After_Crash(t *testing.T) {
	t.Parallel()

	t.Run("SortedInLiveView", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "b.txt", "b", 0o644, false)
		writeFile(t, crash, "a.txt", "a", 0o644, false)
		writeFile(t, crash, "c.txt", "c", 0o644, false)

		entries, err := crash.ReadDir(".")
		if err != nil {
			t.Fatalf("ReadDir(\".\"): %v", err)
		}

		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Name())
		}

		if !sort.StringsAreSorted(got) {
			t.Fatalf("ReadDir returned unsorted names: %v", got)
		}

		want := []string{"a.txt", "b.txt", "c.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ReadDir names=%v, want %v", got, want)
		}
	})

	t.Run("NotDurableWithoutDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "a.txt", "a", 0o644, false)
		writeFile(t, crash, "b.txt", "b", 0o644, false)

		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		entries, err := crash.ReadDir(".")
		if err != nil {
			t.Fatalf("ReadDir(\".\"): %v", err)
		}

		if len(entries) != 0 {
			t.Fatalf("ReadDir(\".\"): got %d entries, want 0", len(entries))
		}
	})

	t.Run("DurableWithDirSync", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "b.txt", "b", 0o644, false)
		writeFile(t, crash, "a.txt", "a", 0o644, false)
		writeFile(t, crash, "c.txt", "c", 0o644, false)
		syncDir(t, crash, ".")

		err := crash.SimulateCrash()
		if err != nil {
			t.Fatalf("SimulateCrash: %v", err)
		}

		entries, err := crash.ReadDir(".")
		if err != nil {
			t.Fatalf("ReadDir(\".\"): %v", err)
		}

		got := make([]string, 0, len(entries))
		for _, e := range entries {
			got = append(got, e.Name())
		}

		want := []string{"a.txt", "b.txt", "c.txt"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("ReadDir names=%v, want %v", got, want)
		}
	})
}

func Test_Crash_Does_Not_Resurrect_Stale_FileSnapshot_After_Delete_And_Recreate(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	// Make a durable file snapshot.
	writeFile(t, crash, "data.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	// Make the deletion durable.
	err := crash.Remove("data.txt")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	syncDir(t, crash, ".")

	// Recreate the same path without syncing file contents. The new entry is durable
	// but content is not.
	writeFile(t, crash, "data.txt", testContentNew, 0o644, false)
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got := mustReadFile(t, crash, "data.txt"); got != "" {
		t.Fatalf("ReadFile(\"data.txt\")=%q, want empty", got)
	}
}

func Test_Crash_DirSync_Does_Not_Mutate_State_When_Directory_Has_Been_Removed(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("dir", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "dir/keep.txt", "durable", 0o644, true)
	syncDir(t, crash, "dir")
	syncDir(t, crash, ".")

	d, err := crash.Open("dir")
	if err != nil {
		t.Fatalf("Open(\"dir\"): %v", err)
	}

	err = crash.RemoveAll("dir")
	if err != nil {
		_ = d.Close()

		t.Fatalf("RemoveAll: %v", err)
	}

	err = d.Sync()
	if err != nil {
		_ = d.Close()

		t.Fatalf("Sync(\"dir\"): %v", err)
	}

	err = d.Close()
	if err != nil {
		t.Fatalf("Close(\"dir\"): %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("fs.Crash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "dir/keep.txt"), "durable"; got != want {
		t.Fatalf("ReadFile(\"dir/keep.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_Persists_File_Mode_Only_When_File_Is_Synced(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	f, err := crash.OpenFile("mode.txt", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	_, err = f.Write([]byte(testContentData))
	if err != nil {
		_ = f.Close()

		t.Fatalf("Write: %v", err)
	}

	err = f.Chmod(0o600)
	if err != nil {
		_ = f.Close()

		t.Fatalf("Chmod(0600): %v", err)
	}

	err = f.Sync()
	if err != nil {
		_ = f.Close()

		t.Fatalf("Sync: %v", err)
	}

	err = f.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	syncDir(t, crash, ".")

	// Mutate the mode without a file sync; it should not become durable.
	g, err := crash.Open("mode.txt")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	err = g.Chmod(0o644)
	if err != nil {
		_ = g.Close()

		t.Fatalf("Chmod(0644): %v", err)
	}

	err = g.Close()
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	info, err := crash.Stat("mode.txt")
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}

	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("mode.txt perm=%#o, want %#o", got, want)
	}
}

func Test_Crash_Resolves_Paths_When_They_Are_Absolute_Or_Escaping(t *testing.T) {
	t.Parallel()

	t.Run("AbsolutePathsAreSandboxRootRelative", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "/abs.txt", "x", 0o644, false)

		if got, want := mustReadFile(t, crash, "/abs.txt"), "x"; got != want {
			t.Fatalf("ReadFile(\"/abs.txt\")=%q, want %q", got, want)
		}
	})

	t.Run("RelativePathsRejectEscapes", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{})

		writeFile(t, crash, "./inside.txt", "ok", 0o644, false)

		if got, want := mustReadFile(t, crash, "inside.txt"), "ok"; got != want {
			t.Fatalf("ReadFile(\"inside.txt\")=%q, want %q", got, want)
		}

		err := crash.WriteFile("../outside.txt", []byte("nope"), 0o644)
		if err == nil {
			t.Fatal("WriteFile(\"../outside.txt\"): want error")
		}
	})
}

func Test_Crash_DirHandleSync_Does_Not_Resurrect_Stale_Subtree_When_Directory_Name_Reused_Before_Parent_Sync(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll("a", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("b", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "a/old.txt", testContentOld, 0o644, true)
	syncDir(t, crash, "a")

	err = crash.Remove("a/old.txt")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	requireNotExists(t, crash, "a/old.txt")

	err = crash.Remove("a")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}

	requireNotExists(t, crash, "a")

	err = crash.Rename("b", "a")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Make the reused directory entry durable.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	info, err := crash.Stat("a")
	if err != nil {
		t.Fatalf("Stat(\"a\"): %v", err)
	}

	if !info.IsDir() {
		t.Fatal("Stat(\"a\").IsDir()=false, want true")
	}

	requireNotExists(t, crash, "b")
	requireNotExists(t, crash, "a/old.txt")
}

func Test_Crash_DirHandleSync_Preserves_Subtree_When_Directory_Renamed_Before_Parent_Sync(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "old/data.txt", testContentData, 0o644, true)
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Make the rename durable in the parent.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, testContentOld)

	if got, want := mustReadFile(t, crash, "new/data.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"new/data.txt\")=%q, want %q", got, want)
	}
}

func Test_Crash_DirHandleSync_Preserves_Empty_Directory_When_Directory_Renamed_Before_Parent_Sync(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Record the directory contents durability without creating any files.
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	requireNotExists(t, crash, testContentOld)

	info, err := crash.Stat(testContentNew)
	if err != nil {
		t.Fatalf("Stat(\"new\"): %v", err)
	}

	if !info.IsDir() {
		t.Fatal("Stat(\"new\").IsDir()=false, want true")
	}

	entries, err := crash.ReadDir(testContentNew)
	if err != nil {
		t.Fatalf("ReadDir(\"new\"): %v", err)
	}

	if got := len(entries); got != 0 {
		t.Fatalf("ReadDir(\"new\"): got len=%d, want len=%d", got, 0)
	}
}

func Test_Crash_DirHandleSync_Does_Not_Confuse_Reused_Name_When_Directory_Renamed_Before_Parent_Sync(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	err = crash.MkdirAll("keep", 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "old/data.txt", testContentData, 0o644, true)
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	// Reuse the old name for a different directory inode before syncing the parent.
	err = crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make both entries durable.
	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	if got, want := mustReadFile(t, crash, "new/data.txt"), testContentData; got != want {
		t.Fatalf("ReadFile(\"new/data.txt\")=%q, want %q", got, want)
	}

	info, err := crash.Stat(testContentOld)
	if err != nil {
		t.Fatalf("Stat(\"old\"): %v", err)
	}

	if !info.IsDir() {
		t.Fatal("Stat(\"old\").IsDir()=false, want true")
	}
}

func Test_Crash_DirHandleSync_Preserves_FileMode_When_Directory_Renamed_Before_Parent_Sync(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{})

	err := crash.MkdirAll(testContentOld, 0o755)
	if err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	writeFile(t, crash, "old/data.txt", testContentData, 0o600, true)
	syncDir(t, crash, testContentOld)

	err = crash.Rename(testContentOld, testContentNew)
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}

	syncDir(t, crash, ".")

	err = crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash: %v", err)
	}

	info, err := crash.Stat("new/data.txt")
	if err != nil {
		t.Fatalf("Stat(\"new/data.txt\"): %v", err)
	}

	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("Stat(\"new/data.txt\").Mode().Perm()=%v, want %v", got, want)
	}
}
