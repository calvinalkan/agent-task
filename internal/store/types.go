package store

import "time"

// Summary is the SQLite-backed view used by list/ready-style queries.
// Keep it compact and denormalized so callers don't need extra joins.
type Summary struct {
	ID          string
	ShortID     string
	Path        string
	MtimeNS     int64
	Status      string
	Type        string
	Priority    int64
	Assignee    string
	Parent      string
	CreatedAt   time.Time
	ClosedAt    *time.Time
	ExternalRef string
	Title       string
	BlockedBy   []string
}

// QueryOptions mirrors the allowed SQLite filters; zero values mean "no filter".
// Priority uses 0 to mean "any" so callers can pass through CLI defaults.
type QueryOptions struct {
	Status        string
	Type          string
	Priority      int
	Parent        string
	ShortIDPrefix string
	Limit         int
	Offset        int
}
