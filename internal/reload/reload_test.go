package reload

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/policylayer/intercept/internal/config"
)

const validPolicy = `version: "1"
description: test
tools:
  delete_customer:
    rules:
      - name: block_delete
        action: deny
        on_deny: "not allowed"
`

const validPolicyUpdated = `version: "1"
description: updated
tools:
  read_file:
    rules:
      - name: check_path
        action: evaluate
        conditions:
          - path: args.path
            op: contains
            value: /tmp
`

const invalidYAML = `{{{not yaml at all`

const invalidConfig = `version: "1"
tools:
  tool:
    rules:
      - name: bad_rule
        action: evaluate
        conditions:
          - path: args.x
            op: bad_operator
            value: 1
`

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
}

func TestTryReloadValid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, validPolicy)

	cfg, err := TryReload(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Description != "test" {
		t.Errorf("description = %q, want %q", cfg.Description, "test")
	}
	if _, ok := cfg.Tools["delete_customer"]; !ok {
		t.Error("expected delete_customer tool in config")
	}
}

func TestTryReloadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, invalidYAML)

	_, err := TryReload(path)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestTryReloadValidationError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, invalidConfig)

	_, err := TryReload(path)
	if err == nil {
		t.Fatal("expected error for invalid config")
	}
}

func TestWatchReloadsOnChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, validPolicy)

	swapped := make(chan *config.Config, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, path, func(cfg *config.Config) {
			swapped <- cfg
		}, nil)
	}()

	// Give the watcher time to start.
	time.Sleep(50 * time.Millisecond)

	writeFile(t, path, validPolicyUpdated)

	select {
	case cfg := <-swapped:
		if cfg.Description != "updated" {
			t.Errorf("description = %q, want %q", cfg.Description, "updated")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for config swap")
	}

	cancel()
	if err := <-errCh; err != nil {
		t.Fatalf("Watch returned error: %v", err)
	}
}

func TestWatchRejectsInvalidChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, validPolicy)

	swapped := make(chan *config.Config, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		Watch(ctx, path, func(cfg *config.Config) {
			swapped <- cfg
		}, nil)
	}()

	time.Sleep(50 * time.Millisecond)

	writeFile(t, path, invalidYAML)

	select {
	case <-swapped:
		t.Fatal("swap should not be called for invalid config")
	case <-time.After(500 * time.Millisecond):
		// Expected: no swap for invalid config.
	}

	cancel()
}

func TestWatchCancellation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, validPolicy)

	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- Watch(ctx, path, func(cfg *config.Config) {}, nil)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Watch should return nil on cancellation, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for Watch to return")
	}
}

func TestWatchCallsOnErrorForInvalidReload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.yaml")
	writeFile(t, path, validPolicy)

	onErrorCalled := make(chan error, 1)
	ctx := t.Context()

	go func() {
		Watch(ctx, path, func(cfg *config.Config) {}, func(err error) {
			onErrorCalled <- err
		})
	}()

	time.Sleep(50 * time.Millisecond)

	writeFile(t, path, invalidYAML)

	select {
	case err := <-onErrorCalled:
		if err == nil {
			t.Fatal("expected non-nil error in onError callback")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for onError callback")
	}
}
