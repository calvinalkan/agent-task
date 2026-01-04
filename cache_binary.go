package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sort"
	"syscall"
	"time"

	"github.com/natefinch/atomic"
)

// Binary cache format constants.
const (
	cacheMagic       = "TKC1"
	cacheVersionNum  = 2
	cacheHeaderSize  = 32
	indexEntrySize   = 48
	maxFilenameLen   = 32
	minCacheFileSize = cacheHeaderSize
)

// Status byte values for index.
const (
	statusByteOpen       = 0
	statusByteInProgress = 1
	statusByteClosed     = 2
)

// Binary cache errors.
var (
	errInvalidMagic    = errors.New("invalid cache magic")
	errVersionMismatch = errors.New("cache version mismatch")
	errFileTooSmall    = errors.New("cache file too small")
	errFilenameTooLong = errors.New("filename too long for cache")
)

// BinaryCache provides mmap-based cache access.
type BinaryCache struct {
	data       []byte // mmap'd file contents
	entryCount int
	// For modifications during operation
	updates map[string]CacheEntry
	// Track which original entries are still valid
	validOriginals map[string]bool
}

// LoadBinaryCache loads the cache using mmap.
// Returns errCacheNotFound if file doesn't exist.
// Returns errVersionMismatch if version doesn't match (caller should rebuild).
func LoadBinaryCache(ticketDir string) (*BinaryCache, error) {
	cachePath := ticketDir + "/" + cacheFileName

	file, err := os.Open(cachePath) //nolint:gosec
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
	fileSize := uint32(size)
	for i := 0; i < entryCount; i++ {
		offset := cacheHeaderSize + (i * indexEntrySize)
		dataOffset := binary.LittleEndian.Uint32(data[offset+32+8 : offset+32+8+4])
		dataLength := binary.LittleEndian.Uint16(data[offset+32+8+4 : offset+32+8+4+2])

		// Check bounds
		if dataOffset >= fileSize || uint32(dataLength) > fileSize-dataOffset {
			_ = syscall.Munmap(data)

			return nil, errCacheCorrupt
		}
	}

	return &BinaryCache{
		data:           data,
		entryCount:     entryCount,
		updates:        make(map[string]CacheEntry),
		validOriginals: make(map[string]bool),
	}, nil
}

// Close unmaps the cache file.
func (bc *BinaryCache) Close() error {
	if bc.data != nil {
		return syscall.Munmap(bc.data)
	}

	return nil
}

// Lookup finds an entry by filename using binary search.
// Returns nil if not found or if cache data is corrupted.
func (bc *BinaryCache) Lookup(filename string) (result *CacheEntry) {
	// Recover from any panics caused by corrupted data
	defer func() {
		if r := recover(); r != nil {
			result = nil
		}
	}()

	// Check updates first (modified entries)
	if entry, ok := bc.updates[filename]; ok {
		return &entry
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

// LookupMtime finds mtime for a filename without reading data section.
// Returns zero time if not found or corrupted. This is faster than full Lookup.
func (bc *BinaryCache) LookupMtime(filename string) (result time.Time) {
	// Recover from any panics caused by corrupted data
	defer func() {
		if r := recover(); r != nil {
			result = time.Time{}
		}
	}()

	// Check updates first
	if entry, ok := bc.updates[filename]; ok {
		return entry.Mtime
	}

	// Binary search in mmap'd index
	idx := bc.binarySearch(filename)
	if idx < 0 {
		return time.Time{}
	}

	// Read just mtime from index (no data section access)
	offset := cacheHeaderSize + (idx * indexEntrySize) + maxFilenameLen
	mtime := int64(binary.LittleEndian.Uint64(bc.data[offset : offset+8]))

	return time.Unix(0, mtime)
}

// indexEntry holds parsed index data.
type indexEntryData struct {
	filename   string
	mtime      int64
	dataOffset uint32
	dataLength uint16
	status     uint8
}

// binarySearch finds filename in sorted index, returns -1 if not found.
func (bc *BinaryCache) binarySearch(filename string) int {
	low, high := 0, bc.entryCount-1

	for low <= high {
		mid := (low + high) / 2
		midName := bc.readFilename(mid)

		cmp := compareStrings(filename, midName)
		if cmp == 0 {
			return mid
		} else if cmp < 0 {
			high = mid - 1
		} else {
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

	return indexEntryData{
		filename:   string(nameBytes[:end]),
		mtime:      int64(binary.LittleEndian.Uint64(data[32:40])),
		dataOffset: binary.LittleEndian.Uint32(data[40:44]),
		dataLength: binary.LittleEndian.Uint16(data[44:46]),
		status:     data[46],
	}
}

// readDataEntry reads variable-length data for an index entry.
func (bc *BinaryCache) readDataEntry(entry indexEntryData) TicketSummary {
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
	id := readString1()
	title := readString2()
	ticketType := readString1()
	created := readString1()
	closed := readString1()
	assignee := readString1()
	path := readString2()

	// Priority (1 byte)
	priority := int(data[pos])
	pos++

	// Read BlockedBy (1 byte count + length-prefixed strings)
	blockedByCount := int(data[pos])
	pos++

	blockedBy := make([]string, 0, blockedByCount)
	for i := 0; i < blockedByCount; i++ {
		blockedBy = append(blockedBy, readString1())
	}

	// Status from index entry
	status := statusByteToString(entry.status)

	return TicketSummary{
		ID:        id,
		Status:    status,
		Title:     title,
		Type:      ticketType,
		Priority:  priority,
		Created:   created,
		Closed:    closed,
		Assignee:  assignee,
		BlockedBy: blockedBy,
		Path:      path,
	}
}

// MarkValid marks an original cache entry as still valid.
func (bc *BinaryCache) MarkValid(filename string) {
	bc.validOriginals[filename] = true
}

// Update stores a new or modified cache entry.
func (bc *BinaryCache) Update(filename string, entry CacheEntry) {
	bc.updates[filename] = entry
}

// HasChanges returns true if there are any updates.
func (bc *BinaryCache) HasChanges() bool {
	return len(bc.updates) > 0
}

// SaveBinaryCache writes the cache to disk.
// Merges all original entries with updates.
func SaveBinaryCache(ticketDir string, bc *BinaryCache) error {
	cachePath := ticketDir + "/" + cacheFileName

	// Collect all entries: all originals + updates
	entries := make(map[string]CacheEntry)

	// Add ALL originals from mmap'd data (not just "valid" ones)
	// This ensures partial reads (--limit) don't lose unread entries
	if bc != nil && bc.data != nil {
		for i := 0; i < bc.entryCount; i++ {
			entry := bc.readIndexEntry(i)
			// Skip entries that will be overwritten by updates
			if _, updated := bc.updates[entry.filename]; !updated {
				entries[entry.filename] = CacheEntry{
					Mtime:   time.Unix(0, entry.mtime),
					Summary: bc.readDataEntry(entry),
				}
			}
		}
	}

	// Add updates (overwrites any originals)
	for filename, entry := range bc.updates {
		entries[filename] = entry
	}

	return writeBinaryCache(cachePath, entries)
}

// NewBinaryCache creates a new empty cache for writing.
func NewBinaryCache() *BinaryCache {
	return &BinaryCache{
		updates:        make(map[string]CacheEntry),
		validOriginals: make(map[string]bool),
	}
}

// writeBinaryCache writes entries to a new cache file.
func writeBinaryCache(path string, entries map[string]CacheEntry) error {
	// Sort filenames
	filenames := make([]string, 0, len(entries))
	for filename := range entries {
		if len(filename) > maxFilenameLen {
			return fmt.Errorf("%w: %s", errFilenameTooLong, filename)
		}

		filenames = append(filenames, filename)
	}

	sort.Strings(filenames)

	// Calculate sizes
	entryCount := len(filenames)
	indexSize := entryCount * indexEntrySize
	dataOffset := cacheHeaderSize + indexSize

	// Build data section and track offsets
	var dataBuf bytes.Buffer
	dataOffsets := make([]uint32, entryCount)
	dataLengths := make([]uint16, entryCount)

	// Helper to write length-prefixed string (1 byte length)
	writeString1 := func(s string) {
		dataBuf.WriteByte(byte(len(s)))
		dataBuf.WriteString(s)
	}

	// Helper to write length-prefixed string (2 byte length)
	writeString2 := func(s string) {
		lenBytes := make([]byte, 2)
		binary.LittleEndian.PutUint16(lenBytes, uint16(len(s)))
		dataBuf.Write(lenBytes)
		dataBuf.WriteString(s)
	}

	for i, filename := range filenames {
		dataOffsets[i] = uint32(dataOffset + dataBuf.Len())
		startLen := dataBuf.Len()

		entry := entries[filename]
		s := entry.Summary

		// Write fields in order (must match read order)
		writeString1(s.ID)
		writeString2(s.Title)
		writeString1(s.Type)
		writeString1(s.Created)
		writeString1(s.Closed)
		writeString1(s.Assignee)
		writeString2(s.Path)

		// Priority (1 byte)
		dataBuf.WriteByte(byte(s.Priority))

		// BlockedBy (1 byte count + length-prefixed strings)
		dataBuf.WriteByte(byte(len(s.BlockedBy)))
		for _, b := range s.BlockedBy {
			writeString1(b)
		}

		dataLengths[i] = uint16(dataBuf.Len() - startLen)
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

		// Filename (null-padded)
		copy(buf[offset:offset+maxFilenameLen], filename)

		// Mtime
		binary.LittleEndian.PutUint64(buf[offset+32:offset+40], uint64(entry.Mtime.UnixNano()))

		// Data offset and length
		binary.LittleEndian.PutUint32(buf[offset+40:offset+44], dataOffsets[i])
		binary.LittleEndian.PutUint16(buf[offset+44:offset+46], dataLengths[i])

		// Status
		buf[offset+46] = statusStringToByte(entry.Summary.Status)

		// byte 47 reserved
	}

	// Write data section
	copy(buf[dataOffset:], dataBuf.Bytes())

	// Atomic write
	return atomic.WriteFile(path, bytes.NewReader(buf))
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

func compareStrings(a, b string) int {
	if a < b {
		return -1
	}

	if a > b {
		return 1
	}

	return 0
}
