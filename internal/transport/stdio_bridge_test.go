package transport

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Fake upstream helpers for StdioBridgeTransport
// ---------------------------------------------------------------------------

// jsonUpstreamWithSession returns an httptest.Server that echoes JSON-RPC
// POSTs and sets a Mcp-Session-Id header on the response.
func jsonUpstreamWithSession(t *testing.T, sessionID string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			body, _ := io.ReadAll(r.Body)
			var msg rpcMessage
			if err := json.Unmarshal(body, &msg); err == nil && msg.ID != nil {
				if sessionID != "" {
					w.Header().Set("Mcp-Session-Id", sessionID)
				}
				resp := map[string]any{
					"jsonrpc": "2.0",
					"id":      json.RawMessage(msg.ID),
					"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}},
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(resp)
				return
			}
			// Batch.
			var batch []rpcMessage
			if json.Unmarshal(body, &batch) == nil {
				var responses []map[string]any
				for _, msg := range batch {
					if msg.ID != nil {
						responses = append(responses, map[string]any{
							"jsonrpc": "2.0",
							"id":      json.RawMessage(msg.ID),
							"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "ok"}}},
						})
					}
				}
				if sessionID != "" {
					w.Header().Set("Mcp-Session-Id", sessionID)
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(responses)
				return
			}
			// Notifications get 202.
			w.WriteHeader(http.StatusAccepted)

		case "GET":
			w.WriteHeader(http.StatusMethodNotAllowed)

		case "DELETE":
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

// sseUpstreamForBridge returns an httptest.Server where POST returns SSE responses.
func sseUpstreamForBridge(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			body, _ := io.ReadAll(r.Body)
			var msg rpcMessage
			if err := json.Unmarshal(body, &msg); err != nil || msg.ID == nil {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			resp, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"id":      json.RawMessage(msg.ID),
				"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "sse-ok"}}},
			})

			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			sw, _ := newSSEWriter(w)
			sw.WriteEvent(sseEvent{Type: "message", Data: string(resp)})

		case "GET":
			w.WriteHeader(http.StatusMethodNotAllowed)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

// runBridgeTransport sends the given input lines through a StdioBridgeTransport
// pointed at the upstream URL, with the given handler.
func runBridgeTransport(t *testing.T, upstreamURL string, input string, handler ToolCallHandler) string {
	t.Helper()

	var out bytes.Buffer
	tr := &StdioBridgeTransport{
		URL:    upstreamURL,
		Stdin:  strings.NewReader(input),
		Stdout: &out,
		Stderr: io.Discard,
	}

	if err := tr.Start(t.Context(), handler); err != nil {
		t.Fatalf("Start returned error: %v", err)
	}
	return out.String()
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestBridgePassthroughJSON(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	out := runBridgeTransport(t, upstream.URL, msg+"\n", PassthroughHandler)

	if !strings.Contains(out, `"id":1`) {
		t.Errorf("expected response with id:1, got: %s", out)
	}
	if !strings.Contains(out, `"result"`) {
		t.Errorf("expected result in response, got: %s", out)
	}
}

func TestBridgePassthroughSSE(t *testing.T) {
	upstream := sseUpstreamForBridge(t)
	defer upstream.Close()

	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	out := runBridgeTransport(t, upstream.URL, msg+"\n", PassthroughHandler)

	if !strings.Contains(out, "sse-ok") {
		t.Errorf("expected sse-ok in response, got: %s", out)
	}
}

func TestBridgeToolsCallDenied(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	denied := `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"denied by policy"}}`
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{Handled: true, Response: json.RawMessage(denied)}
	}

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dangerous","arguments":{}}}`
	out := runBridgeTransport(t, upstream.URL, msg+"\n", handler)

	if !strings.Contains(out, "denied by policy") {
		t.Errorf("expected denied response, got: %s", out)
	}
}

func TestBridgeToolsCallAllowed(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	var callbackCalled bool
	var mu sync.Mutex
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled: false,
			OnResponse: func(data json.RawMessage) {
				mu.Lock()
				callbackCalled = true
				mu.Unlock()
			},
		}
	}

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	out := runBridgeTransport(t, upstream.URL, msg+"\n", handler)

	if !strings.Contains(out, "ok") {
		t.Errorf("expected ok response, got: %s", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if !callbackCalled {
		t.Error("expected OnResponse callback to be invoked")
	}
}

func TestBridgeToolsCallAllowedSSE(t *testing.T) {
	upstream := sseUpstreamForBridge(t)
	defer upstream.Close()

	var callbackCalled bool
	var mu sync.Mutex
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled: false,
			OnResponse: func(data json.RawMessage) {
				mu.Lock()
				callbackCalled = true
				mu.Unlock()
			},
		}
	}

	msg := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	out := runBridgeTransport(t, upstream.URL, msg+"\n", handler)

	if !strings.Contains(out, "sse-ok") {
		t.Errorf("expected sse-ok in response, got: %s", out)
	}

	mu.Lock()
	defer mu.Unlock()
	if !callbackCalled {
		t.Error("expected OnResponse callback to be invoked")
	}
}

func TestBridgeSessionIDTracking(t *testing.T) {
	var mu sync.Mutex
	var receivedSessionIDs []string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			mu.Lock()
			receivedSessionIDs = append(receivedSessionIDs, r.Header.Get("Mcp-Session-Id"))
			mu.Unlock()

			body, _ := io.ReadAll(r.Body)
			var msg rpcMessage
			if err := json.Unmarshal(body, &msg); err == nil && msg.ID != nil {
				w.Header().Set("Mcp-Session-Id", "test-session-42")
				w.Header().Set("Content-Type", "application/json")
				resp := map[string]any{
					"jsonrpc": "2.0",
					"id":      json.RawMessage(msg.ID),
					"result":  map[string]any{},
				}
				json.NewEncoder(w).Encode(resp)
				return
			}
			w.WriteHeader(http.StatusAccepted)

		case "GET":
			w.WriteHeader(http.StatusMethodNotAllowed)

		case "DELETE":
			mu.Lock()
			receivedSessionIDs = append(receivedSessionIDs, "DELETE:"+r.Header.Get("Mcp-Session-Id"))
			mu.Unlock()
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer upstream.Close()

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"

	runBridgeTransport(t, upstream.URL, input, PassthroughHandler)

	mu.Lock()
	defer mu.Unlock()

	// First request should have no session ID.
	if len(receivedSessionIDs) < 2 {
		t.Fatalf("expected at least 2 requests, got %d", len(receivedSessionIDs))
	}
	if receivedSessionIDs[0] != "" {
		t.Errorf("first request should have no session ID, got %q", receivedSessionIDs[0])
	}
	// Second request should include the session ID from the first response.
	if receivedSessionIDs[1] != "test-session-42" {
		t.Errorf("second request session ID = %q, want %q", receivedSessionIDs[1], "test-session-42")
	}

	// Cleanup DELETE should include the session ID.
	found := slices.Contains(receivedSessionIDs, "DELETE:test-session-42")
	if !found {
		t.Errorf("expected DELETE with session ID, got: %v", receivedSessionIDs)
	}
}

func TestBridgeNotificationAccepted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		w.WriteHeader(http.StatusMethodNotAllowed)
	}))
	defer upstream.Close()

	// Notification (no ID) should get 202 and produce no output.
	msg := `{"jsonrpc":"2.0","method":"notifications/initialized"}`
	out := runBridgeTransport(t, upstream.URL, msg+"\n", PassthroughHandler)

	if strings.TrimSpace(out) != "" {
		t.Errorf("expected no output for notification, got: %s", out)
	}
}

func TestBridgeMixedTraffic(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	denied := `{"jsonrpc":"2.0","id":2,"error":{"code":-32600,"message":"denied"}}`
	handler := func(req ToolCallRequest) ToolCallResult {
		if req.Name == "dangerous" {
			return ToolCallResult{Handled: true, Response: json.RawMessage(denied)}
		}
		return ToolCallResult{Handled: false}
	}

	input := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dangerous","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}`,
	}, "\n") + "\n"

	out := runBridgeTransport(t, upstream.URL, input, handler)

	if !strings.Contains(out, `"id":1`) {
		t.Error("expected initialize response")
	}
	if !strings.Contains(out, "denied") {
		t.Error("expected denied response for tools/call")
	}
	if !strings.Contains(out, `"id":3`) {
		t.Error("expected tools/list response")
	}
}

func TestBridgeBatchMixed(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	handler := func(req ToolCallRequest) ToolCallResult {
		if req.Name == "dangerous" {
			return ToolCallResult{
				Handled:  true,
				Response: json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32600,"message":"denied"}}`, string(req.ID))),
			}
		}
		return ToolCallResult{Handled: false}
	}

	batch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}},{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dangerous","arguments":{}}}]`
	out := runBridgeTransport(t, upstream.URL, batch+"\n", handler)

	if !strings.Contains(out, "denied") {
		t.Errorf("expected denied in batch response, got: %s", out)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("expected ok for allowed call, got: %s", out)
	}
}

func TestBridgeBatchAllDenied(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled:  true,
			Response: json.RawMessage(fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32600,"message":"denied"}}`, string(req.ID))),
		}
	}

	batch := `[{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"a","arguments":{}}},{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"b","arguments":{}}}]`
	out := runBridgeTransport(t, upstream.URL, batch+"\n", handler)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Errorf("expected 2 lines (one per denied response), got %d: %v", len(lines), lines)
	}
}

func TestBridgeAcceptHeaders(t *testing.T) {
	var mu sync.Mutex
	var postAccept string

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			mu.Lock()
			postAccept = r.Header.Get("Accept")
			mu.Unlock()

			body, _ := io.ReadAll(r.Body)
			var msg rpcMessage
			if json.Unmarshal(body, &msg) == nil && msg.ID != nil {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      json.RawMessage(msg.ID),
					"result":  map[string]any{},
				})
				return
			}
			w.WriteHeader(http.StatusAccepted)
		case "GET":
			w.WriteHeader(http.StatusMethodNotAllowed)
		case "DELETE":
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer upstream.Close()

	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	runBridgeTransport(t, upstream.URL, msg+"\n", PassthroughHandler)

	mu.Lock()
	defer mu.Unlock()
	if postAccept != "application/json, text/event-stream" {
		t.Errorf("POST Accept = %q, want %q", postAccept, "application/json, text/event-stream")
	}
}

func TestBridgeEmptyInput(t *testing.T) {
	upstream := jsonUpstreamWithSession(t, "")
	defer upstream.Close()

	out := runBridgeTransport(t, upstream.URL, "", PassthroughHandler)
	if strings.TrimSpace(out) != "" {
		t.Errorf("expected empty output, got: %q", out)
	}
}
