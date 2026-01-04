package main

import (
	"bufio"
	"encoding/base32"
	"errors"
	"fmt"
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
	ID          string
	Status      string
	BlockedBy   []string
	Created     time.Time
	Type        string
	Priority    int
	Assignee    string
	ExternalRef string
	Title       string
	Description string
	Design      string
	Acceptance  string
}

// validTypes are the allowed ticket types.
var validTypes = []string{"bug", "feature", "task", "epic", "chore"} //nolint:gochecknoglobals // package-level constant

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

//nolint:gochecknoglobals // package-level constant
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
	if !TicketExists(ticketDir, base) {
		return base, nil
	}

	// Append letter suffixes: a, b, ..., z, za, zb, ..., zz, zza, ...
	suffix := ""

	for {
		suffix = nextSuffix(suffix)
		candidate := base + suffix

		if !TicketExists(ticketDir, candidate) {
			return candidate, nil
		}

		// Safety limit to prevent infinite loop
		if len(suffix) > maxSuffixLength {
			return "", errIDGenerationFailed
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
func FormatTicket(ticket Ticket) string {
	var builder strings.Builder

	// YAML frontmatter
	builder.WriteString("---\n")
	builder.WriteString("id: " + ticket.ID + "\n")
	builder.WriteString("status: " + ticket.Status + "\n")
	builder.WriteString("blocked-by: " + formatBlockedBy(ticket.BlockedBy) + "\n")
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
func WriteTicket(ticketDir string, ticket Ticket) (string, error) {
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
		return "", fmt.Errorf("%w: %s", errTicketFileExists, path)
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
func WriteTicketAtomic(ticketDir string, ticket Ticket) (string, string, error) {
	// Ensure directory exists first (before locking)
	mkdirErr := os.MkdirAll(ticketDir, dirPerms)
	if mkdirErr != nil {
		return "", "", fmt.Errorf("failed to create ticket directory: %w", mkdirErr)
	}

	// Generate base ID and lock on it to serialize concurrent creates
	baseID := GenerateID()
	lockPath := filepath.Join(ticketDir, baseID+".md")

	var ticketID, ticketPath string

	lockErr := WithLock(lockPath, func() error {
		// Inside lock: find unique ID (base or with suffix)
		var genErr error

		ticketID, genErr = GenerateUniqueID(ticketDir)
		if genErr != nil {
			return genErr
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

// TicketExists checks if a ticket ID exists in the ticket directory.
func TicketExists(ticketDir, ticketID string) bool {
	path := filepath.Join(ticketDir, ticketID+".md")

	_, statErr := os.Stat(path)

	return statErr == nil
}

// TicketPath returns the full path to a ticket file.
func TicketPath(ticketDir, ticketID string) string {
	return filepath.Join(ticketDir, ticketID+".md")
}

// ReadTicketStatus reads just the status field from a ticket file.
// getStatusFromContent extracts the status field from ticket content.
func getStatusFromContent(content []byte) (string, error) {
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

	return "", errStatusNotFound
}

// ReadTicketStatus reads just the status field from a ticket file.
func ReadTicketStatus(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return "", fmt.Errorf("reading ticket: %w", err)
	}

	return getStatusFromContent(content)
}

// updateStatusInContent updates the status field in ticket content.
// Returns the new content or an error.
func updateStatusInContent(content []byte, newStatus string) ([]byte, error) {
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
		return nil, errStatusNotFound
	}

	return []byte(strings.Join(lines, "\n")), nil
}

// UpdateTicketStatus updates the status field in a ticket file.
// Deprecated: Use UpdateTicketStatusLocked for concurrent-safe updates.
func UpdateTicketStatus(path, newStatus string) error {
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent, err := updateStatusInContent(content, newStatus)
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
		return updateStatusInContent(content, newStatus)
	})
}

// ReadTicket reads and returns the raw contents of a ticket file.
func ReadTicket(path string) (string, error) {
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return "", fmt.Errorf("reading ticket: %w", err)
	}

	return string(content), nil
}

// addFieldToContent adds a new field to ticket content (after status).
// Returns the new content or an error.
func addFieldToContent(content []byte, field, value string) ([]byte, error) {
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
		return nil, errFieldInsertFailed
	}

	// Insert new field
	newLine := field + ": " + value
	lines = append(lines[:insertIndex], append([]string{newLine}, lines[insertIndex:]...)...)

	return []byte(strings.Join(lines, "\n")), nil
}

// AddTicketField adds a new field to the ticket frontmatter (after status).
// Deprecated: Use AddTicketFieldLocked for concurrent-safe updates.
func AddTicketField(path, field, value string) error {
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent, err := addFieldToContent(content, field, value)
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
		return addFieldToContent(content, field, value)
	})
}

// removeFieldFromContent removes a field from ticket content.
// Returns the new content or nil if field not found (no change needed).
func removeFieldFromContent(content []byte, field string) []byte {
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
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent := removeFieldFromContent(content, field)
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
		return removeFieldFromContent(content, field), nil
	})
}

// TicketSummary contains the essential fields from a ticket's frontmatter.
type TicketSummary struct {
	ID        string
	Status    string
	BlockedBy []string
	Created   string // Keep as string for simplicity
	Type      string
	Priority  int
	Assignee  string
	Closed    string // Optional, only if status=closed
	Title     string
	Path      string
}

// TicketResult holds the result of parsing a single ticket file.
type TicketResult struct {
	Summary *TicketSummary
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
//
//nolint:gochecknoglobals // package-level constant
var validStatuses = []string{StatusOpen, StatusInProgress, StatusClosed}

func isValidTicketStatus(status string) bool {
	return slices.Contains(validStatuses, status)
}

func isValidTicketType(ticketType string) bool {
	return slices.Contains(validTypes, ticketType)
}

// ListTicketsOptions configures ListTickets behavior.
type ListTicketsOptions struct {
	NeedAll bool // true if caller needs all tickets (e.g., has status filter)
	Limit   int  // max tickets to return (0 = no limit)
	Offset  int  // skip first N tickets
}

// listTicketsState holds state for parallel ticket listing.
type listTicketsState struct {
	cache   *BinaryCache
	cacheMu sync.RWMutex
	results []TicketResult
}

// ListTickets reads all ticket files from a directory and returns parsed summaries.
// Uses parallel file reading for performance with mtime-based caching.
// Returns (nil, err) if directory cannot be read.
// Returns (results, nil) if directory was read - individual results may have errors.
//
//nolint:cyclop,funlen // iteration logic requires multiple conditions
func ListTickets(ticketDir string, opts ListTicketsOptions) ([]TicketResult, error) {
	entries, err := os.ReadDir(ticketDir)
	if os.IsNotExist(err) {
		// Directory doesn't exist = no tickets
		return []TicketResult{}, nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading ticket directory: %w", err)
	}

	// Load cache
	cache, cacheErr := LoadBinaryCache(ticketDir)
	cacheWasCold := cacheErr != nil

	if cacheWasCold {
		// Cache missing, corrupt, or version mismatch - start fresh
		cache = NewBinaryCache()

		// If old format or corrupt, delete in background
		if cacheErr != nil && !errors.Is(cacheErr, errCacheNotFound) {
			go func() {
				_ = DeleteCache(ticketDir)
			}()
		}
	}
	defer func() {
		if cache != nil {
			_ = cache.Close()
		}
	}()

	// If cache was cold, we must process ALL files to build cache
	// Then apply offset/limit in memory at the end
	// If cache is warm and NeedAll=false, we can skip files
	canSkipFiles := !cacheWasCold && !opts.NeedAll

	state := &listTicketsState{
		cache:   cache,
		results: make([]TicketResult, len(entries)),
	}

	var waitGroup sync.WaitGroup

	mdCount := 0
	resultIdx := 0

	for _, entry := range entries {
		// Skip directories and non-.md files (.cache, .lock files, etc)
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}

		// Only skip files if cache is warm and NeedAll=false
		if canSkipFiles {
			// Skip for offset
			if mdCount < opts.Offset {
				mdCount++

				continue
			}

			// Stop at limit (limit=0 means no limit)
			if opts.Limit > 0 && mdCount >= opts.Offset+opts.Limit {
				break
			}
		}

		mdCount++

		idx := resultIdx // capture index for goroutine
		resultIdx++

		path := filepath.Join(ticketDir, entry.Name())
		filename := entry.Name()

		waitGroup.Add(1)

		go processTicketEntry(state, &waitGroup, idx, path, filename, entry)
	}

	waitGroup.Wait()

	// Save cache if changed
	if state.cache.HasChanges() {
		_ = SaveBinaryCache(ticketDir, state.cache)
	}

	results := state.results[:resultIdx]

	// Check for out-of-bounds offset (only when not NeedAll, since status filter handles its own offset)
	if !opts.NeedAll && opts.Offset > 0 {
		// For warm cache path: resultIdx is 0 if offset was beyond available files
		// For cold cache path: check against total results before applying offset
		totalCount := len(results)
		if cacheWasCold {
			// Cold cache: all files in results, check before applying offset
			if opts.Offset >= totalCount {
				return nil, errOffsetOutOfBounds
			}
		} else if resultIdx == 0 {
			// Warm cache: if we processed 0 files with offset > 0, offset was out of bounds
			return nil, errOffsetOutOfBounds
		}
	}

	// If cache was cold and we processed all files, apply offset/limit now
	if cacheWasCold && !opts.NeedAll {
		results = applyOffsetLimit(results, opts.Offset, opts.Limit)
	}

	return results, nil
}

// applyOffsetLimit applies offset and limit to results slice.
// limit=0 means no limit (show all).
func applyOffsetLimit(results []TicketResult, offset, limit int) []TicketResult {
	if offset >= len(results) {
		return []TicketResult{}
	}

	results = results[offset:]

	if limit > 0 && limit < len(results) {
		results = results[:limit]
	}

	return results
}

// processTicketEntry processes a single ticket file entry.
func processTicketEntry(
	state *listTicketsState,
	waitGroup *sync.WaitGroup,
	idx int,
	path, filename string,
	entry os.DirEntry,
) {
	defer waitGroup.Done()

	// Get file info for mtime
	info, infoErr := entry.Info()
	if infoErr != nil {
		state.results[idx] = TicketResult{Path: path, Err: infoErr}

		return
	}

	mtime := info.ModTime()

	// Check cache mtime first (fast - only reads index, not data)
	state.cacheMu.RLock()
	cachedMtime := state.cache.LookupMtime(filename)
	state.cacheMu.RUnlock()

	if !cachedMtime.IsZero() && cachedMtime.Equal(mtime) {
		// Mtime matches - now load full cached data
		state.cacheMu.RLock()
		cached := state.cache.Lookup(filename)
		state.cacheMu.RUnlock()

		if cached != nil {
			summary := cached.Summary
			summary.Path = path
			state.results[idx] = TicketResult{Path: path, Summary: &summary}
			// Mark as valid so it's preserved on save
			state.cacheMu.Lock()
			state.cache.MarkValid(filename)
			state.cacheMu.Unlock()

			return
		}
	}

	// Cache miss - parse file
	summary, parseErr := ParseTicketFrontmatter(path)

	state.results[idx] = TicketResult{Path: path, Err: parseErr}
	if parseErr == nil {
		state.results[idx].Summary = &summary
		state.cacheMu.Lock()
		state.cache.Update(filename, CacheEntry{Mtime: mtime, Summary: summary})
		state.cacheMu.Unlock()
	}
}

// ParseTicketFrontmatter parses a ticket file and extracts the summary.
// Reads only until the title line for efficiency.
//
//nolint:funlen,cyclop,gocognit,gocyclo,nestif,maintidx // parsing logic is inherently complex
func ParseTicketFrontmatter(path string) (TicketSummary, error) {
	file, err := os.Open(path) //nolint:gosec // path comes from directory listing
	if err != nil {
		return TicketSummary{}, fmt.Errorf("opening ticket: %w", err)
	}

	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)

	// Track parsing state
	inFrontmatter := false
	frontmatterStarted := false
	frontmatterEnded := false
	lineCount := 0

	// Fields to extract
	var summary TicketSummary

	summary.Path = path

	// Track which required fields we've seen
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
			return TicketSummary{}, errFrontmatterTooLong
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
				case "id":
					if value == "" {
						return TicketSummary{}, fmt.Errorf("%w: id (empty)", errInvalidFieldValue)
					}

					summary.ID = value
					hasID = true

				case "status":
					if value == "" {
						return TicketSummary{}, fmt.Errorf("%w: status (empty)", errInvalidFieldValue)
					}

					if !isValidTicketStatus(value) {
						return TicketSummary{}, fmt.Errorf("%w: status %q", errInvalidFieldValue, value)
					}

					summary.Status = value
					hasStatus = true

				case "type":
					if value == "" {
						return TicketSummary{}, fmt.Errorf("%w: type (empty)", errInvalidFieldValue)
					}

					if !isValidTicketType(value) {
						return TicketSummary{}, fmt.Errorf("%w: type %q", errInvalidFieldValue, value)
					}

					summary.Type = value
					hasType = true

				case "priority":
					priority, parseErr := parseInt(value)
					if parseErr != nil {
						return TicketSummary{}, fmt.Errorf("%w: priority %q", errInvalidFieldValue, value)
					}

					if !IsValidPriority(priority) {
						return TicketSummary{}, fmt.Errorf("%w: priority %d (out of range)", errInvalidFieldValue, priority)
					}

					summary.Priority = priority
					hasPriority = true

				case "created":
					if value == "" {
						return TicketSummary{}, fmt.Errorf("%w: created (empty)", errInvalidFieldValue)
					}

					_, parseErr := time.Parse(time.RFC3339, value)
					if parseErr != nil {
						return TicketSummary{}, fmt.Errorf("%w: created %q", errInvalidFieldValue, value)
					}

					summary.Created = value
					hasCreated = true

				case "blocked-by":
					blockedBy, parseErr := parseBlockedByValue(value)
					if parseErr != nil {
						return TicketSummary{}, parseErr
					}

					summary.BlockedBy = blockedBy

				case "assignee":
					if value == "" {
						return TicketSummary{}, fmt.Errorf("%w: assignee (empty)", errInvalidFieldValue)
					}

					summary.Assignee = value

				case fieldClosed:
					if value == "" {
						return TicketSummary{}, fmt.Errorf("%w: closed (empty)", errInvalidFieldValue)
					}

					_, parseErr := time.Parse(time.RFC3339, value)
					if parseErr != nil {
						return TicketSummary{}, fmt.Errorf("%w: closed %q", errInvalidFieldValue, value)
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
				return TicketSummary{}, fmt.Errorf("%w: title (empty)", errInvalidFieldValue)
			}

			summary.Title = title

			break // Done parsing
		}
	}

	scanErr := scanner.Err()
	if scanErr != nil {
		return TicketSummary{}, fmt.Errorf("scanning ticket: %w", scanErr)
	}

	// Validate we got frontmatter
	if !frontmatterStarted {
		return TicketSummary{}, errNoFrontmatter
	}

	if !frontmatterEnded {
		return TicketSummary{}, errUnclosedFrontmatter
	}

	// Validate required fields
	if !hasID {
		return TicketSummary{}, fmt.Errorf("%w: id", errMissingField)
	}

	if !hasStatus {
		return TicketSummary{}, fmt.Errorf("%w: status", errMissingField)
	}

	if !hasType {
		return TicketSummary{}, fmt.Errorf("%w: type", errMissingField)
	}

	if !hasPriority {
		return TicketSummary{}, fmt.Errorf("%w: priority", errMissingField)
	}

	if !hasCreated {
		return TicketSummary{}, fmt.Errorf("%w: created", errMissingField)
	}

	if summary.Title == "" {
		return TicketSummary{}, errNoTitle
	}

	// Cross-field validation
	if summary.Status == StatusClosed && summary.Closed == "" {
		return TicketSummary{}, errClosedWithoutTimestamp
	}

	if summary.Status != StatusClosed && summary.Closed != "" {
		return TicketSummary{}, errClosedTimestampOnNonClosed
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

// ReadTicketBlockedBy reads the blocked-by list from a ticket file.
// getBlockedByFromContent extracts the blocked-by list from ticket content.
func getBlockedByFromContent(content []byte) ([]string, error) {
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
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return nil, fmt.Errorf("reading ticket: %w", err)
	}

	return getBlockedByFromContent(content)
}

// UpdateTicketBlockedBy updates the blocked-by field in a ticket file.
// updateBlockedByInContent updates the blocked-by field in ticket content.
// Returns the new content or an error.
func updateBlockedByInContent(content []byte, blockedBy []string) ([]byte, error) {
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
		return nil, errBlockedByNotFound
	}

	return []byte(strings.Join(lines, "\n")), nil
}

// UpdateTicketBlockedBy updates the blocked-by field in a ticket file.
// Deprecated: Use UpdateTicketBlockedByLocked for concurrent-safe updates.
func UpdateTicketBlockedBy(path string, blockedBy []string) error {
	content, err := os.ReadFile(path) //nolint:gosec // path is validated by caller
	if err != nil {
		return fmt.Errorf("reading ticket: %w", err)
	}

	newContent, err := updateBlockedByInContent(content, blockedBy)
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
		return updateBlockedByInContent(content, blockedBy)
	})
}
