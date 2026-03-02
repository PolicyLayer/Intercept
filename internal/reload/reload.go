// Package reload provides file-watching hot reload for policy configuration.
// When the policy YAML changes on disk, it is re-parsed, validated, and swapped
// into the running proxy without restarting.
package reload

import (
	"context"
	"log/slog"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/policylayer/intercept/internal/config"
)

// TryReload loads and validates a config file. Returns the parsed config
// or the first error encountered.
func TryReload(path string) (*config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	if errs := config.Validate(cfg); len(errs) > 0 {
		return nil, errs[0]
	}

	return cfg, nil
}

// Watch monitors a config file for changes, debounces rapid edits, and calls
// swap with the new config on successful reload. It blocks until ctx is
// cancelled, then returns nil.
func Watch(ctx context.Context, path string, swap func(*config.Config), onError func(error)) error {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer watcher.Close()

	if err := watcher.Add(path); err != nil {
		return err
	}

	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	defer debounce.Stop()

	const debounceDelay = 100 * time.Millisecond

	for {
		select {
		case <-ctx.Done():
			return nil

		case event, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if event.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename) != 0 {
				debounce.Reset(debounceDelay)
			}

		case err, ok := <-watcher.Errors:
			if !ok {
				return nil
			}
			slog.Warn("config watcher error", "error", err)

		case <-debounce.C:
			cfg, err := TryReload(path)
			if err != nil {
				slog.Warn("config reload failed, keeping previous config", "error", err)
				if onError != nil {
					onError(err)
				}
				continue
			}

			swap(cfg)

			// Re-add the watch to handle inode replacement (vim/emacs save pattern).
			_ = watcher.Remove(path)
			if err := watcher.Add(path); err != nil {
				slog.Warn("failed to re-add config watch", "error", err)
			}

			slog.Info("policy reloaded successfully")
		}
	}
}
