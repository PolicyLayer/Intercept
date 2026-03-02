package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// ListToolsHTTP connects to an MCP server over HTTP and returns all available
// tools. It sends JSON-RPC requests as HTTP POSTs to the given URL.
// Optional headers are included on every request.
func ListToolsHTTP(ctx context.Context, url string, headers map[string]string) ([]MCPTool, error) {
	client := &http.Client{}
	nextID := 1

	send := func(method string, params any) (*jsonRPCResponse, error) {
		id := nextID
		nextID++
		req := jsonRPCRequest{
			JSONRPC: "2.0",
			ID:      id,
			Method:  method,
			Params:  params,
		}
		body, err := json.Marshal(req)
		if err != nil {
			return nil, fmt.Errorf("marshalling request: %w", err)
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("creating HTTP request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			httpReq.Header.Set(k, v)
		}

		httpResp, err := client.Do(httpReq)
		if err != nil {
			return nil, fmt.Errorf("sending HTTP request: %w", err)
		}
		defer httpResp.Body.Close()

		respBody, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return nil, fmt.Errorf("reading HTTP response: %w", err)
		}

		if httpResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("HTTP %d: %s", httpResp.StatusCode, string(respBody))
		}

		var resp jsonRPCResponse
		if err := json.Unmarshal(respBody, &resp); err != nil {
			return nil, fmt.Errorf("parsing response: %w", err)
		}
		return &resp, nil
	}

	// Step 1: Initialize.
	resp, err := send("initialize", initParams())
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("initialize error: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}

	// Step 2: Send initialized notification (fire and forget, but still POST).
	notif := jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  "notifications/initialized",
	}
	notifBody, _ := json.Marshal(notif)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(notifBody))
	if err != nil {
		return nil, fmt.Errorf("creating notification request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}
	notifResp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("sending initialized notification: %w", err)
	}
	notifResp.Body.Close()

	// Step 3: List tools with pagination.
	return paginateToolsList(send)
}
