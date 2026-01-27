package testutil

import "time"

// Clock provides deterministic, monotonically increasing timestamps
// for spec-model operations.
type Clock struct {
	current time.Time
	step    time.Duration
}

// NewClock returns a clock initialized to a fixed UTC start time.
func NewClock() *Clock {
	return &Clock{
		current: time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC),
		step:    time.Second,
	}
}

// NextTimestamp returns the next timestamp in RFC3339 format.
func (c *Clock) NextTimestamp() string {
	c.current = c.current.Add(c.step)

	return c.current.Format(time.RFC3339)
}
