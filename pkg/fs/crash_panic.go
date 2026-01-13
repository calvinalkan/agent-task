package fs

import (
	"fmt"
	"os"
)

// CrashPanicError is the panic value used for crash injection.
//
// It implements [error] and can be identified with errors.As(err, *CrashPanicError).
//
// Crash injection is intended for tests only.
//
// See [CrashFailpointConfig] for how to enable injection.
// See [Crash.Recover] for how to continue using a [Crash] instance after a panic.
type CrashPanicError struct {
	// Op is the operation that triggered the crash.
	Op CrashOp

	// Path is the raw path argument passed to the operation.
	Path string

	// Rel is Path normalized into crashfs root-relative form.
	Rel string

	// NewPath is the raw destination path for rename operations.
	NewPath string

	// NewRel is NewPath normalized into crashfs root-relative form.
	NewRel string

	// Seq is the 1-indexed count of eligible operations observed by the failpoint.
	Seq uint64

	// Cause is an internal error encountered while rotating/restoring the crash view.
	// It is usually nil.
	Cause error
}

// Error implements [error].
func (p *CrashPanicError) Error() string {
	msg := fmt.Sprintf("crashfs: injected crash op=%s seq=%d", p.Op, p.Seq)
	if p.Path != "" {
		msg += fmt.Sprintf(" path=%q", p.Path)
	}

	if p.Rel != "" && p.Rel != p.Path {
		msg += fmt.Sprintf(" rel=%q", p.Rel)
	}

	if p.NewPath != "" {
		msg += fmt.Sprintf(" newpath=%q", p.NewPath)
	}

	if p.NewRel != "" && p.NewRel != p.NewPath {
		msg += fmt.Sprintf(" newrel=%q", p.NewRel)
	}

	if p.Cause != nil {
		msg += fmt.Sprintf(" cause=%v", p.Cause)
	}

	return msg
}

// Unwrap returns the internal Cause, if any.
func (p *CrashPanicError) Unwrap() error { return p.Cause }

// terminateCrash terminates execution for an injected crash.
//
// exitCode is only honored when non-zero. A value of 0 means "do not call os.Exit".
// If panicVal is non-nil, terminateCrash panics with it.
func terminateCrash(panicVal *CrashPanicError, exitCode int) {
	if exitCode != 0 {
		os.Exit(exitCode)
	}

	if panicVal != nil {
		panic(panicVal)
	}
}
