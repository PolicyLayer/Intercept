package proxy

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/policylayer/intercept/internal/config"
	"github.com/policylayer/intercept/internal/engine"
	"github.com/policylayer/intercept/internal/events"
	"github.com/policylayer/intercept/internal/transport"
)

// mockStore implements state.StateStore for testing.
type mockStore struct {
	reserveFn  func(key string, amount, limit int64, window time.Duration) (bool, int64, error)
	rollbackFn func(key string, amount int64) error
	resolveFn  func(path string) (any, bool, error)

	reserveCalls  []reserveCall
	rollbackCalls []rollbackCall
}

type reserveCall struct {
	Key    string
	Amount int64
	Limit  int64
	Window time.Duration
}

type rollbackCall struct {
	Key    string
	Amount int64
}

func (m *mockStore) Reserve(key string, amount, limit int64, window time.Duration) (bool, int64, error) {
	m.reserveCalls = append(m.reserveCalls, reserveCall{key, amount, limit, window})
	if m.reserveFn != nil {
		return m.reserveFn(key, amount, limit, window)
	}
	return true, amount, nil
}

func (m *mockStore) Rollback(key string, amount int64) error {
	m.rollbackCalls = append(m.rollbackCalls, rollbackCall{key, amount})
	if m.rollbackFn != nil {
		return m.rollbackFn(key, amount)
	}
	return nil
}

func (m *mockStore) Resolve(path string) (any, bool, error) {
	if m.resolveFn != nil {
		return m.resolveFn(path)
	}
	return int64(0), true, nil
}

func (m *mockStore) Get(key string) (int64, time.Time, error) { return 0, time.Time{}, nil }
func (m *mockStore) Reset(key string) error                   { return nil }
func (m *mockStore) Close() error                             { return nil }

// helper to parse a denied response and extract the text.
func parseDeniedText(t *testing.T, data json.RawMessage) string {
	t.Helper()
	var resp struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
			IsError bool `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		t.Fatalf("failed to parse denied response: %v", err)
	}
	if !resp.Result.IsError {
		t.Fatal("expected isError=true in denied response")
	}
	if len(resp.Result.Content) == 0 {
		t.Fatal("no content blocks in denied response")
	}
	return resp.Result.Content[0].Text
}

func TestStatelessDeny(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"delete_customer": {Rules: []config.Rule{
				{Name: "block_delete", Action: "deny", OnDeny: "Deleting customers is not allowed"},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`1`),
		Name: "delete_customer",
	})

	if !result.Handled {
		t.Fatal("expected Handled=true for denied call")
	}

	text := parseDeniedText(t, result.Response)
	if text != "[INTERCEPT POLICY DENIED] Deleting customers is not allowed" {
		t.Errorf("unexpected denied text: %s", text)
	}

	if len(store.reserveCalls) != 0 {
		t.Errorf("expected no Reserve calls, got %d", len(store.reserveCalls))
	}
}

func TestStatelessAllow(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"read_file": {Rules: []config.Rule{
				{
					Name: "only_tmp", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "args.path", Op: "contains", Value: "/tmp"},
					},
				},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:        json.RawMessage(`2`),
		Name:      "read_file",
		Arguments: map[string]any{"path": "/tmp/test.txt"},
	})

	if result.Handled {
		t.Fatal("expected Handled=false for allowed call")
	}
	if result.OnResponse != nil {
		t.Error("expected nil OnResponse for stateless allow")
	}
}

func TestStatefulAllow(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"send_email": {Rules: []config.Rule{
				{
					Name: "rate_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.send_email.daily_count", Op: "lt", Value: 10},
					},
					State:  &config.StateDef{Counter: "daily_count", Window: "day", Increment: 1},
					OnDeny: "Daily email limit exceeded",
				},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`3`),
		Name: "send_email",
	})

	if result.Handled {
		t.Fatal("expected Handled=false for allowed stateful call")
	}
	if result.OnResponse == nil {
		t.Fatal("expected non-nil OnResponse for stateful allow")
	}
	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected 1 Reserve call, got %d", len(store.reserveCalls))
	}
	rc := store.reserveCalls[0]
	if rc.Key != "send_email.daily_count" {
		t.Errorf("Reserve key = %q, want %q", rc.Key, "send_email.daily_count")
	}
	if rc.Amount != 1 {
		t.Errorf("Reserve amount = %d, want 1", rc.Amount)
	}
	if rc.Limit != 9 {
		t.Errorf("Reserve limit = %d, want 9 (lt 10 means limit=9)", rc.Limit)
	}
}

func TestStatefulDenyLimitExceeded(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"send_email": {Rules: []config.Rule{
				{
					Name: "rate_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.send_email.daily_count", Op: "lt", Value: 10},
					},
					State:  &config.StateDef{Counter: "daily_count", Window: "day", Increment: 1},
					OnDeny: "Daily email limit exceeded",
				},
			}},
		},
	}

	store := &mockStore{
		reserveFn: func(key string, amount, limit int64, window time.Duration) (bool, int64, error) {
			return false, 10, nil
		},
		resolveFn: func(path string) (any, bool, error) {
			// Return value below limit so engine passes the condition.
			return int64(5), true, nil
		},
	}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`4`),
		Name: "send_email",
	})

	if !result.Handled {
		t.Fatal("expected Handled=true for limit exceeded")
	}

	text := parseDeniedText(t, result.Response)
	if text != "[INTERCEPT POLICY DENIED] Daily email limit exceeded" {
		t.Errorf("unexpected denied text: %s", text)
	}
}

func TestMultiReserveRollback(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"send_email": {Rules: []config.Rule{
				{
					Name: "per_tool_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.send_email.tool_count", Op: "lte", Value: 100},
					},
					State:  &config.StateDef{Counter: "tool_count", Window: "hour", Increment: 1},
					OnDeny: "Tool limit exceeded",
				},
			}},
			"*": {Rules: []config.Rule{
				{
					Name: "global_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state._global.all_calls", Op: "lt", Value: 50},
					},
					State:  &config.StateDef{Counter: "all_calls", Window: "hour", Increment: 1},
					OnDeny: "Global limit exceeded",
				},
			}},
		},
	}

	callCount := 0
	store := &mockStore{
		reserveFn: func(key string, amount, limit int64, window time.Duration) (bool, int64, error) {
			callCount++
			if callCount == 2 {
				return false, 50, nil
			}
			return true, 1, nil
		},
		resolveFn: func(path string) (any, bool, error) {
			return int64(5), true, nil
		},
	}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`5`),
		Name: "send_email",
	})

	if !result.Handled {
		t.Fatal("expected Handled=true when second reserve fails")
	}

	// First reserve succeeded, so it should be rolled back.
	if len(store.rollbackCalls) != 1 {
		t.Fatalf("expected 1 rollback call, got %d", len(store.rollbackCalls))
	}
	if store.rollbackCalls[0].Key != "send_email.tool_count" {
		t.Errorf("rollback key = %q, want %q", store.rollbackCalls[0].Key, "send_email.tool_count")
	}
}

func TestOnResponseWithJSONRPCError(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"send_email": {Rules: []config.Rule{
				{
					Name: "rate_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.send_email.daily_count", Op: "lt", Value: 10},
					},
					State:  &config.StateDef{Counter: "daily_count", Window: "day", Increment: 1},
					OnDeny: "Limit exceeded",
				},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`6`),
		Name: "send_email",
	})

	if result.OnResponse == nil {
		t.Fatal("expected OnResponse callback")
	}

	// Invoke with a JSON-RPC error response.
	errorResp := json.RawMessage(`{"jsonrpc":"2.0","id":6,"error":{"code":-32600,"message":"bad"}}`)
	result.OnResponse(errorResp)

	if len(store.rollbackCalls) != 1 {
		t.Fatalf("expected 1 rollback after error response, got %d", len(store.rollbackCalls))
	}
	if store.rollbackCalls[0].Key != "send_email.daily_count" {
		t.Errorf("rollback key = %q", store.rollbackCalls[0].Key)
	}
}

func TestOnResponseWithSuccess(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"send_email": {Rules: []config.Rule{
				{
					Name: "rate_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.send_email.daily_count", Op: "lt", Value: 10},
					},
					State:  &config.StateDef{Counter: "daily_count", Window: "day", Increment: 1},
					OnDeny: "Limit exceeded",
				},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`7`),
		Name: "send_email",
	})

	// Invoke with a success response.
	successResp := json.RawMessage(`{"jsonrpc":"2.0","id":7,"result":{"content":[{"type":"text","text":"sent"}]}}`)
	result.OnResponse(successResp)

	if len(store.rollbackCalls) != 0 {
		t.Errorf("expected no rollback for success response, got %d", len(store.rollbackCalls))
	}
}

func TestIncrementFromDynamic(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"transfer_funds": {Rules: []config.Rule{
				{
					Name: "spend_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.transfer_funds.daily_spend", Op: "lte", Value: 1000},
					},
					State: &config.StateDef{
						Counter:       "daily_spend",
						Window:        "day",
						IncrementFrom: "args.amount",
					},
					OnDeny: "Spend limit exceeded",
				},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:        json.RawMessage(`8`),
		Name:      "transfer_funds",
		Arguments: map[string]any{"amount": float64(250)},
	})

	if result.Handled {
		t.Fatal("expected Handled=false for allowed call")
	}

	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected 1 Reserve call, got %d", len(store.reserveCalls))
	}
	if store.reserveCalls[0].Amount != 250 {
		t.Errorf("Reserve amount = %d, want 250", store.reserveCalls[0].Amount)
	}
}

func TestIncrementFromMissingPath(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"transfer_funds": {Rules: []config.Rule{
				{
					Name: "spend_limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.transfer_funds.daily_spend", Op: "lte", Value: 1000},
					},
					State: &config.StateDef{
						Counter:       "daily_spend",
						Window:        "day",
						IncrementFrom: "args.amount",
					},
					OnDeny: "Spend limit exceeded",
				},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:        json.RawMessage(`9`),
		Name:      "transfer_funds",
		Arguments: map[string]any{"other_field": "value"},
	})

	if !result.Handled {
		t.Fatal("expected Handled=true for missing increment_from path")
	}

	text := parseDeniedText(t, result.Response)
	if text != "[INTERCEPT POLICY DENIED] Internal policy error" {
		t.Errorf("unexpected denied text: %s", text)
	}
}

func TestWildcardAndToolSpecificScoping(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"send_email": {Rules: []config.Rule{
				{
					Name: "per_tool", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.send_email.count", Op: "lt", Value: 5},
					},
					State: &config.StateDef{Counter: "count", Window: "minute", Increment: 1},
				},
			}},
			"*": {Rules: []config.Rule{
				{
					Name: "global", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state._global.total", Op: "lt", Value: 100},
					},
					State: &config.StateDef{Counter: "total", Window: "hour", Increment: 1},
				},
			}},
		},
	}

	store := &mockStore{
		resolveFn: func(path string) (any, bool, error) {
			return int64(0), true, nil
		},
	}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`10`),
		Name: "send_email",
	})

	if result.Handled {
		t.Fatal("expected Handled=false")
	}

	if len(store.reserveCalls) != 2 {
		t.Fatalf("expected 2 Reserve calls, got %d", len(store.reserveCalls))
	}

	if store.reserveCalls[0].Key != "send_email.count" {
		t.Errorf("first reserve key = %q, want %q", store.reserveCalls[0].Key, "send_email.count")
	}
	if store.reserveCalls[1].Key != "_global.total" {
		t.Errorf("second reserve key = %q, want %q", store.reserveCalls[1].Key, "_global.total")
	}
}

func TestActionDenyUnconditional(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"rm_rf": {Rules: []config.Rule{
				{Name: "never", Action: "deny", OnDeny: "This tool is forbidden"},
			}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`11`),
		Name: "rm_rf",
	})

	if !result.Handled {
		t.Fatal("expected Handled=true for deny action")
	}

	text := parseDeniedText(t, result.Response)
	if text != "[INTERCEPT POLICY DENIED] This tool is forbidden" {
		t.Errorf("unexpected denied text: %s", text)
	}
}

func TestUnknownToolAllowed(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools:   map[string]config.ToolDef{},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`12`),
		Name: "unknown_tool",
	})

	if result.Handled {
		t.Fatal("expected Handled=false for unknown tool with no matching rules")
	}
}

func TestLteOperatorLimit(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"api_call": {Rules: []config.Rule{
				{
					Name: "limit", Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.api_call.count", Op: "lte", Value: 10},
					},
					State: &config.StateDef{Counter: "count", Window: "hour", Increment: 1},
				},
			}},
		},
	}

	store := &mockStore{
		resolveFn: func(path string) (any, bool, error) {
			return int64(0), true, nil
		},
	}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`13`),
		Name: "api_call",
	})

	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected 1 Reserve call, got %d", len(store.reserveCalls))
	}
	// lte 10 means limit = 10
	if store.reserveCalls[0].Limit != 10 {
		t.Errorf("Reserve limit = %d, want 10 (lte 10)", store.reserveCalls[0].Limit)
	}
}

func TestSetConfigConcurrent(t *testing.T) {
	cfg1 := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"tool": {Rules: []config.Rule{
				{Name: "block", Action: "deny", OnDeny: "blocked"},
			}},
		},
	}
	cfg2 := &config.Config{
		Version: "1",
		Tools:   map[string]config.ToolDef{},
	}

	store := &mockStore{}
	eng := engine.New(cfg1, store)
	h := New(eng, store, cfg1, nil)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range 1000 {
			h.Handle(transport.ToolCallRequest{
				ID:   json.RawMessage(`1`),
				Name: "tool",
			})
		}
	}()

	go func() {
		defer wg.Done()
		for i := range 1000 {
			if i%2 == 0 {
				h.SetConfig(cfg2)
			} else {
				h.SetConfig(cfg1)
			}
		}
	}()

	wg.Wait()
}

// mockEmitter captures emitted events for testing.
type mockEmitter struct {
	mu       sync.Mutex
	captured []events.Event
}

func (m *mockEmitter) Emit(ev events.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.captured = append(m.captured, ev)
	return nil
}

func (m *mockEmitter) Close() error { return nil }

func TestToolCallEventOnDeny(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"delete_customer": {Rules: []config.Rule{
				{Name: "block_delete", Action: "deny", OnDeny: "Deleting customers is not allowed"},
			}},
		},
	}

	em := &mockEmitter{}
	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, em)

	h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`1`),
		Name: "delete_customer",
	})

	em.mu.Lock()
	defer em.mu.Unlock()

	if len(em.captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.captured))
	}
	ev := em.captured[0]
	if ev.Type != "tool_call" {
		t.Errorf("type = %q, want %q", ev.Type, "tool_call")
	}
	if ev.Tool != "delete_customer" {
		t.Errorf("tool = %q, want %q", ev.Tool, "delete_customer")
	}
	if ev.Result != "denied" {
		t.Errorf("result = %q, want %q", ev.Result, "denied")
	}
	if ev.Rule != "block_delete" {
		t.Errorf("rule = %q, want %q", ev.Rule, "block_delete")
	}
	if ev.Message != "Deleting customers is not allowed" {
		t.Errorf("message = %q", ev.Message)
	}
}

func TestToolCallEventOnAllow(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools:   map[string]config.ToolDef{},
	}

	em := &mockEmitter{}
	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, em)

	h.Handle(transport.ToolCallRequest{
		ID:        json.RawMessage(`2`),
		Name:      "read_file",
		Arguments: map[string]any{"path": "/tmp/test.txt"},
	})

	em.mu.Lock()
	defer em.mu.Unlock()

	if len(em.captured) != 1 {
		t.Fatalf("expected 1 event, got %d", len(em.captured))
	}
	ev := em.captured[0]
	if ev.Result != "allowed" {
		t.Errorf("result = %q, want %q", ev.Result, "allowed")
	}
	if ev.ArgsHash == "" {
		t.Error("expected non-empty args hash for non-empty args")
	}
}

func TestRateLimitShorthandEndToEnd(t *testing.T) {
	// Config as if rate_limit: 5/hour was already desugared by applyDefaults.
	cfg := &config.Config{
		Version: "1",
		Tools: map[string]config.ToolDef{
			"create_issue": {Rules: []config.Rule{
				{
					Name:   "hourly issue limit",
					Action: "evaluate",
					Conditions: []config.Condition{
						{Path: "state.create_issue._rate_hour", Op: "lte", Value: 5},
					},
					State:  &config.StateDef{Counter: "_rate_hour", Window: "hour", Increment: 1},
					OnDeny: "Hourly limit of 5 new issues reached",
				},
			}},
		},
	}

	store := &mockStore{
		resolveFn: func(path string) (any, bool, error) {
			return int64(0), true, nil
		},
	}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`20`),
		Name: "create_issue",
	})

	if result.Handled {
		t.Fatal("expected Handled=false for allowed call")
	}

	if len(store.reserveCalls) != 1 {
		t.Fatalf("expected 1 Reserve call, got %d", len(store.reserveCalls))
	}
	rc := store.reserveCalls[0]
	if rc.Key != "create_issue._rate_hour" {
		t.Errorf("Reserve key = %q, want %q", rc.Key, "create_issue._rate_hour")
	}
	// lte 5 means limit = 5
	if rc.Limit != 5 {
		t.Errorf("Reserve limit = %d, want 5 (lte 5)", rc.Limit)
	}
}

func TestDefaultDenyUnlistedTool(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"allowed_tool": {Rules: []config.Rule{}},
		},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	result := h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`99`),
		Name: "unlisted_tool",
	})

	if !result.Handled {
		t.Fatal("expected Handled=true for unlisted tool under default deny")
	}

	text := parseDeniedText(t, result.Response)
	if text != `[INTERCEPT POLICY DENIED] Tool "unlisted_tool" is not permitted by policy` {
		t.Errorf("unexpected denied text: %s", text)
	}
}

func TestNoEventWhenEmitterNil(t *testing.T) {
	cfg := &config.Config{
		Version: "1",
		Tools:   map[string]config.ToolDef{},
	}

	store := &mockStore{}
	eng := engine.New(cfg, store)
	h := New(eng, store, cfg, nil)

	// Should not panic.
	h.Handle(transport.ToolCallRequest{
		ID:   json.RawMessage(`3`),
		Name: "some_tool",
	})
}
