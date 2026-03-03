package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/policylayer/intercept/internal/events"
)

// maxRequestBody is the upper limit for incoming request bodies (10 MB).
const maxRequestBody = 10 * 1024 * 1024

// HTTPTransport proxies JSON-RPC messages to an upstream MCP server over HTTP.
// It serves both Streamable HTTP (POST/GET/DELETE /mcp) and legacy SSE
// (GET /sse, POST /message) protocols to clients simultaneously.
type HTTPTransport struct {
	Upstream       string    // upstream MCP server URL
	Bind           string    // local bind address
	Port           int       // local port (0 = auto)
	Stderr         io.Writer // for startup message
	TransportMode  string    // "sse", "http", or "" for auto-detect
	ToolListFilter ToolListFilter
}

// Start creates a local HTTP server that proxies to the upstream MCP server.
// It blocks until ctx is cancelled, then shuts down gracefully.
func (t *HTTPTransport) Start(ctx context.Context, handler ToolCallHandler) error {
	upstreamURL, err := url.Parse(t.Upstream)
	if err != nil {
		return fmt.Errorf("parsing upstream URL: %w", err)
	}

	mode := t.TransportMode
	if mode == "" {
		mode = "streamable" // default; auto-detect on first failure
	} else if mode == "http" {
		mode = "streamable"
	}

	p := &httpProxy{
		upstream:     t.Upstream,
		upstreamHost: upstreamURL.Host,
		client:       &http.Client{Timeout: 0}, // no timeout; SSE streams are long-lived
		handler:      handler,
		upstreamMode: mode,
		filter:       t.ToolListFilter,
	}

	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", t.Bind, t.Port))
	if err != nil {
		return fmt.Errorf("listening: %w", err)
	}

	p.localBase = fmt.Sprintf("http://%s", ln.Addr().String())

	mux := http.NewServeMux()
	mux.HandleFunc("POST /mcp", p.handlePostMCP)
	mux.HandleFunc("GET /mcp", p.handleGetMCP)
	mux.HandleFunc("DELETE /mcp", p.handleDeleteMCP)
	mux.HandleFunc("GET /sse", p.handleGetSSE)
	mux.HandleFunc("POST /message", p.handlePostMessage)

	srv := &http.Server{Handler: mux}

	fmt.Fprintf(t.Stderr, "Intercept listening on %s/mcp\n", p.localBase)

	serveErr := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != http.ErrServerClosed {
			serveErr <- err
		}
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("HTTP server: %w", err)
	case <-ctx.Done():
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	srv.Shutdown(shutdownCtx)

	return nil
}

// httpProxy holds the runtime state for an active HTTP transport.
type httpProxy struct {
	upstream     string
	upstreamHost string
	client       *http.Client
	handler      ToolCallHandler
	localBase    string
	filter       ToolListFilter

	upstreamMode string // "streamable" or "sse"

	// Legacy SSE upstream state.
	legacySessions sync.Map // sessionID -> *legacySession
}

// legacySession tracks the state of a single legacy SSE client session.
type legacySession struct {
	upstreamPostURL string
	pending         *pendingCallbacks
	cancel          context.CancelFunc
}

// ---------------------------------------------------------------------------
// POST /mcp (Streamable HTTP)
// ---------------------------------------------------------------------------

func (p *httpProxy) handlePostMCP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	// Try batch parse first.
	batch, err := parseBatch(body)
	if err != nil {
		httpError(w, http.StatusBadRequest, "invalid JSON-RPC batch")
		return
	}
	if batch != nil {
		p.handleBatch(w, r, body, batch)
		return
	}

	// Single message.
	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		// Not valid JSON-RPC; forward as-is.
		p.forwardAndRelay(w, r, body, nil, nil)
		return
	}

	if ir := interceptToolsCall(&msg, p.handler); ir != nil {
		if ir.denied {
			w.Header().Set("Content-Type", "application/json")
			w.Write(ir.response)
			return
		}
		pending := newPendingCallbacks()
		if ir.onResponse != nil {
			pending.Add(msg.ID, ir.onResponse)
		}
		p.forwardAndRelay(w, r, body, pending, nil)
		return
	}

	// Register filter for tools/list requests.
	var filters *pendingFilters
	if isToolsList(&msg) && p.filter != nil {
		filters = newPendingFilters()
		registerToolListFilter(&msg, p.filter, filters)
	}

	p.forwardAndRelay(w, r, body, nil, filters)
}

// handleBatch processes a JSON-RPC batch request. tools/call messages are
// intercepted; the rest are forwarded to upstream. Results are merged.
func (p *httpProxy) handleBatch(w http.ResponseWriter, r *http.Request, rawBody []byte, batch []rpcMessage) {
	br := interceptBatch(batch, p.handler)
	pending := newPendingCallbacks()
	for _, cb := range br.callbacks {
		pending.Add(cb.id, cb.fn)
	}
	var filters *pendingFilters
	for i := range batch {
		if isToolsList(&batch[i]) && p.filter != nil {
			if filters == nil {
				filters = newPendingFilters()
			}
			registerToolListFilter(&batch[i], p.filter, filters)
		}
	}

	forwardMsgs := br.forwardMsgs
	denied := br.denied

	// If all were denied, return the denied responses directly.
	if len(forwardMsgs) == 0 {
		var responses []json.RawMessage
		for _, d := range denied {
			responses = append(responses, d.response)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(responses)
		return
	}

	// Forward remaining messages.
	var forwardBody []byte
	if len(forwardMsgs) == 1 {
		forwardBody, _ = json.Marshal(forwardMsgs[0])
	} else {
		forwardBody, _ = json.Marshal(forwardMsgs)
	}

	upstreamResp, err := p.doUpstreamRequest(r, forwardBody)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer upstreamResp.Body.Close()

	upstreamBody, err := io.ReadAll(upstreamResp.Body)
	if err != nil {
		httpError(w, http.StatusBadGateway, "failed to read upstream response")
		return
	}

	// Invoke any pending callbacks.
	var upstreamResponses []json.RawMessage
	if err := json.Unmarshal(upstreamBody, &upstreamResponses); err != nil {
		// Not a batch response; try as single.
		var single rpcMessage
		if json.Unmarshal(upstreamBody, &single) == nil && single.ID != nil {
			if fn, ok := pending.Take(single.ID); ok {
				fn(upstreamBody)
			}
		}
		upstreamResponses = []json.RawMessage{json.RawMessage(applyFilter(upstreamBody, filters))}
	} else {
		for i, raw := range upstreamResponses {
			var msg rpcMessage
			if json.Unmarshal(raw, &msg) == nil && msg.ID != nil {
				if fn, ok := pending.Take(msg.ID); ok {
					fn(raw)
				}
			}
			upstreamResponses[i] = json.RawMessage(applyFilter([]byte(raw), filters))
		}
	}

	// Merge denied + upstream responses.
	if len(denied) > 0 {
		for _, d := range denied {
			upstreamResponses = append(upstreamResponses, d.response)
		}
	}

	copyResponseHeaders(w, upstreamResp.Header)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(upstreamResponses)
}

// ---------------------------------------------------------------------------
// GET /mcp (Streamable HTTP: server-initiated SSE)
// ---------------------------------------------------------------------------

func (p *httpProxy) handleGetMCP(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), "GET", p.upstream, nil)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed to create upstream request")
		return
	}
	forwardRequestHeaders(req.Header, r.Header, p.upstreamHost)
	req.Host = p.upstreamHost

	resp, err := p.client.Do(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)

	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		sw, err := newSSEWriter(w)
		if err != nil {
			return
		}
		p.relaySSE(sw, newSSEReader(resp.Body), nil, nil)
		return
	}

	io.Copy(w, resp.Body)
}

// ---------------------------------------------------------------------------
// DELETE /mcp
// ---------------------------------------------------------------------------

func (p *httpProxy) handleDeleteMCP(w http.ResponseWriter, r *http.Request) {
	req, err := http.NewRequestWithContext(r.Context(), "DELETE", p.upstream, r.Body)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed to create upstream request")
		return
	}
	forwardRequestHeaders(req.Header, r.Header, p.upstreamHost)
	req.Host = p.upstreamHost

	resp, err := p.client.Do(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// ---------------------------------------------------------------------------
// Legacy SSE: GET /sse
// ---------------------------------------------------------------------------

func (p *httpProxy) handleGetSSE(w http.ResponseWriter, r *http.Request) {
	sw, err := newSSEWriter(w)
	if err != nil {
		httpError(w, http.StatusInternalServerError, "SSE not supported")
		return
	}

	sessionID := generateSessionID()
	sessionCtx, sessionCancel := context.WithCancel(r.Context())
	defer sessionCancel()

	sess := &legacySession{
		pending: newPendingCallbacks(),
		cancel:  sessionCancel,
	}

	if p.upstreamMode == "sse" {
		upstreamPostURL, upstreamReader, upstreamCloser, err := p.connectLegacyUpstream(sessionCtx)
		if err != nil {
			httpError(w, http.StatusBadGateway, "failed to connect to upstream SSE")
			return
		}
		defer upstreamCloser.Close()
		sess.upstreamPostURL = upstreamPostURL
		p.initLegacySession(w, sw, sessionID, sess)
		p.relaySSE(sw, upstreamReader, sess.pending, nil)
	} else {
		sess.upstreamPostURL = p.upstream
		p.initLegacySession(w, sw, sessionID, sess)

		req, err := http.NewRequestWithContext(sessionCtx, "GET", p.upstream, nil)
		if err != nil {
			return
		}
		req.Header.Set("Accept", "text/event-stream")
		resp, err := p.client.Do(req)
		if err != nil || resp.StatusCode != http.StatusOK {
			if resp != nil {
				resp.Body.Close()
			}
			<-sessionCtx.Done()
			return
		}
		defer resp.Body.Close()
		p.relaySSE(sw, newSSEReader(resp.Body), nil, nil)
	}

	p.legacySessions.Delete(sessionID)
}

// initLegacySession sets SSE headers, sends the endpoint event, and registers
// the session in the session map.
func (p *httpProxy) initLegacySession(w http.ResponseWriter, sw *sseWriter, sessionID string, sess *legacySession) {
	setSSEHeaders(w)
	endpointURL := fmt.Sprintf("%s/message?session=%s", p.localBase, sessionID)
	sw.WriteEvent(sseEvent{Type: "endpoint", Data: endpointURL})
	p.legacySessions.Store(sessionID, sess)
}

// ---------------------------------------------------------------------------
// Legacy SSE: POST /message
// ---------------------------------------------------------------------------

func (p *httpProxy) handlePostMessage(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	val, ok := p.legacySessions.Load(sessionID)
	if !ok {
		httpError(w, http.StatusBadRequest, "unknown session")
		return
	}
	sess := val.(*legacySession)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err != nil {
		httpError(w, http.StatusBadRequest, "failed to read request body")
		return
	}

	var msg rpcMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		// Forward as-is.
		p.forwardToLegacyUpstream(w, r, sess, body)
		return
	}

	if ir := interceptToolsCall(&msg, p.handler); ir != nil {
		if ir.denied {
			w.Header().Set("Content-Type", "application/json")
			w.Write(ir.response)
			return
		}
		if ir.onResponse != nil {
			sess.pending.Add(msg.ID, ir.onResponse)
		}
	}

	p.forwardToLegacyUpstream(w, r, sess, body)
}

// forwardToLegacyUpstream POSTs a message to the session's upstream URL.
func (p *httpProxy) forwardToLegacyUpstream(w http.ResponseWriter, r *http.Request, sess *legacySession, body []byte) {
	req, err := http.NewRequestWithContext(r.Context(), "POST", sess.upstreamPostURL, bytes.NewReader(body))
	if err != nil {
		httpError(w, http.StatusInternalServerError, "failed to create upstream request")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	forwardRequestHeaders(req.Header, r.Header, p.upstreamHost)
	req.Host = p.upstreamHost

	resp, err := p.client.Do(req)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(w, resp.Header)
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}

// connectLegacyUpstream connects to the upstream SSE endpoint and reads the
// endpoint event to learn the POST URL.
func (p *httpProxy) connectLegacyUpstream(ctx context.Context) (postURL string, reader *sseReader, closer io.Closer, err error) {
	req, err := http.NewRequestWithContext(ctx, "GET", p.upstream, nil)
	if err != nil {
		return "", nil, nil, fmt.Errorf("creating upstream SSE request: %w", err)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := p.client.Do(req)
	if err != nil {
		return "", nil, nil, fmt.Errorf("connecting to upstream SSE: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return "", nil, nil, fmt.Errorf("upstream SSE returned status %d", resp.StatusCode)
	}

	reader = newSSEReader(resp.Body)

	// Read events until we find the endpoint event.
	for {
		ev, err := reader.Next()
		if err != nil {
			resp.Body.Close()
			return "", nil, nil, fmt.Errorf("reading upstream endpoint event: %w", err)
		}
		if ev.Type == "endpoint" {
			postURL = ev.Data
			// If postURL is relative, resolve against upstream.
			if !strings.HasPrefix(postURL, "http://") && !strings.HasPrefix(postURL, "https://") {
				base, _ := url.Parse(p.upstream)
				ref, _ := url.Parse(postURL)
				postURL = base.ResolveReference(ref).String()
			}
			return postURL, reader, resp.Body, nil
		}
	}
}

// ---------------------------------------------------------------------------
// Upstream request and response relay
// ---------------------------------------------------------------------------

// forwardAndRelay sends a request to upstream and relays the response to the client.
// If pending is non-nil, response callbacks are checked for JSON responses.
func (p *httpProxy) forwardAndRelay(w http.ResponseWriter, r *http.Request, body []byte, pending *pendingCallbacks, filters *pendingFilters) {
	resp, err := p.doUpstreamRequest(r, body)
	if err != nil {
		httpError(w, http.StatusBadGateway, "upstream unreachable")
		return
	}
	defer resp.Body.Close()

	ct := resp.Header.Get("Content-Type")

	switch {
	case strings.HasPrefix(ct, "text/event-stream"):
		copyResponseHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		sw, err := newSSEWriter(w)
		if err != nil {
			return
		}
		p.relaySSE(sw, newSSEReader(resp.Body), pending, filters)

	case resp.StatusCode == http.StatusAccepted:
		copyResponseHeaders(w, resp.Header)
		w.WriteHeader(http.StatusAccepted)

	default:
		// JSON or other response.
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			httpError(w, http.StatusBadGateway, "failed to read upstream response")
			return
		}

		if pending != nil {
			var msg rpcMessage
			if json.Unmarshal(respBody, &msg) == nil && !msg.isRequest() && msg.ID != nil {
				if fn, ok := pending.Take(msg.ID); ok {
					fn(json.RawMessage(respBody))
				}
			}
		}

		respBody = applyFilter(respBody, filters)

		copyResponseHeaders(w, resp.Header)
		w.WriteHeader(resp.StatusCode)
		w.Write(respBody)
	}
}

// doUpstreamRequest creates and executes a POST to the upstream server.
func (p *httpProxy) doUpstreamRequest(r *http.Request, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(r.Context(), "POST", p.upstream, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	forwardRequestHeaders(req.Header, r.Header, p.upstreamHost)
	req.Host = p.upstreamHost
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	return p.client.Do(req)
}

// relaySSE streams SSE events from upstream to the client, invoking pending
// callbacks for response messages.
func (p *httpProxy) relaySSE(sw *sseWriter, reader *sseReader, pending *pendingCallbacks, filters *pendingFilters) {
	for {
		ev, err := reader.Next()
		if err != nil {
			return
		}

		if ev.Type == "message" || ev.Type == "" {
			if pending != nil {
				var msg rpcMessage
				if json.Unmarshal([]byte(ev.Data), &msg) == nil && !msg.isRequest() && msg.ID != nil {
					if fn, ok := pending.Take(msg.ID); ok {
						fn(json.RawMessage(ev.Data))
					}
				}
			}
			filtered := applyFilter([]byte(ev.Data), filters)
			ev.Data = string(filtered)
		}

		if err := sw.WriteEvent(ev); err != nil {
			return
		}
	}
}

// ---------------------------------------------------------------------------
// Header forwarding
// ---------------------------------------------------------------------------

// forwardRequestHeaders copies all headers from src to dst, overriding Host.
// Content-Length is skipped since Go handles it.
func forwardRequestHeaders(dst, src http.Header, upstreamHost string) {
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") || strings.EqualFold(k, "Host") {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	// Note: Go's http.Client uses req.Host (not the header map) for the wire
	// Host header. Callers must set req.Host = upstreamHost separately.
}

// copyResponseHeaders copies upstream response headers to the client writer.
// Content-Length is skipped since Go's ResponseWriter calculates it.
func copyResponseHeaders(w http.ResponseWriter, src http.Header) {
	for k, vs := range src {
		if strings.EqualFold(k, "Content-Length") {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// setSSEHeaders writes the standard Server-Sent Events response headers.
func setSSEHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
}

// httpError writes a JSON-RPC error response with the given HTTP status code.
func httpError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"error": map[string]any{
			"code":    -32603,
			"message": message,
		},
	})
}

// generateSessionID returns a random 8-character hex string for session tracking.
func generateSessionID() string {
	return events.NewInstanceID()
}
