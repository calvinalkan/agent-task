package fs

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
)

// CrashOp identifies an operation that can be crash-injected.
//
// These values are used by [CrashFailpointConfig.Ops].
type CrashOp string

// Valid CrashOp values for failpoint configuration.
const (
	CrashOpOpen      CrashOp = CrashOp(chaosOpOpen)
	CrashOpCreate    CrashOp = CrashOp(chaosOpCreate)
	CrashOpReadFile  CrashOp = "readfile"
	CrashOpWriteFile CrashOp = "writefile"
	CrashOpReadDir   CrashOp = "readdir"
	CrashOpMkdirAll  CrashOp = CrashOp(faultMkdirAll)
	CrashOpStat      CrashOp = CrashOp(faultStat)
	CrashOpExists    CrashOp = "exists"
	CrashOpRemove    CrashOp = CrashOp(faultRemove)
	CrashOpRemoveAll CrashOp = CrashOp(faultRemoveAll)
	CrashOpRename    CrashOp = "rename"
	CrashOpFileRead  CrashOp = "file.read"
	CrashOpFileWrite CrashOp = "file.write"
	CrashOpFileSeek  CrashOp = "file.seek"
	CrashOpFileStat  CrashOp = "file.stat"
	CrashOpFileSync  CrashOp = "file.sync"
	CrashOpFileClose CrashOp = "file.close"
	CrashOpFileChmod CrashOp = "file.chmod"

	crashOpCrash CrashOp = "crash"
)

// CrashFailpointAction determines how an injected crash terminates execution.
//
// See [CrashFailpointConfig.Action].
type CrashFailpointAction uint8

const (
	// CrashFailpointPanic panics with a [*CrashPanicError].
	//
	// This is convenient for single-process tests (recover in the same goroutine).
	CrashFailpointPanic CrashFailpointAction = iota

	// CrashFailpointExit terminates the process via [os.Exit].
	//
	// This is useful for subprocess crash testing because it stops all goroutines
	// and does not run defers.
	CrashFailpointExit
)

// CrashFailpointConfig configures crash injection.
//
// Crash injection is optional. The zero value disables injection.
//
// When a failpoint triggers, [Crash] rotates to a fresh working directory restored
// from the durable snapshot, latches into the post-crash view until [Crash.Recover],
// and terminates execution according to [CrashFailpointConfig.Action].
//
// In panic mode ([CrashFailpointPanic]), tests should recover the panic, call
// [Crash.Recover], and then assert state.
//
// In exit mode ([CrashFailpointExit]), [Crash] calls [os.Exit]. Use this from a
// subprocess crash-test harness and inspect the crashfs work directory from the
// parent process.
//
// Example:
//
//	crash, _ := fs.NewCrash(t, fs.NewReal(), fs.CrashConfig{
//		Failpoint: fs.CrashFailpointConfig{After: 1, Action: fs.CrashFailpointPanic},
//	})
type CrashFailpointConfig struct {
	// After triggers an injected crash on the Nth eligible operation (1-indexed).
	//
	// If After is 0 and Rate is 0 but other filters are set (Ops/Paths/etc), [Crash]
	// defaults After to 1 (crash on the first eligible operation).
	After uint64

	// Seed seeds the pseudo-random generator used by Rate.
	Seed int64

	// Rate is the probability in [0,1] that an eligible operation triggers a crash.
	//
	// If both After and Rate are set, a crash triggers when either condition matches.
	Rate float64

	// Ops restrict which operations are eligible. If empty, all operations are eligible.
	Ops []CrashOp

	// Paths restrict eligibility to an exact set of filesystem paths.
	//
	// The strings are interpreted the same way [Crash] interprets operation paths:
	//   - paths starting with "/" are root-relative ("/a/b")
	//   - other paths are root-relative ("a/b")
	//   - "." and "" refer to the root
	//   - relative paths that clean to a leading ".." are rejected
	//
	// Both configured paths and operation paths are normalized with [filepath.Clean]
	// before comparison, so "a/../b" matches "b".
	//
	// For [CrashOpRename], both the source and destination paths are checked against
	// Paths and PathPrefixes.
	//
	// For file-handle operations ([CrashOpFileRead], [CrashOpFileWrite], etc.),
	// the matched path is the path the handle was opened with.
	Paths []string

	// PathPrefixes restrict eligibility to paths under one of these prefixes.
	//
	// Prefixes use the same path rules as Paths. Matching is directory-aware: a
	// prefix matches the path itself and any descendant (e.g. "/a" matches "/a"
	// and "/a/b", but not "/ab").
	PathPrefixes []string

	// Action controls how [Crash] terminates execution when the failpoint triggers.
	// The default is [CrashFailpointPanic].
	Action CrashFailpointAction

	// ExitCode is used when Action is [CrashFailpointExit].
	//
	// ExitCode must be non-zero.
	ExitCode int
}

// crashFailpoint holds normalized failpoint filters and mutable state.
//
// It is created once at [NewCrash] time and mutated under [Crash.mu] as eligible
// operations execute.
type crashFailpoint struct {
	armed bool
	count uint64

	after uint64
	rate  float64

	action   CrashFailpointAction
	exitCode int

	opSet   map[CrashOp]struct{}
	pathSet map[string]struct{}
	prefix  []string

	rng *rand.Rand
}

// newCrashFailpoint validates and normalizes failpoint configuration.
//
// It resolves configured paths into [Crash]'s root-relative namespace so eligibility
// checks can be done via simple string comparisons at runtime.
func newCrashFailpoint(crash *Crash, cfg *CrashFailpointConfig) (*crashFailpoint, error) {
	if cfg.Rate < 0 || cfg.Rate > 1 {
		return nil, fmt.Errorf("crashfs: invalid failpoint config: rate %f", cfg.Rate)
	}

	action := cfg.Action
	switch action {
	case CrashFailpointPanic, CrashFailpointExit:
	default:
		return nil, fmt.Errorf("crashfs: invalid failpoint config: action %d", action)
	}

	if action == CrashFailpointExit && cfg.ExitCode <= 0 {
		return nil, errors.New("crashfs: invalid failpoint config: exit code must be > 0")
	}

	hasFilters := len(cfg.Ops) > 0 || len(cfg.Paths) > 0 || len(cfg.PathPrefixes) > 0
	if cfg.After == 0 && cfg.Rate == 0 && !hasFilters {
		return &crashFailpoint{armed: false}, nil
	}

	// If filters are set but no trigger is specified, crash on first eligible op.
	after := cfg.After
	if after == 0 && cfg.Rate == 0 {
		after = 1
	}

	fp := &crashFailpoint{
		armed:    true,
		after:    after,
		rate:     cfg.Rate,
		action:   action,
		exitCode: cfg.ExitCode,
	}

	if len(cfg.Ops) > 0 {
		fp.opSet = make(map[CrashOp]struct{}, len(cfg.Ops))
		for _, op := range cfg.Ops {
			fp.opSet[op] = struct{}{}
		}
	}

	if len(cfg.Paths) > 0 {
		fp.pathSet = make(map[string]struct{}, len(cfg.Paths))
		for _, p := range cfg.Paths {
			rel, err := crash.virtualRel(p)
			if err != nil {
				return nil, fmt.Errorf("crashfs: invalid failpoint path %q: %w", p, err)
			}

			fp.pathSet[rel] = struct{}{}
		}
	}

	if len(cfg.PathPrefixes) > 0 {
		fp.prefix = make([]string, 0, len(cfg.PathPrefixes))
		for _, p := range cfg.PathPrefixes {
			rel, err := crash.virtualRel(p)
			if err != nil {
				return nil, fmt.Errorf("crashfs: invalid failpoint prefix %q: %w", p, err)
			}

			fp.prefix = append(fp.prefix, rel)
		}
	}

	if fp.rate > 0 {
		fp.rng = rand.New(rand.NewPCG(uint64(cfg.Seed), uint64(cfg.Seed)))
	}

	return fp, nil
}

// eligible reports whether an operation passes op/path filters.
//
// It does not apply counters or random rate checks; the caller is responsible for
// incrementing count and deciding whether to trigger.
func (fp *crashFailpoint) eligible(op CrashOp, rel, newRel string) bool {
	if fp == nil || !fp.armed {
		return false
	}

	if len(fp.opSet) > 0 {
		if _, ok := fp.opSet[op]; !ok {
			return false
		}
	}

	if len(fp.pathSet) > 0 {
		if newRel == "" {
			if _, ok := fp.pathSet[rel]; !ok {
				return false
			}
		} else {
			_, okOld := fp.pathSet[rel]

			_, okNew := fp.pathSet[newRel]
			if !okOld && !okNew {
				return false
			}
		}
	}

	if len(fp.prefix) > 0 {
		matched := false

		for _, pref := range fp.prefix {
			if pathHasPrefix(rel, pref) || (newRel != "" && pathHasPrefix(newRel, pref)) {
				matched = true

				break
			}
		}

		if !matched {
			return false
		}
	}

	return true
}

func (fp *crashFailpoint) shouldTrigger() bool {
	fp.count++

	if fp.after > 0 && fp.count == fp.after {
		return true
	}

	if fp.rate > 0 {
		return fp.rng.Float64() < fp.rate
	}

	return false
}

// pathHasPrefix checks for a directory-aware prefix match.
//
// It treats empty prefix as a wildcard and ensures "/a" does not match "/ab".
func pathHasPrefix(path, prefix string) bool {
	if prefix == "" {
		return true
	}

	if path == prefix {
		return true
	}

	sep := string(os.PathSeparator)

	return strings.HasPrefix(path, prefix+sep)
}
