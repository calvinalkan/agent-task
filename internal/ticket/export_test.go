package ticket

// Exported constants for testing.
const (
	TestCacheMagic      = cacheMagic
	TestCacheVersionNum = cacheVersionNum
	TestCacheHeaderSize = cacheHeaderSize
	TestIndexEntrySize  = indexEntrySize
	TestDirPerms        = dirPerms
	TestFilePerms       = filePerms
)

// Exported status bytes for testing.
const (
	TestStatusByteOpen       = statusByteOpen
	TestStatusByteInProgress = statusByteInProgress
	TestStatusByteClosed     = statusByteClosed
)

// Exported type bytes for testing.
const (
	TestTypeByteBug     = typeByteBug
	TestTypeByteFeature = typeByteFeature
	TestTypeByteTask    = typeByteTask
	TestTypeByteEpic    = typeByteEpic
)

// Exported errors for testing.
var (
	ErrTestCacheCorrupt    = errCacheCorrupt
	ErrTestCacheNotFound   = errCacheNotFound
	ErrTestFileTooSmall    = errFileTooSmall
	ErrTestInvalidMagic    = errInvalidMagic
	ErrTestVersionMismatch = errVersionMismatch
)

// TestRawCacheEntry is an exported version of rawCacheEntry for testing.
type TestRawCacheEntry struct {
	Filename   string
	Mtime      int64
	Status     uint8
	Priority   uint8
	TicketType uint8
	Data       []byte
}

// TestEncodeSummaryData exposes encodeSummaryData for testing.
var TestEncodeSummaryData = encodeSummaryData

// ExportWriteBinaryCacheRaw exposes writeBinaryCacheRaw for testing with exported entry type.
func ExportWriteBinaryCacheRaw(path string, entries map[string]TestRawCacheEntry) error {
	internal := make(map[string]rawCacheEntry, len(entries))

	for key, val := range entries {
		internal[key] = rawCacheEntry{
			filename:   val.Filename,
			mtime:      val.Mtime,
			status:     val.Status,
			priority:   val.Priority,
			ticketType: val.TicketType,
			data:       val.Data,
		}
	}

	return writeBinaryCacheRaw(path, internal)
}

// TestWriteBinaryCache exposes writeBinaryCache for testing.
var TestWriteBinaryCache = writeBinaryCache

// TestDirMtimeNewerThanCache exposes dirMtimeNewerThanCache for testing.
var TestDirMtimeNewerThanCache = dirMtimeNewerThanCache
