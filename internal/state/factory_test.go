package state

import (
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestPrefixedStoreIsolation(t *testing.T) {
	mr := miniredis.RunT(t)
	clk := &testClock{now: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC)}

	base, err := NewRedisStore("redis://"+mr.Addr(), RedisOptions{Clock: clk})
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()

	storeA := &prefixedStore{Store: base, prefix: "a:"}
	storeB := &prefixedStore{Store: base, prefix: "b:"}

	storeA.Reserve("counter", 5, 10, time.Minute)
	storeB.Reserve("counter", 3, 10, time.Minute)

	valA, _, err := storeA.Get("counter")
	if err != nil {
		t.Fatal(err)
	}
	if valA != 5 {
		t.Fatalf("expected storeA counter=5, got %d", valA)
	}

	valB, _, err := storeB.Get("counter")
	if err != nil {
		t.Fatal(err)
	}
	if valB != 3 {
		t.Fatalf("expected storeB counter=3, got %d", valB)
	}

	// Resolve should also be isolated
	rv, exists, err := storeA.Resolve("state.counter")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || rv.(int64) != 5 {
		t.Fatalf("expected storeA resolve=5, got %v", rv)
	}

	rv, exists, err = storeB.Resolve("state.counter")
	if err != nil {
		t.Fatal(err)
	}
	if !exists || rv.(int64) != 3 {
		t.Fatalf("expected storeB resolve=3, got %v", rv)
	}
}

func TestEmptyPrefixPassthrough(t *testing.T) {
	mr := miniredis.RunT(t)

	store, err := OpenStore("redis://"+mr.Addr(), "", "", "closed")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	store.Reserve("bare_key", 7, 10, time.Minute)

	// Verify the key exists in Redis without any prefix via miniredis inspection
	if !mr.Exists("bare_key") {
		t.Fatal("expected bare_key to exist in Redis without prefix")
	}
}
