package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func Test_StrictFS_Does_Not_Panic_When_The_Error_Is_A_ChaosError(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	chaos := NewChaos(NewReal(), 0, ChaosConfig{OpenFailRate: 1.0})
	chaos.SetMode(ChaosModeActive)
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: chaos})
	path := filepath.Join(t.TempDir(), "test.txt")

	_, err := strict.Open(path)
	if err == nil {
		t.Fatalf("Open(%q): want error, got nil", path)
	}
	if !IsChaosErr(err) {
		t.Errorf("IsChaosErr(err) for Open(%q): want true, got false", path)
	}
	if tb.failed {
		t.Errorf("tb.failed after chaos Open(%q): want false, got true", path)
	}
}

func Test_StrictFS_ReadFile_Returns_Data_And_ChaosError_When_Chaos_Partially_Reads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "test.txt")
	full := []byte("hello world")
	if err := os.WriteFile(path, full, 0o644); err != nil {
		t.Fatalf("setup WriteFile(%q): %v", path, err)
	}

	tb := &fakeTB{}
	chaos := NewChaos(NewReal(), 0, ChaosConfig{PartialReadRate: 1.0})
	chaos.SetMode(ChaosModeActive)
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: chaos})

	data, err := strict.ReadFile(path)
	if err == nil {
		t.Fatalf("ReadFile(%q): want error, got nil", path)
	}
	if !IsChaosErr(err) {
		t.Fatalf("IsChaosErr(err) for ReadFile(%q): want true, got false (err=%v)", path, err)
	}
	if got := len(data); got <= 0 || got >= len(full) {
		t.Fatalf("ReadFile(%q): len(data)=%d, want 0 < len(data) < %d", path, got, len(full))
	}
	if tb.failed {
		t.Fatalf("tb.failed after chaos ReadFile(%q): want false, got true", path)
	}
}

func Test_StrictFS_ReadDir_Returns_Entries_And_ChaosError_When_Chaos_Partially_Reads(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatalf("setup WriteFile(a.txt): %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatalf("setup WriteFile(b.txt): %v", err)
	}

	realFS := NewReal()
	fullEntries, err := realFS.ReadDir(dir)
	if err != nil {
		t.Fatalf("setup ReadDir(%q): %v", dir, err)
	}
	if len(fullEntries) < 2 {
		t.Fatalf("setup: want at least 2 dir entries, got %d", len(fullEntries))
	}

	tb := &fakeTB{}
	chaos := NewChaos(realFS, 0, ChaosConfig{ReadDirPartialRate: 1.0})
	chaos.SetMode(ChaosModeActive)
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: chaos})

	entries, err := strict.ReadDir(dir)
	if err == nil {
		t.Fatalf("ReadDir(%q): want error, got nil", dir)
	}
	if !IsChaosErr(err) {
		t.Fatalf("IsChaosErr(err) for ReadDir(%q): want true, got false (err=%v)", dir, err)
	}
	if got := len(entries); got <= 0 || got >= len(fullEntries) {
		t.Fatalf("ReadDir(%q): len(entries)=%d, want 0 < len(entries) < %d", dir, got, len(fullEntries))
	}
	if tb.failed {
		t.Fatalf("tb.failed after chaos ReadDir(%q): want false, got true", dir)
	}
}

func Test_StrictFS_Does_Not_Panic_When_The_Error_Is_EOF(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	stub := stubFS{
		open: func(string) (File, error) {
			return nil, io.EOF
		},
	}
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: stub})

	_, err := strict.Open("any")
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Open(): want io.EOF, got %v", err)
	}
	if tb.failed {
		t.Fatalf("tb.failed after Open() returned io.EOF: want false, got true")
	}
}

func Test_StrictFS_Trace_Is_Empty_Before_Any_Ops(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal()})

	if got := strict.Trace(); got != "" {
		t.Fatalf("Trace(): want empty string, got %q", got)
	}
}

func Test_StrictFS_Trace_Is_Empty_When_TraceCapacity_Is_Zero(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	cap := 0
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal(), TraceCapacity: &cap})

	// Exercise a few ops that would normally produce trace output.
	path := filepath.Join(t.TempDir(), "file.txt")
	f, err := strict.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	if _, err := f.Write([]byte("hello")); err != nil {
		t.Fatalf("Write(%q): %v", path, err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close(%q): %v", path, err)
	}

	if got := strict.Trace(); got != "" {
		t.Fatalf("Trace() with TraceCapacity=0: want empty string, got %q", got)
	}

	tb.failed = true
	if tb.cleanup == nil {
		t.Fatal("expected StrictFS to register Cleanup")
	}
	tb.cleanup()
	if tb.logMsg != "" {
		t.Fatalf("cleanup log with TraceCapacity=0: want empty, got %q", tb.logMsg)
	}
}

func Test_StrictFS_Trace_Is_Bounded_To_TraceCapacity(t *testing.T) {
	t.Parallel()

	t.Run("DefaultCapacityIs200", func(t *testing.T) {
		t.Parallel()

		tb := &fakeTB{}
		strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal()})
		dir := t.TempDir()

		for i := range 205 {
			path := filepath.Join(dir, fmt.Sprintf("exists-%03d", i))
			_, err := strict.Exists(path)
			if err != nil {
				t.Fatalf("Exists(%q): %v", path, err)
			}
		}

		trace := strict.Trace()
		lines := splitTraceLines(trace)
		if want, got := 200, len(lines); want != got {
			t.Fatalf("Trace() line count: want %d, got %d\ntrace:\n%s", want, got, trace)
		}

		oldest := filepath.Join(dir, "exists-000")
		newest := filepath.Join(dir, "exists-204")
		if strings.Contains(trace, fmt.Sprintf("path=%q", oldest)) {
			t.Fatalf("Trace() should not include oldest entry %q\ntrace:\n%s", oldest, trace)
		}
		if !strings.Contains(trace, fmt.Sprintf("path=%q", newest)) {
			t.Fatalf("Trace() should include newest entry %q\ntrace:\n%s", newest, trace)
		}
	})

	t.Run("CustomCapacity", func(t *testing.T) {
		t.Parallel()

		tb := &fakeTB{}
		cap := 3
		strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal(), TraceCapacity: &cap})
		dir := t.TempDir()

		paths := []string{
			filepath.Join(dir, "missing-1"),
			filepath.Join(dir, "missing-2"),
			filepath.Join(dir, "missing-3"),
			filepath.Join(dir, "missing-4"),
			filepath.Join(dir, "missing-5"),
		}

		for _, p := range paths {
			_, err := strict.Exists(p)
			if err != nil {
				t.Fatalf("Exists(%q): %v", p, err)
			}
		}

		trace := strict.Trace()
		lines := splitTraceLines(trace)
		if want, got := 3, len(lines); want != got {
			t.Fatalf("Trace() line count: want %d, got %d\ntrace:\n%s", want, got, trace)
		}

		for _, shouldNotContain := range paths[:2] {
			if strings.Contains(trace, fmt.Sprintf("path=%q", shouldNotContain)) {
				t.Fatalf("Trace() should not include %q\ntrace:\n%s", shouldNotContain, trace)
			}
		}
		for _, shouldContain := range paths[2:] {
			if !strings.Contains(trace, fmt.Sprintf("path=%q", shouldContain)) {
				t.Fatalf("Trace() should include %q\ntrace:\n%s", shouldContain, trace)
			}
		}
	})
}

func Test_StrictFS_Trace_Records_Ops_In_Order(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal()})
	dir := t.TempDir()

	missing := filepath.Join(dir, "missing.txt")
	subdir := filepath.Join(dir, "sub")
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	flag := os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	perm := os.FileMode(0o600)
	c := filepath.Join(dir, "c.txt")

	var (
		f  File
		f2 File
		f3 File
	)

	steps := []struct {
		op  string
		run func() error
	}{
		{
			op: "exists",
			run: func() error {
				exists, err := strict.Exists(missing)
				if err != nil {
					return err
				}
				if exists {
					return errors.New("expected exists=false")
				}
				return nil
			},
		},
		{op: "mkdirall", run: func() error { return strict.MkdirAll(subdir, 0o755) }},
		{
			op: "create",
			run: func() error {
				var err error
				f, err = strict.Create(a)
				return err
			},
		},
		{
			op: "file.write",
			run: func() error {
				n, err := f.Write([]byte("hello"))
				if err != nil {
					return err
				}
				if n != 5 {
					return fmt.Errorf("write n=%d, want 5", n)
				}
				return nil
			},
		},
		{op: "file.sync", run: func() error { return f.Sync() }},
		{op: "file.stat", run: func() error { _, err := f.Stat(); return err }},
		{
			op: "file.seek",
			run: func() error {
				pos, err := f.Seek(0, io.SeekStart)
				if err != nil {
					return err
				}
				if pos != 0 {
					return fmt.Errorf("seek pos=%d, want 0", pos)
				}
				return nil
			},
		},
		{
			op: "file.read",
			run: func() error {
				buf := make([]byte, 5)
				n, err := f.Read(buf)
				if err != nil {
					return err
				}
				if n != 5 {
					return fmt.Errorf("read n=%d, want 5", n)
				}
				return nil
			},
		},
		{op: "file.close", run: func() error { return f.Close() }},
		{
			op: "readfile",
			run: func() error {
				data, err := strict.ReadFile(a)
				if err != nil {
					return err
				}
				if string(data) != "hello" {
					return fmt.Errorf("data=%q, want %q", string(data), "hello")
				}
				return nil
			},
		},
		{op: "readdir", run: func() error { _, err := strict.ReadDir(dir); return err }},
		{op: "stat", run: func() error { _, err := strict.Stat(a); return err }},
		{
			op: "open",
			run: func() error {
				var err error
				f2, err = strict.Open(a)
				return err
			},
		},
		{op: "file.close", run: func() error { return f2.Close() }},
		{
			op: "openfile",
			run: func() error {
				var err error
				f3, err = strict.OpenFile(b, flag, perm)
				return err
			},
		},
		{op: "file.write", run: func() error { _, err := f3.Write([]byte("x")); return err }},
		{op: "file.close", run: func() error { return f3.Close() }},
		{op: "rename", run: func() error { return strict.Rename(b, c) }},
		{op: "remove", run: func() error { return strict.Remove(c) }},
		{op: "removeall", run: func() error { return strict.RemoveAll(subdir) }},
	}

	wantOps := make([]string, 0, len(steps))
	for _, s := range steps {
		wantOps = append(wantOps, s.op)
	}

	for _, s := range steps {
		if err := s.run(); err != nil {
			t.Fatalf("%s: %v", s.op, err)
		}
	}

	assertTraceOps(t, strict.Trace(), wantOps)
}

func Test_StrictFS_Panics_For_Each_Method_When_The_Underlying_FS_Errors(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")
	base := alwaysErrFS{err: errBoom}

	tests := []struct {
		name    string
		traceOp string
		call    func(*StrictTestFS)
	}{
		{"Open", "open", func(s *StrictTestFS) { _, _ = s.Open("x") }},
		{"Create", "create", func(s *StrictTestFS) { _, _ = s.Create("x") }},
		{"OpenFile", "openfile", func(s *StrictTestFS) { _, _ = s.OpenFile("x", os.O_RDONLY, 0o644) }},
		{"ReadFile", "readfile", func(s *StrictTestFS) { _, _ = s.ReadFile("x") }},
		{"ReadDir", "readdir", func(s *StrictTestFS) { _, _ = s.ReadDir("x") }},
		{"MkdirAll", "mkdirall", func(s *StrictTestFS) { _ = s.MkdirAll("x", 0o755) }},
		{"Stat", "stat", func(s *StrictTestFS) { _, _ = s.Stat("x") }},
		{"Exists", "exists", func(s *StrictTestFS) { _, _ = s.Exists("x") }},
		{"Remove", "remove", func(s *StrictTestFS) { _ = s.Remove("x") }},
		{"RemoveAll", "removeall", func(s *StrictTestFS) { _ = s.RemoveAll("x") }},
		{"Rename", "rename", func(s *StrictTestFS) { _ = s.Rename("old", "new") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tb := &fakeTB{}
			strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: base})

			panicMsg := mustPanic(t, func() { tt.call(strict) })
			if !strings.Contains(panicMsg, errBoom.Error()) {
				t.Fatalf("panic message: want %q, got %q", errBoom.Error(), panicMsg)
			}
			if !strings.Contains(panicMsg, "#1 "+tt.traceOp) {
				t.Fatalf("panic message: want trace op %q, got %q", "#1 "+tt.traceOp, panicMsg)
			}
			if !tb.failed {
				t.Fatal("tb.failed after real error: want true, got false")
			}
		})
	}
}

// --- File operation tests ---

func Test_StrictFile_Does_Not_Panic_When_Read_Returns_EOF(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal()})
	path := filepath.Join(t.TempDir(), "test.txt")

	f, err := strict.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	f.Close()

	f, err = strict.Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	defer f.Close()

	buf := make([]byte, 10)
	_, err = f.Read(buf)
	if !errors.Is(err, io.EOF) {
		t.Errorf("Read(%q): want io.EOF, got %v", path, err)
	}
	if tb.failed {
		t.Errorf("tb.failed after Read(%q) returned io.EOF: want false, got true", path)
	}
}

func Test_StrictFile_Fd_Returns_The_Underlying_Fd(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	stubFile := &testFile{fd: 123}
	strict := NewStrictTestFS(tb, StrictTestFSOptions{
		FS: stubFS{
			create: func(string) (File, error) { return stubFile, nil },
		},
	})

	f, err := strict.Create("ignored")
	if err != nil {
		t.Fatalf("Create(): %v", err)
	}

	if got, want := f.Fd(), uintptr(123); got != want {
		t.Fatalf("Fd(): want %d, got %d", want, got)
	}
}

func Test_StrictFile_Panics_For_Each_Method_When_The_Underlying_File_Errors(t *testing.T) {
	t.Parallel()

	errBoom := errors.New("boom")

	tests := []struct {
		name    string
		traceOp string
		file    *testFile
		call    func(File)
	}{
		{
			name:    "Read",
			traceOp: "file.read",
			file:    &testFile{readErr: errBoom},
			call:    func(f File) { _, _ = f.Read(make([]byte, 1)) },
		},
		{
			name:    "Write",
			traceOp: "file.write",
			file:    &testFile{writeErr: errBoom},
			call:    func(f File) { _, _ = f.Write([]byte("x")) },
		},
		{
			name:    "Close",
			traceOp: "file.close",
			file:    &testFile{closeErr: errBoom},
			call:    func(f File) { _ = f.Close() },
		},
		{
			name:    "Seek",
			traceOp: "file.seek",
			file:    &testFile{seekErr: errBoom},
			call:    func(f File) { _, _ = f.Seek(0, io.SeekStart) },
		},
		{
			name:    "Stat",
			traceOp: "file.stat",
			file:    &testFile{statErr: errBoom},
			call:    func(f File) { _, _ = f.Stat() },
		},
		{
			name:    "Sync",
			traceOp: "file.sync",
			file:    &testFile{syncErr: errBoom},
			call:    func(f File) { _ = f.Sync() },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tb := &fakeTB{}
			strict := NewStrictTestFS(tb, StrictTestFSOptions{
				FS: stubFS{
					create: func(string) (File, error) { return tt.file, nil },
				},
			})

			f, err := strict.Create("ignored")
			if err != nil {
				t.Fatalf("Create(): %v", err)
			}

			panicMsg := mustPanic(t, func() { tt.call(f) })
			if !strings.Contains(panicMsg, errBoom.Error()) {
				t.Fatalf("panic message: want %q, got %q", errBoom.Error(), panicMsg)
			}
			if !strings.Contains(panicMsg, "#2 "+tt.traceOp) {
				t.Fatalf("panic message: want trace op %q, got %q", "#2 "+tt.traceOp, panicMsg)
			}
			if !tb.failed {
				t.Fatal("tb.failed after real file error: want true, got false")
			}
		})
	}
}

func Test_StrictFile_Does_Not_Panic_For_Chaos_Errors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		config  ChaosConfig
		call    func(File) error
		wantErr bool
	}{
		{
			name:    "Read",
			config:  ChaosConfig{ReadFailRate: 1.0},
			call:    func(f File) error { _, err := f.Read(make([]byte, 1)); return err },
			wantErr: true,
		},
		{
			name:    "Write",
			config:  ChaosConfig{WriteFailRate: 1.0},
			call:    func(f File) error { _, err := f.Write([]byte("x")); return err },
			wantErr: true,
		},
		{
			name:    "Seek",
			config:  ChaosConfig{SeekFailRate: 1.0},
			call:    func(f File) error { _, err := f.Seek(0, io.SeekStart); return err },
			wantErr: true,
		},
		{
			name:    "Stat",
			config:  ChaosConfig{FileStatFailRate: 1.0},
			call:    func(f File) error { _, err := f.Stat(); return err },
			wantErr: true,
		},
		{
			name:    "Sync",
			config:  ChaosConfig{SyncFailRate: 1.0},
			call:    func(f File) error { return f.Sync() },
			wantErr: true,
		},
		{
			name:    "Close",
			config:  ChaosConfig{CloseFailRate: 1.0},
			call:    func(f File) error { return f.Close() },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tb := &fakeTB{}
			chaos := NewChaos(NewReal(), 0, tt.config)
			chaos.SetMode(ChaosModeActive)
			strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: chaos})
			path := filepath.Join(t.TempDir(), "test.txt")

			f, err := strict.Create(path)
			if err != nil {
				t.Fatalf("Create(%q): %v", path, err)
			}

			if tt.name != "Close" {
				defer func() { _ = f.Close() }()
			}

			err = tt.call(f)
			if (err != nil) != tt.wantErr {
				t.Fatalf("%s(): err=%v, wantErr=%t", tt.name, err, tt.wantErr)
			}
			if err != nil && !IsChaosErr(err) {
				t.Fatalf("IsChaosErr(err) after %s(): want true, got false (err=%v)", tt.name, err)
			}
			if tb.failed {
				t.Fatalf("tb.failed after chaos %s(): want false, got true", tt.name)
			}
		})
	}
}

func Test_StrictFile_Does_Not_Panic_On_Chaos_Partial_Write(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	chaos := NewChaos(NewReal(), 0, ChaosConfig{PartialWriteRate: 1.0, ShortWriteRate: 0.0})
	chaos.SetMode(ChaosModeActive)
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: chaos})
	path := filepath.Join(t.TempDir(), "test.txt")

	f, err := strict.Create(path)
	if err != nil {
		t.Fatalf("Create(%q): %v", path, err)
	}
	defer func() { _ = f.Close() }()

	payload := []byte("hello")
	n, err := f.Write(payload)
	if err == nil {
		t.Fatalf("Write(%q): want error, got nil", path)
	}
	if !IsChaosErr(err) {
		t.Fatalf("IsChaosErr(err) for partial Write(%q): want true, got false (err=%v)", path, err)
	}
	if n <= 0 || n >= len(payload) {
		t.Fatalf("Write(%q): n=%d, want 0 < n < %d", path, n, len(payload))
	}
	if tb.failed {
		t.Fatalf("tb.failed after chaos partial Write(%q): want false, got true", path)
	}
}

// --- Cleanup behavior ---

func Test_StrictFS_Cleanup_Logs_Trace_Only_When_The_Test_Fails(t *testing.T) {
	t.Parallel()

	tb := &fakeTB{}
	strict := NewStrictTestFS(tb, StrictTestFSOptions{FS: NewReal()})
	path := filepath.Join(t.TempDir(), "test.txt")

	f, _ := strict.Create(path)
	if f != nil {
		_, _ = f.Write([]byte("hello"))
		_ = f.Close()
	}

	if tb.cleanup == nil {
		t.Fatal("expected StrictFS to register Cleanup")
	}

	// No log when the test isn't failing.
	tb.cleanup()
	if tb.logMsg != "" {
		t.Fatalf("tb.logMsg after cleanup without failure: want empty string, got %q", tb.logMsg)
	}

	tb.failed = true // Simulate test failure
	tb.cleanup()

	if tb.logMsg == "" {
		t.Fatal("tb.logMsg after cleanup: want trace output, got empty string")
	}
	if !strings.Contains(tb.logMsg, "#1 create") {
		t.Errorf("tb.logMsg: want substring %q, got %q", "#1 create", tb.logMsg)
	}
}

// --- Test helper ---

type fakeTB struct {
	failed  bool
	logMsg  string
	cleanup func()
}

func (f *fakeTB) Helper() {}

func (f *fakeTB) Cleanup(fn func()) {
	f.cleanup = fn
}

func (f *fakeTB) Failed() bool {
	return f.failed
}

func (f *fakeTB) Logf(format string, args ...any) {
	f.logMsg = fmt.Sprintf(format, args...)
}

func (f *fakeTB) Fatalf(format string, args ...any) {
	f.failed = true
	panic(fmt.Sprintf(format, args...))
}

type alwaysErrFS struct {
	err error
}

func (e alwaysErrFS) Open(string) (File, error)                       { return nil, e.err }
func (e alwaysErrFS) Create(string) (File, error)                     { return nil, e.err }
func (e alwaysErrFS) OpenFile(string, int, os.FileMode) (File, error) { return nil, e.err }
func (e alwaysErrFS) ReadFile(string) ([]byte, error)                 { return nil, e.err }
func (e alwaysErrFS) ReadDir(string) ([]os.DirEntry, error)           { return nil, e.err }
func (e alwaysErrFS) MkdirAll(string, os.FileMode) error              { return e.err }
func (e alwaysErrFS) Stat(string) (os.FileInfo, error)                { return nil, e.err }
func (e alwaysErrFS) Exists(string) (bool, error)                     { return false, e.err }
func (e alwaysErrFS) Remove(string) error                             { return e.err }
func (e alwaysErrFS) RemoveAll(string) error                          { return e.err }
func (e alwaysErrFS) Rename(string, string) error                     { return e.err }

type stubFS struct {
	open      func(path string) (File, error)
	create    func(path string) (File, error)
	openFile  func(path string, flag int, perm os.FileMode) (File, error)
	readFile  func(path string) ([]byte, error)
	readDir   func(path string) ([]os.DirEntry, error)
	mkdirAll  func(path string, perm os.FileMode) error
	stat      func(path string) (os.FileInfo, error)
	exists    func(path string) (bool, error)
	remove    func(path string) error
	removeAll func(path string) error
	rename    func(oldpath, newpath string) error
}

func (s stubFS) Open(path string) (File, error) {
	if s.open == nil {
		panic("stubFS.Open: not implemented")
	}
	return s.open(path)
}

func (s stubFS) Create(path string) (File, error) {
	if s.create == nil {
		panic("stubFS.Create: not implemented")
	}
	return s.create(path)
}

func (s stubFS) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	if s.openFile == nil {
		panic("stubFS.OpenFile: not implemented")
	}
	return s.openFile(path, flag, perm)
}

func (s stubFS) ReadFile(path string) ([]byte, error) {
	if s.readFile == nil {
		panic("stubFS.ReadFile: not implemented")
	}
	return s.readFile(path)
}

func (s stubFS) ReadDir(path string) ([]os.DirEntry, error) {
	if s.readDir == nil {
		panic("stubFS.ReadDir: not implemented")
	}
	return s.readDir(path)
}

func (s stubFS) MkdirAll(path string, perm os.FileMode) error {
	if s.mkdirAll == nil {
		panic("stubFS.MkdirAll: not implemented")
	}
	return s.mkdirAll(path, perm)
}

func (s stubFS) Stat(path string) (os.FileInfo, error) {
	if s.stat == nil {
		panic("stubFS.Stat: not implemented")
	}
	return s.stat(path)
}

func (s stubFS) Exists(path string) (bool, error) {
	if s.exists == nil {
		panic("stubFS.Exists: not implemented")
	}
	return s.exists(path)
}

func (s stubFS) Remove(path string) error {
	if s.remove == nil {
		panic("stubFS.Remove: not implemented")
	}
	return s.remove(path)
}

func (s stubFS) RemoveAll(path string) error {
	if s.removeAll == nil {
		panic("stubFS.RemoveAll: not implemented")
	}
	return s.removeAll(path)
}

func (s stubFS) Rename(oldpath, newpath string) error {
	if s.rename == nil {
		panic("stubFS.Rename: not implemented")
	}
	return s.rename(oldpath, newpath)
}

type testFile struct {
	fd uintptr

	readErr  error
	writeErr error
	closeErr error
	seekErr  error
	statErr  error
	syncErr  error
}

func (f *testFile) Read([]byte) (int, error)  { return 0, f.readErr }
func (f *testFile) Write([]byte) (int, error) { return 0, f.writeErr }
func (f *testFile) Close() error              { return f.closeErr }
func (f *testFile) Seek(int64, int) (int64, error) {
	return 0, f.seekErr
}
func (f *testFile) Fd() uintptr { return f.fd }
func (f *testFile) Stat() (os.FileInfo, error) {
	return nil, f.statErr
}
func (f *testFile) Sync() error { return f.syncErr }

func mustPanic(t *testing.T, fn func()) (msg string) {
	t.Helper()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("expected panic, got none")
		}
		msg = fmt.Sprint(r)
	}()

	fn()

	return ""
}

func splitTraceLines(trace string) []string {
	if trace == "" {
		return nil
	}
	return strings.Split(trace, "\n")
}

func assertTraceOps(t *testing.T, trace string, wantOps []string) {
	t.Helper()

	if trace == "" {
		t.Fatal("Trace(): want non-empty trace, got empty string")
	}

	lines := splitTraceLines(trace)
	if want, got := len(wantOps), len(lines); want != got {
		t.Fatalf("Trace() line count: want %d, got %d\ntrace:\n%s", want, got, trace)
	}

	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Fatalf("Trace()[%d]: invalid trace line %q\ntrace:\n%s", i, line, trace)
		}

		if !strings.Contains(line, "path=") {
			t.Fatalf("Trace()[%d]: missing path in line %q\ntrace:\n%s", i, line, trace)
		}

		if got, want := fields[1], wantOps[i]; got != want {
			t.Fatalf("Trace()[%d] op: want %q, got %q\nline: %q\ntrace:\n%s", i, want, got, line, trace)
		}
	}
}

var _ TestBuilder = (*fakeTB)(nil)
var _ FS = (*alwaysErrFS)(nil)
var _ FS = (*stubFS)(nil)
var _ File = (*testFile)(nil)
