// Package events provides structured JSONL event logging for intercept proxy
// instances. Events are written to per-instance files in ~/.intercept/events/
// and are consumed by the "intercept status" command.
package events

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// EventEmitter writes structured events to a persistent log.
type EventEmitter interface {
	Emit(event Event) error
	Close() error
}

// Event is a flat struct covering all event types. Optional fields use omitempty
// so the JSONL output stays clean.
type Event struct {
	Ts       time.Time `json:"ts"`
	Type     string    `json:"type"`
	Instance string    `json:"instance"`

	// startup
	Server       string `json:"server,omitempty"`
	Config       string `json:"config,omitempty"`
	PID          int    `json:"pid,omitempty"`
	StateBackend string `json:"state_backend,omitempty"`
	FailMode     string `json:"fail_mode,omitempty"`

	// tool_call
	Tool     string `json:"tool,omitempty"`
	Result   string `json:"result,omitempty"`
	ArgsHash string `json:"args_hash,omitempty"`
	Rule     string `json:"rule,omitempty"`
	Message  string `json:"message,omitempty"`

	// config_reload
	Status string `json:"status,omitempty"`

	// heartbeat
	Counters map[string]int64 `json:"counters,omitempty"`
}

// JSONLEmitter writes events as newline-delimited JSON to a file.
type JSONLEmitter struct {
	mu       sync.Mutex
	file     *os.File
	buf      *bufio.Writer
	instance string
}

// NewJSONLEmitter creates an emitter that appends events to dir/<instanceID>.jsonl.
func NewJSONLEmitter(dir, instanceID string) (*JSONLEmitter, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating events directory: %w", err)
	}

	path := filepath.Join(dir, instanceID+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, fmt.Errorf("opening event file: %w", err)
	}

	return &JSONLEmitter{
		file:     f,
		buf:      bufio.NewWriter(f),
		instance: instanceID,
	}, nil
}

// Emit stamps the event with a timestamp and instance ID, then writes it as a
// single JSON line.
func (e *JSONLEmitter) Emit(event Event) error {
	event.Ts = time.Now().UTC()
	event.Instance = e.instance

	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if _, err := e.buf.Write(data); err != nil {
		return err
	}
	if err := e.buf.WriteByte('\n'); err != nil {
		return err
	}
	return e.buf.Flush()
}

// Close flushes any buffered data and closes the underlying file.
func (e *JSONLEmitter) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if err := e.buf.Flush(); err != nil {
		return err
	}
	return e.file.Close()
}

// NopEmitter is an EventEmitter that discards all events.
type NopEmitter struct{}

func (NopEmitter) Emit(Event) error { return nil }
func (NopEmitter) Close() error     { return nil }

// HashArgs returns a deterministic SHA-256 hash of the arguments map, prefixed
// with "sha256:". Returns an empty string for nil or empty args.
func HashArgs(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	data, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// EventRetention is the fixed duration after which old event files are pruned.
const EventRetention = 7 * 24 * time.Hour

// PruneOldFiles removes .jsonl files in dir whose modification time is older
// than EventRetention. If the directory does not exist, it returns nil.
// Errors are collected but pruning continues for all files.
func PruneOldFiles(dir string) error {
	retention := EventRetention
	matches, err := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	if err != nil {
		return fmt.Errorf("globbing event files: %w", err)
	}

	var firstErr error
	for _, path := range matches {
		info, err := os.Stat(path)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("stat %s: %w", path, err)
			}
			continue
		}
		age := time.Since(info.ModTime())
		if age > retention {
			slog.Debug("pruning old event file", "path", path, "age", age.Round(time.Second))
			if err := os.Remove(path); err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("removing %s: %w", path, err)
				}
			}
		}
	}
	return firstErr
}

// NewInstanceID returns an 8-character hex string from 4 random bytes.
func NewInstanceID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("reading crypto/rand: %v", err))
	}
	return hex.EncodeToString(b)
}
