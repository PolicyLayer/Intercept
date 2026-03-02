package scan

import (
	"encoding/json"
	"fmt"
)

// initParams returns the MCP initialize request parameters, identifying
// the client as "intercept-scan".
func initParams() map[string]any {
	return map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo": map[string]string{
			"name":    "intercept-scan",
			"version": "1.0.0",
		},
	}
}

// rpcSender abstracts sending a JSON-RPC request and receiving a response,
// allowing paginateToolsList to work over both stdio and HTTP transports.
type rpcSender func(method string, params any) (*jsonRPCResponse, error)

// paginateToolsList sends tools/list requests through send, following cursor
// pagination until all tools are collected (up to 100 pages).
func paginateToolsList(send rpcSender) ([]MCPTool, error) {
	const maxPages = 100
	var allTools []MCPTool
	var cursor string

	for range maxPages {
		var params any
		if cursor != "" {
			params = map[string]string{"cursor": cursor}
		}

		resp, err := send("tools/list", params)
		if err != nil {
			return nil, fmt.Errorf("tools/list: %w", err)
		}
		if resp.Error != nil {
			return nil, fmt.Errorf("tools/list error: %s (code %d)", resp.Error.Message, resp.Error.Code)
		}

		var result toolsListResult
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return nil, fmt.Errorf("parsing tools/list result: %w", err)
		}

		allTools = append(allTools, result.Tools...)

		if result.NextCursor == "" {
			break
		}
		cursor = result.NextCursor
	}

	return allTools, nil
}
