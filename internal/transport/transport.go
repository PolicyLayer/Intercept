// Package transport provides the communication layer between clients and
// upstream MCP servers. Three transport modes are supported: stdio (child
// process), stdio-bridge (stdin/stdout to remote HTTP), and HTTP proxy
// (Streamable HTTP and legacy SSE).
package transport

import (
	"context"
	"encoding/json"
)

// Transport proxies JSON-RPC messages between a client and an upstream MCP server.
type Transport interface {
	Start(ctx context.Context, handler ToolCallHandler) error
}

// ToolCallRequest contains the fields extracted from a tools/call JSON-RPC request.
type ToolCallRequest struct {
	ID        json.RawMessage
	Name      string
	Arguments map[string]any
}

// ToolCallResult tells the transport how to handle a tools/call request.
// When Handled is false the original message is forwarded to the upstream.
// When Handled is true, Response is sent directly to the client.
// OnResponse, when non-nil, is called with the child's response for forwarded calls.
type ToolCallResult struct {
	Handled    bool
	Response   json.RawMessage
	OnResponse func(json.RawMessage)
}

// ToolCallHandler inspects a tools/call request and decides whether to intercept it.
type ToolCallHandler func(req ToolCallRequest) ToolCallResult

// ToolListFilter transforms a tools/list JSON-RPC response body, typically to
// remove hidden tools. Returns the modified body. A nil ToolListFilter means
// no filtering is needed.
type ToolListFilter func(responseBody json.RawMessage) json.RawMessage
