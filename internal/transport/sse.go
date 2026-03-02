package transport

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// sseEvent represents a single Server-Sent Event.
type sseEvent struct {
	Type string // "event:" field (e.g., "message", "endpoint"); empty = default
	Data string // "data:" field (may have been multi-line, joined with \n)
	ID   string // "id:" field; empty if not set
}

// sseReader reads SSE events from a stream.
type sseReader struct {
	scanner *bufio.Scanner
}

// newSSEReader creates an sseReader over the given io.Reader.
func newSSEReader(r io.Reader) *sseReader {
	return &sseReader{scanner: bufio.NewScanner(r)}
}

// Next reads the next complete SSE event from the stream. Returns io.EOF
// when the stream ends. Per the SSE spec, blank lines delimit events; lines
// starting with ":" are comments and are ignored; multiple "data:" lines are
// joined with "\n".
func (r *sseReader) Next() (sseEvent, error) {
	var ev sseEvent
	var dataLines []string
	hasFields := false

	for r.scanner.Scan() {
		line := r.scanner.Text()

		// Blank line: dispatch event if we have accumulated fields.
		if line == "" {
			if hasFields {
				ev.Data = strings.Join(dataLines, "\n")
				return ev, nil
			}
			continue
		}

		// Comment line.
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, _ := strings.Cut(line, ":")
		// Strip a single leading space from value per SSE spec.
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			ev.Type = value
			hasFields = true
		case "data":
			dataLines = append(dataLines, value)
			hasFields = true
		case "id":
			ev.ID = value
			hasFields = true
		}
	}

	if err := r.scanner.Err(); err != nil {
		return sseEvent{}, err
	}

	// Stream ended. If we accumulated fields without a final blank line,
	// emit the event (lenient parsing).
	if hasFields {
		ev.Data = strings.Join(dataLines, "\n")
		return ev, nil
	}

	return sseEvent{}, io.EOF
}

// sseWriter writes SSE events to an http.ResponseWriter, flushing after each.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSEWriter creates an sseWriter. The ResponseWriter must implement http.Flusher.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("ResponseWriter does not implement http.Flusher")
	}
	return &sseWriter{w: w, flusher: f}, nil
}

// WriteEvent formats and writes a single SSE event, then flushes. Multi-line
// data is split into multiple "data:" lines.
func (sw *sseWriter) WriteEvent(ev sseEvent) error {
	if ev.Type != "" {
		if _, err := fmt.Fprintf(sw.w, "event: %s\n", ev.Type); err != nil {
			return err
		}
	}

	if ev.Data != "" {
		for line := range strings.SplitSeq(ev.Data, "\n") {
			if _, err := fmt.Fprintf(sw.w, "data: %s\n", line); err != nil {
				return err
			}
		}
	} else {
		if _, err := fmt.Fprint(sw.w, "data: \n"); err != nil {
			return err
		}
	}

	if ev.ID != "" {
		if _, err := fmt.Fprintf(sw.w, "id: %s\n", ev.ID); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprint(sw.w, "\n"); err != nil {
		return err
	}

	sw.flusher.Flush()
	return nil
}
