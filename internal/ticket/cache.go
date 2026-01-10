// Package ticket provides core ticket operations and caching.
package ticket

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/natefinch/atomic"
)

// Binary cache format constants.
const (
	cacheMagic       = "TKC1"
	cacheVersionNum  = 6 // Bumped for parent in index
	cacheHeaderSize  = 32
	indexEntrySize   = 68 // Was 56, added 12 for parent
	maxFilenameLen   = 32
	maxParentLen     = 12 // 11 char max ID + null terminator
	minCacheFileSize = cacheHeaderSize
)

// Status byte values for index.
const (
	statusByteOpen       = 0
	statusByteInProgress = 1
	statusByteClosed     = 2
)

// Type byte values for index.
const (
	typeByteBug     = 0
	typeByteFeature = 1
	typeByteTask    = 2
	typeByteEpic    = 3
	typeByteChore   = 4
)

// Binary cache errors.
var (
	errInvalidMagic    = errors.New("invalid cache magic")
	errVersionMismatch = errors.New("cache version mismatch")
	errFileTooSmall    = errors.New("cache file too small")
	errFileTooLarge    = errors.New("cache file too large")
	errFilenameTooLong = errors.New("filename too long")
	errTooManyEntries  = errors.New("too many cache entries")
	errNegativeMtime   = errors.New("negative mtime")
)

// BinaryCache provides read-only access to the ticket cache via mmap.
// The cache stores ticket summaries in a binary format for fast lookup
// without parsing markdown files. Must be closed after use to unmap memory.
type BinaryCache struct {
	data       []byte // mmap'd file contents, nil after Close
	entryCount int    // number of tickets in the cache, from header bytes 6-10
}

// LoadBinaryCache loads the cache using mmap.
// Returns errCacheNotFound if file doesn't exist.
// Returns errVersionMismatch if version doesn't match (caller should rebuild).
func LoadBinaryCache(ticketDir string) (*BinaryCache, error) {
	cachePath := filepath.Join(ticketDir, CacheFileName)

	file, err := os.Open(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errCacheNotFound
		}

		return nil, fmt.Errorf("opening cache: %w", err)
	}

	defer func() { _ = file.Close() }()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat cache: %w", err)
	}

	size := info.Size()
	if size < minCacheFileSize {
		return nil, errFileTooSmall
	}

	// mmap the file
	data, err := syscall.Mmap(int(file.Fd()), 0, int(size), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap cache: %w", err)
	}

	// Validate header
	if string(data[0:4]) != cacheMagic {
		_ = syscall.Munmap(data)

		return nil, errInvalidMagic
	}

	version := binary.LittleEndian.Uint16(data[4:6])
	if version != cacheVersionNum {
		_ = syscall.Munmap(data)

		return nil, errVersionMismatch
	}

	entryCount := int(binary.LittleEndian.Uint32(data[6:10]))

	// Validate file size covers header + index
	expectedMinSize := cacheHeaderSize + (entryCount * indexEntrySize)
	if int(size) < expectedMinSize {
		_ = syscall.Munmap(data)

		return nil, errFileTooSmall
	}

	// Validate all index entries have valid data offsets
	if size > uint32Max {
		_ = syscall.Munmap(data)

		return nil, errFileTooLarge
	}

	fileSize := uint32(size)

	// Validate all index entries point to data within file bounds.
	// This upfront check allows readDataEntry to skip bounds checking later.
	for i := range entryCount {
		offset := cacheHeaderSize + (i * indexEntrySize)
		// Index entry layout: filename(32) + mtime(8) + dataOffset(4) + dataLength(2) + ...
		dataOffset := binary.LittleEndian.Uint32(data[offset+32+8 : offset+32+8+4])
		dataLength := binary.LittleEndian.Uint16(data[offset+32+8+4 : offset+32+8+4+2])

		if dataOffset >= fileSize || uint32(dataLength) > fileSize-dataOffset {
			_ = syscall.Munmap(data)

			return nil, errCacheCorrupt
		}
	}

	return &BinaryCache{
		data:       data,
		entryCount: entryCount,
	}, nil
}

// Close unmaps the cache file and releases memory.
// Safe to call multiple times; subsequent calls are no-ops.
// After Close, all read methods will panic.
// The cache cannot be reopened; load a new BinaryCache instead.
func (bc *BinaryCache) Close() error {
	if bc.data == nil {
		return nil
	}

	err := syscall.Munmap(bc.data)
	bc.data = nil
	bc.entryCount = 0

	if err != nil {
		return fmt.Errorf("munmap cache: %w", err)
	}

	return nil
}

// Lookup finds an entry by filename using binary search.
// Returns nil if not found or if cache data is corrupted.
func (bc *BinaryCache) Lookup(filename string) *CacheEntry {
	if bc.data == nil {
		panic("BinaryCache: read from closed cache")
	}

	// Binary search in mmap'd index
	idx := bc.binarySearch(filename)
	if idx < 0 {
		return nil
	}

	// Parse the entry
	entry := bc.readIndexEntry(idx)
	summary := bc.readDataEntry(entry)

	return &CacheEntry{
		Mtime:   time.Unix(0, entry.mtime),
		Summary: summary,
	}
}

// indexEntry holds parsed index data.
type indexEntryData struct {
	filename   string
	mtime      int64
	dataOffset uint32
	dataLength uint16
	status     uint8
	priority   uint8
	ticketType uint8
	parent     string
}

// GetEntryByIndex returns the filename and summary at the given cache index.
func (bc *BinaryCache) GetEntryByIndex(idx int) (string, Summary) {
	if bc.data == nil {
		panic("BinaryCache: read from closed cache")
	}

	entry := bc.readIndexEntry(idx)

	return entry.filename, bc.readDataEntry(entry)
}

// FilterEntriesOpts contains filter options for FilterEntries.
type FilterEntriesOpts struct {
	Status    int    // -1 = any, otherwise status byte (0=open,1=in_progress,2=closed)
	Priority  int    // 0 = any, otherwise exact priority (1-4)
	Type      int    // -1 = any, otherwise type byte (0-4)
	Parent    string // "" = any, otherwise exact parent ID
	RootsOnly bool   // true = only entries without parent
	Limit     int    // 0 = no limit
	Offset    int    // skip first N matches
}

// FilterEntries returns indices of entries matching the given filter criteria.
// Filter parameters:
//   - status: -1 = any, otherwise status byte (0=open,1=in_progress,2=closed)
//   - priority: 0 = any, otherwise exact priority (1-4)
//   - ticketType: -1 = any, otherwise type byte (0-4)
//
// Pagination:
//   - limit: 0 = no limit
//   - offset: skip first N matches
//
// Returns nil if offset is out of bounds (only when offset > 0).
func (bc *BinaryCache) FilterEntries(status, priority, ticketType, limit, offset int) []int {
	return bc.FilterEntriesWithOpts(FilterEntriesOpts{
		Status:   status,
		Priority: priority,
		Type:     ticketType,
		Limit:    limit,
		Offset:   offset,
	})
}

// FilterEntriesWithOpts returns indices of entries matching the given filter options.
// Returns nil if offset is out of bounds (only when offset > 0).
func (bc *BinaryCache) FilterEntriesWithOpts(opts FilterEntriesOpts) []int {
	if bc.data == nil {
		panic("BinaryCache: read from closed cache")
	}

	results := make([]int, 0)

	if bc.entryCount == 0 {
		if opts.Offset > 0 {
			return nil
		}

		return results
	}

	matchCount := 0

	for i := range bc.entryCount {
		entryOffset := cacheHeaderSize + (i * indexEntrySize)
		entryStatus := int(bc.data[entryOffset+46])
		entryPriority := int(bc.data[entryOffset+47])
		entryType := int(bc.data[entryOffset+48])
		entryParent := bc.readParentAt(entryOffset + 49)

		if opts.Status != -1 && entryStatus != opts.Status {
			continue
		}

		if opts.Priority != 0 && entryPriority != opts.Priority {
			continue
		}

		if opts.Type != -1 && entryType != opts.Type {
			continue
		}

		if opts.Parent != "" && entryParent != opts.Parent {
			continue
		}

		if opts.RootsOnly && entryParent != "" {
			continue
		}

		// Match
		if matchCount < opts.Offset {
			matchCount++

			continue
		}

		results = append(results, i)
		matchCount++

		if opts.Limit > 0 && len(results) >= opts.Limit {
			break
		}
	}

	if opts.Offset > 0 && matchCount <= opts.Offset {
		return nil
	}

	return results
}

// readParentAt reads the null-terminated parent ID at the given offset.
func (bc *BinaryCache) readParentAt(offset int) string {
	parentBytes := bc.data[offset : offset+maxParentLen]

	end := bytes.IndexByte(parentBytes, 0)
	if end < 0 {
		end = maxParentLen
	}

	return string(parentBytes[:end])
}

// binarySearch finds filename in sorted index, returns -1 if not found.
func (bc *BinaryCache) binarySearch(filename string) int {
	low, high := 0, bc.entryCount-1

	for low <= high {
		mid := (low + high) / 2
		midName := bc.readFilename(mid)

		cmp := compareStrings(filename, midName)

		switch {
		case cmp == 0:
			return mid
		case cmp < 0:
			high = mid - 1
		default:
			low = mid + 1
		}
	}

	return -1
}

// readFilename reads the null-terminated filename at index position.
func (bc *BinaryCache) readFilename(idx int) string {
	offset := cacheHeaderSize + (idx * indexEntrySize)
	nameBytes := bc.data[offset : offset+maxFilenameLen]

	// Find null terminator
	end := bytes.IndexByte(nameBytes, 0)
	if end < 0 {
		end = maxFilenameLen
	}

	return string(nameBytes[:end])
}

// readIndexEntry reads full index entry at position.
func (bc *BinaryCache) readIndexEntry(idx int) indexEntryData {
	offset := cacheHeaderSize + (idx * indexEntrySize)
	data := bc.data[offset:]

	nameBytes := data[0:maxFilenameLen]
	end := bytes.IndexByte(nameBytes, 0)

	if end < 0 {
		end = maxFilenameLen
	}

	mtimeRaw := binary.LittleEndian.Uint64(data[32:40])

	var mtime int64
	if mtimeRaw <= math.MaxInt64 {
		mtime = int64(mtimeRaw)
	}

	// Read parent (null-terminated, 12 bytes max at offset 49)
	parentBytes := data[49 : 49+maxParentLen]
	parentEnd := bytes.IndexByte(parentBytes, 0)

	if parentEnd < 0 {
		parentEnd = maxParentLen
	}

	return indexEntryData{
		filename:   string(nameBytes[:end]),
		mtime:      mtime,
		dataOffset: binary.LittleEndian.Uint32(data[40:44]),
		dataLength: binary.LittleEndian.Uint16(data[44:46]),
		status:     data[46],
		priority:   data[47],
		ticketType: data[48],
		parent:     string(parentBytes[:parentEnd]),
	}
}

// readDataEntry reads variable-length data for an index entry.
func (bc *BinaryCache) readDataEntry(entry indexEntryData) Summary {
	data := bc.data[entry.dataOffset : entry.dataOffset+uint32(entry.dataLength)]
	pos := 0

	// Helper to read length-prefixed string (1 byte length)
	readString1 := func() string {
		length := int(data[pos])
		pos++
		s := string(data[pos : pos+length])
		pos += length

		return s
	}

	// Helper to read length-prefixed string (2 byte length)
	readString2 := func() string {
		length := int(binary.LittleEndian.Uint16(data[pos : pos+2]))
		pos += 2
		s := string(data[pos : pos+length])
		pos += length

		return s
	}

	// Read fields in order
	// SchemaVersion (1 byte)
	schemaVersion := int(data[pos])
	pos++

	id := readString1()
	title := readString2()
	created := readString1()
	closed := readString1()
	assignee := readString1()
	path := readString2()

	// Read BlockedBy (1 byte count + length-prefixed strings)
	blockedByCount := int(data[pos])
	pos++

	blockedBy := make([]string, 0, blockedByCount)
	for range blockedByCount {
		blockedBy = append(blockedBy, readString1())
	}

	// Read Parent (1 byte length + string)
	parent := readString1()

	// Status from index entry
	status := statusByteToString(entry.status)

	return Summary{
		SchemaVersion: schemaVersion,
		ID:            id,
		Status:        status,
		BlockedBy:     blockedBy,
		Parent:        parent,
		Title:         title,
		Type:          typeByteToString(entry.ticketType),
		Priority:      int(entry.priority),
		Created:       created,
		Closed:        closed,
		Assignee:      assignee,
		Path:          path,
	}
}

type rawCacheEntry struct {
	filename   string
	mtime      int64
	status     uint8
	priority   uint8
	ticketType uint8
	parent     string
	data       []byte
}

func (bc *BinaryCache) readRawEntry(idx int) rawCacheEntry {
	entry := bc.readIndexEntry(idx)

	return rawCacheEntry{
		filename:   entry.filename,
		mtime:      entry.mtime,
		status:     entry.status,
		priority:   entry.priority,
		ticketType: entry.ticketType,
		parent:     entry.parent,
		data:       bc.data[entry.dataOffset : entry.dataOffset+uint32(entry.dataLength)],
	}
}

// writeBinaryCache writes entries to a new cache file.
func writeBinaryCache(path string, entries map[string]CacheEntry) error {
	// Sort filenames
	filenames := make([]string, 0, len(entries))
	for filename := range entries {
		if len(filename) > maxFilenameLen {
			return fmt.Errorf("%w (max %d chars): %s", errFilenameTooLong, maxFilenameLen, filename)
		}

		filenames = append(filenames, filename)
	}

	sort.Strings(filenames)

	rawEntries := make(map[string]rawCacheEntry, len(entries))
	for filename := range entries {
		entry := entries[filename]

		data, err := encodeSummaryData(&entry.Summary)
		if err != nil {
			return err
		}

		prio, prioErr := priorityToUint8(entry.Summary.Priority)
		if prioErr != nil {
			return prioErr
		}

		rawEntries[filename] = rawCacheEntry{
			filename:   filename,
			mtime:      entry.Mtime.UnixNano(),
			status:     statusStringToByte(entry.Summary.Status),
			priority:   prio,
			ticketType: typeStringToByte(entry.Summary.Type),
			parent:     entry.Summary.Parent,
			data:       data,
		}
	}

	return writeBinaryCacheRaw(path, rawEntries)
}

func writeBinaryCacheRaw(path string, entries map[string]rawCacheEntry) error {
	// Sort filenames
	filenames := make([]string, 0, len(entries))
	for filename := range entries {
		if len(filename) > maxFilenameLen {
			return fmt.Errorf("%w (max %d chars): %s", errFilenameTooLong, maxFilenameLen, filename)
		}

		filenames = append(filenames, filename)
	}

	sort.Strings(filenames)

	entryCount := len(filenames)
	indexSize := entryCount * indexEntrySize
	dataOffset := cacheHeaderSize + indexSize

	// Build data section and track offsets
	var dataBuf bytes.Buffer

	dataOffsets := make([]uint32, entryCount)
	dataLengths := make([]uint16, entryCount)

	if entryCount > uint32Max {
		return errTooManyEntries
	}

	for i, filename := range filenames {
		entry := entries[filename]

		dataLen := len(entry.data)
		if dataLen > uint16Max {
			return errEntryTooLarge
		}

		currentOffset := dataOffset + dataBuf.Len()
		if currentOffset < 0 || currentOffset > uint32Max {
			return errFileTooLarge
		}

		// Validated above, safe to convert
		dataOffsets[i] = uint32(currentOffset)

		if dataLen < 0 {
			panic("unreachable: len() returned negative")
		}

		dataLengths[i] = uint16(dataLen)

		dataBuf.Write(entry.data)
	}

	// Build final file
	totalSize := dataOffset + dataBuf.Len()
	buf := make([]byte, totalSize)

	// Write header
	copy(buf[0:4], cacheMagic)
	binary.LittleEndian.PutUint16(buf[4:6], cacheVersionNum)
	binary.LittleEndian.PutUint32(buf[6:10], uint32(entryCount))
	// bytes 10-31 reserved (zeros)

	// Write index entries
	for i, filename := range filenames {
		offset := cacheHeaderSize + (i * indexEntrySize)
		entry := entries[filename]

		if entry.mtime < 0 {
			return errNegativeMtime
		}

		// Filename (null-padded)
		copy(buf[offset:offset+maxFilenameLen], filename)

		// Mtime
		binary.LittleEndian.PutUint64(buf[offset+32:offset+40], uint64(entry.mtime))

		// Data offset and length
		binary.LittleEndian.PutUint32(buf[offset+40:offset+44], dataOffsets[i])
		binary.LittleEndian.PutUint16(buf[offset+44:offset+46], dataLengths[i])

		// Status, Priority, Type
		buf[offset+46] = entry.status
		buf[offset+47] = entry.priority
		buf[offset+48] = entry.ticketType

		// Parent (null-padded, 12 bytes at offset 49)
		copy(buf[offset+49:offset+49+maxParentLen], entry.parent)

		// bytes 61-67 reserved (zeros)
	}

	// Write data section
	copy(buf[dataOffset:], dataBuf.Bytes())

	// Atomic write
	err := atomic.WriteFile(path, bytes.NewReader(buf))
	if err != nil {
		return fmt.Errorf("writing cache: %w", err)
	}

	// atomic.WriteFile uses a temp file + rename, which can result in the ticket
	// directory mtime being slightly newer than the cache file mtime. Since cache
	// validation compares ticket dir mtime vs cache file mtime, ensure the cache
	// file mtime is >= the directory mtime to avoid reconciling on every read.
	ticketDir := filepath.Dir(path)

	dirInfo, dirErr := os.Stat(ticketDir)
	if dirErr == nil {
		now := time.Now()
		if dirInfo.ModTime().After(now) {
			now = dirInfo.ModTime()
		}

		_ = os.Chtimes(path, now, now)
	}

	return nil
}

const (
	uint8Max  = 255
	uint16Max = 65535
	uint32Max = math.MaxUint32
)

// priorityToUint8 safely converts priority int to uint8.
// priorityToUint8 safely converts priority int to uint8.
// Priority is validated to be 1-5 during parsing.
func priorityToUint8(p int) (uint8, error) {
	if p < 0 || p > uint8Max {
		return 0, errInvalidTicketPrio
	}

	return uint8(p), nil
}

var (
	errAssigneeTooLong   = errors.New("assignee too long (max 255 chars)")
	errTooManyBlockers   = errors.New("too many blockers (max 255)")
	errBlockerIDTooLong  = errors.New("blocker ID too long (max 255 chars)")
	errEntryTooLarge     = errors.New("entry too large (max 65535 bytes)")
	errInvalidTicketType = errors.New("invalid ticket type")
	errInvalidTicketPrio = errors.New("invalid priority")
	errIDTooLong         = errors.New("id too long (max 255 chars)")
	errCreatedTooLong    = errors.New("created too long (max 255 chars)")
	errClosedTooLong     = errors.New("closed too long (max 255 chars)")
	errTitleTooLong      = errors.New("title too long (max 65535 chars)")
	errPathTooLong       = errors.New("path too long (max 65535 chars)")
	errParentTooLong     = errors.New("parent too long (max 255 chars)")
)

func encodeSummaryData(summary *Summary) ([]byte, error) {
	// Validate 1-byte length strings
	if len(summary.ID) > uint8Max {
		return nil, errIDTooLong
	}

	if len(summary.Created) > uint8Max {
		return nil, errCreatedTooLong
	}

	if len(summary.Closed) > uint8Max {
		return nil, errClosedTooLong
	}

	if len(summary.Assignee) > uint8Max {
		return nil, errAssigneeTooLong
	}

	if len(summary.Parent) > uint8Max {
		return nil, errParentTooLong
	}

	// Validate 2-byte length strings
	if len(summary.Title) > uint16Max {
		return nil, errTitleTooLong
	}

	if len(summary.Path) > uint16Max {
		return nil, errPathTooLong
	}

	// Validate blocked-by
	if len(summary.BlockedBy) > uint8Max {
		return nil, errTooManyBlockers
	}

	for _, blocker := range summary.BlockedBy {
		if len(blocker) > uint8Max {
			return nil, fmt.Errorf("%w: %s", errBlockerIDTooLong, blocker)
		}
	}

	// Validate type + priority (stored in index)
	if !IsValidTicketType(summary.Type) {
		return nil, fmt.Errorf("%w: %s", errInvalidTicketType, summary.Type)
	}

	if summary.Priority < MinPriority || summary.Priority > MaxPriority {
		return nil, fmt.Errorf("%w: %d", errInvalidTicketPrio, summary.Priority)
	}

	var dataBuf bytes.Buffer

	// Helper to write length-prefixed string (1 byte length)
	writeString1 := func(str string) {
		dataBuf.WriteByte(byte(len(str)))
		dataBuf.WriteString(str)
	}

	// Helper to write length-prefixed string (2 byte length)
	// Caller must validate len(str) <= uint16Max before calling.
	writeString2 := func(str string) {
		strLen := len(str)
		if strLen > uint16Max {
			panic("writeString2: string too long (caller must validate)")
		}

		lenBytes := make([]byte, 2)
		binary.LittleEndian.PutUint16(lenBytes, uint16(strLen))
		dataBuf.Write(lenBytes)
		dataBuf.WriteString(str)
	}

	// SchemaVersion (1 byte)
	dataBuf.WriteByte(byte(summary.SchemaVersion))

	// Fields in order (must match read order)
	writeString1(summary.ID)
	writeString2(summary.Title)
	writeString1(summary.Created)
	writeString1(summary.Closed)
	writeString1(summary.Assignee)
	writeString2(summary.Path)

	dataBuf.WriteByte(byte(len(summary.BlockedBy)))

	for _, blocker := range summary.BlockedBy {
		writeString1(blocker)
	}

	// Write Parent (1 byte length + string)
	writeString1(summary.Parent)

	if dataBuf.Len() > uint16Max {
		return nil, errEntryTooLarge
	}

	return dataBuf.Bytes(), nil
}

func statusByteToString(b uint8) string {
	switch b {
	case statusByteOpen:
		return StatusOpen
	case statusByteInProgress:
		return StatusInProgress
	case statusByteClosed:
		return StatusClosed
	default:
		return StatusOpen
	}
}

func statusStringToByte(s string) uint8 {
	switch s {
	case StatusOpen:
		return statusByteOpen
	case StatusInProgress:
		return statusByteInProgress
	case StatusClosed:
		return statusByteClosed
	default:
		return statusByteOpen
	}
}

func typeStringToByte(s string) uint8 {
	switch strings.ToLower(s) {
	case TypeBug:
		return typeByteBug
	case TypeFeature:
		return typeByteFeature
	case TypeTask:
		return typeByteTask
	case TypeEpic:
		return typeByteEpic
	case TypeChore:
		return typeByteChore
	default:
		return typeByteBug
	}
}

func typeByteToString(b uint8) string {
	switch b {
	case typeByteBug:
		return TypeBug
	case typeByteFeature:
		return TypeFeature
	case typeByteTask:
		return TypeTask
	case typeByteEpic:
		return TypeEpic
	case typeByteChore:
		return TypeChore
	default:
		return TypeBug
	}
}

// UpdateCacheEntry updates or inserts a single cache entry. Uses a lock to avoid
// lost updates when multiple tk commands run concurrently.
func UpdateCacheEntry(ticketDir, filename string, summary *Summary) error {
	cachePath := filepath.Join(ticketDir, CacheFileName)

	// Lock on cache file path (creates .cache.lock)
	return WithLock(cachePath, func() error {
		// If cache is missing or invalid, rebuild from scratch (includes this file).
		cache, err := LoadBinaryCache(ticketDir)
		if err != nil {
			if errors.Is(err, errCacheNotFound) || errors.Is(err, errVersionMismatch) || errors.Is(err, errInvalidMagic) ||
				errors.Is(err, errFileTooSmall) || errors.Is(err, errCacheCorrupt) {
				_, rebuildErr := buildCacheParallel(ticketDir, nil)

				return rebuildErr
			}

			return err
		}

		defer func() { _ = cache.Close() }()

		entries := cacheEntriesAsRawMap(cache)

		// Reconcile if directory changed (added/deleted files).
		needReconcile, dirErr := dirMtimeNewerThanCache(ticketDir, cachePath)
		if dirErr != nil {
			return dirErr
		}

		if needReconcile {
			reconcileErr := reconcileRawCacheEntries(ticketDir, entries)
			if reconcileErr != nil {
				return reconcileErr
			}
		}

		// Update the requested entry.
		if len(filename) > maxFilenameLen {
			return fmt.Errorf("caching ticket: %w (max %d chars): %s", errFilenameTooLong, maxFilenameLen, filename)
		}

		ticketPath := filepath.Join(ticketDir, filename)

		info, statErr := os.Stat(ticketPath)
		if statErr != nil {
			return fmt.Errorf("stat ticket: %w", statErr)
		}

		data, encErr := encodeSummaryData(summary)
		if encErr != nil {
			return fmt.Errorf("caching ticket: %w", encErr)
		}

		prio, prioErr := priorityToUint8(summary.Priority)
		if prioErr != nil {
			return fmt.Errorf("caching ticket: %w", prioErr)
		}

		entries[filename] = rawCacheEntry{
			filename:   filename,
			mtime:      info.ModTime().UnixNano(),
			status:     statusStringToByte(summary.Status),
			priority:   prio,
			ticketType: typeStringToByte(summary.Type),
			parent:     summary.Parent,
			data:       data,
		}

		return writeBinaryCacheRaw(cachePath, entries)
	})
}

// DeleteCacheEntry removes a single cache entry. No-op if cache or entry doesn't exist.
func DeleteCacheEntry(ticketDir, filename string) error {
	cachePath := filepath.Join(ticketDir, CacheFileName)

	return WithLock(cachePath, func() error {
		cache, err := LoadBinaryCache(ticketDir)
		if err != nil {
			if errors.Is(err, errCacheNotFound) {
				return nil
			}

			// If cache is invalid, treat as deleted (it will be rebuilt on next read).
			if errors.Is(err, errVersionMismatch) || errors.Is(err, errInvalidMagic) || errors.Is(err, errFileTooSmall) || errors.Is(err, errCacheCorrupt) {
				return nil
			}

			return err
		}

		defer func() { _ = cache.Close() }()

		entries := cacheEntriesAsRawMap(cache)

		delete(entries, filename)

		return writeBinaryCacheRaw(cachePath, entries)
	})
}

func cacheEntriesAsRawMap(cache *BinaryCache) map[string]rawCacheEntry {
	entries := make(map[string]rawCacheEntry, cache.entryCount)
	for i := range cache.entryCount {
		e := cache.readRawEntry(i)
		entries[e.filename] = e
	}

	return entries
}

func dirMtimeNewerThanCache(ticketDir, cachePath string) (bool, error) {
	dirInfo, err := os.Stat(ticketDir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}

		return false, fmt.Errorf("reading ticket directory: %w", err)
	}

	cacheInfo, err := os.Stat(cachePath)
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}

		return false, fmt.Errorf("stat cache: %w", err)
	}

	return dirInfo.ModTime().After(cacheInfo.ModTime()), nil
}

func reconcileRawCacheEntries(ticketDir string, entries map[string]rawCacheEntry) error {
	dirEntries, err := os.ReadDir(ticketDir)
	if err != nil {
		return fmt.Errorf("reading ticket directory: %w", err)
	}

	currentFiles := make(map[string]bool, len(dirEntries))

	for _, entry := range dirEntries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()

		// Ignore hidden and non-md files.
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}

		currentFiles[name] = true

		if _, ok := entries[name]; ok {
			continue
		}

		// New file: parse and add.
		path := filepath.Join(ticketDir, name)

		summary, parseErr := ParseTicketFrontmatter(path)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}

		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("stat %s: %w", path, infoErr)
		}

		data, encErr := encodeSummaryData(&summary)
		if encErr != nil {
			return fmt.Errorf("caching ticket: %w", encErr)
		}

		prio, prioErr := priorityToUint8(summary.Priority)
		if prioErr != nil {
			return fmt.Errorf("caching ticket: %w", prioErr)
		}

		entries[name] = rawCacheEntry{
			filename:   name,
			mtime:      info.ModTime().UnixNano(),
			status:     statusStringToByte(summary.Status),
			priority:   prio,
			ticketType: typeStringToByte(summary.Type),
			parent:     summary.Parent,
			data:       data,
		}
	}

	// Deleted files: remove.
	for filename := range entries {
		if !currentFiles[filename] {
			delete(entries, filename)
		}
	}

	return nil
}

func compareStrings(a, b string) int {
	if a < b {
		return -1
	}

	if a > b {
		return 1
	}

	return 0
}
