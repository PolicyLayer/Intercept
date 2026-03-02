package scan

import (
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"
)

// mockServer reads JSON-RPC requests from r and writes responses to w.
// The handler function maps method names to result payloads.
func mockServer(r io.Reader, w io.Writer, handler func(method string, params json.RawMessage) (json.RawMessage, *jsonRPCError)) {
	buf := make([]byte, 64*1024)
	for {
		n, err := r.Read(buf)
		if err != nil {
			return
		}

		for line := range strings.SplitSeq(string(buf[:n]), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}

			var req struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      int             `json:"id"`
				Method  string          `json:"method"`
				Params  json.RawMessage `json:"params"`
			}
			if err := json.Unmarshal([]byte(line), &req); err != nil {
				continue
			}

			// Notifications have no ID; skip sending a response.
			if req.ID == 0 {
				continue
			}

			result, rpcErr := handler(req.Method, req.Params)

			type rpcResponse struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      int             `json:"id"`
				Result  json.RawMessage `json:"result,omitempty"`
				Error   *jsonRPCError   `json:"error,omitempty"`
			}
			resp := rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  result,
				Error:   rpcErr,
			}
			data, _ := json.Marshal(resp)
			data = append(data, '\n')
			w.Write(data)
		}
	}
}

func TestListTools(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	go mockServer(serverIn, serverOut, func(method string, _ json.RawMessage) (json.RawMessage, *jsonRPCError) {
		switch method {
		case "initialize":
			return json.RawMessage(`{"protocolVersion":"2024-11-05"}`), nil
		case "tools/list":
			return json.RawMessage(`{"tools":[
				{"name":"create_issue","description":"Create a new issue","inputSchema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"]}},
				{"name":"get_repo","description":"Get repository info","inputSchema":{"type":"object","properties":{"owner":{"type":"string"}}}}
			]}`), nil
		default:
			return nil, &jsonRPCError{Code: -32601, Message: "method not found"}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools, err := ListTools(ctx, clientOut, clientIn)
	clientOut.Close()
	serverOut.Close()

	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(tools))
	}
	if tools[0].Name != "create_issue" {
		t.Errorf("expected first tool name 'create_issue', got %q", tools[0].Name)
	}
	if tools[0].Description != "Create a new issue" {
		t.Errorf("unexpected description: %q", tools[0].Description)
	}
	if tools[1].Name != "get_repo" {
		t.Errorf("expected second tool name 'get_repo', got %q", tools[1].Name)
	}
}

func TestListToolsPagination(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	page := 0
	go mockServer(serverIn, serverOut, func(method string, params json.RawMessage) (json.RawMessage, *jsonRPCError) {
		switch method {
		case "initialize":
			return json.RawMessage(`{"protocolVersion":"2024-11-05"}`), nil
		case "tools/list":
			page++
			if page == 1 {
				return json.RawMessage(`{"tools":[{"name":"tool_a","description":"A"}],"nextCursor":"page2"}`), nil
			}
			return json.RawMessage(`{"tools":[{"name":"tool_b","description":"B"}]}`), nil
		default:
			return nil, &jsonRPCError{Code: -32601, Message: "method not found"}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tools, err := ListTools(ctx, clientOut, clientIn)
	clientOut.Close()
	serverOut.Close()

	if err != nil {
		t.Fatalf("ListTools() error: %v", err)
	}
	if len(tools) != 2 {
		t.Fatalf("expected 2 tools across pages, got %d", len(tools))
	}
	if tools[0].Name != "tool_a" || tools[1].Name != "tool_b" {
		t.Errorf("unexpected tool names: %v, %v", tools[0].Name, tools[1].Name)
	}
}

func TestListToolsTimeout(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()
	defer serverIn.Close()
	defer serverOut.Close()

	// Server never responds.
	go func() {
		buf := make([]byte, 64*1024)
		for {
			if _, err := serverIn.Read(buf); err != nil {
				return
			}
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := ListTools(ctx, clientOut, clientIn)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected context deadline error, got: %v", err)
	}
}

func TestListToolsServerError(t *testing.T) {
	serverIn, clientOut := io.Pipe()
	clientIn, serverOut := io.Pipe()

	go mockServer(serverIn, serverOut, func(method string, _ json.RawMessage) (json.RawMessage, *jsonRPCError) {
		switch method {
		case "initialize":
			return nil, &jsonRPCError{Code: -32600, Message: "invalid request"}
		default:
			return nil, &jsonRPCError{Code: -32601, Message: "method not found"}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := ListTools(ctx, clientOut, clientIn)
	clientOut.Close()
	serverOut.Close()

	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid request") {
		t.Errorf("expected 'invalid request' in error, got: %v", err)
	}
}
