package state

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func newRedisTestStore(t *testing.T, clock *testClock) testableStore {
	t.Helper()
	mr := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+mr.Addr(), RedisOptions{Clock: clock})
	if err != nil {
		t.Fatalf("NewRedisStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// --- Basic operations ---

func TestRedisReserveEmpty(t *testing.T) { testReserveEmpty(t, newRedisTestStore) }
func TestRedisReserveMultipleWithinLimit(t *testing.T) {
	testReserveMultipleWithinLimit(t, newRedisTestStore)
}
func TestRedisReserveExceedsLimit(t *testing.T) { testReserveExceedsLimit(t, newRedisTestStore) }
func TestRedisReserveBoundaryExactLimit(t *testing.T) {
	testReserveBoundaryExactLimit(t, newRedisTestStore)
}
func TestRedisReserveAmountZero(t *testing.T)    { testReserveAmountZero(t, newRedisTestStore) }
func TestRedisRollbackBasic(t *testing.T)        { testRollbackBasic(t, newRedisTestStore) }
func TestRedisRollbackFloorsAtZero(t *testing.T) { testRollbackFloorsAtZero(t, newRedisTestStore) }
func TestRedisRollbackNonExistent(t *testing.T)  { testRollbackNonExistent(t, newRedisTestStore) }
func TestRedisGetExisting(t *testing.T)          { testGetExisting(t, newRedisTestStore) }
func TestRedisGetNonExistent(t *testing.T)       { testGetNonExistent(t, newRedisTestStore) }
func TestRedisResetExisting(t *testing.T)        { testResetExisting(t, newRedisTestStore) }
func TestRedisResetNonExistent(t *testing.T)     { testResetNonExistent(t, newRedisTestStore) }

// --- Window behavior ---

func TestRedisWindowMinuteReset(t *testing.T) { testWindowMinuteReset(t, newRedisTestStore) }
func TestRedisWindowHourReset(t *testing.T)   { testWindowHourReset(t, newRedisTestStore) }
func TestRedisWindowDayReset(t *testing.T)    { testWindowDayReset(t, newRedisTestStore) }
func TestRedisNoResetWithinSameWindow(t *testing.T) {
	testNoResetWithinSameWindow(t, newRedisTestStore)
}
func TestRedisMultiWindowGap(t *testing.T)       { testMultiWindowGap(t, newRedisTestStore) }
func TestRedisCalendarAlignmentDay(t *testing.T) { testCalendarAlignmentDay(t, newRedisTestStore) }

// --- StateResolver contract ---

func TestRedisResolveStripPrefix(t *testing.T)   { testResolveStripPrefix(t, newRedisTestStore) }
func TestRedisResolveNonExistent(t *testing.T)   { testResolveNonExistent(t, newRedisTestStore) }
func TestRedisResolveExpiredWindow(t *testing.T) { testResolveExpiredWindow(t, newRedisTestStore) }
func TestRedisResolveActiveWindow(t *testing.T)  { testResolveActiveWindow(t, newRedisTestStore) }

// --- Concurrency ---

func TestRedisConcurrentReserve(t *testing.T) { testConcurrentReserve(t, newRedisTestStore) }

// --- Fail mode ---

func TestRedisFailOpen(t *testing.T) {
	mr := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+mr.Addr(), RedisOptions{FailMode: FailOpen})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	mr.Close()

	ok, val, err := store.Reserve("k", 1, 10, time.Minute)
	if err != nil {
		t.Fatalf("expected nil error in fail-open, got: %v", err)
	}
	if !ok {
		t.Fatal("expected allowed in fail-open")
	}
	if val != 0 {
		t.Fatalf("expected val=0 in fail-open, got %d", val)
	}

	err = store.Rollback("k", 1)
	if err != nil {
		t.Fatalf("expected nil error for rollback in fail-open, got: %v", err)
	}

	v, ws, err := store.Get("k")
	if err != nil {
		t.Fatalf("expected nil error for get in fail-open, got: %v", err)
	}
	if v != 0 || !ws.IsZero() {
		t.Fatalf("expected (0, zero) in fail-open, got (%d, %v)", v, ws)
	}

	rv, exists, err := store.Resolve("state.k")
	if err != nil {
		t.Fatalf("expected nil error for resolve in fail-open, got: %v", err)
	}
	if !exists || rv.(int64) != 0 {
		t.Fatalf("expected (0, true) in fail-open, got (%v, %v)", rv, exists)
	}
}

func TestRedisFailClosed(t *testing.T) {
	mr := miniredis.RunT(t)
	store, err := NewRedisStore("redis://"+mr.Addr(), RedisOptions{FailMode: FailClosed})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	mr.Close()

	ok, _, err := store.Reserve("k", 1, 10, time.Minute)
	if err == nil {
		t.Fatal("expected error in fail-closed")
	}
	if ok {
		t.Fatal("expected denied in fail-closed")
	}
}
