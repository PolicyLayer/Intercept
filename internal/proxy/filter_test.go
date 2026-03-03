package proxy

import (
	"encoding/json"
	"testing"
)

// helper to build a tools/list JSON-RPC response with the given tools and optional cursor.
func buildToolsListResponse(t *testing.T, id json.RawMessage, tools []map[string]any, cursor string) json.RawMessage {
	t.Helper()

	result := map[string]any{
		"tools": tools,
	}
	if cursor != "" {
		result["nextCursor"] = cursor
	}

	resultBytes, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("failed to marshal result: %v", err)
	}

	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  json.RawMessage(resultBytes),
	}

	out, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("failed to marshal response: %v", err)
	}
	return out
}

func TestFilterToolsList_SomeHidden(t *testing.T) {
	tools := []map[string]any{
		{"name": "read_file", "description": "Read a file"},
		{"name": "write_file", "description": "Write a file"},
		{"name": "delete_file", "description": "Delete a file"},
	}
	body := buildToolsListResponse(t, json.RawMessage(`1`), tools, "")

	hidden := map[string]bool{
		"delete_file": true,
	}

	got := filterToolsList(body, hidden)

	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("failed to unmarshal filtered response: %v", err)
	}

	if len(parsed.Result.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(parsed.Result.Tools))
	}
	if parsed.Result.Tools[0].Name != "read_file" {
		t.Errorf("tools[0].name = %q, want %q", parsed.Result.Tools[0].Name, "read_file")
	}
	if parsed.Result.Tools[1].Name != "write_file" {
		t.Errorf("tools[1].name = %q, want %q", parsed.Result.Tools[1].Name, "write_file")
	}
}

func TestFilterToolsList_AllHidden(t *testing.T) {
	tools := []map[string]any{
		{"name": "read_file", "description": "Read a file"},
		{"name": "write_file", "description": "Write a file"},
	}
	body := buildToolsListResponse(t, json.RawMessage(`2`), tools, "")

	hidden := map[string]bool{
		"*": true,
	}

	got := filterToolsList(body, hidden)

	var parsed struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("failed to unmarshal filtered response: %v", err)
	}

	if len(parsed.Result.Tools) != 0 {
		t.Errorf("expected 0 tools when all hidden, got %d", len(parsed.Result.Tools))
	}
}

func TestFilterToolsList_NoHidden(t *testing.T) {
	tools := []map[string]any{
		{"name": "read_file", "description": "Read a file"},
		{"name": "write_file", "description": "Write a file"},
	}
	body := buildToolsListResponse(t, json.RawMessage(`3`), tools, "")

	hidden := map[string]bool{}

	got := filterToolsList(body, hidden)

	// With empty hidden map, should return the original body unchanged.
	if string(got) != string(body) {
		t.Errorf("expected unchanged body with empty hidden map.\ngot:  %s\nwant: %s", got, body)
	}
}

func TestFilterToolsList_MalformedBody(t *testing.T) {
	body := json.RawMessage(`this is not valid json at all`)

	hidden := map[string]bool{
		"read_file": true,
	}

	got := filterToolsList(body, hidden)

	if string(got) != string(body) {
		t.Errorf("expected malformed body to pass through unchanged.\ngot:  %s\nwant: %s", got, body)
	}
}

func TestFilterToolsList_EmptyToolsArray(t *testing.T) {
	tools := []map[string]any{}
	body := buildToolsListResponse(t, json.RawMessage(`5`), tools, "")

	hidden := map[string]bool{
		"read_file": true,
	}

	got := filterToolsList(body, hidden)

	var parsed struct {
		Result struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("failed to unmarshal filtered response: %v", err)
	}

	if len(parsed.Result.Tools) != 0 {
		t.Errorf("expected 0 tools for empty array, got %d", len(parsed.Result.Tools))
	}
}

func TestFilterToolsList_PaginatedResponse(t *testing.T) {
	tools := []map[string]any{
		{"name": "read_file", "description": "Read a file"},
		{"name": "write_file", "description": "Write a file"},
	}
	body := buildToolsListResponse(t, json.RawMessage(`6`), tools, "cursor-abc-123")

	hidden := map[string]bool{
		"write_file": true,
	}

	got := filterToolsList(body, hidden)

	var parsed struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
			Cursor string `json:"nextCursor"`
		} `json:"result"`
	}
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatalf("failed to unmarshal filtered response: %v", err)
	}

	if len(parsed.Result.Tools) != 1 {
		t.Fatalf("expected 1 tool after filtering, got %d", len(parsed.Result.Tools))
	}
	if parsed.Result.Tools[0].Name != "read_file" {
		t.Errorf("tools[0].name = %q, want %q", parsed.Result.Tools[0].Name, "read_file")
	}
	if parsed.Result.Cursor != "cursor-abc-123" {
		t.Errorf("nextCursor = %q, want %q", parsed.Result.Cursor, "cursor-abc-123")
	}
}

func TestFilterToolsList_ErrorResponse(t *testing.T) {
	body := json.RawMessage(`{"jsonrpc":"2.0","id":7,"error":{"code":-32600,"message":"Invalid Request"}}`)

	hidden := map[string]bool{
		"read_file": true,
	}

	got := filterToolsList(body, hidden)

	if string(got) != string(body) {
		t.Errorf("expected error response to pass through unchanged.\ngot:  %s\nwant: %s", got, body)
	}
}
