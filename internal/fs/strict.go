package fs

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync"
)

// TestBuilder is the subset of [testing.T] used by [StrictTestFS].
//
// This keeps [StrictTestFS] usable from tests in other packages without
// depending on _test.go files.
type TestBuilder interface {
	// [testing.T.Helper]
	Helper()
	// [testing.T.Cleanup]
	Cleanup(func())
	// [testing.T.Failed]
	Failed() bool
	// [testing.T.Logf]
	Logf(format string, args ...any)
	// [testing.T.Fatalf]
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

// StrictTestFSOptions configures a [StrictTestFS].
type StrictTestFSOptions struct {
	// FS is the underlying filesystem to wrap.
	FS FS
	// TraceCapacity is the max number of operations to keep in the trace log.
	// Defaults to 200. Set to a pointer to 0 to disable tracing.
	TraceCapacity *int
}

// NewStrictTestFS creates a new [StrictTestFS] wrapping the given [FS].
//
// On test failure, logs the trace of recent FS operations via tb.Cleanup.
func NewStrictTestFS(tb TestBuilder, opts StrictTestFSOptions) *StrictTestFS {
	tb.Helper()

	s := &StrictTestFS{
		tb:    tb,
		fs:    opts.FS,
		trace: newTraceLog(opts.TraceCapacity),
	}

	tb.Cleanup(func() {
		if tb.Failed() {
			if trace := s.Trace(); trace != "" {
				tb.Logf("fs trace:\n%s", trace)
			}
		}
	})

	return s
}

// Trace returns a formatted string of recent FS operations.
func (s *StrictTestFS) Trace() string {
	return s.trace.String()
}

func (s *StrictTestFS) Open(path string) (File, error) {
	s.tb.Helper()
	f, err := s.fs.Open(path)

	return s.wrapFile("open", path, f, err)
}

func (s *StrictTestFS) Create(path string) (File, error) {
	s.tb.Helper()
	f, err := s.fs.Create(path)

	return s.wrapFile("create", path, f, err)
}

func (s *StrictTestFS) OpenFile(path string, flag int, perm os.FileMode) (File, error) {
	s.tb.Helper()
	f, err := s.fs.OpenFile(path, flag, perm)

	return s.wrapFile("openfile", path, f, err, attr("flag", strconv.Itoa(flag)), attr("perm", fmt.Sprintf("%#o", perm)))
}

func (s *StrictTestFS) ReadFile(path string) ([]byte, error) {
	s.tb.Helper()
	data, err := s.fs.ReadFile(path)

	return data, s.wrap("readfile", path, err, attr("n", strconv.Itoa(len(data))))
}

func (s *StrictTestFS) ReadDir(path string) ([]os.DirEntry, error) {
	s.tb.Helper()
	entries, err := s.fs.ReadDir(path)

	return entries, s.wrap("readdir", path, err, attr("n", strconv.Itoa(len(entries))))
}

func (s *StrictTestFS) MkdirAll(path string, perm os.FileMode) error {
	s.tb.Helper()

	return s.wrap("mkdirall", path, s.fs.MkdirAll(path, perm), attr("perm", fmt.Sprintf("%#o", perm)))
}

func (s *StrictTestFS) Stat(path string) (os.FileInfo, error) {
	s.tb.Helper()
	info, err := s.fs.Stat(path)

	return info, s.wrap("stat", path, err)
}

func (s *StrictTestFS) Exists(path string) (bool, error) {
	s.tb.Helper()
	exists, err := s.fs.Exists(path)

	return exists, s.wrap("exists", path, err, attr("exists", strconv.FormatBool(exists)))
}

func (s *StrictTestFS) Remove(path string) error {
	s.tb.Helper()

	return s.wrap("remove", path, s.fs.Remove(path))
}

func (s *StrictTestFS) RemoveAll(path string) error {
	s.tb.Helper()

	return s.wrap("removeall", path, s.fs.RemoveAll(path))
}

func (s *StrictTestFS) Rename(oldpath, newpath string) error {
	s.tb.Helper()

	return s.wrap("rename", oldpath, s.fs.Rename(oldpath, newpath), attr("dest", newpath))
}

// Interface compliance.
var _ FS = (*StrictTestFS)(nil)

// wrap traces the operation and fatals on real (non-injected) errors.
func (s *StrictTestFS) wrap(op, path string, err error, attrs ...kv) error {
	s.tb.Helper()

	s.trace.add(op, path, err, attrs...)

	if err != nil && !IsChaosErr(err) && !errors.Is(err, io.EOF) {
		trace := s.Trace()
		if trace != "" {
			trace = "\n" + trace
		}

		s.tb.Fatalf("strictfs: underlying filesystem error: %v%s", err, trace)
	}

	return err
}

// wrapFile traces the operation, fatals on real errors, and wraps the file.
func (s *StrictTestFS) wrapFile(op, path string, f File, err error, attrs ...kv) (File, error) {
	s.tb.Helper()

	if err := s.wrap(op, path, err, attrs...); err != nil {
		return nil, err
	}

	return &strictFile{tb: s.tb, f: f, trace: s.trace, path: path}, nil
}

// kv is a key-value pair for trace context.
type kv struct {
	k string
	v string
}

func attr(k, v string) kv {
	return kv{k: k, v: v}
}

// traceEvent records a single FS operation.
type traceEvent struct {
	seq      uint64
	op       string
	path     string
	err      error
	injected bool
	attrs    []kv
}

func (e traceEvent) String() string {
	var b strings.Builder

	fmt.Fprintf(&b, "#%d %s", e.seq, e.op)

	if e.path != "" {
		fmt.Fprintf(&b, " path=%q", e.path)
	}

	for _, a := range e.attrs {
		fmt.Fprintf(&b, " %s=%s", a.k, a.v)
	}

	if e.err == nil {
		b.WriteString(" ok")

		return b.String()
	}

	fmt.Fprintf(&b, " err=%v injected=%t", e.err, e.injected)

	return b.String()
}

// traceLog is a bounded circular buffer of [traceEvent].
type traceLog struct {
	mu       sync.Mutex
	capacity int
	events   []traceEvent
	next     int
	full     bool
	seq      uint64
}

func newTraceLog(capacity *int) *traceLog {
	cap := 200
	if capacity != nil {
		cap = *capacity
	}

	return &traceLog{
		capacity: cap,
		events:   make([]traceEvent, 0, cap),
	}
}

func (t *traceLog) add(op, path string, err error, attrs ...kv) {
	if t.capacity == 0 {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.seq++

	event := traceEvent{
		seq:      t.seq,
		op:       op,
		path:     path,
		err:      err,
		injected: IsChaosErr(err),
		attrs:    attrs,
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
		return ""
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

var _ File = (*strictFile)(nil)

func (sf *strictFile) wrap(op string, err error, attrs ...kv) error {
	sf.tb.Helper()
	sf.trace.add(op, sf.path, err, attrs...)

	if err != nil && !IsChaosErr(err) && !errors.Is(err, io.EOF) {
		trace := sf.trace.String()
		if trace != "" {
			trace = "\n" + trace
		}

		sf.tb.Fatalf("strictfs: unexpected real fs error: %v%s", err, trace)
	}

	return err
}

func (sf *strictFile) Read(p []byte) (int, error) {
	sf.tb.Helper()
	n, err := sf.f.Read(p)

	return n, sf.wrap("file.read", err, attr("n", strconv.Itoa(n)))
}

func (sf *strictFile) Write(p []byte) (int, error) {
	sf.tb.Helper()
	n, err := sf.f.Write(p)

	return n, sf.wrap("file.write", err, attr("n", strconv.Itoa(n)))
}

func (sf *strictFile) Close() error {
	sf.tb.Helper()

	return sf.wrap("file.close", sf.f.Close())
}

func (sf *strictFile) Seek(offset int64, whence int) (int64, error) {
	sf.tb.Helper()
	pos, err := sf.f.Seek(offset, whence)

	return pos, sf.wrap("file.seek", err, attr("offset", strconv.FormatInt(offset, 10)), attr("whence", strconv.Itoa(whence)), attr("pos", strconv.FormatInt(pos, 10)))
}

func (sf *strictFile) Fd() uintptr {
	return sf.f.Fd()
}

func (sf *strictFile) Stat() (os.FileInfo, error) {
	sf.tb.Helper()
	info, err := sf.f.Stat()

	return info, sf.wrap("file.stat", err)
}

func (sf *strictFile) Sync() error {
	sf.tb.Helper()

	return sf.wrap("file.sync", sf.f.Sync())
}
