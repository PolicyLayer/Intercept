// Package scan discovers tools from MCP servers and generates policy scaffold
// YAML files. It supports both stdio and HTTP server connections.
package scan

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

// MCPTool represents a tool discovered from an MCP server.
type MCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// jsonRPCRequest is a JSON-RPC 2.0 request used for MCP communication.
type jsonRPCRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int    `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonRPCResponse is a JSON-RPC 2.0 response received from an MCP server.
type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int             `json:"id"`
	Result  json.RawMessage `json:"result"`
	Error   *jsonRPCError   `json:"error"`
}

// jsonRPCError is the error object in a JSON-RPC 2.0 error response.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// toolsListResult is the result payload of a tools/list JSON-RPC response.
type toolsListResult struct {
	Tools      []MCPTool `json:"tools"`
	NextCursor string    `json:"nextCursor"`
}

// protocolVersion is the MCP protocol version sent during the initialize handshake.
const protocolVersion = "2024-11-05"

// ListTools connects to an MCP server over stdin/stdout and returns all
// available tools. It sends the initialize handshake, then pages through
// tools/list until all tools are collected.
func ListTools(ctx context.Context, stdin io.Writer, stdout io.Reader) ([]MCPTool, error) {
	type lineResult struct {
		data []byte
		err  error
	}

	lines := make(chan lineResult, 16)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
		for scanner.Scan() {
			b := scanner.Bytes()
			cp := make([]byte, len(b))
			copy(cp, b)
			lines <- lineResult{data: cp}
		}
		if err := scanner.Err(); err != nil {
			lines <- lineResult{err: err}
		}
	}()

	nextID := 1
	send := func(method string, params any) (int, error) {
		id := nextID
		nextID++
		req := jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      id,
			Method:  method,
			Params:  params,
		}
		data, err := json.Marshal(req)
		if err != nil {
			return 0, fmt.Errorf("marshalling request: %w", err)
		}
		data = append(data, '\n')
		if _, err := stdin.Write(data); err != nil {
			return 0, fmt.Errorf("writing request: %w", err)
		}
		return id, nil
	}

	sendNotification := func(method string) error {
		req := jsonRPCRequest{
			JSONRPC: "2.0",
			Method:  method,
		}
		data, err := json.Marshal(req)
		if err != nil {
			return fmt.Errorf("marshalling notification: %w", err)
		}
		data = append(data, '\n')
		if _, err := stdin.Write(data); err != nil {
			return fmt.Errorf("writing notification: %w", err)
		}
		return nil
	}

	readResponse := func(expectedID int) (*jsonRPCResponse, error) {
		for {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case lr, ok := <-lines:
				if !ok {
					return nil, fmt.Errorf("server closed connection before responding")
				}
				if lr.err != nil {
					return nil, fmt.Errorf("reading response: %w", lr.err)
				}
				if len(lr.data) == 0 {
					continue
				}
				var resp jsonRPCResponse
				if err := json.Unmarshal(lr.data, &resp); err != nil {
					continue // skip non-JSON lines (e.g. server log output)
				}
				// Skip notifications (no ID) or responses for other requests.
				if resp.ID == 0 && resp.Error == nil && resp.Result == nil {
					continue
				}
				if resp.ID != expectedID {
					continue
				}
				return &resp, nil
			}
		}
	}

	// Step 1: Initialize.
	initID, err := send("initialize", initParams())
	if err != nil {
		return nil, fmt.Errorf("sending initialize: %w", err)
	}

	resp, err := readResponse(initID)
	if err != nil {
		return nil, fmt.Errorf("reading initialize response: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("initialize error: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}

	// Step 2: Send initialized notification.
	if err := sendNotification("notifications/initialized"); err != nil {
		return nil, fmt.Errorf("sending initialized notification: %w", err)
	}

	// Step 3: List tools with pagination.
	return paginateToolsList(func(method string, params any) (*jsonRPCResponse, error) {
		id, err := send(method, params)
		if err != nil {
			return nil, err
		}
		return readResponse(id)
	})
}
