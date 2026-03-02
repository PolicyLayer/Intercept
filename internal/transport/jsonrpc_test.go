package transport

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// interceptToolsCall
// ---------------------------------------------------------------------------

func TestInterceptToolsCallNotToolsCall(t *testing.T) {
	msg := &rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize"}
	ir := interceptToolsCall(msg, PassthroughHandler)
	if ir != nil {
		t.Errorf("expected nil for non-tools/call, got %+v", ir)
	}
}

func TestInterceptToolsCallParseError(t *testing.T) {
	msg := &rpcMessage{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`invalid`)}
	ir := interceptToolsCall(msg, PassthroughHandler)
	if ir != nil {
		t.Errorf("expected nil for parse error, got %+v", ir)
	}
}

func TestInterceptToolsCallDenied(t *testing.T) {
	denied := json.RawMessage(`{"jsonrpc":"2.0","id":1,"error":{"code":-32600,"message":"denied"}}`)
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{Handled: true, Response: denied}
	}

	msg := &rpcMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"dangerous","arguments":{}}`),
	}

	ir := interceptToolsCall(msg, handler)
	if ir == nil {
		t.Fatal("expected non-nil result")
	}
	if !ir.denied {
		t.Error("expected Denied to be true")
	}
	if string(ir.response) != string(denied) {
		t.Errorf("response = %s, want %s", ir.response, denied)
	}
}

func TestInterceptToolsCallAllowed(t *testing.T) {
	ir := interceptToolsCall(&rpcMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"read_file","arguments":{"path":"/tmp"}}`),
	}, PassthroughHandler)

	if ir == nil {
		t.Fatal("expected non-nil result")
	}
	if ir.denied {
		t.Error("expected Denied to be false")
	}
}

func TestInterceptToolsCallOnResponse(t *testing.T) {
	called := false
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled: false,
			OnResponse: func(json.RawMessage) {
				called = true
			},
		}
	}

	ir := interceptToolsCall(&rpcMessage{
		JSONRPC: "2.0",
		ID:      json.RawMessage(`1`),
		Method:  "tools/call",
		Params:  json.RawMessage(`{"name":"read_file","arguments":{}}`),
	}, handler)

	if ir == nil {
		t.Fatal("expected non-nil result")
	}
	if ir.onResponse == nil {
		t.Fatal("expected OnResponse to be set")
	}
	ir.onResponse(nil)
	if !called {
		t.Error("OnResponse callback was not invoked")
	}
}

// ---------------------------------------------------------------------------
// interceptBatch
// ---------------------------------------------------------------------------

func TestInterceptBatchMixed(t *testing.T) {
	handler := func(req ToolCallRequest) ToolCallResult {
		if req.Name == "dangerous" {
			return ToolCallResult{
				Handled:  true,
				Response: json.RawMessage(`{"denied":true}`),
			}
		}
		return ToolCallResult{Handled: false}
	}

	batch := []rpcMessage{
		{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"safe","arguments":{}}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call", Params: json.RawMessage(`{"name":"dangerous","arguments":{}}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`3`), Method: "tools/list"},
	}

	r := interceptBatch(batch, handler)

	if len(r.forwardMsgs) != 2 {
		t.Errorf("expected 2 forwarded messages, got %d", len(r.forwardMsgs))
	}
	if len(r.denied) != 1 {
		t.Errorf("expected 1 denied, got %d", len(r.denied))
	}
	if r.denied[0].index != 1 {
		t.Errorf("denied index = %d, want 1", r.denied[0].index)
	}
}

func TestInterceptBatchAllDenied(t *testing.T) {
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{Handled: true, Response: json.RawMessage(`{"denied":true}`)}
	}

	batch := []rpcMessage{
		{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"a","arguments":{}}`)},
		{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/call", Params: json.RawMessage(`{"name":"b","arguments":{}}`)},
	}

	r := interceptBatch(batch, handler)

	if len(r.forwardMsgs) != 0 {
		t.Errorf("expected 0 forwarded, got %d", len(r.forwardMsgs))
	}
	if len(r.denied) != 2 {
		t.Errorf("expected 2 denied, got %d", len(r.denied))
	}
}

func TestInterceptBatchCallbacks(t *testing.T) {
	called := false
	handler := func(req ToolCallRequest) ToolCallResult {
		return ToolCallResult{
			Handled: false,
			OnResponse: func(json.RawMessage) {
				called = true
			},
		}
	}

	batch := []rpcMessage{
		{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "tools/call", Params: json.RawMessage(`{"name":"a","arguments":{}}`)},
	}

	r := interceptBatch(batch, handler)

	if len(r.callbacks) != 1 {
		t.Fatalf("expected 1 callback, got %d", len(r.callbacks))
	}
	r.callbacks[0].fn(nil)
	if !called {
		t.Error("callback was not invoked")
	}
}

func TestInterceptBatchNoToolsCalls(t *testing.T) {
	batch := []rpcMessage{
		{JSONRPC: "2.0", ID: json.RawMessage(`1`), Method: "initialize"},
		{JSONRPC: "2.0", ID: json.RawMessage(`2`), Method: "tools/list"},
	}

	r := interceptBatch(batch, PassthroughHandler)

	if len(r.forwardMsgs) != 2 {
		t.Errorf("expected 2 forwarded, got %d", len(r.forwardMsgs))
	}
	if len(r.denied) != 0 {
		t.Errorf("expected 0 denied, got %d", len(r.denied))
	}
	if len(r.callbacks) != 0 {
		t.Errorf("expected 0 callbacks, got %d", len(r.callbacks))
	}
}
