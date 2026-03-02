package proxy

import (
	"encoding/json"
	"testing"
)

func TestBuildDeniedResponse(t *testing.T) {
	resp := buildDeniedResponse(json.RawMessage(`1`), "Too many calls")

	var parsed struct {
		JSONRPC string `json:"jsonrpc"`
		ID      int    `json:"id"`
		Result  struct {
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}

	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("failed to unmarshal denied response: %v", err)
	}

	if parsed.JSONRPC != "2.0" {
		t.Errorf("jsonrpc = %q, want %q", parsed.JSONRPC, "2.0")
	}
	if parsed.ID != 1 {
		t.Errorf("id = %d, want 1", parsed.ID)
	}
	if !parsed.Result.IsError {
		t.Error("isError should be true")
	}
	if len(parsed.Result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(parsed.Result.Content))
	}
	if parsed.Result.Content[0].Type != "text" {
		t.Errorf("content type = %q, want %q", parsed.Result.Content[0].Type, "text")
	}
	if parsed.Result.Content[0].Text != "[INTERCEPT POLICY DENIED] Too many calls" {
		t.Errorf("content text = %q", parsed.Result.Content[0].Text)
	}
}

func TestBuildDeniedResponseStringID(t *testing.T) {
	resp := buildDeniedResponse(json.RawMessage(`"abc-123"`), "denied")

	var parsed struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	if parsed.ID != "abc-123" {
		t.Errorf("id = %q, want %q", parsed.ID, "abc-123")
	}
}

func TestIsErrorResponse(t *testing.T) {
	tests := []struct {
		name string
		data string
		want bool
	}{
		{
			name: "JSON-RPC error",
			data: `{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"Invalid Request"}}`,
			want: true,
		},
		{
			name: "success result",
			data: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"ok"}]}}`,
			want: false,
		},
		{
			name: "tool result with isError",
			data: `{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"fail"}],"isError":true}}`,
			want: false,
		},
		{
			name: "null error field",
			data: `{"jsonrpc":"2.0","id":1,"error":null,"result":{}}`,
			want: false,
		},
		{
			name: "malformed JSON",
			data: `not json`,
			want: false,
		},
		{
			name: "empty object",
			data: `{}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isErrorResponse(json.RawMessage(tt.data))
			if got != tt.want {
				t.Errorf("isErrorResponse() = %v, want %v", got, tt.want)
			}
		})
	}
}
