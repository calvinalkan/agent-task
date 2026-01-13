package fs_test

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/calvinalkan/agent-task/pkg/fs"
)

func Test_NewCrash_Returns_Error_When_Input_Is_Invalid(t *testing.T) {
	t.Parallel()

	t.Run("NilTB", func(t *testing.T) {
		t.Parallel()

		_, err := fs.NewCrash(nil, fs.NewReal(), &fs.CrashConfig{})
		if err == nil {
			t.Fatal("fs.NewCrash(nil, ...): want error")
		}
	})

	t.Run("NilFS", func(t *testing.T) {
		t.Parallel()

		_, err := fs.NewCrash(t, nil, &fs.CrashConfig{})
		if err == nil {
			t.Fatal("fs.NewCrash(..., nil, ...): want error")
		}
	})

	t.Run("EmptyTempDir", func(t *testing.T) {
		t.Parallel()

		_, err := fs.NewCrash(stubTempDirer{dir: ""}, fs.NewReal(), &fs.CrashConfig{})
		if err == nil {
			t.Fatal("fs.NewCrash(tb.TempDir()=\"\"): want error")
		}
	})

	t.Run("InvalidFailpointConfig", func(t *testing.T) {
		t.Parallel()

		cases := []fs.CrashFailpointConfig{
			{Rate: -0.1, Ops: []fs.CrashOp{fs.CrashOpStat}},
			{Rate: 1.1, Ops: []fs.CrashOp{fs.CrashOpStat}},
			{Action: fs.CrashFailpointAction(99), Ops: []fs.CrashOp{fs.CrashOpStat}},
			{Action: fs.CrashFailpointExit, ExitCode: 0, Ops: []fs.CrashOp{fs.CrashOpStat}},
			{Action: fs.CrashFailpointExit, ExitCode: -1, Ops: []fs.CrashOp{fs.CrashOpStat}},
		}

		for i, fp := range cases {
			_, err := fs.NewCrash(t, fs.NewReal(), &fs.CrashConfig{Failpoint: fp})
			if err == nil {
				t.Fatalf("case %d: fs.NewCrash(...): want error", i)
			}
		}
	})
}

func Test_Crash_Returns_Error_When_Writeback_File_Weight_Is_Invalid(t *testing.T) {
	t.Parallel()

	cfg := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			FileWeights: fs.CrashWritebackFileWeights{
				KeepOld: math.NaN(),
			},
		},
	}

	_, err := fs.NewCrash(t, fs.NewReal(), &cfg)
	if err == nil {
		t.Fatal("fs.NewCrash(...): want error")
	}
}

func Test_Crash_Returns_Error_When_Writeback_Dir_Weight_Is_Invalid(t *testing.T) {
	t.Parallel()

	cfg := fs.CrashConfig{
		Writeback: fs.CrashWritebackConfig{
			DirEntryWeights: fs.CrashWritebackDirEntryWeights{
				KeepNew: -1,
			},
		},
	}

	_, err := fs.NewCrash(t, fs.NewReal(), &cfg)
	if err == nil {
		t.Fatal("fs.NewCrash(...): want error")
	}
}

func Test_NewCrash_Returns_Error_When_Failpoint_Paths_Escape_Root(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		cfg  fs.CrashFailpointConfig
	}{
		{
			name: "InvalidPath",
			cfg: fs.CrashFailpointConfig{
				After: 1,
				Ops:   []fs.CrashOp{fs.CrashOpExists},
				Paths: []string{"../escape"},
			},
		},
		{
			name: "InvalidPrefix",
			cfg: fs.CrashFailpointConfig{
				After:        1,
				Ops:          []fs.CrashOp{fs.CrashOpExists},
				PathPrefixes: []string{"../escape"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := fs.NewCrash(t, fs.NewReal(), &fs.CrashConfig{Failpoint: tc.cfg})
			if err == nil {
				t.Fatal("fs.NewCrash(...): want error")
			}
		})
	}
}

func Test_CrashFSErr_Marks_Internal_Errors_And_Panics_On_Nil(t *testing.T) {
	t.Parallel()

	t.Run("PanicsOnNil", func(t *testing.T) {
		t.Parallel()

		defer func() {
			if recover() == nil {
				t.Fatal("fs.CrashFSErr(nil): want panic")
			}
		}()

		_ = fs.CrashFSErr("op", nil)
	})

	t.Run("IsErrCrashFS", func(t *testing.T) {
		t.Parallel()

		base := errors.New("base")
		err := fs.CrashFSErr("op", base)

		if !errors.Is(err, fs.ErrCrashFS) {
			t.Fatalf("errors.Is(err, fs.ErrCrashFS)=false, want true; err=%v", err)
		}

		if !errors.Is(err, base) {
			t.Fatalf("errors.Is(err, base)=false, want true; err=%v", err)
		}

		if !strings.Contains(err.Error(), "crashfs: op:") {
			t.Fatalf("err.Error()=%q, want contains %q", err.Error(), "crashfs: op:")
		}
	})
}

type stubTempDirer struct{ dir string }

func (s stubTempDirer) TempDir() string { return s.dir }
