package state

import (
	"path/filepath"
	"testing"
)

func newSQLiteTestStore(t *testing.T, clock *testClock) testableStore {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.sqlite")
	store, err := NewSQLiteStoreWithClock(path, clock)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithClock: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func newSQLiteTestStoreWithPath(t *testing.T, clock *testClock) (testableStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "persist.sqlite")
	store, err := NewSQLiteStoreWithClock(path, clock)
	if err != nil {
		t.Fatalf("NewSQLiteStoreWithClock: %v", err)
	}
	return store, path
}

func reopenSQLiteStore(t *testing.T, path string, clock *testClock) testableStore {
	t.Helper()
	store, err := NewSQLiteStoreWithClock(path, clock)
	if err != nil {
		t.Fatalf("reopen SQLiteStore: %v", err)
	}
	return store
}

// --- Basic operations ---

func TestReserveEmpty(t *testing.T) { testReserveEmpty(t, newSQLiteTestStore) }
func TestReserveMultipleWithinLimit(t *testing.T) {
	testReserveMultipleWithinLimit(t, newSQLiteTestStore)
}
func TestReserveExceedsLimit(t *testing.T) { testReserveExceedsLimit(t, newSQLiteTestStore) }
func TestReserveBoundaryExactLimit(t *testing.T) {
	testReserveBoundaryExactLimit(t, newSQLiteTestStore)
}
func TestReserveAmountZero(t *testing.T)    { testReserveAmountZero(t, newSQLiteTestStore) }
func TestRollbackBasic(t *testing.T)        { testRollbackBasic(t, newSQLiteTestStore) }
func TestRollbackFloorsAtZero(t *testing.T) { testRollbackFloorsAtZero(t, newSQLiteTestStore) }
func TestRollbackNonExistent(t *testing.T)  { testRollbackNonExistent(t, newSQLiteTestStore) }
func TestGetExisting(t *testing.T)          { testGetExisting(t, newSQLiteTestStore) }
func TestGetNonExistent(t *testing.T)       { testGetNonExistent(t, newSQLiteTestStore) }
func TestResetExisting(t *testing.T)        { testResetExisting(t, newSQLiteTestStore) }
func TestResetNonExistent(t *testing.T)     { testResetNonExistent(t, newSQLiteTestStore) }

// --- Window behavior ---

func TestWindowMinuteReset(t *testing.T)       { testWindowMinuteReset(t, newSQLiteTestStore) }
func TestWindowHourReset(t *testing.T)         { testWindowHourReset(t, newSQLiteTestStore) }
func TestWindowDayReset(t *testing.T)          { testWindowDayReset(t, newSQLiteTestStore) }
func TestNoResetWithinSameWindow(t *testing.T) { testNoResetWithinSameWindow(t, newSQLiteTestStore) }
func TestMultiWindowGap(t *testing.T)          { testMultiWindowGap(t, newSQLiteTestStore) }
func TestCalendarAlignmentDay(t *testing.T)    { testCalendarAlignmentDay(t, newSQLiteTestStore) }

// --- StateResolver contract ---

func TestResolveStripPrefix(t *testing.T)   { testResolveStripPrefix(t, newSQLiteTestStore) }
func TestResolveNonExistent(t *testing.T)   { testResolveNonExistent(t, newSQLiteTestStore) }
func TestResolveExpiredWindow(t *testing.T) { testResolveExpiredWindow(t, newSQLiteTestStore) }
func TestResolveActiveWindow(t *testing.T)  { testResolveActiveWindow(t, newSQLiteTestStore) }

// --- Concurrency ---

func TestConcurrentReserve(t *testing.T) { testConcurrentReserve(t, newSQLiteTestStore) }

// --- Persistence ---

func TestPersistenceAcrossReopen(t *testing.T) {
	testPersistenceAcrossReopen(t, newSQLiteTestStoreWithPath, reopenSQLiteStore)
}
