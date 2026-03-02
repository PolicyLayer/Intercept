package state

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// SQLiteStore implements StateStore backed by SQLite in WAL mode.
// Multiple processes can safely share the same database file.
type SQLiteStore struct {
	db    *sql.DB
	clock Clock
}

// NewSQLiteStore opens or creates a SQLite database at path.
func NewSQLiteStore(path string) (*SQLiteStore, error) {
	return NewSQLiteStoreWithClock(path, realClock{})
}

// NewSQLiteStoreWithClock opens or creates a SQLite database with a custom clock (for testing).
func NewSQLiteStoreWithClock(path string, clock Clock) (*SQLiteStore, error) {
	dsn := fmt.Sprintf("file:%s?_txlock=immediate", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite db: %w", err)
	}

	// Serialize all access through one connection within this process.
	// Multi-process concurrency is handled by SQLite's WAL mode file locking.
	db.SetMaxOpenConns(1)

	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil, fmt.Errorf("exec %s: %w", pragma, err)
		}
	}

	const createTable = `CREATE TABLE IF NOT EXISTS counters (
		key          TEXT PRIMARY KEY,
		value        INTEGER NOT NULL DEFAULT 0,
		window_start TEXT NOT NULL,
		window_dur   INTEGER NOT NULL DEFAULT 0
	)`
	if _, err := db.Exec(createTable); err != nil {
		db.Close()
		return nil, fmt.Errorf("create counters table: %w", err)
	}

	return &SQLiteStore{db: db, clock: clock}, nil
}

// Reserve atomically checks and increments a counter.
// Returns (allowed, currentValue, error). The counter is only modified when allowed.
func (s *SQLiteStore) Reserve(key string, amount int64, limit int64, window time.Duration) (bool, int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return false, 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var value int64
	var wsText string
	var wdNanos int64
	err = tx.QueryRow("SELECT value, window_start, window_dur FROM counters WHERE key = ?", key).
		Scan(&value, &wsText, &wdNanos)

	now := s.clock.Now()
	ws := windowStart(now, window)

	if err == sql.ErrNoRows {
		value = 0
	} else if err != nil {
		return false, 0, fmt.Errorf("select counter: %w", err)
	} else {
		storedWS, parseErr := time.Parse(time.RFC3339Nano, wsText)
		if parseErr != nil {
			return false, 0, fmt.Errorf("parse window_start for %q: %w", key, parseErr)
		}
		if !storedWS.Equal(ws) {
			value = 0
		}
	}

	if value+amount > limit {
		return false, value, nil
	}

	value += amount
	_, err = tx.Exec(
		`INSERT INTO counters (key, value, window_start, window_dur) VALUES (?, ?, ?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, window_start = excluded.window_start, window_dur = excluded.window_dur`,
		key, value, ws.Format(time.RFC3339Nano), int64(window),
	)
	if err != nil {
		return false, 0, fmt.Errorf("upsert counter: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, 0, fmt.Errorf("commit tx: %w", err)
	}
	return true, value, nil
}

// Rollback decrements a counter by amount, flooring at zero.
func (s *SQLiteStore) Rollback(key string, amount int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	var value int64
	err = tx.QueryRow("SELECT value FROM counters WHERE key = ?", key).Scan(&value)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return fmt.Errorf("select counter: %w", err)
	}

	value -= amount
	if value < 0 {
		value = 0
	}

	_, err = tx.Exec("UPDATE counters SET value = ? WHERE key = ?", value, key)
	if err != nil {
		return fmt.Errorf("update counter: %w", err)
	}

	return tx.Commit()
}

// Get returns the raw counter value and window start time.
// Returns (0, zero time, nil) for non-existent keys.
func (s *SQLiteStore) Get(key string) (int64, time.Time, error) {
	var value int64
	var wsText string
	err := s.db.QueryRow("SELECT value, window_start FROM counters WHERE key = ?", key).
		Scan(&value, &wsText)
	if err == sql.ErrNoRows {
		return 0, time.Time{}, nil
	}
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("select counter: %w", err)
	}

	ws, parseErr := time.Parse(time.RFC3339Nano, wsText)
	if parseErr != nil {
		return 0, time.Time{}, fmt.Errorf("parse window_start for %q: %w", key, parseErr)
	}
	return value, ws, nil
}

// Reset deletes a counter key entirely.
func (s *SQLiteStore) Reset(key string) error {
	_, err := s.db.Exec("DELETE FROM counters WHERE key = ?", key)
	return err
}

// Close closes the SQLite database.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

// Resolve implements engine.StateResolver via structural typing.
// It strips the "state." prefix and returns the current counter value,
// accounting for window expiry.
func (s *SQLiteStore) Resolve(path string) (any, bool, error) {
	key := strings.TrimPrefix(path, "state.")

	var value int64
	var wsText string
	var wdNanos int64
	err := s.db.QueryRow("SELECT value, window_start, window_dur FROM counters WHERE key = ?", key).
		Scan(&value, &wsText, &wdNanos)
	if err == sql.ErrNoRows {
		return int64(0), true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("select counter: %w", err)
	}

	storedWS, parseErr := time.Parse(time.RFC3339Nano, wsText)
	if parseErr != nil {
		return nil, false, fmt.Errorf("parse window_start for %q: %w", key, parseErr)
	}
	now := s.clock.Now()
	ws := windowStart(now, time.Duration(wdNanos))
	if !storedWS.Equal(ws) {
		return int64(0), true, nil
	}

	return value, true, nil
}
