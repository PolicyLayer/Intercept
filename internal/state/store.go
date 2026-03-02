package state

import "time"

// StateStore provides atomic counter operations with windowed expiry.
type StateStore interface {
	Reserve(key string, amount int64, limit int64, window time.Duration) (bool, int64, error)
	Rollback(key string, amount int64) error
	Get(key string) (int64, time.Time, error)
	Reset(key string) error
	Close() error
}

// Clock abstracts time for testability.
type Clock interface {
	Now() time.Time
}

// realClock is the production Clock implementation that delegates to time.Now.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// WindowDuration converts a named window ("minute", "hour", "day") to a time.Duration.
// Returns 0 for unrecognised names.
func WindowDuration(name string) time.Duration {
	switch name {
	case "minute":
		return time.Minute
	case "hour":
		return time.Hour
	case "day":
		return 24 * time.Hour
	default:
		return 0
	}
}

// windowStart returns the calendar-aligned UTC start of the window containing now.
func windowStart(now time.Time, windowDur time.Duration) time.Time {
	now = now.UTC()
	y, m, d := now.Date()
	switch windowDur {
	case time.Minute:
		return time.Date(y, m, d, now.Hour(), now.Minute(), 0, 0, time.UTC)
	case time.Hour:
		return time.Date(y, m, d, now.Hour(), 0, 0, 0, time.UTC)
	case 24 * time.Hour:
		return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	default:
		return now.Truncate(windowDur)
	}
}
