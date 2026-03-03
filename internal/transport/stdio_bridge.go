package transport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
)

// StdioBridgeTransport reads newline-delimited JSON-RPC from stdin, applies
// policy via the handler, and forwards messages to a remote Streamable HTTP MCP
// server. Responses (JSON or SSE) are converted back to newline-delimited JSON
// on stdout.
type StdioBridgeTransport struct {
	URL            string
	Headers        map[string]string
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	ToolListFilter ToolListFilter
}

// NewStdioBridgeTransport returns a StdioBridgeTransport wired to os.Stdin/Stdout/Stderr.
func NewStdioBridgeTransport(url string, headers map[string]string, filter ToolListFilter) *StdioBridgeTransport {
	return &StdioBridgeTransport{
		URL:            url,
		Headers:        headers,
		ToolListFilter: filter,
		Stdin:          os.Stdin,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	}
}

// Start reads JSON-RPC messages from stdin and proxies them to the remote
// Streamable HTTP server. It blocks until stdin closes, the context is
// cancelled, or a fatal error occurs.
func (t *StdioBridgeTransport) Start(ctx context.Context, handler ToolCallHandler) error {
	if _, err := url.Parse(t.URL); err != nil {
		return fmt.Errorf("invalid upstream URL: %w", err)
	}

	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	c := &bridgeClient{
		url:     t.URL,
		headers: t.Headers,
		client:  &http.Client{},
		handler: handler,
		out:     &syncWriter{w: t.Stdout},
		pending: newPendingCallbacks(),
		filter:  t.ToolListFilter,
		filters: newPendingFilters(),
	}

	// Forward termination signals to trigger clean shutdown.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, processSignals()...)
	go func() {
		select {
		case <-sigCh:
			innerCancel()
		case <-innerCtx.Done():
		}
		signal.Stop(sigCh)
	}()

	scanner := bufio.NewScanner(t.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), maxScannerBuffer)

	for scanner.Scan() {
		select {
		case <-innerCtx.Done():
			c.cleanup()
			return nil
		default:
		}

		line := scanner.Bytes()
		if err := c.handleLine(innerCtx, line); err != nil {
			slog.Error("handling message", "error", err)
		}
	}

	c.cleanup()
	return nil
}

// bridgeClient holds the runtime state for an active stdio-to-HTTP bridge session.
type bridgeClient struct {
	url     string
	headers map[string]string
	client  *http.Client
	handler ToolCallHandler
	out     *syncWriter
	pending *pendingCallbacks
	filter  ToolListFilter
	filters *pendingFilters

	mu        sync.Mutex
	sessionID string

	notifyOnce sync.Once
	notifyStop context.CancelFunc
}

// handleLine dispatches a single line from stdin.
func (c *bridgeClient) handleLine(ctx context.Context, line []byte) error {
	// Try batch parse first.
	batch, err := parseBatch(line)
	if err != nil {
		// Malformed batch; forward as-is.
		return c.postAndRelay(ctx, line)
	}
	if batch != nil {
		return c.handleBatch(ctx, line, batch)
	}

	// Single message.
	var msg rpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return c.postAndRelay(ctx, line)
	}

	return c.handleSingle(ctx, msg, line)
}

// handleSingle processes a single JSON-RPC message, intercepting tools/call.
func (c *bridgeClient) handleSingle(ctx context.Context, msg rpcMessage, rawLine []byte) error {
	if ir := interceptToolsCall(&msg, c.handler); ir != nil {
		if ir.denied {
			c.out.writeLine(ir.response)
			return nil
		}
		if ir.onResponse != nil {
			c.pending.Add(msg.ID, ir.onResponse)
		}
	}

	registerToolListFilter(&msg, c.filter, c.filters)

	return c.postAndRelay(ctx, rawLine)
}

// handleBatch processes a JSON-RPC batch request.
func (c *bridgeClient) handleBatch(ctx context.Context, rawBody []byte, batch []rpcMessage) error {
	br := interceptBatch(batch, c.handler)
	for _, cb := range br.callbacks {
		c.pending.Add(cb.id, cb.fn)
	}
	for i := range batch {
		registerToolListFilter(&batch[i], c.filter, c.filters)
	}

	forwardMsgs := br.forwardMsgs
	denied := br.denied

	// If all were denied, write each denied response.
	if len(forwardMsgs) == 0 {
		for _, d := range denied {
			c.out.writeLine(d.response)
		}
		return nil
	}

	// Forward remaining messages.
	var forwardBody []byte
	if len(forwardMsgs) == 1 && len(denied) == 0 {
		forwardBody, _ = json.Marshal(forwardMsgs[0])
	} else if len(forwardMsgs) > 0 {
		forwardBody, _ = json.Marshal(forwardMsgs)
	}

	resp, err := c.post(ctx, forwardBody)
	if err != nil {
		return fmt.Errorf("posting batch to upstream: %w", err)
	}
	defer resp.Body.Close()

	if err := c.relayResponse(resp); err != nil {
		return err
	}

	// Write denied responses after upstream responses.
	for _, d := range denied {
		c.out.writeLine(d.response)
	}

	return nil
}

// post sends a POST request to the upstream URL with the appropriate headers.
func (c *bridgeClient) post(ctx context.Context, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	c.setRequestHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}

	// Track session ID from response.
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		c.mu.Lock()
		c.sessionID = sid
		c.mu.Unlock()
	}

	// HTTP 404 with a session ID means the session was terminated by the server.
	if resp.StatusCode == http.StatusNotFound {
		c.mu.Lock()
		hasSession := c.sessionID != ""
		c.mu.Unlock()
		if hasSession {
			resp.Body.Close()
			return nil, fmt.Errorf("server terminated session (HTTP 404)")
		}
	}

	return resp, nil
}

// postAndRelay posts a message and relays the response to stdout.
func (c *bridgeClient) postAndRelay(ctx context.Context, body []byte) error {
	resp, err := c.post(ctx, body)
	if err != nil {
		return fmt.Errorf("posting to upstream: %w", err)
	}
	defer resp.Body.Close()

	return c.relayResponse(resp)
}

// relayResponse reads the HTTP response and writes JSON-RPC messages to stdout.
func (c *bridgeClient) relayResponse(resp *http.Response) error {
	// 202 Accepted: nothing to write (notification acknowledgment).
	if resp.StatusCode == http.StatusAccepted {
		return nil
	}

	ct := resp.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		return c.relaySSEToStdout(resp.Body)

	default:
		// JSON or other response.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading upstream response: %w", err)
		}

		if len(bytes.TrimSpace(body)) == 0 {
			return nil
		}

		// Check for pending callbacks.
		var msg rpcMessage
		if json.Unmarshal(body, &msg) == nil && !msg.isRequest() && msg.ID != nil {
			if fn, ok := c.pending.Take(msg.ID); ok {
				fn(json.RawMessage(body))
			}
		}

		body = applyFilter(body, c.filters)

		// Start notification stream after receiving a session ID (typically
		// from the InitializeResult response).
		c.maybeStartNotificationStream()

		c.out.writeLine(body)
		return nil
	}
}

// relaySSEToStdout reads SSE events and writes each JSON-RPC message as a
// separate line to stdout.
func (c *bridgeClient) relaySSEToStdout(r io.Reader) error {
	reader := newSSEReader(r)
	for {
		ev, err := reader.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("reading SSE event: %w", err)
		}

		if ev.Type != "message" && ev.Type != "" {
			continue
		}

		data := []byte(ev.Data)
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}

		// Invoke pending callbacks for response messages.
		var msg rpcMessage
		if json.Unmarshal(data, &msg) == nil && !msg.isRequest() && msg.ID != nil {
			if fn, ok := c.pending.Take(msg.ID); ok {
				fn(json.RawMessage(data))
			}
		}

		data = applyFilter(data, c.filters)
		c.out.writeLine(data)
	}
}

// maybeStartNotificationStream starts the background GET SSE stream for
// server-initiated notifications once a session ID is available.
func (c *bridgeClient) maybeStartNotificationStream() {
	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()

	if sid == "" {
		return
	}

	c.notifyOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		c.mu.Lock()
		c.notifyStop = cancel
		c.mu.Unlock()
		go c.runNotificationStream(ctx)
	})
}

// runNotificationStream opens a GET SSE connection for server-initiated
// notifications and writes them to stdout.
func (c *bridgeClient) runNotificationStream(ctx context.Context) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.url, nil)
	if err != nil {
		slog.Debug("notification stream request failed", "error", err)
		return
	}
	req.Header.Set("Accept", "text/event-stream")
	c.setRequestHeaders(req)

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Debug("notification stream connection failed", "error", err)
		return
	}
	defer resp.Body.Close()

	// Server may return 405 if it doesn't support GET SSE.
	if resp.StatusCode == http.StatusMethodNotAllowed {
		slog.Debug("server does not support GET SSE notifications")
		return
	}

	if resp.StatusCode != http.StatusOK {
		slog.Debug("notification stream unexpected status", "status", resp.StatusCode)
		return
	}

	reader := newSSEReader(resp.Body)
	for {
		ev, err := reader.Next()
		if err != nil {
			return
		}

		if ev.Type != "message" && ev.Type != "" {
			continue
		}

		data := []byte(ev.Data)
		if len(bytes.TrimSpace(data)) == 0 {
			continue
		}

		c.out.writeLine(data)
	}
}

// cleanup sends a DELETE request with the session ID to cleanly end the
// session, and stops the notification stream.
func (c *bridgeClient) cleanup() {
	c.mu.Lock()
	stop := c.notifyStop
	c.mu.Unlock()
	if stop != nil {
		stop()
	}

	c.mu.Lock()
	sid := c.sessionID
	c.mu.Unlock()

	if sid == "" {
		return
	}

	req, err := http.NewRequest("DELETE", c.url, nil)
	if err != nil {
		return
	}
	req.Header.Set("Mcp-Session-Id", sid)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		slog.Debug("session DELETE failed", "error", err)
		return
	}
	resp.Body.Close()
	slog.Debug("session ended", "status", resp.StatusCode)
}

// setRequestHeaders applies the session ID and custom headers to a request.
func (c *bridgeClient) setRequestHeaders(req *http.Request) {
	c.mu.Lock()
	if c.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", c.sessionID)
	}
	c.mu.Unlock()

	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}
