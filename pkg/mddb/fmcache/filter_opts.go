package fmcache

// FilterOpts configures scan order and paging for FilterIndex/AllEntries.
//
// Offset and Limit are applied during the scan.
// Limit == 0 means no limit.
//
// A negative Offset or Limit is invalid.
type FilterOpts struct {
	Reverse bool // false = key asc, true = key desc
	Offset  int  // skip first N matches; must be >= 0
	Limit   int  // 0 = no limit; must be >= 0
}
