package transport

import (
	"bytes"
	"encoding/json"
	"log/slog"
)

// rpcMessage is a minimal JSON-RPC 2.0 envelope. Fields we don't inspect are
// kept as json.RawMessage so round-tripping is lossless.
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// isRequest returns true when the message has a method field (request or notification).
func (m *rpcMessage) isRequest() bool {
	return m.Method != ""
}

// isToolsCall returns true for a tools/call JSON-RPC request.
func (m *rpcMessage) isToolsCall() bool {
	return m.isRequest() && m.Method == "tools/call"
}

// toolsCallParams holds the fields we need from a tools/call request's params.
type toolsCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// parseToolsCallParams extracts the tool name and arguments from a tools/call params blob.
func parseToolsCallParams(raw json.RawMessage) (toolsCallParams, error) {
	var p toolsCallParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return toolsCallParams{}, err
	}
	return p, nil
}

// interceptResult describes what the transport should do with a tools/call message.
// A nil *interceptResult means the message is not a tools/call or params failed
// to parse, so the transport should forward it as-is.
type interceptResult struct {
	// denied is true when the handler intercepted the call. The transport
	// should write response to the client and NOT forward the message.
	denied bool

	// response is the denial payload (only set when denied is true).
	response json.RawMessage

	// onResponse, when non-nil, should be registered as a pending callback
	// for the message's JSON-RPC ID. Only set when denied is false.
	onResponse func(json.RawMessage)
}

// interceptToolsCall evaluates a tools/call message through the handler.
// Returns nil if the message is not a tools/call or if params fail to parse
// (caller should forward the message as-is).
func interceptToolsCall(msg *rpcMessage, handler ToolCallHandler) *interceptResult {
	if !msg.isToolsCall() {
		return nil
	}

	params, err := parseToolsCallParams(msg.Params)
	if err != nil {
		slog.Warn("failed to parse tools/call params, forwarding", "error", err)
		return nil
	}

	result := handler(ToolCallRequest{
		ID:        msg.ID,
		Name:      params.Name,
		Arguments: params.Arguments,
	})

	if result.Handled {
		return &interceptResult{denied: true, response: result.Response}
	}

	return &interceptResult{onResponse: result.OnResponse}
}

// indexedResult pairs a batch index with a denied response payload.
type indexedResult struct {
	index    int
	response json.RawMessage
}

// pendingCallback pairs a JSON-RPC ID with an OnResponse function.
type pendingCallback struct {
	id json.RawMessage
	fn func(json.RawMessage)
}

// batchInterceptResult holds the outcome of intercepting a JSON-RPC batch.
type batchInterceptResult struct {
	forwardMsgs []rpcMessage
	denied      []indexedResult
	callbacks   []pendingCallback
}

// interceptBatch evaluates each message in a JSON-RPC batch through the handler,
// separating denied responses from messages that should be forwarded.
func interceptBatch(batch []rpcMessage, handler ToolCallHandler) batchInterceptResult {
	var r batchInterceptResult
	for i, msg := range batch {
		ir := interceptToolsCall(&msg, handler)
		if ir == nil {
			r.forwardMsgs = append(r.forwardMsgs, msg)
			continue
		}
		if ir.denied {
			r.denied = append(r.denied, indexedResult{index: i, response: ir.response})
		} else {
			if ir.onResponse != nil {
				r.callbacks = append(r.callbacks, pendingCallback{id: msg.ID, fn: ir.onResponse})
			}
			r.forwardMsgs = append(r.forwardMsgs, msg)
		}
	}
	return r
}

// parseBatch attempts to parse raw as a JSON-RPC batch (JSON array).
// Returns nil, nil if raw is not a batch (does not start with '[').
func parseBatch(raw []byte) ([]rpcMessage, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, nil
	}
	var batch []rpcMessage
	if err := json.Unmarshal(trimmed, &batch); err != nil {
		return nil, err
	}
	return batch, nil
}
