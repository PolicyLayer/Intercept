package proxy

import "encoding/json"

// rpcResponse is a JSON-RPC 2.0 response used to build denial payloads.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  *deniedResult   `json:"result,omitempty"`
}

// deniedResult represents the MCP tool result body for a denied call.
type deniedResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError"`
}

// contentBlock is a single content entry in an MCP tool result.
type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// buildDeniedResponse constructs a JSON-RPC response that returns an MCP
// isError:true result with the given denial message.
func buildDeniedResponse(id json.RawMessage, message string) json.RawMessage {
	resp := rpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result: &deniedResult{
			Content: []contentBlock{{Type: "text", Text: "[INTERCEPT POLICY DENIED] " + message}},
			IsError: true,
		},
	}
	// resp is all primitives, so Marshal cannot fail.
	data, _ := json.Marshal(resp)
	return data
}

// isErrorResponse returns true when data contains a JSON-RPC error (non-null "error" field).
// MCP tool results with isError:true inside "result" are NOT JSON-RPC errors.
func isErrorResponse(data json.RawMessage) bool {
	var msg struct {
		Error json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return false
	}
	return len(msg.Error) > 0 && string(msg.Error) != "null"
}
