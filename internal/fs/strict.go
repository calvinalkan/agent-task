package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

// TestBuilder is the subset of [testing.T] used by [StrictTestFS].
//
// This keeps [StrictTestFS] usable from tests in other packages without
// depending on _test.go files.
type TestBuilder interface {
	Helper()
	Cleanup(func())
	Failed() bool
	Logf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// StrictTestFS wraps an [FS] for tests:
//   - Records a bounded trace of recent FS operations
//   - Fails the test on any non-injected (real) filesystem error
//
// Use it to detect unexpected environment/OS failures while running [Chaos].
type StrictTestFS struct {
	tb    TestBuilder
	fs    FS
	trace *traceLog
}

// NewStrictTestFS creates a new [StrictTestFS] wrapping the given [FS].
//
// On test failure, logs the trace of recent FS operations via tb.Cleanup.
// Panics if tb or fs is nil.
func NewStrictTestFS(tb TestBuilder, fs FS) *StrictTestFS {
	tb.Helper()

	s := &StrictTestFS{
		tb:    tb,
		fs:    fs,
		trace: newTraceLog(defaultStrictTraceCapacity),
	}

	tb.Cleanup(func() {
		if tb.Failed() {
			tb.Logf("fs trace:\n%s", s.Trace())
		}
	})

	return s
}

// Trace returns a formatted string of recent FS operations.
func (s *StrictTestFS) Trace() string {
	return s.trace.String()
}

// Underlying returns the wrapped [FS].
func (s *StrictTestFS) Underlying() FS {
	return s.fs
}

// A wrapper for [FS.Open] that traces and validates errors.
func (s *StrictTestFS) Open(path string) (File, error) {
	s.tb.Helper()

	f, err := s.fs.Open(path)
	s.trace.add("open", path, "", "", err)
	s.fatalIfReal("Open", err)

	if err != nil {
		return nil, err
	}

	return &strictFile{tb: s.tb, f: f, trace: s.trace, path: path}, nil
}

// A wrapper for [FS.Create] that traces and validates errors.
func (s *StrictTestFS) Create(path string) (File, error) {
	s.tb.Helper()

	f, err := s.fs.Create(path)
	s.trace.add("create", path, "", "", err)
	s.fatalIfReal("Create", err)

	if err != nil {
		return nil, err
	}

	return &strictFile{tb: s.tb, f: f, trace: s.trace, path: path}, nil
}

// A wrapper for [FS.OpenFile] that traces and validates errors.
func (s *StrictTestFS) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	s.tb.Helper()

	f, err := s.fs.OpenFile(path, flag, perm)
	s.trace.add("openfile", path, "", fmt.Sprintf("flag=%d perm=%#o", flag, perm), err)
	s.fatalIfReal("OpenFile", err)

	if err != nil {
		return nil, err
	}

	return &strictFile{tb: s.tb, f: f, trace: s.trace, path: path}, nil
}

// A wrapper for [FS.ReadFile] that traces and validates errors.
func (s *StrictTestFS) ReadFile(path string) ([]byte, error) {
	s.tb.Helper()

	data, err := s.fs.ReadFile(path)
	s.trace.add("readfile", path, "", fmt.Sprintf("n=%d", len(data)), err)
	s.fatalIfReal("ReadFile", err)

	return data, err
}

// A wrapper for [FS.WriteFileAtomic] that traces and validates errors.
func (s *StrictTestFS) WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	s.tb.Helper()

	err := s.fs.WriteFileAtomic(path, data, perm)
	s.trace.add("writefileatomic", path, "", fmt.Sprintf("n=%d perm=%#o", len(data), perm), err)
	s.fatalIfReal("WriteFileAtomic", err)

	return err
}

// A wrapper for [FS.ReadDir] that traces and validates errors.
func (s *StrictTestFS) ReadDir(path string) ([]os.DirEntry, error) {
	s.tb.Helper()

	entries, err := s.fs.ReadDir(path)
	s.trace.add("readdir", path, "", fmt.Sprintf("n=%d", len(entries)), err)
	s.fatalIfReal("ReadDir", err)

	return entries, err
}

// A wrapper for [FS.MkdirAll] that traces and validates errors.
func (s *StrictTestFS) MkdirAll(path string, perm os.FileMode) error {
	s.tb.Helper()

	err := s.fs.MkdirAll(path, perm)
	s.trace.add("mkdirall", path, "", fmt.Sprintf("perm=%#o", perm), err)
	s.fatalIfReal("MkdirAll", err)

	return err
}

// A wrapper for [FS.Stat] that traces and validates errors.
func (s *StrictTestFS) Stat(path string) (os.FileInfo, error) {
	s.tb.Helper()

	info, err := s.fs.Stat(path)
	s.trace.add("stat", path, "", "", err)
	s.fatalIfReal("Stat", err)

	return info, err
}

// A wrapper for [FS.Exists] that traces and validates errors.
func (s *StrictTestFS) Exists(path string) (bool, error) {
	s.tb.Helper()

	exists, err := s.fs.Exists(path)
	s.trace.add("exists", path, "", fmt.Sprintf("exists=%t", exists), err)
	s.fatalIfReal("Exists", err)

	return exists, err
}

// A wrapper for [FS.Remove] that traces and validates errors.
func (s *StrictTestFS) Remove(path string) error {
	s.tb.Helper()

	err := s.fs.Remove(path)
	s.trace.add("remove", path, "", "", err)
	s.fatalIfReal("Remove", err)

	return err
}

// A wrapper for [FS.RemoveAll] that traces and validates errors.
func (s *StrictTestFS) RemoveAll(path string) error {
	s.tb.Helper()

	err := s.fs.RemoveAll(path)
	s.trace.add("removeall", path, "", "", err)
	s.fatalIfReal("RemoveAll", err)

	return err
}

// A wrapper for [FS.Rename] that traces and validates errors.
func (s *StrictTestFS) Rename(oldpath, newpath string) error {
	s.tb.Helper()

	err := s.fs.Rename(oldpath, newpath)
	s.trace.add("rename", oldpath, newpath, "", err)
	s.fatalIfReal("Rename", err)

	return err
}

// A wrapper for [FS.Lock] that traces and validates errors.
func (s *StrictTestFS) Lock(path string) (Locker, error) {
	s.tb.Helper()

	lock, err := s.fs.Lock(path)
	s.trace.add("lock", path, "", "", err)
	s.fatalIfReal("Lock", err)

	if err != nil {
		return nil, err
	}

	return &strictLocker{tb: s.tb, l: lock, trace: s.trace, path: path}, nil
}

// Interface compliance.
var _ FS = (*StrictTestFS)(nil)

// --- Unexported ---

const defaultStrictTraceCapacity = 200

// fatalIfReal fails the test if err is a real (non-injected) filesystem error.
// Ignores nil and [io.EOF] (normal streaming signal).
func (s *StrictTestFS) fatalIfReal(op string, err error) {
	s.tb.Helper()

	if err == nil || IsInjected(err) {
		return
	}

	// io.EOF is a normal signal for streaming reads (not an OS failure).
	if errors.Is(err, io.EOF) {
		return
	}

	s.tb.Fatalf("unexpected real fs error during %s: %v\nfs trace:\n%s", op, err, s.Trace())
}

// traceEvent records a single FS operation.
type traceEvent struct {
	Seq      uint64
	Op       string
	Path     string
	Path2    string
	Info     string
	Injected bool
	Err      error
}

func (e traceEvent) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "#%d %s", e.Seq, e.Op)

	if e.Path != "" {
		fmt.Fprintf(&b, " path=%q", e.Path)
	}

	if e.Path2 != "" {
		fmt.Fprintf(&b, " path2=%q", e.Path2)
	}

	if e.Info != "" {
		fmt.Fprintf(&b, " %s", e.Info)
	}

	if e.Err == nil {
		b.WriteString(" ok")
		return b.String()
	}

	fmt.Fprintf(&b, " err=%v injected=%t", e.Err, e.Injected)

	return b.String()
}

// traceLog is a bounded circular buffer of [traceEvent].
type traceLog struct {
	mu       sync.Mutex
	capacity int

	events []traceEvent
	next   int
	full   bool
	seq    uint64
}

func newTraceLog(capacity int) *traceLog {
	if capacity < 1 {
		capacity = 1
	}

	return &traceLog{
		capacity: capacity,
		events:   make([]traceEvent, 0, capacity),
	}
}

func (t *traceLog) add(op, path, path2, info string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.seq++

	event := traceEvent{
		Seq:      t.seq,
		Op:       op,
		Path:     path,
		Path2:    path2,
		Info:     info,
		Err:      err,
		Injected: IsInjected(err),
	}

	if len(t.events) < t.capacity {
		t.events = append(t.events, event)
		return
	}

	t.events[t.next] = event
	t.next = (t.next + 1) % t.capacity
	t.full = true
}

func (t *traceLog) snapshot() []traceEvent {
	t.mu.Lock()
	defer t.mu.Unlock()

	if !t.full {
		return append([]traceEvent(nil), t.events...)
	}

	out := make([]traceEvent, 0, len(t.events))
	out = append(out, t.events[t.next:]...)
	out = append(out, t.events[:t.next]...)

	return out
}

func (t *traceLog) String() string {
	events := t.snapshot()
	if len(events) == 0 {
		return "(no events)"
	}

	var b strings.Builder

	for i, e := range events {
		if i > 0 {
			b.WriteByte('\n')
		}

		b.WriteString(e.String())
	}

	return b.String()
}

// strictFile wraps a [File] to trace and validate errors.
type strictFile struct {
	tb    TestBuilder
	f     File
	trace *traceLog
	path  string
}

// Interface compliance.
var _ File = (*strictFile)(nil)

// A wrapper for [File.Read] that traces and validates errors.
func (sf *strictFile) Read(p []byte) (int, error) {
	sf.tb.Helper()

	n, err := sf.f.Read(p)
	sf.trace.add("file.read", sf.path, "", fmt.Sprintf("n=%d", n), err)

	if err != nil && !errors.Is(err, io.EOF) {
		if !IsInjected(err) {
			sf.tb.Fatalf("unexpected real fs error during File.Read: %v\nfs trace:\n%s", err, sf.trace.String())
		}
	}

	return n, err
}

// A wrapper for [File.Write] that traces and validates errors.
func (sf *strictFile) Write(p []byte) (int, error) {
	sf.tb.Helper()

	n, err := sf.f.Write(p)
	sf.trace.add("file.write", sf.path, "", fmt.Sprintf("n=%d", n), err)

	if err != nil && !IsInjected(err) {
		sf.tb.Fatalf("unexpected real fs error during File.Write: %v\nfs trace:\n%s", err, sf.trace.String())
	}

	return n, err
}

// A wrapper for [File.Close] that traces and validates errors.
func (sf *strictFile) Close() error {
	sf.tb.Helper()

	err := sf.f.Close()
	sf.trace.add("file.close", sf.path, "", "", err)

	if err != nil && !IsInjected(err) {
		sf.tb.Fatalf("unexpected real fs error during File.Close: %v\nfs trace:\n%s", err, sf.trace.String())
	}

	return err
}

// A wrapper for [File.Seek] that traces and validates errors.
func (sf *strictFile) Seek(offset int64, whence int) (int64, error) {
	sf.tb.Helper()

	pos, err := sf.f.Seek(offset, whence)
	sf.trace.add("file.seek", sf.path, "", fmt.Sprintf("offset=%d whence=%d pos=%d", offset, whence, pos), err)

	if err != nil && !IsInjected(err) {
		sf.tb.Fatalf("unexpected real fs error during File.Seek: %v\nfs trace:\n%s", err, sf.trace.String())
	}

	return pos, err
}

// A passthrough wrapper for [File.Fd].
func (sf *strictFile) Fd() uintptr { return sf.f.Fd() }

// A wrapper for [File.Stat] that traces and validates errors.
func (sf *strictFile) Stat() (os.FileInfo, error) {
	sf.tb.Helper()

	info, err := sf.f.Stat()
	sf.trace.add("file.stat", sf.path, "", "", err)

	if err != nil && !IsInjected(err) {
		sf.tb.Fatalf("unexpected real fs error during File.Stat: %v\nfs trace:\n%s", err, sf.trace.String())
	}

	return info, err
}

// A wrapper for [File.Sync] that traces and validates errors.
func (sf *strictFile) Sync() error {
	sf.tb.Helper()

	err := sf.f.Sync()
	sf.trace.add("file.sync", sf.path, "", "", err)

	if err != nil && !IsInjected(err) {
		sf.tb.Fatalf("unexpected real fs error during File.Sync: %v\nfs trace:\n%s", err, sf.trace.String())
	}

	return err
}

// strictLocker wraps a [Locker] to trace and validate errors.
type strictLocker struct {
	tb    TestBuilder
	l     Locker
	trace *traceLog
	path  string
}

// Interface compliance.
var _ Locker = (*strictLocker)(nil)

// A wrapper for [Locker.Close] that traces and validates errors.
func (sl *strictLocker) Close() error {
	sl.tb.Helper()

	err := sl.l.Close()
	sl.trace.add("lock.close", sl.path, "", "", err)

	if err != nil && !IsInjected(err) {
		sl.tb.Fatalf("unexpected real fs error during Locker.Close: %v\nfs trace:\n%s", err, sl.trace.String())
	}

	return err
}
