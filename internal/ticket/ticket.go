package ticket

import (
	"bufio"
	"encoding/base32"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/natefinch/atomic"
)

// Ticket represents a ticket with all its fields.
type Ticket struct {
	SchemaVersion int
	ID            string
	Status        string
	BlockedBy     []string
	Parent        string
	Created       time.Time
	Type          string
	Priority      int
	Assignee      string
	ExternalRef   string
	Title         string
	Description   string
	Design        string
	Acceptance    string
}

// validTypes are the allowed ticket types.
var validTypes = []string{"bug", "feature", "task", "epic", "chore"}

// Priority bounds.
const (
	MinPriority     = 1
	MaxPriority     = 4
	DefaultPriority = 2

	dirPerms  = 0o750
	filePerms = 0o600
)

// GenerateID creates a lexicographically sortable ticket ID.
// The ID is a base32-encoded timestamp (seconds since 2024-01-01) for sortability.
func GenerateID() string {
	return generateTimestampComponent()
}

// crockfordBase32 is a sortable base32 alphabet (digits before letters).
const crockfordBase32 = "0123456789abcdefghjkmnpqrstvwxyz"

var crockfordEncoding = base32.NewEncoding(crockfordBase32).WithPadding(base32.NoPadding)

const (
	timestampBytes = 4
	byteMask       = 0xFF
)

// generateTimestampComponent creates a base32-encoded timestamp for sortability.
// Uses Unix seconds encoded in Crockford's base32 (7 chars). Works until 2106.
func generateTimestampComponent() string {
	sec := time.Now().Unix()

	buf := make([]byte, timestampBytes)
	for i := timestampBytes - 1; i >= 0; i-- {
		buf[i] = byte(sec & byteMask)
		sec >>= 8
	}

	return crockfordEncoding.EncodeToString(buf)
}

const maxSuffixLength = 4

// GenerateUniqueID generates an ID that doesn't exist in the ticket directory.
// On collision, appends letter suffixes (a, b, ..., z, za, zb, ...).
func GenerateUniqueID(ticketDir string) (string, error) {
	base := GenerateID()

	// Try base ID first
	if !Exists(ticketDir, base) {
		return base, nil
	}

	// Append letter suffixes: a, b, ..., z, za, zb, ..., zz, zza, ...
	suffix := ""

	for {
		suffix = nextSuffix(suffix)
		candidate := base + suffix

		if !Exists(ticketDir, candidate) {
			return candidate, nil
		}

		// Safety limit to prevent infinite loop
		if len(suffix) > maxSuffixLength {
			return "", ErrIDGenerationFailed
		}
	}
}

// nextSuffix increments a suffix like base-26: "" -> "a", "a" -> "b", ..., "z" -> "za".
func nextSuffix(suffix string) string {
	if suffix == "" {
		return "a"
	}

	// Convert to rune slice for easier manipulation
	runes := []rune(suffix)

	// Increment from the rightmost character
	for idx := len(runes) - 1; idx >= 0; idx-- {
		if runes[idx] < 'z' {
			runes[idx]++

			return string(runes)
		}

		// Current char is 'z', reset to 'a' and continue carry
		runes[idx] = 'a'
	}

	// All chars were 'z', append 'a' (e.g., "z" -> "za", "zz" -> "zza")
	return suffix + "a"
}

// IsValidType checks if the type is valid.
func IsValidType(ticketType string) bool {
	return slices.Contains(validTypes, ticketType)
}

// IsValidPriority checks if priority is in valid range.
func IsValidPriority(p int) bool {
	return p >= MinPriority && p <= MaxPriority
}

// FormatTicket formats a ticket as markdown with YAML frontmatter.
func FormatTicket(ticket *Ticket) string {
	var builder strings.Builder

	// YAML frontmatter
	builder.WriteString("---\n")
	builder.WriteString(fmt.Sprintf("schema_version: %d\n", ticket.SchemaVersion))
	builder.WriteString("id: " + ticket.ID + "\n")
	builder.WriteString("status: " + ticket.Status + "\n")
	builder.WriteString("blocked-by: " + formatBlockedBy(ticket.BlockedBy) + "\n")

	if ticket.Parent != "" {
		builder.WriteString("parent: " + ticket.Parent + "\n")
	}

	builder.WriteString("created: " + ticket.Created.UTC().Format(time.RFC3339) + "\n")
	builder.WriteString("type: " + ticket.Type + "\n")
	builder.WriteString(fmt.Sprintf("priority: %d\n", ticket.Priority))

	if ticket.Assignee != "" {
		builder.WriteString("assignee: " + ticket.Assignee + "\n")
	}

	if ticket.ExternalRef != "" {
		builder.WriteString("external-ref: " + ticket.ExternalRef + "\n")
	}

	builder.WriteString("---\n")

	// Title
	builder.WriteString("# " + ticket.Title + "\n")

	// Description (no header)
	if ticket.Description != "" {
		builder.WriteString("\n" + ticket.Description + "\n")
	}

	// Design section
	if ticket.Design != "" {
		builder.WriteString("\n## Design\n\n" + ticket.Design + "\n")
	}

	// Acceptance Criteria section
	if ticket.Acceptance != "" {
		builder.WriteString("\n## Acceptance Criteria\n\n" + ticket.Acceptance + "\n")
	}

	return builder.String()
}

func formatBlockedBy(blockedBy []string) string {
	if len(blockedBy) == 0 {
		return "[]"
	}

	return "[" + strings.Join(blockedBy, ", ") + "]"
}

// WriteTicket writes a ticket to the specified directory.
// Returns the full path of the created file.
func WriteTicket(ticketDir string, ticket *Ticket) (string, error) {
	// Ensure directory exists
	mkdirErr := os.MkdirAll(ticketDir, dirPerms)
	if mkdirErr != nil {
		return "", fmt.Errorf("failed to create ticket directory: %w", mkdirErr)
	}

	filename := ticket.ID + ".md"
	path := filepath.Join(ticketDir, filename)

	// Check if file already exists
	_, statErr := os.Stat(path)
	if statErr == nil {
		return "", fmt.Errorf("%w: %s", ErrTicketFileExists, path)
	}

	content := FormatTicket(ticket)

	writeErr := atomic.WriteFile(path, strings.NewReader(content))
	if writeErr != nil {
		return "", fmt.Errorf("failed to write ticket file: %w", writeErr)
	}

	// Set file permissions (atomic.WriteFile doesn't set them for new files)
	chmodErr := os.Chmod(path, filePerms)
	if chmodErr != nil {
		return "", fmt.Errorf("failed to set file permissions: %w", chmodErr)
	}

	return path, nil
}

// WriteTicketAtomic generates a unique ID and writes the ticket atomically.
// Uses file locking to prevent race conditions when multiple tickets are created
// in the same second. Returns the generated ID and file path.
func WriteTicketAtomic(ticketDir string, ticket *Ticket) (string, string, error) {
	// Ensure directory exists first (before locking)
	mkdirErr := os.MkdirAll(ticketDir, dirPerms)
	if mkdirErr != nil {
		return "", "", fmt.Errorf("creating ticket directory: %w", mkdirErr)
	}

	// Generate base ID and lock on it to serialize concurrent creates
	// TODO: Consider binding ticket.Created to the same timestamp/time source used
	// for ID generation so ordering assumptions like "sort by ID == sort by
	// created time" hold even under contention.
	baseID := GenerateID()
	lockPath := filepath.Join(ticketDir, baseID+".md")

	var ticketID, ticketPath string

	lockErr := WithLock(lockPath, func() error {
		// Inside lock: find unique ID (base or with suffix)
		var genErr error

		ticketID, genErr = GenerateUniqueID(ticketDir)
		if genErr != nil {
			return fmt.Errorf("generting unique id: %w", genErr)
		}

		ticket.ID = ticketID

		var writeErr error

		ticketPath, writeErr = WriteTicket(ticketDir, ticket)

		return writeErr
	})
	if lockErr != nil {
		return "", "", lockErr
	}

	return ticketID, ticketPath, nil
}

// Exists checks if a ticket ID exists in the ticket directory.
func Exists(ticketDir, ticketID string) bool {
	path := filepath.Join(ticketDir, ticketID+".md")

	_, statErr := os.Stat(path)

	return statErr == nil
}

// Path returns the full path to a ticket file.
func Path(ticketDir, ticketID string) string {
	return filepath.Join(ticketDir, ticketID+".md")
}

// GetStatusFromContent extracts the status field from ticket content.
func GetStatusFromContent(content []byte) (string, error) {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false

	for _, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		if inFrontmatter && strings.HasPrefix(line, "status: ") {
			return strings.TrimPrefix(line, "status: "), nil
		}
	}

	return "", ErrStatusNotFound
}

// ReadTicketStatus reads just the status field from a ticket file.
func ReadTicketStatus(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading ticket: %w", err)
	}

	return GetStatusFromContent(content)
}

// UpdateStatusInContent updates the status field in ticket content.
// Returns the new content or an error.
func UpdateStatusInContent(content []byte, newStatus string) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false
	updated := false

	for lineIdx, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		if inFrontmatter && strings.HasPrefix(line, "status: ") {
			lines[lineIdx] = "status: " + newStatus
			updated = true

			break
		}
	}

	if !updated {
		return nil, ErrStatusNotFound
	}

	return []byte(strings.Join(lines, "\n")), nil
}

// UpdateTicketStatus updates the status field in a ticket file.
// Deprecated: Use UpdateTicketStatusLocked for concurrent-safe updates.
func UpdateTicketStatus(path, newStatus string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent, err := UpdateStatusInContent(content, newStatus)
	if err != nil {
		return err
	}

	writeErr := atomic.WriteFile(path, strings.NewReader(string(newContent)))
	if writeErr != nil {
		return fmt.Errorf("writing ticket: %w", writeErr)
	}

	return nil
}

// UpdateTicketStatusLocked updates the status field with file locking.
func UpdateTicketStatusLocked(path, newStatus string) error {
	return WithTicketLock(path, func(content []byte) ([]byte, error) {
		return UpdateStatusInContent(content, newStatus)
	})
}

// ReadTicket reads and returns the raw contents of a ticket file.
func ReadTicket(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading ticket: %w", err)
	}

	return string(content), nil
}

// AddFieldToContent adds a new field to ticket content (after status).
// Returns the new content or an error.
func AddFieldToContent(content []byte, field, value string) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false
	insertIndex := -1

	for lineIdx, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		// Insert after status line
		if inFrontmatter && strings.HasPrefix(line, "status: ") {
			insertIndex = lineIdx + 1

			break
		}
	}

	if insertIndex == -1 {
		return nil, ErrFieldInsertFailed
	}

	// Insert new field
	newLine := field + ": " + value
	lines = append(lines[:insertIndex], append([]string{newLine}, lines[insertIndex:]...)...)

	return []byte(strings.Join(lines, "\n")), nil
}

// AddTicketField adds a new field to the ticket frontmatter (after status).
// Deprecated: Use AddTicketFieldLocked for concurrent-safe updates.
func AddTicketField(path, field, value string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent, err := AddFieldToContent(content, field, value)
	if err != nil {
		return err
	}

	writeErr := atomic.WriteFile(path, strings.NewReader(string(newContent)))
	if writeErr != nil {
		return fmt.Errorf("writing ticket: %w", writeErr)
	}

	return nil
}

// AddTicketFieldLocked adds a new field with file locking.
func AddTicketFieldLocked(path, field, value string) error {
	return WithTicketLock(path, func(content []byte) ([]byte, error) {
		return AddFieldToContent(content, field, value)
	})
}

// RemoveFieldFromContent removes a field from ticket content.
// Returns the new content or nil if field not found (no change needed).
func RemoveFieldFromContent(content []byte, field string) []byte {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false
	removeIndex := -1
	prefix := field + ": "

	for lineIdx, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		if inFrontmatter && strings.HasPrefix(line, prefix) {
			removeIndex = lineIdx

			break
		}
	}

	// Field not found - that's okay, nothing to remove
	if removeIndex == -1 {
		return nil
	}

	// Remove the line
	lines = append(lines[:removeIndex], lines[removeIndex+1:]...)

	return []byte(strings.Join(lines, "\n"))
}

// RemoveTicketField removes a field from the ticket frontmatter.
// Deprecated: Use RemoveTicketFieldLocked for concurrent-safe updates.
func RemoveTicketField(path, field string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent := RemoveFieldFromContent(content, field)
	if newContent == nil {
		return nil // nothing to remove
	}

	writeErr := atomic.WriteFile(path, strings.NewReader(string(newContent)))
	if writeErr != nil {
		return fmt.Errorf("writing ticket: %w", writeErr)
	}

	return nil
}

// RemoveTicketFieldLocked removes a field with file locking.
func RemoveTicketFieldLocked(path, field string) error {
	return WithTicketLock(path, func(content []byte) ([]byte, error) {
		return RemoveFieldFromContent(content, field), nil
	})
}

// Summary contains the essential fields from a ticket's frontmatter.
type Summary struct {
	SchemaVersion int
	ID            string
	Status        string
	BlockedBy     []string
	Parent        string // empty if no parent
	Created       string // Keep as string for simplicity
	Type          string
	Priority      int
	Assignee      string
	Closed        string // Optional, only if status=closed
	Title         string
	Path          string
}

// Result holds the result of parsing a single ticket file.
type Result struct {
	Summary *Summary
	Path    string
	Err     error
}

// MaxFrontmatterLines is the maximum number of lines allowed in frontmatter.
// If the closing delimiter is not found within this limit, parsing fails.
const MaxFrontmatterLines = 100

// Validation errors for ticket parsing.
var (
	errMissingField        = errors.New("missing required field")
	errInvalidFieldValue   = errors.New("invalid field value")
	errNoFrontmatter       = errors.New("no frontmatter found")
	errUnclosedFrontmatter = errors.New("unclosed frontmatter")
	errFrontmatterTooLong  = errors.New("frontmatter exceeds maximum line limit")
	errNoTitle             = errors.New("no title found")
	errOffsetOutOfBounds   = errors.New("offset out of bounds")
)

// Valid ticket statuses.
var validStatuses = []string{StatusOpen, StatusInProgress, StatusClosed}

func isValidTicketStatus(status string) bool {
	return slices.Contains(validStatuses, status)
}

// IsValidTicketType returns true if the given type is a valid ticket type.
func IsValidTicketType(ticketType string) bool {
	return slices.Contains(validTypes, ticketType)
}

// ListTicketsOptions configures ListTickets behavior.
type ListTicketsOptions struct {
	Status    string // filter by status ("" = all)
	Priority  int    // filter by priority (0 = all)
	Type      string // filter by type ("" = all)
	Parent    string // filter by parent ID ("" = all)
	RootsOnly bool   // only tickets without parent
	Limit     int    // max tickets to return (0 = no limit)
	Offset    int    // skip first N matching tickets
}

// ListTickets reads all ticket files from a directory and returns parsed summaries.
// Uses a binary write-through cache with directory mtime validation.
// Returns (nil, err) if directory cannot be read.
// Returns (results, nil) if directory was read - individual results may have errors.
func ListTickets(ticketDir string, opts ListTicketsOptions, diagOut io.Writer) ([]Result, error) {
	if diagOut == nil {
		diagOut = io.Discard
	}

	// Ticket directory missing => no tickets.
	_, statErr := os.Stat(ticketDir)
	if os.IsNotExist(statErr) {
		return []Result{}, nil
	}

	if statErr != nil {
		return nil, fmt.Errorf("reading ticket directory: %w", statErr)
	}

	cachePath := filepath.Join(ticketDir, CacheFileName)

	cache, cacheErr := LoadBinaryCache(ticketDir)
	if cacheErr != nil {
		if !errors.Is(cacheErr, errCacheNotFound) {
			// Corrupt or incompatible cache - rebuild.
			_, _ = fmt.Fprintln(diagOut, "loading cache: invalid format, rebuilding")
		}

		results, rebuildErr := BuildCacheParallelLocked(ticketDir, nil)
		if rebuildErr != nil {
			return nil, rebuildErr
		}

		return filterTicketResults(results, opts)
	}

	defer func() { _ = cache.Close() }()

	needReconcile, mtimeErr := dirMtimeNewerThanCache(ticketDir, cachePath)
	if mtimeErr != nil {
		return nil, mtimeErr
	}

	var reconcileResults []Result

	if needReconcile {
		// Reconcile is a write operation; do it under a lock starting from the latest cache.
		_ = cache.Close()
		cache = nil

		var reconcileErr error

		reconcileResults, reconcileErr = reconcileCacheOnDisk(ticketDir)
		if reconcileErr != nil {
			return nil, reconcileErr
		}

		cache, cacheErr = LoadBinaryCache(ticketDir)
		if cacheErr != nil {
			// Cache should exist after reconcile; fall back to full rebuild.
			_, _ = fmt.Fprintln(diagOut, "loading cache: invalid format, rebuilding")

			results, rebuildErr := BuildCacheParallelLocked(ticketDir, nil)
			if rebuildErr != nil {
				return nil, rebuildErr
			}

			filtered, filterErr := filterTicketResults(results, opts)
			if filterErr != nil {
				return nil, filterErr
			}

			return append(filtered, reconcileResults...), nil
		}

		defer func() { _ = cache.Close() }()
	}

	statusFilter := -1
	if opts.Status != "" {
		statusFilter = int(statusStringToByte(opts.Status))
	}

	priorityFilter := opts.Priority

	typeFilter := -1
	if opts.Type != "" {
		typeFilter = int(typeStringToByte(opts.Type))
	}

	indexes := cache.FilterEntriesWithOpts(FilterEntriesOpts{
		Status:    statusFilter,
		Priority:  priorityFilter,
		Type:      typeFilter,
		Parent:    opts.Parent,
		RootsOnly: opts.RootsOnly,
		Limit:     opts.Limit,
		Offset:    opts.Offset,
	})
	if indexes == nil {
		return nil, errOffsetOutOfBounds
	}

	results := make([]Result, 0, len(indexes)+len(reconcileResults))

	for _, idx := range indexes {
		_, summary := cache.GetEntryByIndex(idx)
		s := summary
		results = append(results, Result{Path: s.Path, Summary: &s})
	}

	// Include reconcile parse errors (warnings) without affecting pagination.
	results = append(results, reconcileResults...)

	return results, nil
}

func filterTicketResults(results []Result, opts ListTicketsOptions) ([]Result, error) {
	filtered := make([]Result, 0, len(results))

	limitReached := false
	matchCount := 0
	emitted := 0

	for _, result := range results {
		if result.Err != nil {
			// Always include parse errors as warnings.
			filtered = append(filtered, result)

			continue
		}

		if !ticketSummaryMatches(result.Summary, opts) {
			continue
		}

		if matchCount < opts.Offset {
			matchCount++

			continue
		}

		matchCount++

		if limitReached {
			continue
		}

		filtered = append(filtered, result)
		emitted++

		if opts.Limit > 0 && emitted >= opts.Limit {
			limitReached = true
		}
	}

	if opts.Offset > 0 && matchCount <= opts.Offset {
		return nil, errOffsetOutOfBounds
	}

	return filtered, nil
}

func ticketSummaryMatches(summary *Summary, opts ListTicketsOptions) bool {
	if opts.Status != "" && summary.Status != opts.Status {
		return false
	}

	if opts.Priority != 0 && summary.Priority != opts.Priority {
		return false
	}

	if opts.Type != "" && summary.Type != opts.Type {
		return false
	}

	if opts.Parent != "" && summary.Parent != opts.Parent {
		return false
	}

	if opts.RootsOnly && summary.Parent != "" {
		return false
	}

	return true
}

type parseJob struct {
	idx      int
	filename string
	path     string
	entry    os.DirEntry
}

const cacheBuildWorkers = 16

// buildCacheParallel builds the cache from scratch by parsing all ticket files in parallel.
// If entries is nil, it reads the directory.
func buildCacheParallel(ticketDir string, entries []os.DirEntry) ([]Result, error) {
	if entries == nil {
		var err error

		entries, err = os.ReadDir(ticketDir)
		if err != nil {
			return nil, fmt.Errorf("reading ticket directory: %w", err)
		}
	}

	jobs := make([]parseJob, 0, len(entries))

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}

		jobs = append(jobs, parseJob{
			idx:      len(jobs),
			filename: name,
			path:     filepath.Join(ticketDir, name),
			entry:    entry,
		})
	}

	results := make([]Result, len(jobs))
	cacheEntries := make(map[string]CacheEntry, len(jobs))

	workerCount := min(len(jobs), cacheBuildWorkers)
	jobCh := make(chan parseJob, workerCount)

	var waitGroup sync.WaitGroup

	var cacheMu sync.Mutex

	worker := func() {
		defer waitGroup.Done()

		for job := range jobCh {
			summary, parseErr := ParseTicketFrontmatter(job.path)
			if parseErr != nil {
				results[job.idx] = Result{Path: job.path, Err: parseErr}

				continue
			}

			info, infoErr := job.entry.Info()
			if infoErr != nil {
				results[job.idx] = Result{Path: job.path, Err: infoErr}

				continue
			}

			results[job.idx] = Result{Path: job.path, Summary: &summary}

			cacheMu.Lock()

			cacheEntries[job.filename] = CacheEntry{Mtime: info.ModTime(), Summary: summary}

			cacheMu.Unlock()
		}
	}

	waitGroup.Add(workerCount)

	for range workerCount {
		go worker()
	}

	for _, job := range jobs {
		jobCh <- job
	}

	close(jobCh)

	waitGroup.Wait()

	// Always write cache (even if empty) to establish cache file mtime.
	cachePath := filepath.Join(ticketDir, CacheFileName)

	err := writeBinaryCache(cachePath, cacheEntries)
	if err != nil {
		return nil, fmt.Errorf("writing cache: %w", err)
	}

	return results, nil
}

// BuildCacheParallelLocked rebuilds the cache in parallel while holding the lock.
func BuildCacheParallelLocked(ticketDir string, entries []os.DirEntry) ([]Result, error) {
	cachePath := filepath.Join(ticketDir, CacheFileName)

	var results []Result

	err := WithLock(cachePath, func() error {
		var buildErr error

		results, buildErr = buildCacheParallel(ticketDir, entries)

		return buildErr
	})
	if err != nil {
		return nil, err
	}

	return results, nil
}

func reconcileCacheOnDisk(ticketDir string) ([]Result, error) {
	cachePath := filepath.Join(ticketDir, CacheFileName)

	var reconcileResults []Result

	err := WithLock(cachePath, func() error {
		cache, err := LoadBinaryCache(ticketDir)
		if err != nil {
			// Missing or invalid cache - just rebuild from scratch.
			results, rebuildErr := buildCacheParallel(ticketDir, nil)
			if rebuildErr != nil {
				return rebuildErr
			}

			for _, r := range results {
				if r.Err != nil {
					reconcileResults = append(reconcileResults, r)
				}
			}

			return nil
		}

		defer func() { _ = cache.Close() }()

		entries := cacheEntriesAsRawMap(cache)

		dirEntries, err := os.ReadDir(ticketDir)
		if err != nil {
			return fmt.Errorf("reading ticket directory: %w", err)
		}

		currentFiles := make(map[string]os.DirEntry, len(dirEntries))

		for _, entry := range dirEntries {
			if entry.IsDir() {
				continue
			}

			name := entry.Name()
			if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
				continue
			}

			currentFiles[name] = entry

			if _, ok := entries[name]; ok {
				continue
			}

			// New file: parse and add.
			path := filepath.Join(ticketDir, name)

			summary, parseErr := ParseTicketFrontmatter(path)
			if parseErr != nil {
				reconcileResults = append(reconcileResults, Result{Path: path, Err: parseErr})

				continue
			}

			info, infoErr := entry.Info()
			if infoErr != nil {
				reconcileResults = append(reconcileResults, Result{Path: path, Err: infoErr})

				continue
			}

			data, encErr := encodeSummaryData(&summary)
			if encErr != nil {
				reconcileResults = append(reconcileResults, Result{Path: path, Err: fmt.Errorf("caching ticket: %w", encErr)})

				continue
			}

			prio, prioErr := priorityToUint8(summary.Priority)
			if prioErr != nil {
				reconcileResults = append(reconcileResults, Result{Path: path, Err: fmt.Errorf("caching ticket: %w", prioErr)})

				continue
			}

			entries[name] = rawCacheEntry{
				filename:   name,
				mtime:      info.ModTime().UnixNano(),
				status:     statusStringToByte(summary.Status),
				priority:   prio,
				ticketType: typeStringToByte(summary.Type),
				data:       data,
			}
		}

		for filename := range entries {
			if _, ok := currentFiles[filename]; !ok {
				delete(entries, filename)
			}
		}

		writeErr := writeBinaryCacheRaw(cachePath, entries)
		if writeErr != nil {
			return writeErr
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return reconcileResults, nil
}

// ParseTicketFrontmatter parses a ticket file and extracts the summary.
// Reads only until the title line for efficiency.
func ParseTicketFrontmatter(path string) (Summary, error) {
	file, err := os.Open(path)
	if err != nil {
		return Summary{}, fmt.Errorf("opening ticket: %w", err)
	}

	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)

	// Track parsing state
	inFrontmatter := false
	frontmatterStarted := false
	frontmatterEnded := false
	lineCount := 0

	// Fields to extract
	var summary Summary

	summary.Path = path

	// Track which required fields we've seen
	hasSchemaVersion := false
	hasID := false
	hasStatus := false
	hasType := false
	hasPriority := false
	hasCreated := false

	for scanner.Scan() {
		line := scanner.Text()
		lineCount++

		// Check for runaway frontmatter (missing closing delimiter)
		if inFrontmatter && lineCount > MaxFrontmatterLines {
			return Summary{}, errFrontmatterTooLong
		}

		// Handle frontmatter delimiters
		if line == frontmatterDelimiter {
			if !frontmatterStarted {
				frontmatterStarted = true
				inFrontmatter = true

				continue
			}

			// End of frontmatter
			inFrontmatter = false
			frontmatterEnded = true

			continue
		}

		// Parse frontmatter fields
		if inFrontmatter {
			if idx := strings.Index(line, ": "); idx > 0 {
				key := line[:idx]
				value := line[idx+2:]

				switch key {
				case "schema_version":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: schema_version (empty)", errInvalidFieldValue)
					}

					schemaVersion, parseErr := parseInt(value)
					if parseErr != nil {
						return Summary{}, fmt.Errorf("%w: schema_version %q", errInvalidFieldValue, value)
					}

					if schemaVersion < 1 {
						return Summary{}, fmt.Errorf("%w: schema_version must be positive", errInvalidFieldValue)
					}

					if schemaVersion != 1 {
						return Summary{}, fmt.Errorf("%w: %d (only version 1 supported)", ErrUnsupportedSchemaVersion, schemaVersion)
					}

					summary.SchemaVersion = schemaVersion
					hasSchemaVersion = true

				case "id":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: id (empty)", errInvalidFieldValue)
					}

					summary.ID = value
					hasID = true

				case "status":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: status (empty)", errInvalidFieldValue)
					}

					if !isValidTicketStatus(value) {
						return Summary{}, fmt.Errorf("%w: status %q", errInvalidFieldValue, value)
					}

					summary.Status = value
					hasStatus = true

				case "type":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: type (empty)", errInvalidFieldValue)
					}

					if !IsValidTicketType(value) {
						return Summary{}, fmt.Errorf("%w: type %q", errInvalidFieldValue, value)
					}

					summary.Type = value
					hasType = true

				case "priority":
					priority, parseErr := parseInt(value)
					if parseErr != nil {
						return Summary{}, fmt.Errorf("%w: priority %q", errInvalidFieldValue, value)
					}

					if !IsValidPriority(priority) {
						return Summary{}, fmt.Errorf("%w: priority %d (out of range)", errInvalidFieldValue, priority)
					}

					summary.Priority = priority
					hasPriority = true

				case "created":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: created (empty)", errInvalidFieldValue)
					}

					_, parseErr := time.Parse(time.RFC3339, value)
					if parseErr != nil {
						return Summary{}, fmt.Errorf("%w: created %q", errInvalidFieldValue, value)
					}

					summary.Created = value
					hasCreated = true

				case "blocked-by":
					blockedBy, parseErr := parseBlockedByValue(value)
					if parseErr != nil {
						return Summary{}, parseErr
					}

					summary.BlockedBy = blockedBy

				case "parent":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: parent (empty)", errInvalidFieldValue)
					}

					summary.Parent = value

				case "assignee":
					if value == "" {
						return Summary{}, fmt.Errorf("%w: assignee (empty)", errInvalidFieldValue)
					}

					summary.Assignee = value

				case fieldClosed:
					if value == "" {
						return Summary{}, fmt.Errorf("%w: closed (empty)", errInvalidFieldValue)
					}

					_, parseErr := time.Parse(time.RFC3339, value)
					if parseErr != nil {
						return Summary{}, fmt.Errorf("%w: closed %q", errInvalidFieldValue, value)
					}

					summary.Closed = value
				}
			}

			continue
		}

		// After frontmatter, look for title
		if frontmatterEnded && strings.HasPrefix(line, "# ") {
			title := strings.TrimPrefix(line, "# ")
			if title == "" {
				return Summary{}, fmt.Errorf("%w: title (empty)", errInvalidFieldValue)
			}

			summary.Title = title

			break // Done parsing
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		return Summary{}, fmt.Errorf("scanning ticket: %w", scanErr)
	}

	// Validate we got frontmatter
	if !frontmatterStarted {
		return Summary{}, errNoFrontmatter
	}

	if !frontmatterEnded {
		return Summary{}, errUnclosedFrontmatter
	}

	// Validate required fields
	if !hasSchemaVersion {
		return Summary{}, ErrMissingSchemaVersion
	}

	if !hasID {
		return Summary{}, fmt.Errorf("%w: id", errMissingField)
	}

	if !hasStatus {
		return Summary{}, fmt.Errorf("%w: status", errMissingField)
	}

	if !hasType {
		return Summary{}, fmt.Errorf("%w: type", errMissingField)
	}

	if !hasPriority {
		return Summary{}, fmt.Errorf("%w: priority", errMissingField)
	}

	if !hasCreated {
		return Summary{}, fmt.Errorf("%w: created", errMissingField)
	}

	if summary.Title == "" {
		return Summary{}, errNoTitle
	}

	// Cross-field validation
	if summary.Status == StatusClosed && summary.Closed == "" {
		return Summary{}, ErrClosedWithoutTimestamp
	}

	if summary.Status != StatusClosed && summary.Closed != "" {
		return Summary{}, ErrClosedTimestampOnNonClosed
	}

	return summary, nil
}

func parseInt(s string) (int, error) {
	var result int

	_, err := fmt.Sscanf(s, "%d", &result)
	if err != nil {
		return 0, fmt.Errorf("parsing integer: %w", err)
	}

	return result, nil
}

func parseBlockedByValue(value string) ([]string, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		return nil, fmt.Errorf("%w: blocked-by (invalid format)", errInvalidFieldValue)
	}

	if value == "[]" {
		return []string{}, nil
	}

	if !strings.HasPrefix(value, "[") || !strings.HasSuffix(value, "]") {
		return nil, fmt.Errorf("%w: blocked-by (missing brackets)", errInvalidFieldValue)
	}

	inner := strings.TrimPrefix(strings.TrimSuffix(value, "]"), "[")
	if inner == "" {
		return []string{}, nil
	}

	parts := strings.Split(inner, ",")
	blockedBy := make([]string, 0, len(parts))

	for _, p := range parts {
		id := strings.TrimSpace(p)
		if id != "" {
			blockedBy = append(blockedBy, id)
		}
	}

	return blockedBy, nil
}

// GetBlockedByFromContent extracts the blocked-by list from ticket content.
func GetBlockedByFromContent(content []byte) ([]string, error) {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false

	for _, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		if inFrontmatter && strings.HasPrefix(line, "blocked-by: ") {
			value := strings.TrimPrefix(line, "blocked-by: ")

			return parseBlockedByValue(value)
		}
	}

	// blocked-by not found, return empty list
	return []string{}, nil
}

// ReadTicketBlockedBy reads the blocked-by list from a ticket file.
func ReadTicketBlockedBy(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading ticket: %w", err)
	}

	return GetBlockedByFromContent(content)
}

// GetParentFromContent extracts the parent field from ticket content.
func GetParentFromContent(content []byte) string {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false

	for _, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		if inFrontmatter && strings.HasPrefix(line, "parent: ") {
			return strings.TrimPrefix(line, "parent: ")
		}
	}

	return ""
}

// ReadTicketParent reads the parent field from a ticket file.
func ReadTicketParent(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading ticket: %w", err)
	}

	return GetParentFromContent(content), nil
}

// FindChildren returns IDs of all tickets that have the given ticket as parent.
func FindChildren(ticketDir, parentID string) ([]string, error) {
	entries, err := os.ReadDir(ticketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("reading ticket directory: %w", err)
	}

	var children []string

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(ticketDir, name)

		parent, err := ReadTicketParent(path)
		if err != nil {
			continue // Skip files we can't read
		}

		if parent == parentID {
			ticketID := strings.TrimSuffix(name, ".md")
			children = append(children, ticketID)
		}
	}

	return children, nil
}

// FindOpenChildren returns IDs of children that are not closed.
func FindOpenChildren(ticketDir, parentID string) ([]string, error) {
	children, err := FindChildren(ticketDir, parentID)
	if err != nil {
		return nil, err
	}

	var openChildren []string

	for _, childID := range children {
		path := Path(ticketDir, childID)

		status, err := ReadTicketStatus(path)
		if err != nil {
			continue // Skip files we can't read
		}

		if status != StatusClosed {
			openChildren = append(openChildren, childID)
		}
	}

	return openChildren, nil
}

// UpdateBlockedByInContent updates the blocked-by field in ticket content.
// Returns the new content or an error.
func UpdateBlockedByInContent(content []byte, blockedBy []string) ([]byte, error) {
	lines := strings.Split(string(content), "\n")
	inFrontmatter := false
	updated := false

	for lineIdx, line := range lines {
		if line == frontmatterDelimiter {
			if inFrontmatter {
				break // End of frontmatter
			}

			inFrontmatter = true

			continue
		}

		if inFrontmatter && strings.HasPrefix(line, "blocked-by: ") {
			lines[lineIdx] = "blocked-by: " + formatBlockedBy(blockedBy)
			updated = true

			break
		}
	}

	if !updated {
		return nil, ErrBlockedByNotFound
	}

	return []byte(strings.Join(lines, "\n")), nil
}

// UpdateTicketBlockedBy updates the blocked-by list in a ticket file.
// Deprecated: Use UpdateTicketBlockedByLocked for concurrent-safe updates.
func UpdateTicketBlockedBy(path string, blockedBy []string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent, err := UpdateBlockedByInContent(content, blockedBy)
	if err != nil {
		return err
	}

	writeErr := atomic.WriteFile(path, strings.NewReader(string(newContent)))
	if writeErr != nil {
		return fmt.Errorf("writing ticket: %w", writeErr)
	}

	return nil
}

// UpdateTicketBlockedByLocked updates the blocked-by field with file locking.
func UpdateTicketBlockedByLocked(path string, blockedBy []string) error {
	return WithTicketLock(path, func(content []byte) ([]byte, error) {
		return UpdateBlockedByInContent(content, blockedBy)
	})
}
