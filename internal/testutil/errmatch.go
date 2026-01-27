package testutil

import (
	"errors"
	"strings"

	"github.com/calvinalkan/agent-task/internal/testutil/spec"
)

// ErrorBuckets maps spec error codes to broad CLI error substrings.
//
// The goal is loose matching: if both model and CLI fail, we check that
// the CLI error falls into an expected bucket for the model's error code.
// This avoids brittle exact string matching while still catching
// "wrong error" bugs.
var ErrorBuckets = map[spec.ErrCode][]string{
	// Not found errors
	spec.ErrTicketNotFound:  {"not found", "does not exist"},
	spec.ErrBlockerNotFound: {"not found", "blocker"},
	spec.ErrParentNotFound:  {"not found", "parent"},

	// Invalid input errors
	spec.ErrTitleRequired:     {"title", "required"},
	spec.ErrInvalidType:       {"invalid", "type"},
	spec.ErrInvalidPriority:   {"invalid", "priority"},
	spec.ErrInvalidStatus:     {"invalid", "status"},
	spec.ErrInvalidTimestamp:  {"invalid", "timestamp"},
	spec.ErrInvalidOffset:     {"offset", "invalid", "negative"},
	spec.ErrInvalidLimit:      {"limit", "invalid", "negative"},
	spec.ErrOffsetOutOfBounds: {"offset", "out of bounds"},

	// State transition errors
	spec.ErrCantStartNotOpen:     {"not open", "status", "in_progress", "closed"},
	spec.ErrCantCloseOpen:        {"started", "must be started", "open"},
	spec.ErrCantCloseClosed:      {"already closed", "closed"},
	spec.ErrCantReopenOpen:       {"already open", "open"},
	spec.ErrCantReopenInProgress: {"in_progress", "not closed"},

	// Blocker errors
	spec.ErrCantBlockSelf:     {"itself", "self", "cannot block"},
	spec.ErrAlreadyBlocked:    {"already blocked"},
	spec.ErrNotBlocked:        {"not blocked"},
	spec.ErrBlockerCycle:      {"cycle", "circular"},
	spec.ErrHasOpenBlockers:   {"blocked", "blocker", "open"},
	spec.ErrBlockerIDRequired: {"blocker", "required", "empty"},
	spec.ErrDuplicateBlocker:  {"duplicate", "blocker"},

	// Parent errors
	spec.ErrParentNotStarted:    {"parent", "started", "open"},
	spec.ErrParentAlreadyClosed: {"parent", "closed"},
	spec.ErrHasOpenChildren:     {"children", "open", "child"},
	spec.ErrAncestorNotReady:    {"ancestor", "parent", "blocked"},

	// Closed ticket errors
	spec.ErrTicketClosed: {"closed", "cannot"},

	// ID errors
	spec.ErrIDRequired:          {"id", "required"},
	spec.ErrTicketAlreadyExists: {"already exists", "exists"},

	// Content/misc errors
	spec.ErrContentRequired:      {"content", "required", "description"},
	spec.ErrClaimedByRequired:    {"claimed", "required"},
	spec.ErrEmptyTag:             {"tag", "empty"},
	spec.ErrDuplicateTag:         {"tag", "duplicate"},
	spec.ErrClosedBeforeCreated:  {"closed", "before", "created"},
	spec.ErrStartedBeforeCreated: {"started", "before", "created"},
}

// MatchesErrorBucket checks if stderr contains any substring from the
// bucket associated with the given spec error code.
//
// Returns true if:
//   - the error code has a bucket AND stderr matches any substring, OR
//   - the error code has no bucket (we don't validate unknown errors)
func MatchesErrorBucket(specErr *spec.Error, stderr string) bool {
	if specErr == nil {
		return true
	}

	bucket, ok := ErrorBuckets[specErr.Code]
	if !ok {
		// No bucket defined for this error - accept any error message
		return true
	}

	lower := strings.ToLower(stderr)
	for _, substr := range bucket {
		if strings.Contains(lower, strings.ToLower(substr)) {
			return true
		}
	}

	return false
}

// ClassifySpecError extracts the error code from a spec error.
// Returns empty string if err is nil or not a *spec.Error.
func ClassifySpecError(err error) spec.ErrCode {
	if err == nil {
		return ""
	}

	specErr := &spec.Error{}
	if errors.As(err, &specErr) {
		return specErr.Code
	}

	return ""
}

// ExpectedBucketSubstrings returns the substrings for an error code.
// Returns nil if the error code has no defined bucket.
func ExpectedBucketSubstrings(code spec.ErrCode) []string {
	return ErrorBuckets[code]
}
