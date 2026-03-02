package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Subprocess helper: when invoked with GO_WANT_HELPER_PROCESS=1 it acts as a
// simple echo server (reads lines from stdin, writes them back to stdout).
// ---------------------------------------------------------------------------

func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	defer os.Exit(0)

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBuffer)
	for scanner.Scan() {
		fmt.Println(scanner.Text())
	}
}

// helperCommand returns the command slice to spawn the echo helper subprocess.
func helperCommand() []string {
	return []string{os.Args[0], "-test.run=TestHelperProcess", "--"}
}

// ---------------------------------------------------------------------------
// JSON-RPC parsing unit tests
// ---------------------------------------------------------------------------

func TestIsToolsCall(t *testing.T) {
	tests := []struct {
		name string
		msg  rpcMessage
		want bool
	}{
		{
			name: "tools/call request",
			msg:  rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call"},
			want: true,
		},
		{
			name: "initialize request",
			msg:  rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize"},
			want: false,
		},
		{
			name: "response message",
			msg:  rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Result: json.RawMessage(`{}`)},
			want: false,
		},
		{
			name: "notification",
			msg:  rpcMessage{JSONRPC: "2.0", Method: "notifications/cancelled"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.msg.isToolsCall(); got != tt.want {
				t.Errorf("isToolsCall() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseToolsCallParams(t *testing.T) {
	raw := json.RawMessage(`{"name":"read_file","arguments":{"path":"/tmp/test.txt"}}`)
	p, err := parseToolsCallParams(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "read_file" {
		t.Errorf("name = %q, want %q", p.Name, "read_file")
	}
	if p.Arguments["path"] != "/tmp/test.txt" {
		t.Errorf("arguments[path] = %v, want %q", p.Arguments["path"], "/tmp/test.txt")
	}
}

func TestParseToolsCallParamsNoArguments(t *testing.T) {
	raw := json.RawMessage(`{"name":"list_tools"}`)
	p, err := parseToolsCallParams(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Name != "list_tools" {
		t.Errorf("name = %q, want %q", p.Name, "list_tools")
	}
	if p.Arguments != nil {
		t.Errorf("expected nil arguments, got %v", p.Arguments)
	}
}

// ---------------------------------------------------------------------------
// Transport integration tests using subprocess echo server
// ---------------------------------------------------------------------------

func runTransport(t *testing.T, input string, handler ToolCallHandler) string {
	t.Helper()

	clientIn := strings.NewReader(input)
	var clientOut bytes.Buffer

	tr := &StdioTransport{
		Command: helperCommand(),
		Stdin:   clientIn,
		Stdout:  &clientOut,
		Stderr:  io.Discard,
	}

	if err := tr.Start(context.Background(), handler); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	return clientOut.String()
}

func TestPassthroughInitialize(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	out := runTransport(t, msg+"\n", PassthroughHandler)

	if !strings.Contains(out, `"method":"initialize"`) {
		t.Errorf("expected initialize message in output, got: %s", out)
	}
}

func TestPassthroughToolsCallNotHandled(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	msg := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	out := runTransport(t, msg+"\n", PassthroughHandler)

	if !strings.Contains(out, `"method":"tools/call"`) {
		t.Errorf("expected tools/call forwarded, got: %s", out)
	}
}

func TestInterception(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	denied := `{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"denied"}}`
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled:  true,
			Response: json.RawMessage(denied),
		}
	}

	msg := `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	out := runTransport(t, msg+"\n", handler)

	if !strings.Contains(out, `"denied"`) {
		t.Errorf("expected denied response, got: %s", out)
	}
	// The echo server should NOT have received the message, so the original
	// tools/call should not appear echoed back.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Errorf("expected exactly 1 line (denied response), got %d: %v", len(lines), lines)
	}
}

func TestMixedTraffic(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	denied := `{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"denied"}}`
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled:  true,
			Response: json.RawMessage(denied),
		}
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dangerous","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"

	out := runTransport(t, input, handler)

	if !strings.Contains(out, `"initialize"`) {
		t.Error("expected initialize to be forwarded")
	}
	if !strings.Contains(out, `"denied"`) {
		t.Error("expected denied response for tools/call")
	}
	if !strings.Contains(out, `"tools/list"`) {
		t.Error("expected tools/list to be forwarded")
	}
}

func TestNonJSONForwarded(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	out := runTransport(t, "not json at all\n", PassthroughHandler)
	if !strings.Contains(out, "not json at all") {
		t.Errorf("expected non-JSON line forwarded, got: %s", out)
	}
}

func TestEmptyCommandError(t *testing.T) {
	tr := &StdioTransport{
		Command: nil,
		Stdin:   strings.NewReader(""),
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	}
	err := tr.Start(context.Background(), PassthroughHandler)
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

func TestNonExistentCommandError(t *testing.T) {
	tr := &StdioTransport{
		Command: []string{"/nonexistent/binary/xyz"},
		Stdin:   strings.NewReader(""),
		Stdout:  io.Discard,
		Stderr:  io.Discard,
	}
	err := tr.Start(context.Background(), PassthroughHandler)
	if err == nil {
		t.Fatal("expected error for nonexistent command")
	}
}

func TestLargeMessage(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	// Build a ~1MB JSON-RPC message.
	bigVal := strings.Repeat("x", 1_000_000)
	msg := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"big","arguments":{"data":"%s"}}}`, bigVal)

	out := runTransport(t, msg+"\n", PassthroughHandler)

	if !strings.Contains(out, bigVal) {
		t.Errorf("large message not faithfully forwarded (output length: %d)", len(out))
	}
}

func TestClientDisconnectCleanShutdown(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	// Empty input simulates immediate client disconnect.
	out := runTransport(t, "", PassthroughHandler)
	if out != "" {
		t.Errorf("expected empty output, got: %q", out)
	}
}

func TestOnResponseNotInvokedForRequests(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	done := make(chan struct{})

	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled: false,
			OnResponse: func(data json.RawMessage) {
				close(done)
			},
		}
	}

	// Echo server echoes the request line, which has "method" set.
	// That makes it a request, not a response, so the callback should NOT fire.
	msg := `{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	out := runTransport(t, msg+"\n", handler)

	if !strings.Contains(out, `"tools/call"`) {
		t.Errorf("expected tools/call forwarded, got: %s", out)
	}

	select {
	case <-done:
		t.Error("callback should not be invoked for echoed request messages")
	default:
	}
}

func TestOnResponseWithRealResponse(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	var callbackData json.RawMessage
	done := make(chan struct{})

	// Send a tools/call that gets forwarded, plus a fake "response" that matches
	// the same ID. The echo server will echo both lines. The second line (the
	// response) should trigger the callback.
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled: false,
			OnResponse: func(data json.RawMessage) {
				callbackData = append(json.RawMessage(nil), data...)
				close(done)
			},
		}
	}

	// Two lines: the tools/call request and a response-shaped message with same ID.
	// The echo server echoes both. The request echo has "method" so it won't trigger.
	// The response echo has no "method" and has "result", so it will trigger.
	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`,
		`{"jsonrpc":"2.0","id":42,"result":{"content":[{"type":"text","text":"ok"}]}}`,
	}, "\n") + "\n"

	clientIn := strings.NewReader(input)
	var clientOut bytes.Buffer

	tr := &StdioTransport{
		Command: helperCommand(),
		Stdin:   clientIn,
		Stdout:  &clientOut,
		Stderr:  io.Discard,
	}

	if err := tr.Start(context.Background(), handler); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}

	select {
	case <-done:
		// Callback was invoked
	default:
		t.Fatal("expected OnResponse callback to be invoked")
	}

	if callbackData == nil {
		t.Fatal("callback data is nil")
	}

	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(callbackData, &resp); err != nil {
		t.Fatalf("failed to unmarshal callback data: %v", err)
	}
	if resp.ID != 42 {
		t.Errorf("callback response ID = %d, want 42", resp.ID)
	}
}

func TestNilOnResponseBackwardCompatible(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	// PassthroughHandler returns nil OnResponse. Should work exactly as before.
	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	out := runTransport(t, msg+"\n", PassthroughHandler)

	if !strings.Contains(out, `"tools/call"`) {
		t.Errorf("expected tools/call forwarded, got: %s", out)
	}
}
