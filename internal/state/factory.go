// Package state provides atomic windowed counter storage for rate limiting.
// Two backends are supported: SQLite (default, local) and Redis (shared).
package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is the composite interface satisfied by all state backends.
// It combines the StateStore operations with the engine's Resolve method.
type Store interface {
	StateStore
	Resolve(path string) (any, bool, error)
}

// FailMode controls behavior when a state backend is unreachable.
type FailMode int

const (
	FailClosed FailMode = iota
	FailOpen
)

// ParseFailMode converts a string to a FailMode.
func ParseFailMode(s string) (FailMode, error) {
	switch s {
	case "closed", "":
		return FailClosed, nil
	case "open":
		return FailOpen, nil
	default:
		return 0, fmt.Errorf("unknown fail mode %q: must be \"open\" or \"closed\"", s)
	}
}

// OpenStore creates a Store from the provided configuration.
// If dsn is non-empty, a Redis backend is used; otherwise SQLite.
func OpenStore(dsn, dir, prefix, failModeStr string) (Store, error) {
	var s Store
	if dsn != "" {
		fm, err := ParseFailMode(failModeStr)
		if err != nil {
			return nil, err
		}
		rs, err := NewRedisStore(dsn, RedisOptions{FailMode: fm})
		if err != nil {
			return nil, err
		}
		s = rs
	} else {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, fmt.Errorf("creating state directory: %w", err)
		}
		ss, err := NewSQLiteStore(filepath.Join(dir, "intercept.sqlite"))
		if err != nil {
			return nil, err
		}
		s = ss
	}
	if prefix != "" {
		s = &prefixedStore{Store: s, prefix: prefix}
	}
	return s, nil
}

// prefixedStore wraps a Store and prepends a prefix to all keys.
type prefixedStore struct {
	Store
	prefix string
}

// Reserve delegates to the underlying store with the prefix prepended.
func (p *prefixedStore) Reserve(key string, amount int64, limit int64, window time.Duration) (bool, int64, error) {
	return p.Store.Reserve(p.prefix+key, amount, limit, window)
}

// Rollback delegates to the underlying store with the prefix prepended.
func (p *prefixedStore) Rollback(key string, amount int64) error {
	return p.Store.Rollback(p.prefix+key, amount)
}

// Get delegates to the underlying store with the prefix prepended.
func (p *prefixedStore) Get(key string) (int64, time.Time, error) {
	return p.Store.Get(p.prefix + key)
}

// Reset delegates to the underlying store with the prefix prepended.
func (p *prefixedStore) Reset(key string) error {
	return p.Store.Reset(p.prefix + key)
}

// Resolve inserts the prefix between "state." and the counter key before
// delegating to the underlying store.
func (p *prefixedStore) Resolve(path string) (any, bool, error) {
	// Resolve receives "state.X", strip "state." to get the counter key,
	// add prefix, then re-add "state." for the underlying store.
	const statePrefix = "state."
	if after, ok := strings.CutPrefix(path, statePrefix); ok {
		return p.Store.Resolve(statePrefix + p.prefix + after)
	}
	return p.Store.Resolve(path)
}
