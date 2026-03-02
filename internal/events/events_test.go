package events

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestJSONLEmitterWritesEvents(t *testing.T) {
	dir := t.TempDir()
	id := "test1234"
	em, err := NewJSONLEmitter(dir, id)
	if err != nil {
		t.Fatalf("NewJSONLEmitter: %v", err)
	}

	em.Emit(Event{Type: "startup", Server: "cat", PID: 42})
	em.Emit(Event{Type: "tool_call", Tool: "delete_customer", Result: "denied"})
	em.Emit(Event{Type: "shutdown"})
	em.Close()

	data, err := os.ReadFile(filepath.Join(dir, id+".jsonl"))
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(lines))
	}

	types := []string{"startup", "tool_call", "shutdown"}
	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d: invalid JSON: %v", i, err)
		}
		if ev.Type != types[i] {
			t.Errorf("line %d: type = %q, want %q", i, ev.Type, types[i])
		}
		if ev.Instance != id {
			t.Errorf("line %d: instance = %q, want %q", i, ev.Instance, id)
		}
		if ev.Ts.IsZero() {
			t.Errorf("line %d: ts is zero", i)
		}
	}
}

func TestJSONLEmitterConcurrent(t *testing.T) {
	dir := t.TempDir()
	em, err := NewJSONLEmitter(dir, "concurrent")
	if err != nil {
		t.Fatalf("NewJSONLEmitter: %v", err)
	}

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			em.Emit(Event{Type: "tool_call", Tool: "test"})
		}()
	}
	wg.Wait()
	em.Close()

	data, err := os.ReadFile(filepath.Join(dir, "concurrent.jsonl"))
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != n {
		t.Fatalf("expected %d lines, got %d", n, len(lines))
	}

	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("line %d: invalid JSON (interleaving corruption?): %v\nline: %s", i, err, line)
		}
	}
}

func TestEmitStampsMetadata(t *testing.T) {
	dir := t.TempDir()
	em, err := NewJSONLEmitter(dir, "stamps")
	if err != nil {
		t.Fatalf("NewJSONLEmitter: %v", err)
	}

	em.Emit(Event{Type: "startup"})
	em.Close()

	data, err := os.ReadFile(filepath.Join(dir, "stamps.jsonl"))
	if err != nil {
		t.Fatalf("reading event file: %v", err)
	}

	var ev Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(data))), &ev); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if ev.Ts.IsZero() {
		t.Error("expected Ts to be set")
	}
	if ev.Instance != "stamps" {
		t.Errorf("instance = %q, want %q", ev.Instance, "stamps")
	}
}

func TestHashArgsDeterministic(t *testing.T) {
	args := map[string]any{"b": 2, "a": 1}
	h1 := HashArgs(args)
	h2 := HashArgs(args)
	if h1 != h2 {
		t.Errorf("hashes differ: %s vs %s", h1, h2)
	}
	if !strings.HasPrefix(h1, "sha256:") {
		t.Errorf("expected sha256: prefix, got %s", h1)
	}
}

func TestHashArgsEmpty(t *testing.T) {
	if got := HashArgs(nil); got != "" {
		t.Errorf("HashArgs(nil) = %q, want empty", got)
	}
	if got := HashArgs(map[string]any{}); got != "" {
		t.Errorf("HashArgs({}) = %q, want empty", got)
	}
}

func TestNewInstanceID(t *testing.T) {
	id1 := NewInstanceID()
	id2 := NewInstanceID()
	if len(id1) != 8 {
		t.Errorf("instance ID length = %d, want 8", len(id1))
	}
	if id1 == id2 {
		t.Error("two instance IDs should differ")
	}
}

func TestPruneOldFiles(t *testing.T) {
	dir := t.TempDir()

	// Create 3 files: 2 old, 1 recent.
	old1 := filepath.Join(dir, "old1.jsonl")
	old2 := filepath.Join(dir, "old2.jsonl")
	recent := filepath.Join(dir, "recent.jsonl")

	for _, f := range []string{old1, old2, recent} {
		if err := os.WriteFile(f, []byte(`{"type":"test"}`+"\n"), 0644); err != nil {
			t.Fatalf("writing %s: %v", f, err)
		}
	}

	// Set old files to older than EventRetention.
	oldTime := time.Now().Add(-(EventRetention + 24*time.Hour))
	os.Chtimes(old1, oldTime, oldTime)
	os.Chtimes(old2, oldTime, oldTime)

	if err := PruneOldFiles(dir); err != nil {
		t.Fatalf("PruneOldFiles: %v", err)
	}

	// Old files should be gone.
	for _, f := range []string{old1, old2} {
		if _, err := os.Stat(f); !os.IsNotExist(err) {
			t.Errorf("expected %s to be deleted", filepath.Base(f))
		}
	}

	// Recent file should remain.
	if _, err := os.Stat(recent); err != nil {
		t.Errorf("expected recent.jsonl to remain, got: %v", err)
	}
}

func TestPruneOldFilesEmptyDir(t *testing.T) {
	dir := t.TempDir()
	if err := PruneOldFiles(dir); err != nil {
		t.Fatalf("PruneOldFiles on empty dir: %v", err)
	}
}

func TestPruneOldFilesMissingDir(t *testing.T) {
	if err := PruneOldFiles("/tmp/nonexistent-intercept-test-dir"); err != nil {
		t.Fatalf("PruneOldFiles on missing dir: %v", err)
	}
}

func TestNopEmitter(t *testing.T) {
	var em NopEmitter
	if err := em.Emit(Event{Type: "test"}); err != nil {
		t.Errorf("NopEmitter.Emit returned error: %v", err)
	}
	if err := em.Close(); err != nil {
		t.Errorf("NopEmitter.Close returned error: %v", err)
	}
}
