package engine

import (
	"sync"
	"testing"

	"github.com/policylayer/intercept/internal/config"
)

// mockStateResolver implements StateResolver for testing.
type mockStateResolver struct {
	values map[string]any
}

func (m *mockStateResolver) Resolve(path string) (any, bool, error) {
	v, ok := m.values[path]
	return v, ok, nil
}

func cfg(tools map[string]config.ToolDef) *config.Config {
	return &config.Config{Version: "1", Tools: tools}
}

func rule(name, action, onDeny string, conditions ...config.Condition) config.Rule {
	return config.Rule{Name: name, Action: action, OnDeny: onDeny, Conditions: conditions}
}

func cond(path, op string, value any) config.Condition {
	return config.Condition{Path: path, Op: op, Value: value}
}

func call(name string, args map[string]any) ToolCall {
	return ToolCall{Name: name, Arguments: args}
}

// --- Operator tests ---

func TestOperatorEq(t *testing.T) {
	tests := []struct {
		name     string
		actual   any
		expected any
		want     bool
	}{
		{"string match", "hello", "hello", true},
		{"string mismatch", "hello", "world", false},
		{"int vs float64", int(50000), float64(50000), true},
		{"float64 vs int", float64(100), int(100), true},
		{"float mismatch", float64(1.1), float64(1.2), false},
		{"bool true", true, true, true},
		{"bool false match", false, false, true},
		{"bool mismatch", true, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalOp("eq", tt.actual, tt.expected); got != tt.want {
				t.Errorf("eq(%v, %v) = %v, want %v", tt.actual, tt.expected, got, tt.want)
			}
		})
	}
}

func TestOperatorNeq(t *testing.T) {
	if !evalOp("neq", "a", "b") {
		t.Error("neq(a, b) should be true")
	}
	if evalOp("neq", "a", "a") {
		t.Error("neq(a, a) should be false")
	}
}

func TestOperatorIn(t *testing.T) {
	list := []any{"us", "uk", "de"}
	if !evalOp("in", "uk", list) {
		t.Error("in(uk, [us,uk,de]) should be true")
	}
	if evalOp("in", "fr", list) {
		t.Error("in(fr, [us,uk,de]) should be false")
	}
}

func TestOperatorNotIn(t *testing.T) {
	list := []any{"us", "uk"}
	if !evalOp("not_in", "fr", list) {
		t.Error("not_in(fr, [us,uk]) should be true")
	}
	if evalOp("not_in", "us", list) {
		t.Error("not_in(us, [us,uk]) should be false")
	}
}

func TestOperatorNumericComparisons(t *testing.T) {
	tests := []struct {
		name string
		op   string
		a, b any
		want bool
	}{
		{"lt true", "lt", 5, 10, true},
		{"lt equal", "lt", 10, 10, false},
		{"lt false", "lt", 15, 10, false},
		{"lte true less", "lte", 5, 10, true},
		{"lte true equal", "lte", 10, 10, true},
		{"lte false", "lte", 15, 10, false},
		{"gt true", "gt", 15, 10, true},
		{"gt equal", "gt", 10, 10, false},
		{"gt false", "gt", 5, 10, false},
		{"gte true greater", "gte", 15, 10, true},
		{"gte true equal", "gte", 10, 10, true},
		{"gte false", "gte", 5, 10, false},
		{"float64 lt", "lt", float64(1.5), float64(2.5), true},
		{"int64 gte", "gte", int64(100), int64(50), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := evalOp(tt.op, tt.a, tt.b); got != tt.want {
				t.Errorf("%s(%v, %v) = %v, want %v", tt.op, tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestOperatorRegex(t *testing.T) {
	if !evalOp("regex", "user@example.com", `^[^@]+@[^@]+\.[^@]+$`) {
		t.Error("regex should match email pattern")
	}
	if evalOp("regex", "not-an-email", `^[^@]+@[^@]+\.[^@]+$`) {
		t.Error("regex should not match non-email")
	}
}

func TestOperatorContains(t *testing.T) {
	// String contains
	if !evalOp("contains", "hello world", "world") {
		t.Error("contains(hello world, world) should be true")
	}
	if evalOp("contains", "hello world", "mars") {
		t.Error("contains(hello world, mars) should be false")
	}

	// Array contains
	arr := []any{"a", "b", "c"}
	if !evalOp("contains", arr, "b") {
		t.Error("contains([a,b,c], b) should be true")
	}
	if evalOp("contains", arr, "d") {
		t.Error("contains([a,b,c], d) should be false")
	}
}

func TestOperatorExists(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"test": {Rules: []config.Rule{
			rule("exists-true", "evaluate", "missing",
				cond("args.name", "exists", true)),
		}},
	}), nil)

	// Path exists
	d := e.Evaluate(call("test", map[string]any{"name": "alice"}))
	if !d.Allowed {
		t.Error("exists=true with present path should allow")
	}

	// Path missing, exists=true should deny
	d = e.Evaluate(call("test", map[string]any{}))
	if d.Allowed {
		t.Error("exists=true with missing path should deny")
	}

	// exists=false with missing path
	e2 := New(cfg(map[string]config.ToolDef{
		"test": {Rules: []config.Rule{
			rule("exists-false", "evaluate", "unexpected",
				cond("args.name", "exists", false)),
		}},
	}), nil)
	d = e2.Evaluate(call("test", map[string]any{}))
	if !d.Allowed {
		t.Error("exists=false with missing path should allow")
	}

	// exists=false with present path should deny
	d = e2.Evaluate(call("test", map[string]any{"name": "alice"}))
	if d.Allowed {
		t.Error("exists=false with present path should deny")
	}

	// exists with nil value
	d = e.Evaluate(call("test", map[string]any{"name": nil}))
	if d.Allowed {
		t.Error("exists=true with nil value should deny")
	}
}

// --- Path resolution tests ---

func TestPathResolutionNested(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"send": {Rules: []config.Rule{
			rule("check-email", "evaluate", "bad email",
				cond("args.recipient.email", "regex", `@example\.com$`)),
		}},
	}), nil)

	d := e.Evaluate(call("send", map[string]any{
		"recipient": map[string]any{
			"email": "user@example.com",
		},
	}))
	if !d.Allowed {
		t.Error("nested path resolution should work for valid email")
	}
}

func TestPathResolutionMissingIntermediate(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"send": {Rules: []config.Rule{
			rule("check-email", "evaluate", "missing",
				cond("args.recipient.email", "eq", "x")),
		}},
	}), nil)

	d := e.Evaluate(call("send", map[string]any{}))
	if d.Allowed {
		t.Error("missing intermediate key should deny")
	}
}

func TestPathResolutionNonMapIntermediate(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"send": {Rules: []config.Rule{
			rule("check", "evaluate", "bad",
				cond("args.recipient.email", "eq", "x")),
		}},
	}), nil)

	d := e.Evaluate(call("send", map[string]any{
		"recipient": "not-a-map",
	}))
	if d.Allowed {
		t.Error("non-map intermediate should deny")
	}
}

func TestPathResolutionTopLevel(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"pay": {Rules: []config.Rule{
			rule("check-amount", "evaluate", "too much",
				cond("args.amount", "lte", 100)),
		}},
	}), nil)

	d := e.Evaluate(call("pay", map[string]any{"amount": 50}))
	if !d.Allowed {
		t.Error("top-level path should resolve correctly")
	}
}

// --- Rule matching tests ---

func TestRuleMatchingToolSpecificOnly(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool_a": {Rules: []config.Rule{
			rule("a-rule", "evaluate", "denied",
				cond("args.x", "eq", "yes")),
		}},
	}), nil)

	d := e.Evaluate(call("tool_a", map[string]any{"x": "yes"}))
	if !d.Allowed {
		t.Error("tool-specific rule should match and allow")
	}

	d = e.Evaluate(call("tool_b", map[string]any{"x": "no"}))
	if !d.Allowed {
		t.Error("unmatched tool should be allowed (no rules)")
	}
}

func TestRuleMatchingWildcardOnly(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"*": {Rules: []config.Rule{
			rule("global", "evaluate", "blocked",
				cond("args.safe", "eq", true)),
		}},
	}), nil)

	d := e.Evaluate(call("anything", map[string]any{"safe": true}))
	if !d.Allowed {
		t.Error("wildcard rule should match any tool")
	}

	d = e.Evaluate(call("anything", map[string]any{"safe": false}))
	if d.Allowed {
		t.Error("wildcard rule should deny when condition fails")
	}
}

func TestRuleMatchingToolSpecificThenWildcard(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool_a": {Rules: []config.Rule{
			rule("specific", "evaluate", "specific denied",
				cond("args.x", "eq", "good")),
		}},
		"*": {Rules: []config.Rule{
			rule("global", "evaluate", "global denied",
				cond("args.y", "eq", "ok")),
		}},
	}), nil)

	// Both pass
	d := e.Evaluate(call("tool_a", map[string]any{"x": "good", "y": "ok"}))
	if !d.Allowed {
		t.Error("both rules pass should allow")
	}

	// Specific passes, wildcard fails
	d = e.Evaluate(call("tool_a", map[string]any{"x": "good", "y": "bad"}))
	if d.Allowed {
		t.Error("wildcard fail should deny")
	}
	if d.Rule != "global" {
		t.Errorf("denying rule should be 'global', got %q", d.Rule)
	}

	// Specific fails
	d = e.Evaluate(call("tool_a", map[string]any{"x": "bad", "y": "ok"}))
	if d.Allowed {
		t.Error("specific fail should deny")
	}
	if d.Rule != "specific" {
		t.Errorf("denying rule should be 'specific', got %q", d.Rule)
	}
}

func TestRuleMatchingNoRules(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{}), nil)
	d := e.Evaluate(call("any_tool", map[string]any{}))
	if !d.Allowed {
		t.Error("no rules should allow")
	}
}

// --- Multi-condition AND tests ---

func TestMultiConditionAllPass(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"pay": {Rules: []config.Rule{
			rule("check", "evaluate", "denied",
				cond("args.amount", "lte", 1000),
				cond("args.currency", "in", []any{"usd", "eur"})),
		}},
	}), nil)

	d := e.Evaluate(call("pay", map[string]any{"amount": 500, "currency": "usd"}))
	if !d.Allowed {
		t.Error("all conditions pass should allow")
	}
}

func TestMultiConditionSecondFails(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"pay": {Rules: []config.Rule{
			rule("check", "evaluate", "bad currency",
				cond("args.amount", "lte", 1000),
				cond("args.currency", "in", []any{"usd", "eur"})),
		}},
	}), nil)

	d := e.Evaluate(call("pay", map[string]any{"amount": 500, "currency": "gbp"}))
	if d.Allowed {
		t.Error("second condition fails should deny")
	}
	if d.Message != "bad currency" {
		t.Errorf("message should be 'bad currency', got %q", d.Message)
	}
}

func TestMultiConditionFirstFails(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"pay": {Rules: []config.Rule{
			rule("check", "evaluate", "too much",
				cond("args.amount", "lte", 1000),
				cond("args.currency", "in", []any{"usd", "eur"})),
		}},
	}), nil)

	d := e.Evaluate(call("pay", map[string]any{"amount": 5000, "currency": "usd"}))
	if d.Allowed {
		t.Error("first condition fails should deny")
	}
}

// --- State path tests ---

func TestStatePathsSkippedWithNilResolver(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("rate-limit", "evaluate", "rate limited",
				cond("state.tool.counter", "lte", 10)),
		}},
	}), nil)

	d := e.Evaluate(call("tool", map[string]any{}))
	if !d.Allowed {
		t.Error("state conditions should auto-pass with nil resolver")
	}
}

func TestMixedArgsAndStateWithNilResolver(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"pay": {Rules: []config.Rule{
			rule("check", "evaluate", "denied",
				cond("args.amount", "lte", 1000),
				cond("state.pay.daily_total", "lte", 50000)),
		}},
	}), nil)

	// Args pass, state auto-passes
	d := e.Evaluate(call("pay", map[string]any{"amount": 500}))
	if !d.Allowed {
		t.Error("args pass + state auto-pass should allow")
	}

	// Args fail, state auto-passes but should still deny
	d = e.Evaluate(call("pay", map[string]any{"amount": 5000}))
	if d.Allowed {
		t.Error("failing args should deny even with state auto-pass")
	}
}

// --- Mock StateResolver tests ---

func TestMockStateResolver(t *testing.T) {
	mock := &mockStateResolver{values: map[string]any{
		"state.pay.daily_total": float64(100),
	}}
	e := New(cfg(map[string]config.ToolDef{
		"pay": {Rules: []config.Rule{
			rule("rate-limit", "evaluate", "over limit",
				cond("state.pay.daily_total", "lte", float64(500))),
		}},
	}), mock)

	d := e.Evaluate(call("pay", map[string]any{}))
	if !d.Allowed {
		t.Error("state value within limit should allow")
	}

	// Update mock to exceed limit
	mock.values["state.pay.daily_total"] = float64(600)
	d = e.Evaluate(call("pay", map[string]any{}))
	if d.Allowed {
		t.Error("state value over limit should deny")
	}
	if d.Message != "over limit" {
		t.Errorf("message should be 'over limit', got %q", d.Message)
	}
}

func TestMockStateResolverExists(t *testing.T) {
	mock := &mockStateResolver{values: map[string]any{
		"state.tool.counter": float64(5),
	}}
	e := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("check-exists", "evaluate", "no counter",
				cond("state.tool.counter", "exists", true)),
		}},
	}), mock)

	d := e.Evaluate(call("tool", map[string]any{}))
	if !d.Allowed {
		t.Error("existing state path with exists=true should allow")
	}

	// Missing state path
	mock2 := &mockStateResolver{values: map[string]any{}}
	e2 := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("check-exists", "evaluate", "no counter",
				cond("state.tool.counter", "exists", true)),
		}},
	}), mock2)

	d = e2.Evaluate(call("tool", map[string]any{}))
	if d.Allowed {
		t.Error("missing state path with exists=true should deny")
	}
}

// --- Type mismatch tests ---

func TestTypeMismatchLteOnString(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("check", "evaluate", "type error",
				cond("args.x", "lte", 100)),
		}},
	}), nil)

	d := e.Evaluate(call("tool", map[string]any{"x": "not-a-number"}))
	if d.Allowed {
		t.Error("lte on string should deny (type mismatch)")
	}
}

func TestTypeMismatchRegexOnNumber(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("check", "evaluate", "type error",
				cond("args.x", "regex", `\d+`)),
		}},
	}), nil)

	d := e.Evaluate(call("tool", map[string]any{"x": 42}))
	if d.Allowed {
		t.Error("regex on number should deny (type mismatch)")
	}
}

func TestTypeMismatchContainsOnNumber(t *testing.T) {
	if evalOp("contains", 42, "4") {
		t.Error("contains on number should return false")
	}
}

// --- Deny action tests ---

func TestDenyAction(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"dangerous": {Rules: []config.Rule{
			rule("block-all", "deny", "tool is blocked"),
		}},
	}), nil)

	d := e.Evaluate(call("dangerous", map[string]any{"any": "arg"}))
	if d.Allowed {
		t.Error("deny action should deny")
	}
	if d.Rule != "block-all" {
		t.Errorf("rule should be 'block-all', got %q", d.Rule)
	}
	if d.Message != "tool is blocked" {
		t.Errorf("message should be 'tool is blocked', got %q", d.Message)
	}
}

func TestDenyBeforeEvaluate(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("block", "deny", "blocked"),
			rule("check", "evaluate", "would deny",
				cond("args.x", "eq", "good")),
		}},
	}), nil)

	d := e.Evaluate(call("tool", map[string]any{"x": "good"}))
	if d.Allowed {
		t.Error("deny should fire before evaluate")
	}
	if d.Rule != "block" {
		t.Errorf("denying rule should be 'block', got %q", d.Rule)
	}
}

// --- SetConfig test ---

func TestSetConfig(t *testing.T) {
	e := New(cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("block", "deny", "blocked"),
		}},
	}), nil)

	d := e.Evaluate(call("tool", map[string]any{}))
	if d.Allowed {
		t.Error("should deny with initial config")
	}

	e.SetConfig(cfg(map[string]config.ToolDef{}))
	d = e.Evaluate(call("tool", map[string]any{}))
	if !d.Allowed {
		t.Error("should allow after config swap")
	}
}

// --- Default deny tests ---

func TestDefaultDenyUnlistedTool(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"listed_tool": {Rules: []config.Rule{}},
		},
	}, nil)

	d := e.Evaluate(call("unlisted_tool", map[string]any{}))
	if d.Allowed {
		t.Error("unlisted tool should be denied under default deny")
	}
	if d.Rule != "(default deny)" {
		t.Errorf("rule = %q, want %q", d.Rule, "(default deny)")
	}
}

func TestDefaultDenyListedToolEmptyRules(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"listed_tool": {Rules: []config.Rule{}},
		},
	}, nil)

	d := e.Evaluate(call("listed_tool", map[string]any{}))
	if !d.Allowed {
		t.Error("listed tool with empty rules should be allowed under default deny")
	}
}

func TestDefaultDenyListedToolPassingRules(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"tool": {Rules: []config.Rule{
				rule("check", "evaluate", "denied",
					cond("args.x", "eq", "good")),
			}},
		},
	}, nil)

	d := e.Evaluate(call("tool", map[string]any{"x": "good"}))
	if !d.Allowed {
		t.Error("listed tool with passing rules should be allowed under default deny")
	}
}

func TestDefaultDenyListedToolFailingRules(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"tool": {Rules: []config.Rule{
				rule("check", "evaluate", "condition failed",
					cond("args.x", "eq", "good")),
			}},
		},
	}, nil)

	d := e.Evaluate(call("tool", map[string]any{"x": "bad"}))
	if d.Allowed {
		t.Error("listed tool with failing rules should be denied")
	}
	if d.Rule != "check" {
		t.Errorf("rule = %q, want %q", d.Rule, "check")
	}
}

func TestDefaultDenyWildcardDoesNotRescueUnlisted(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"listed_tool": {Rules: []config.Rule{}},
			"*": {Rules: []config.Rule{
				rule("global", "evaluate", "global denied",
					cond("args.safe", "eq", true)),
			}},
		},
	}, nil)

	d := e.Evaluate(call("unlisted_tool", map[string]any{"safe": true}))
	if d.Allowed {
		t.Error("wildcard rules should not rescue unlisted tools under default deny")
	}
	if d.Rule != "(default deny)" {
		t.Errorf("rule = %q, want %q", d.Rule, "(default deny)")
	}
}

func TestDefaultDenyWildcardAppliesToListedTools(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "deny",
		Tools: map[string]config.ToolDef{
			"listed_tool": {Rules: []config.Rule{}},
			"*": {Rules: []config.Rule{
				rule("global", "evaluate", "global denied",
					cond("args.safe", "eq", true)),
			}},
		},
	}, nil)

	// Wildcard rule should still apply to listed tools.
	d := e.Evaluate(call("listed_tool", map[string]any{"safe": false}))
	if d.Allowed {
		t.Error("wildcard rules should still apply to listed tools under default deny")
	}
	if d.Rule != "global" {
		t.Errorf("rule = %q, want %q", d.Rule, "global")
	}

	d = e.Evaluate(call("listed_tool", map[string]any{"safe": true}))
	if !d.Allowed {
		t.Error("listed tool passing wildcard rules should be allowed")
	}
}

func TestDefaultAllowPreservesCurrentBehaviour(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Default: "allow",
		Tools:   map[string]config.ToolDef{},
	}, nil)

	d := e.Evaluate(call("any_tool", map[string]any{}))
	if !d.Allowed {
		t.Error("unlisted tool should be allowed under default allow")
	}
}

func TestSetConfigConcurrent(t *testing.T) {
	cfgDeny := cfg(map[string]config.ToolDef{
		"tool": {Rules: []config.Rule{
			rule("block", "deny", "blocked"),
		}},
	})
	cfgAllow := cfg(map[string]config.ToolDef{})

	e := New(cfgDeny, nil)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for range 1000 {
			e.Evaluate(call("tool", map[string]any{}))
		}
	}()

	go func() {
		defer wg.Done()
		for i := range 1000 {
			if i%2 == 0 {
				e.SetConfig(cfgAllow)
			} else {
				e.SetConfig(cfgDeny)
			}
		}
	}()

	wg.Wait()
}

// --- Hide feature tests ---

func TestHiddenToolDenied(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Hide:    []string{"secret_tool"},
		Tools:   map[string]config.ToolDef{},
	}, nil)

	d := e.Evaluate(call("secret_tool", map[string]any{}))
	if d.Allowed {
		t.Error("hidden tool should be denied")
	}
	want := `Tool "secret_tool" is hidden by policy`
	if d.Message != want {
		t.Errorf("message = %q, want %q", d.Message, want)
	}
}

func TestHiddenToolsMethod(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Hide:    []string{"tool_a", "tool_b"},
		Tools:   map[string]config.ToolDef{},
	}, nil)

	hidden := e.HiddenTools()
	if len(hidden) != 2 {
		t.Fatalf("hidden count = %d, want 2", len(hidden))
	}
	if !hidden["tool_a"] {
		t.Error("expected tool_a in hidden set")
	}
	if !hidden["tool_b"] {
		t.Error("expected tool_b in hidden set")
	}

	// Empty hide list returns nil.
	e2 := New(&config.Config{
		Version: "1",
		Tools:   map[string]config.ToolDef{},
	}, nil)
	if got := e2.HiddenTools(); got != nil {
		t.Errorf("expected nil for empty hide list, got %v", got)
	}
}

func TestHideWildcardDeniesAll(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Hide:    []string{"*"},
		Tools:   map[string]config.ToolDef{},
	}, nil)

	for _, name := range []string{"tool_a", "tool_b", "anything"} {
		d := e.Evaluate(call(name, map[string]any{}))
		if d.Allowed {
			t.Errorf("wildcard hide should deny %q", name)
		}
	}
}

func TestHiddenToolNotAffectOthers(t *testing.T) {
	e := New(&config.Config{
		Version: "1",
		Hide:    []string{"tool_x"},
		Tools:   map[string]config.ToolDef{},
	}, nil)

	d := e.Evaluate(call("tool_x", map[string]any{}))
	if d.Allowed {
		t.Error("hidden tool_x should be denied")
	}

	d = e.Evaluate(call("tool_y", map[string]any{}))
	if !d.Allowed {
		t.Error("tool_y should be allowed when only tool_x is hidden")
	}
}
