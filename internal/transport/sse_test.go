package transport

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSSEReaderBasic(t *testing.T) {
	input := "event: message\ndata: hello\nid: 1\n\nevent: endpoint\ndata: /rpc\nid: 2\n\n"
	r := newSSEReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != "message" || ev.Data != "hello" || ev.ID != "1" {
		t.Errorf("event 1: got %+v", ev)
	}

	ev, err = r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != "endpoint" || ev.Data != "/rpc" || ev.ID != "2" {
		t.Errorf("event 2: got %+v", ev)
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}

func TestSSEReaderMultiLineData(t *testing.T) {
	input := "event: message\ndata: line1\ndata: line2\ndata: line3\n\n"
	r := newSSEReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Data != "line1\nline2\nline3" {
		t.Errorf("expected multi-line data, got %q", ev.Data)
	}
}

func TestSSEReaderComments(t *testing.T) {
	input := ": this is a comment\nevent: message\n: another comment\ndata: hello\n\n"
	r := newSSEReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != "message" || ev.Data != "hello" {
		t.Errorf("got %+v", ev)
	}
}

func TestSSEReaderNoEventType(t *testing.T) {
	input := "data: just data\n\n"
	r := newSSEReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != "" {
		t.Errorf("expected empty event type, got %q", ev.Type)
	}
	if ev.Data != "just data" {
		t.Errorf("expected 'just data', got %q", ev.Data)
	}
}

func TestSSEReaderSkipsBlankLinesBeforeEvent(t *testing.T) {
	input := "\n\n\ndata: hello\n\n"
	r := newSSEReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Data != "hello" {
		t.Errorf("expected 'hello', got %q", ev.Data)
	}
}

func TestSSEReaderStreamEndWithoutBlankLine(t *testing.T) {
	// Stream ends without a trailing blank line; event should still be emitted.
	input := "event: message\ndata: final"
	r := newSSEReader(strings.NewReader(input))

	ev, err := r.Next()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ev.Type != "message" || ev.Data != "final" {
		t.Errorf("got %+v", ev)
	}
}

func TestSSEWriterFormat(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = sw.WriteEvent(sseEvent{Type: "message", Data: "hello", ID: "1"})
	if err != nil {
		t.Fatalf("WriteEvent error: %v", err)
	}

	got := rec.Body.String()
	expected := "event: message\ndata: hello\nid: 1\n\n"
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestSSEWriterMultiLineData(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = sw.WriteEvent(sseEvent{Data: "line1\nline2\nline3"})
	if err != nil {
		t.Fatalf("WriteEvent error: %v", err)
	}

	got := rec.Body.String()
	expected := "data: line1\ndata: line2\ndata: line3\n\n"
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestSSEWriterEmptyData(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	err = sw.WriteEvent(sseEvent{Type: "ping"})
	if err != nil {
		t.Fatalf("WriteEvent error: %v", err)
	}

	got := rec.Body.String()
	expected := "event: ping\ndata: \n\n"
	if got != expected {
		t.Errorf("got %q, want %q", got, expected)
	}
}

func TestSSERoundTrip(t *testing.T) {
	events := []sseEvent{
		{Type: "message", Data: `{"jsonrpc":"2.0","id":1}`, ID: "1"},
		{Type: "endpoint", Data: "http://localhost:3000/message"},
		{Data: "default event type"},
		{Type: "message", Data: "line1\nline2", ID: "3"},
	}

	rec := httptest.NewRecorder()
	sw, err := newSSEWriter(rec)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, ev := range events {
		if err := sw.WriteEvent(ev); err != nil {
			t.Fatalf("WriteEvent error: %v", err)
		}
	}

	r := newSSEReader(strings.NewReader(rec.Body.String()))
	for i, want := range events {
		got, err := r.Next()
		if err != nil {
			t.Fatalf("event %d: unexpected error: %v", i, err)
		}
		if got.Type != want.Type || got.Data != want.Data || got.ID != want.ID {
			t.Errorf("event %d: got %+v, want %+v", i, got, want)
		}
	}

	_, err = r.Next()
	if err != io.EOF {
		t.Errorf("expected io.EOF, got %v", err)
	}
}
