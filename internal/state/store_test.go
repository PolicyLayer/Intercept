package state

import (
	"sync"
	"testing"
	"time"
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

type testableStore interface {
	StateStore
	Resolve(path string) (any, bool, error)
}

type storeFactory func(t *testing.T, clock *testClock) testableStore

// persistentStoreFactory also returns the path so the store can be reopened.
type persistentStoreFactory func(t *testing.T, clock *testClock) (testableStore, string)

// --- Basic operations ---

func testReserveEmpty(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	ok, val, err := store.Reserve("k", 1, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 1 {
		t.Fatalf("expected (true, 1), got (%v, %v)", ok, val)
	}
}

func testReserveMultipleWithinLimit(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	for i := int64(1); i <= 5; i++ {
		ok, val, err := store.Reserve("k", 1, 5, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if !ok || val != i {
			t.Fatalf("reserve %d: expected (true, %d), got (%v, %v)", i, i, ok, val)
		}
	}
}

func testReserveExceedsLimit(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 5, time.Minute)

	ok, val, err := store.Reserve("k", 1, 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok || val != 5 {
		t.Fatalf("expected (false, 5), got (%v, %v)", ok, val)
	}
}

func testReserveBoundaryExactLimit(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	// value + amount == limit should succeed
	ok, val, err := store.Reserve("k", 5, 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 5 {
		t.Fatalf("expected (true, 5), got (%v, %v)", ok, val)
	}

	// value + amount == limit + 1 should fail
	ok, val, err = store.Reserve("k", 1, 5, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatalf("expected denied at limit+1, got allowed with val=%d", val)
	}
}

func testReserveAmountZero(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	// Fill to limit
	store.Reserve("k", 10, 10, time.Minute)

	// amount=0 should always succeed
	ok, val, err := store.Reserve("k", 0, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 10 {
		t.Fatalf("expected (true, 10), got (%v, %v)", ok, val)
	}
}

func testRollbackBasic(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, time.Minute)
	if err := store.Rollback("k", 3); err != nil {
		t.Fatal(err)
	}

	val, _, err := store.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if val != 2 {
		t.Fatalf("expected 2 after rollback, got %d", val)
	}
}

func testRollbackFloorsAtZero(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 2, 10, time.Minute)
	if err := store.Rollback("k", 100); err != nil {
		t.Fatal(err)
	}

	val, _, err := store.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if val != 0 {
		t.Fatalf("expected 0 after excessive rollback, got %d", val)
	}
}

func testRollbackNonExistent(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	if err := store.Rollback("missing", 5); err != nil {
		t.Fatalf("rollback on missing key should be no-op, got: %v", err)
	}
}

func testGetExisting(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 7, 10, time.Minute)

	val, ws, err := store.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if val != 7 {
		t.Fatalf("expected 7, got %d", val)
	}
	expected := time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)
	if ws != expected {
		t.Fatalf("expected window start %v, got %v", expected, ws)
	}
}

func testGetNonExistent(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	val, ws, err := store.Get("missing")
	if err != nil {
		t.Fatal(err)
	}
	if val != 0 {
		t.Fatalf("expected 0, got %d", val)
	}
	if !ws.IsZero() {
		t.Fatalf("expected zero time, got %v", ws)
	}
}

func testResetExisting(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, time.Minute)
	if err := store.Reset("k"); err != nil {
		t.Fatal(err)
	}

	val, _, err := store.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if val != 0 {
		t.Fatalf("expected 0 after reset, got %d", val)
	}
}

func testResetNonExistent(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	if err := store.Reset("missing"); err != nil {
		t.Fatalf("reset on missing key should be no-op, got: %v", err)
	}
}

// --- Window behavior ---

func testWindowMinuteReset(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 45, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, time.Minute)

	// Advance to next minute
	clk.Set(time.Date(2026, 1, 15, 10, 31, 0, 0, time.UTC))

	ok, val, err := store.Reserve("k", 1, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 1 {
		t.Fatalf("expected reset to (true, 1), got (%v, %v)", ok, val)
	}
}

func testWindowHourReset(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 45, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, time.Hour)

	// Advance to next hour
	clk.Set(time.Date(2026, 1, 15, 11, 0, 0, 0, time.UTC))

	ok, val, err := store.Reserve("k", 1, 10, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 1 {
		t.Fatalf("expected reset to (true, 1), got (%v, %v)", ok, val)
	}
}

func testWindowDayReset(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 23, 59, 59, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, 24*time.Hour)

	// Advance to next day
	clk.Set(time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC))

	ok, val, err := store.Reserve("k", 1, 10, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 1 {
		t.Fatalf("expected reset to (true, 1), got (%v, %v)", ok, val)
	}
}

func testNoResetWithinSameWindow(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 3, 10, time.Minute)

	// Still within same minute
	clk.Set(time.Date(2026, 1, 15, 10, 30, 59, 0, time.UTC))

	ok, val, err := store.Reserve("k", 2, 10, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 5 {
		t.Fatalf("expected accumulated (true, 5), got (%v, %v)", ok, val)
	}
}

func testMultiWindowGap(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, time.Hour)

	// Advance 3 hours
	clk.Set(time.Date(2026, 1, 15, 13, 0, 0, 0, time.UTC))

	ok, val, err := store.Reserve("k", 1, 10, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != 1 {
		t.Fatalf("expected reset after 3h gap to (true, 1), got (%v, %v)", ok, val)
	}
}

func testCalendarAlignmentDay(t *testing.T, factory storeFactory) {
	// Reserve at 14:00, verify window start is 00:00
	clk := &testClock{now: time.Date(2026, 1, 15, 14, 0, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 1, 10, 24*time.Hour)

	_, ws, err := store.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	expected := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	if ws != expected {
		t.Fatalf("expected day window start %v, got %v", expected, ws)
	}
}

// --- StateResolver contract ---

func testResolveStripPrefix(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("tool.counter", 7, 10, time.Minute)

	val, exists, err := store.Resolve("state.tool.counter")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
	if val.(int64) != 7 {
		t.Fatalf("expected 7, got %v", val)
	}
}

func testResolveNonExistent(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	val, exists, err := store.Resolve("state.missing.counter")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected exists=true for missing counter")
	}
	if val.(int64) != 0 {
		t.Fatalf("expected 0, got %v", val)
	}
}

func testResolveExpiredWindow(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 5, 10, time.Minute)

	// Advance past window
	clk.Set(time.Date(2026, 1, 15, 10, 31, 0, 0, time.UTC))

	val, exists, err := store.Resolve("state.k")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
	if val.(int64) != 0 {
		t.Fatalf("expected 0 for expired window, got %v", val)
	}
}

func testResolveActiveWindow(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	store.Reserve("k", 3, 10, time.Minute)

	val, exists, err := store.Resolve("state.k")
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Fatal("expected exists=true")
	}
	if val.(int64) != 3 {
		t.Fatalf("expected 3, got %v", val)
	}
}

// --- Concurrency ---

func testConcurrentReserve(t *testing.T, factory storeFactory) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}
	store := factory(t, clk)

	const goroutines = 50
	const limit int64 = 20
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			store.Reserve("k", 1, limit, time.Minute)
		}()
	}
	wg.Wait()

	val, _, err := store.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if val > limit {
		t.Fatalf("counter %d exceeds limit %d", val, limit)
	}
	if val != limit {
		t.Fatalf("expected counter to reach limit %d, got %d", limit, val)
	}
}

// --- Persistence ---

func testPersistenceAcrossReopen(t *testing.T, factory persistentStoreFactory, reopener func(t *testing.T, path string, clock *testClock) testableStore) {
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}

	store, path := factory(t, clk)
	store.Reserve("k", 7, 10, time.Minute)
	store.Close()

	store2 := reopener(t, path, clk)
	defer store2.Close()

	val, _, err := store2.Get("k")
	if err != nil {
		t.Fatal(err)
	}
	if val != 7 {
		t.Fatalf("expected 7 after reopen, got %d", val)
	}
}

// --- WindowDuration helper ---

func TestWindowDurationHelper(t *testing.T) {
	tests := []struct {
		name     string
		expected time.Duration
	}{
		{"minute", time.Minute},
		{"hour", time.Hour},
		{"day", 24 * time.Hour},
		{"unknown", 0},
		{"", 0},
	}
	for _, tt := range tests {
		if got := WindowDuration(tt.name); got != tt.expected {
			t.Errorf("WindowDuration(%q) = %v, want %v", tt.name, got, tt.expected)
		}
	}
}
