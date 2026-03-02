package transport

import (
	"encoding/json"
	"io"
	"sync"
)

// maxScannerBuffer is the maximum line size for bufio.Scanner (10 MB),
// accommodating large JSON-RPC messages.
const maxScannerBuffer = 10 * 1024 * 1024

// syncWriter serialises writes so that interleaved output from multiple
// goroutines doesn't corrupt the newline-delimited stream.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

var newline = []byte{'\n'}

// writeLine writes p followed by a newline, holding the mutex for the full write.
func (sw *syncWriter) writeLine(p []byte) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	_, _ = sw.w.Write(p)
	_, _ = sw.w.Write(newline)
}

// writeLineRaw writes p followed by a newline without synchronisation.
// Use only when the writer is not shared between goroutines.
func writeLineRaw(w io.Writer, p []byte) {
	_, _ = w.Write(p)
	_, _ = w.Write(newline)
}

// pendingCallbacks tracks OnResponse callbacks keyed by JSON-RPC request ID.
type pendingCallbacks struct {
	mu sync.Mutex
	m  map[string]func(json.RawMessage)
}

// newPendingCallbacks creates an empty callback registry.
func newPendingCallbacks() *pendingCallbacks {
	return &pendingCallbacks{m: make(map[string]func(json.RawMessage))}
}

// Add registers a callback for the given JSON-RPC request ID.
func (p *pendingCallbacks) Add(id json.RawMessage, fn func(json.RawMessage)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.m[string(id)] = fn
}

// Take removes and returns the callback for the given ID, if one exists.
func (p *pendingCallbacks) Take(id json.RawMessage) (func(json.RawMessage), bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := string(id)
	fn, ok := p.m[key]
	if ok {
		delete(p.m, key)
	}
	return fn, ok
}
