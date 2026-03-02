package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fake upstream helpers
// ---------------------------------------------------------------------------

// jsonUpstream returns an httptest.Server that echoes back JSON-RPC POSTs
// as application/json. GET returns 405. DELETE returns 200.
func jsonUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			body, _ := io.ReadAll(r.Body)
			// Parse request to build a response with matching ID.
			var msg rpcMessage
			if err := json.Unmarshal(body, &msg); err == nil && msg.ID != nil {
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
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(responses)
				return
			}
			w.WriteHeader(http.StatusBadRequest)

		case "DELETE":
			w.WriteHeader(http.StatusOK)

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

// sseUpstream returns an httptest.Server where POST returns an SSE stream
// containing a single "message" event with the JSON-RPC response.
func sseUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			body, _ := io.ReadAll(r.Body)
			var msg rpcMessage
			if err := json.Unmarshal(body, &msg); err != nil || msg.ID == nil {
				w.WriteHeader(http.StatusBadRequest)
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
			// Server-initiated SSE stream with a single event.
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			sw, _ := newSSEWriter(w)
			notif, _ := json.Marshal(map[string]any{
				"jsonrpc": "2.0",
				"method":  "notifications/tools/list_changed",
			})
			sw.WriteEvent(sseEvent{Type: "message", Data: string(notif)})

		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
}

// legacySSEUpstream returns an httptest.Server that speaks the legacy SSE protocol:
// GET returns an SSE stream starting with an "endpoint" event; POST to the
// endpoint URL returns 202 and sends the response on the SSE stream.
func legacySSEUpstream(t *testing.T) *httptest.Server {
	t.Helper()

	type pendingResponse struct {
		data string
	}

	var mu sync.Mutex
	var sseClients []chan pendingResponse

	mux := http.NewServeMux()

	// The server URL is not known yet; we'll fill in the endpoint path.
	var serverURL string

	mux.HandleFunc("GET /sse", func(w http.ResponseWriter, r *http.Request) {
		sw, err := newSSEWriter(w)
		if err != nil {
			http.Error(w, "no flusher", 500)
			return
		}

		// Send endpoint event.
		sw.WriteEvent(sseEvent{Type: "endpoint", Data: serverURL + "/message"})

		ch := make(chan pendingResponse, 16)
		mu.Lock()
		sseClients = append(sseClients, ch)
		mu.Unlock()

		for {
			select {
			case <-r.Context().Done():
				return
			case resp := <-ch:
				sw.WriteEvent(sseEvent{Type: "message", Data: resp.data})
			}
		}
	})

	mux.HandleFunc("POST /message", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg rpcMessage
		if err := json.Unmarshal(body, &msg); err != nil || msg.ID == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		resp, _ := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"id":      json.RawMessage(msg.ID),
			"result":  map[string]any{"content": []map[string]any{{"type": "text", "text": "legacy-ok"}}},
		})

		// Send response on SSE stream.
		mu.Lock()
		for _, ch := range sseClients {
			ch <- pendingResponse{data: string(resp)}
		}
		mu.Unlock()

		w.WriteHeader(http.StatusAccepted)
	})

	srv := httptest.NewServer(mux)
	serverURL = srv.URL
	return srv
}

// ---------------------------------------------------------------------------
// startIntercept starts an HTTPTransport against the given upstream URL and returns
// the local base URL and a cancel function.
// ---------------------------------------------------------------------------

func startIntercept(t *testing.T, upstreamURL string, mode string, handler ToolCallHandler) (localBase string, cancel context.CancelFunc) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	ht := &HTTPTransport{
		Upstream:      upstreamURL,
		Bind:          "127.0.0.1",
		Port:          0,
		Stderr:        io.Discard,
		TransportMode: mode,
	}

	// We need the actual port, so we duplicate a bit of Start's logic to get it.
	// Instead, just start in a goroutine and poll for the listening message.
	// Actually, let's use a pipe to capture stderr for the port.
	pr, pw := io.Pipe()
	ht.Stderr = pw

	errCh := make(chan error, 1)
	go func() {
		errCh <- ht.Start(ctx, handler)
	}()

	// Read the "Intercept listening on..." line.
	buf := make([]byte, 256)
	n, err := pr.Read(buf)
	if err != nil {
		cancel()
		t.Fatalf("failed to read startup message: %v", err)
	}
	line := strings.TrimSpace(string(buf[:n]))
	// line is like "Intercept listening on http://127.0.0.1:12345/mcp"
	const prefix = "Intercept listening on "
	if !strings.HasPrefix(line, prefix) {
		cancel()
		t.Fatalf("unexpected startup message: %q", line)
	}
	listenURL := strings.TrimSuffix(strings.TrimPrefix(line, prefix), "/mcp")
	pw.Close()

	t.Cleanup(func() {
		cancel()
		// Drain error.
		select {
		case <-errCh:
		case <-time.After(3 * time.Second):
		}
	})

	return listenURL, cancel
}

// ---------------------------------------------------------------------------
// Tests: Streamable HTTP (POST /mcp)
// ---------------------------------------------------------------------------

func TestPassthroughPOSTJSON(t *testing.T) {
	upstream := jsonUpstream(t)
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	var result map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	if result["id"] == nil {
		t.Error("expected id in response")
	}
}

func TestPassthroughPOSTSSE(t *testing.T) {
	upstream := sseUpstream(t)
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	// Read the SSE stream.
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "sse-ok") {
		t.Errorf("expected sse-ok in response, got: %s", respBody)
	}
}

func TestToolsCallDenied(t *testing.T) {
	upstream := jsonUpstream(t)
	defer upstream.Close()

	denied := `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"denied by policy"}}`
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{Handled: true, Response: json.RawMessage(denied)}
	}

	base, _ := startIntercept(t, upstream.URL, "http", handler)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"dangerous","arguments":{}}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "denied by policy") {
		t.Errorf("expected denied response, got: %s", respBody)
	}
}

func TestToolsCallAllowedJSON(t *testing.T) {
	upstream := jsonUpstream(t)
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

	base, _ := startIntercept(t, upstream.URL, "http", handler)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "ok") {
		t.Errorf("expected ok response, got: %s", respBody)
	}

	mu.Lock()
	defer mu.Unlock()
	if !callbackCalled {
		t.Error("expected OnResponse callback to be invoked")
	}
}

func TestToolsCallAllowedSSE(t *testing.T) {
	upstream := sseUpstream(t)
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

	base, _ := startIntercept(t, upstream.URL, "http", handler)

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "sse-ok") {
		t.Errorf("expected sse-ok in response, got: %s", respBody)
	}

	mu.Lock()
	defer mu.Unlock()
	if !callbackCalled {
		t.Error("expected OnResponse callback to be invoked")
	}
}

func TestGETSSERelay(t *testing.T) {
	upstream := sseUpstream(t)
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	resp, err := http.Get(base + "/mcp")
	if err != nil {
		t.Fatalf("GET error: %v", err)
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "tools/list_changed") {
		t.Errorf("expected tools/list_changed notification, got: %s", body)
	}
}

func TestDELETEForwarded(t *testing.T) {
	upstream := jsonUpstream(t)
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	req, _ := http.NewRequest("DELETE", base+"/mcp", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("DELETE error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

func TestSessionHeaderForwarding(t *testing.T) {
	var receivedHeader string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeader = r.Header.Get("Mcp-Session-Id")
		w.Header().Set("Mcp-Session-Id", "upstream-session-123")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	req, _ := http.NewRequest("POST", base+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Session-Id", "client-session-456")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if receivedHeader != "client-session-456" {
		t.Errorf("upstream received Mcp-Session-Id = %q, want %q", receivedHeader, "client-session-456")
	}

	if resp.Header.Get("Mcp-Session-Id") != "upstream-session-123" {
		t.Errorf("client received Mcp-Session-Id = %q, want %q", resp.Header.Get("Mcp-Session-Id"), "upstream-session-123")
	}
}

func TestHostHeaderRewritten(t *testing.T) {
	var receivedHost string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost = r.Host
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"jsonrpc":"2.0","id":1,"result":{}}`)
	}))
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	resp.Body.Close()

	// The Host header at the upstream should be the upstream's own host, not intercept's.
	// httptest.Server addresses look like "127.0.0.1:<port>".
	if receivedHost != strings.TrimPrefix(upstream.URL, "http://") {
		t.Errorf("upstream received Host = %q, want %q", receivedHost, strings.TrimPrefix(upstream.URL, "http://"))
	}
}

func TestUpstreamUnreachable(t *testing.T) {
	// Use a URL that will fail to connect.
	base, _ := startIntercept(t, "http://127.0.0.1:1", "http", PassthroughHandler)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", resp.StatusCode)
	}

	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "upstream unreachable") {
		t.Errorf("expected 'upstream unreachable', got: %s", respBody)
	}
}

func TestUpstreamErrorRelayed(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprint(w, `{"jsonrpc":"2.0","error":{"code":-32603,"message":"internal error"}}`)
	}))
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", resp.StatusCode)
	}
}

// ---------------------------------------------------------------------------
// Tests: Legacy SSE
// ---------------------------------------------------------------------------

func TestLegacySSEEndpoint(t *testing.T) {
	upstream := legacySSEUpstream(t)
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL+"/sse", "sse", PassthroughHandler)

	// Connect to GET /sse to get the endpoint event.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/sse", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /sse error: %v", err)
	}
	defer resp.Body.Close()

	reader := newSSEReader(resp.Body)
	ev, err := reader.Next()
	if err != nil {
		t.Fatalf("reading endpoint event: %v", err)
	}
	if ev.Type != "endpoint" {
		t.Fatalf("expected endpoint event, got type=%q", ev.Type)
	}
	if !strings.Contains(ev.Data, "/message?session=") {
		t.Fatalf("expected endpoint URL, got: %s", ev.Data)
	}

	messageURL := ev.Data

	// POST a tools/call to the message endpoint.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`
	postResp, err := http.Post(messageURL, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /message error: %v", err)
	}
	postResp.Body.Close()

	// The response should arrive on the SSE stream.
	ev, err = reader.Next()
	if err != nil {
		t.Fatalf("reading response event: %v", err)
	}
	if !strings.Contains(ev.Data, "legacy-ok") {
		t.Errorf("expected legacy-ok in SSE response, got: %s", ev.Data)
	}
}

// ---------------------------------------------------------------------------
// Tests: Concurrent requests
// ---------------------------------------------------------------------------

func TestConcurrentRequests(t *testing.T) {
	upstream := jsonUpstream(t)
	defer upstream.Close()

	base, _ := startIntercept(t, upstream.URL, "http", PassthroughHandler)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			body := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}}`, id)
			resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(body))
			if err != nil {
				t.Errorf("request %d: POST error: %v", id, err)
				return
			}
			defer resp.Body.Close()

			var result map[string]any
			json.NewDecoder(resp.Body).Decode(&result)
			// Verify the response ID matches the request ID.
			respID, ok := result["id"].(float64)
			if !ok || int(respID) != id {
				t.Errorf("request %d: response ID = %v", id, result["id"])
			}
		}(i)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Tests: Batch requests
// ---------------------------------------------------------------------------

func TestBatchWithMixedToolsCalls(t *testing.T) {
	upstream := jsonUpstream(t)
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

	base, _ := startIntercept(t, upstream.URL, "http", handler)

	batch := `[
		{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"read_file","arguments":{"path":"/tmp"}}},
		{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"dangerous","arguments":{}}},
		{"jsonrpc":"2.0","id":3,"method":"tools/list","params":{}}
	]`

	resp, err := http.Post(base+"/mcp", "application/json", strings.NewReader(batch))
	if err != nil {
		t.Fatalf("POST error: %v", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	// Should contain both the upstream response and the denied response.
	if !strings.Contains(string(respBody), "denied") {
		t.Errorf("expected denied in batch response, got: %s", respBody)
	}
}

// ---------------------------------------------------------------------------
// Tests: parseBatch
// ---------------------------------------------------------------------------

func TestParseBatchValid(t *testing.T) {
	raw := []byte(`[{"jsonrpc":"2.0","id":1,"method":"test"},{"jsonrpc":"2.0","id":2,"method":"test2"}]`)
	batch, err := parseBatch(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(batch) != 2 {
		t.Errorf("expected 2 messages, got %d", len(batch))
	}
}

func TestParseBatchNotBatch(t *testing.T) {
	raw := []byte(`{"jsonrpc":"2.0","id":1,"method":"test"}`)
	batch, err := parseBatch(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batch != nil {
		t.Errorf("expected nil for non-batch, got %v", batch)
	}
}

func TestParseBatchEmpty(t *testing.T) {
	batch, err := parseBatch([]byte(""))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if batch != nil {
		t.Errorf("expected nil for empty input")
	}
}
