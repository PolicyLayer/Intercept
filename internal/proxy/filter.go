package proxy

import "encoding/json"

// filterToolsList removes hidden tools from a tools/list JSON-RPC response.
// If parsing fails, the original body is returned unchanged (fail-open).
func filterToolsList(body json.RawMessage, hidden map[string]bool) json.RawMessage {
	if len(hidden) == 0 {
		return body
	}

	// Parse the response envelope.
	var resp struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   json.RawMessage `json:"error,omitempty"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return body
	}

	// If there's an error response, pass through.
	if len(resp.Error) > 0 && string(resp.Error) != "null" {
		return body
	}

	// Parse the result object to extract tools array.
	var result struct {
		Tools  []json.RawMessage `json:"tools"`
		Cursor string            `json:"nextCursor,omitempty"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return body
	}

	// Filter tools.
	hideAll := hidden["*"]
	filtered := make([]json.RawMessage, 0, len(result.Tools))
	for _, raw := range result.Tools {
		var tool struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(raw, &tool) != nil {
			// Can't parse name; keep it.
			filtered = append(filtered, raw)
			continue
		}
		if hideAll || hidden[tool.Name] {
			continue
		}
		filtered = append(filtered, raw)
	}

	// Re-build the result object.
	newResult := map[string]any{
		"tools": filtered,
	}
	if result.Cursor != "" {
		newResult["nextCursor"] = result.Cursor
	}

	resultBytes, err := json.Marshal(newResult)
	if err != nil {
		return body
	}

	// Re-build the full response.
	newResp := map[string]any{
		"jsonrpc": resp.JSONRPC,
		"id":      resp.ID,
		"result":  json.RawMessage(resultBytes),
	}

	out, err := json.Marshal(newResp)
	if err != nil {
		return body
	}
	return out
}
