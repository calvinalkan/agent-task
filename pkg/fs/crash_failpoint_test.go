package fs_test

import (
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

func Test_CrashFailpoint_Latches_State_When_A_Crash_Is_Injected(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{
		Failpoint: fs.CrashFailpointConfig{
			After: 2,
			Ops:   []fs.CrashOp{fs.CrashOpFileWrite},
		},
	})

	writeFile(t, crash, "a.txt", testContentOld, 0o644, true)
	syncDir(t, crash, ".")

	f, err := crash.OpenFile("a.txt", os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		t.Fatalf("OpenFile: %v", err)
	}

	_ = mustPanicSimulatedCrash(t, func() {
		_, _ = f.Write([]byte(testContentNew))
	})

	// The underlying crash machinery closed all open fds.
	_ = f.Close()

	// fs.Crash should remain latched until Recover() is called.
	_ = mustPanicSimulatedCrash(t, func() {
		_, _ = crash.Stat("a.txt")
	})

	crash.Recover()

	if got, want := mustReadFile(t, crash, "a.txt"), testContentOld; got != want {
		t.Fatalf("ReadFile(\"a.txt\")=%q, want %q", got, want)
	}
}

func Test_CrashFailpoint_Filters_Operations_When_Paths_And_Prefixes_Are_Configured(t *testing.T) {
	t.Parallel()

	t.Run("ExactPathMatch", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After: 1,
				Ops:   []fs.CrashOp{fs.CrashOpExists},
				Paths: []string{"/match"},
			},
		})

		// Ineligible op: should not crash.
		_, err := crash.Exists("/other")
		if err != nil {
			t.Fatalf("Exists(/other): %v", err)
		}

		_ = mustPanicSimulatedCrash(t, func() {
			_, _ = crash.Exists("/match")
		})
	})

	t.Run("PrefixMatchIsDirectoryAware", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After:        1,
				Ops:          []fs.CrashOp{fs.CrashOpExists},
				PathPrefixes: []string{"/a"},
			},
		})

		// "/ab" must not match prefix "/a".
		_, err := crash.Exists("/ab")
		if err != nil {
			t.Fatalf("Exists(/ab): %v", err)
		}

		_ = mustPanicSimulatedCrash(t, func() {
			_, _ = crash.Exists("/a/b")
		})
	})

	t.Run("RenameMatchesOldOrNewPath", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name  string
			paths []string
		}{
			{name: "MatchesNew", paths: []string{"/final"}},
			{name: "MatchesOld", paths: []string{"/tmp"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				crash := mustNewCrash(t, &fs.CrashConfig{
					Failpoint: fs.CrashFailpointConfig{
						After: 1,
						Ops:   []fs.CrashOp{fs.CrashOpRename},
						Paths: tc.paths,
					},
				})

				writeFile(t, crash, "/tmp", "x", 0o644, true)
				syncDir(t, crash, "/")

				_ = mustPanicSimulatedCrash(t, func() {
					_ = crash.Rename("/tmp", "/final")
				})

				crash.Recover()

				requireNotExists(t, crash, "/final")

				if got, want := mustReadFile(t, crash, "/tmp"), "x"; got != want {
					t.Fatalf("ReadFile(\"/tmp\")=%q, want %q", got, want)
				}
			})
		}
	})
}

func Test_Crash_OpenFile_Uses_Create_Op_When_Write_Flags_Are_Set(t *testing.T) {
	t.Parallel()

	t.Run("ReadOnlyUsesOpen", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After: 1,
				Ops:   []fs.CrashOp{fs.CrashOpOpen},
			},
		})

		_ = mustPanicSimulatedCrash(t, func() {
			_, _ = crash.OpenFile("/missing", os.O_RDONLY, 0)
		})
	})

	t.Run("WriteUsesCreate", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After: 1,
				Ops:   []fs.CrashOp{fs.CrashOpCreate},
			},
		})

		_ = mustPanicSimulatedCrash(t, func() {
			_, _ = crash.OpenFile("/new", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		})
	})
}

func Test_Crash_Create_Is_A_Failpoint_Create_Operation(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{
		Failpoint: fs.CrashFailpointConfig{
			After: 1,
			Ops:   []fs.CrashOp{fs.CrashOpCreate},
		},
	})

	_ = mustPanicSimulatedCrash(t, func() {
		_, _ = crash.Create("any")
	})
}

func Test_CrashFailpoint_Defaults_After_To_1_When_Filters_Are_Set(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{
		Failpoint: fs.CrashFailpointConfig{
			Ops: []fs.CrashOp{fs.CrashOpExists},
		},
	})

	_ = mustPanicSimulatedCrash(t, func() {
		_, _ = crash.Exists("any")
	})
}

func Test_CrashFailpoint_Normalizes_Paths_With_filepath_Clean(t *testing.T) {
	t.Parallel()

	t.Run("Paths", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After: 1,
				Ops:   []fs.CrashOp{fs.CrashOpExists},
				Paths: []string{"a/../match"},
			},
		})

		_, err := crash.Exists("/other")
		if err != nil {
			t.Fatalf("Exists(/other): %v", err)
		}

		_ = mustPanicSimulatedCrash(t, func() {
			_, _ = crash.Exists("/match")
		})
	})

	t.Run("Prefixes", func(t *testing.T) {
		t.Parallel()

		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After:        1,
				Ops:          []fs.CrashOp{fs.CrashOpExists},
				PathPrefixes: []string{"a/../pfx"},
			},
		})

		_, err := crash.Exists("/other")
		if err != nil {
			t.Fatalf("Exists(/other): %v", err)
		}

		_ = mustPanicSimulatedCrash(t, func() {
			_, _ = crash.Exists("/pfx/child")
		})
	})
}

func Test_CrashFailpoint_Rate_Triggers_A_Crash_When_Set(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{
		Failpoint: fs.CrashFailpointConfig{
			After: 99,
			Rate:  1,
			Seed:  1,
			Ops:   []fs.CrashOp{fs.CrashOpExists},
		},
	})

	_ = mustPanicSimulatedCrash(t, func() {
		_, _ = crash.Exists("any")
	})
}

func Test_Crash_SimulateCrash_ReTriggers_Termination_When_Latched(t *testing.T) {
	t.Parallel()

	crash := mustNewCrash(t, &fs.CrashConfig{
		Failpoint: fs.CrashFailpointConfig{
			After: 1,
			Ops:   []fs.CrashOp{fs.CrashOpExists},
		},
	})

	_ = mustPanicSimulatedCrash(t, func() {
		_, _ = crash.Exists("any")
	})

	_ = mustPanicSimulatedCrash(t, func() {
		_ = crash.SimulateCrash()
	})

	crash.Recover()

	err := crash.SimulateCrash()
	if err != nil {
		t.Fatalf("SimulateCrash after Recover: %v", err)
	}
}

func Test_CrashFailpoint_ExitAction_Exits_Process_With_Configured_Code(t *testing.T) {
	t.Parallel()

	const envKey = "TK_CRASHFS_EXIT_HELPER"

	if os.Getenv(envKey) == "1" {
		crash := mustNewCrash(t, &fs.CrashConfig{
			Failpoint: fs.CrashFailpointConfig{
				After:    1,
				Ops:      []fs.CrashOp{fs.CrashOpExists},
				Action:   fs.CrashFailpointExit,
				ExitCode: 42,
			},
		})

		_, _ = crash.Exists("any")

		// Unreachable: crashfs should have terminated the process via os.Exit.
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^Test_CrashFailpoint_ExitAction_Exits_Process_With_Configured_Code$")

	cmd.Env = append(os.Environ(), envKey+"=1")

	err := cmd.Run()
	if err == nil {
		t.Fatal("expected subprocess to exit non-zero")
	}

	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("subprocess err=%T, want *exec.ExitError; err=%v", err, err)
	}

	if exitErr.ExitCode() != 42 {
		t.Fatalf("subprocess exit code=%d, want %d", exitErr.ExitCode(), 42)
	}
}
