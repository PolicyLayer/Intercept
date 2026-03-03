package config

import (
	"strings"
	"testing"
)

func TestLoadValidPolicy(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error loading valid policy: %v", err)
	}

	if cfg.Version != "1" {
		t.Errorf("version = %q, want %q", cfg.Version, "1")
	}

	if cfg.Description != "Stripe MCP server policies" {
		t.Errorf("description = %q, want %q", cfg.Description, "Stripe MCP server policies")
	}

	// 5 tools: create_charge, create_issue, create_refund, delete_customer, *
	if got := len(cfg.Tools); got != 5 {
		t.Fatalf("tool count = %d, want 5", got)
	}

	// create_charge has 3 rules
	cc := cfg.Tools["create_charge"]
	if got := len(cc.Rules); got != 3 {
		t.Fatalf("create_charge rule count = %d, want 3", got)
	}

	// First rule: "max single charge"
	r := cc.Rules[0]
	if r.Name != "max single charge" {
		t.Errorf("rule name = %q, want %q", r.Name, "max single charge")
	}
	if r.Action != "evaluate" {
		t.Errorf("rule action = %q, want %q", r.Action, "evaluate")
	}
	if len(r.Conditions) != 1 {
		t.Fatalf("condition count = %d, want 1", len(r.Conditions))
	}
	if r.Conditions[0].Path != "args.amount" {
		t.Errorf("condition path = %q, want %q", r.Conditions[0].Path, "args.amount")
	}
	if r.Conditions[0].Op != "lte" {
		t.Errorf("condition op = %q, want %q", r.Conditions[0].Op, "lte")
	}

	// State block on daily spend cap
	r2 := cc.Rules[1]
	if r2.State == nil {
		t.Fatal("expected state block on daily spend cap rule")
	}
	if r2.State.Counter != "daily_spend" {
		t.Errorf("state counter = %q, want %q", r2.State.Counter, "daily_spend")
	}
	if r2.State.Window != "day" {
		t.Errorf("state window = %q, want %q", r2.State.Window, "day")
	}
	if r2.State.IncrementFrom != "args.amount" {
		t.Errorf("state increment_from = %q, want %q", r2.State.IncrementFrom, "args.amount")
	}

	// Wildcard rule
	wildcard := cfg.Tools["*"]
	if len(wildcard.Rules) != 1 {
		t.Fatalf("wildcard rule count = %d, want 1", len(wildcard.Rules))
	}
	if wildcard.Rules[0].Name != "global rate limit" {
		t.Errorf("wildcard rule name = %q, want %q", wildcard.Rules[0].Name, "global rate limit")
	}

	// delete_customer has deny action
	dc := cfg.Tools["delete_customer"]
	if dc.Rules[0].Action != "deny" {
		t.Errorf("delete_customer action = %q, want %q", dc.Rules[0].Action, "deny")
	}
}

func TestLoadInvalidYAML(t *testing.T) {
	_, err := Load("../../testdata/invalid_policy.yaml")
	if err == nil {
		t.Fatal("expected error loading invalid YAML, got nil")
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	_, err := Load("../../testdata/nonexistent.yaml")
	if err == nil {
		t.Fatal("expected error loading nonexistent file, got nil")
	}
}

func TestValidateInvalidSchema(t *testing.T) {
	cfg, err := Load("../../testdata/invalid_schema.yaml")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation errors, got none")
	}

	// Collect all error messages for checking.
	var msgs []string
	for _, e := range errs {
		msgs = append(msgs, e.Error())
	}
	all := strings.Join(msgs, "\n")

	checks := []string{
		"version must be",
		"rule must have a name",
		"unknown operator",
		"path must start with",
		"requires a list value",
		"requires a numeric value",
		"invalid regex",
		"deny rules must not have conditions",
		"evaluate rules must have at least one condition",
		"rate_limit count must be a positive integer",
		"rate_limit cannot be combined with conditions or state",
		"rate_limit window must be",
		`rate_limit cannot be used with action "deny"`,
	}

	for _, check := range checks {
		if !strings.Contains(all, check) {
			t.Errorf("expected error containing %q, not found in:\n%s", check, all)
		}
	}
}

func TestValidateValidPolicy(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	errs := Validate(cfg)
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("unexpected validation error: %v", e)
		}
	}
}

func TestDefaultAction(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The first rule in create_charge has no explicit action, should default to "evaluate".
	r := cfg.Tools["create_charge"].Rules[0]
	if r.Action != "evaluate" {
		t.Errorf("default action = %q, want %q", r.Action, "evaluate")
	}
}

func TestDefaultIncrement(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The daily refund count rule has a state block with no explicit increment.
	r := cfg.Tools["create_refund"].Rules[1]
	if r.State == nil {
		t.Fatal("expected state block")
	}
	if r.State.Increment != 1 {
		t.Errorf("default increment = %d, want 1", r.State.Increment)
	}
}

func TestValidateConditionOps(t *testing.T) {
	tests := []struct {
		name    string
		cond    Condition
		wantErr bool
		errMsg  string
	}{
		{
			name:    "valid eq",
			cond:    Condition{Path: "args.x", Op: "eq", Value: "test"},
			wantErr: false,
		},
		{
			name:    "unknown op",
			cond:    Condition{Path: "args.x", Op: "bad_op", Value: "test"},
			wantErr: true,
			errMsg:  "unknown operator",
		},
		{
			name:    "in with non-slice",
			cond:    Condition{Path: "args.x", Op: "in", Value: "not_a_list"},
			wantErr: true,
			errMsg:  "requires a list value",
		},
		{
			name:    "in with slice",
			cond:    Condition{Path: "args.x", Op: "in", Value: []any{"a", "b"}},
			wantErr: false,
		},
		{
			name:    "lt with string",
			cond:    Condition{Path: "args.x", Op: "lt", Value: "nope"},
			wantErr: true,
			errMsg:  "requires a numeric value",
		},
		{
			name:    "lt with int",
			cond:    Condition{Path: "args.x", Op: "lt", Value: 42},
			wantErr: false,
		},
		{
			name:    "regex valid",
			cond:    Condition{Path: "args.x", Op: "regex", Value: "^foo.*$"},
			wantErr: false,
		},
		{
			name:    "regex invalid",
			cond:    Condition{Path: "args.x", Op: "regex", Value: "[invalid("},
			wantErr: true,
			errMsg:  "invalid regex",
		},
		{
			name:    "bad path prefix",
			cond:    Condition{Path: "foo.bar", Op: "eq", Value: "x"},
			wantErr: true,
			errMsg:  "path must start with",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateCondition("test", tt.cond)
			if tt.wantErr {
				if len(errs) == 0 {
					t.Error("expected error, got none")
					return
				}
				found := false
				for _, e := range errs {
					if strings.Contains(e.Error(), tt.errMsg) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got %v", tt.errMsg, errs)
				}
			} else {
				if len(errs) > 0 {
					t.Errorf("unexpected errors: %v", errs)
				}
			}
		})
	}
}

func TestParseRateLimit(t *testing.T) {
	tests := []struct {
		input   string
		count   int
		window  string
		wantErr bool
	}{
		{"5/hour", 5, "hour", false},
		{"10/day", 10, "day", false},
		{"100/minute", 100, "minute", false},
		{"1/hour", 1, "hour", false},
		{"five/hour", 0, "", true},
		{"0/hour", 0, "", true},
		{"-1/hour", 0, "", true},
		{"5/week", 0, "", true},
		{"5", 0, "", true},
		{"", 0, "", true},
		{"5/", 0, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			count, window, err := parseRateLimit(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if count != tt.count {
				t.Errorf("count = %d, want %d", count, tt.count)
			}
			if window != tt.window {
				t.Errorf("window = %q, want %q", window, tt.window)
			}
		})
	}
}

func TestRateLimitDesugaring(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	ci := cfg.Tools["create_issue"]
	if len(ci.Rules) != 1 {
		t.Fatalf("create_issue rule count = %d, want 1", len(ci.Rules))
	}

	r := ci.Rules[0]

	// RateLimit should be cleared after desugaring.
	if r.RateLimit != "" {
		t.Errorf("RateLimit should be cleared, got %q", r.RateLimit)
	}

	if r.Action != "evaluate" {
		t.Errorf("action = %q, want %q", r.Action, "evaluate")
	}

	if len(r.Conditions) != 1 {
		t.Fatalf("condition count = %d, want 1", len(r.Conditions))
	}
	c := r.Conditions[0]
	if c.Path != "state.create_issue._rate_hour" {
		t.Errorf("condition path = %q, want %q", c.Path, "state.create_issue._rate_hour")
	}
	if c.Op != "lte" {
		t.Errorf("condition op = %q, want %q", c.Op, "lte")
	}
	if c.Value != 5 {
		t.Errorf("condition value = %v, want 5", c.Value)
	}

	if r.State == nil {
		t.Fatal("expected state block")
	}
	if r.State.Counter != "_rate_hour" {
		t.Errorf("state counter = %q, want %q", r.State.Counter, "_rate_hour")
	}
	if r.State.Window != "hour" {
		t.Errorf("state window = %q, want %q", r.State.Window, "hour")
	}
	if r.State.Increment != 1 {
		t.Errorf("state increment = %d, want 1", r.State.Increment)
	}
}

func TestRateLimitAutoOnDeny(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Tools: map[string]ToolDef{
			"some_tool": {Rules: []Rule{
				{Name: "limit", RateLimit: "3/day"},
			}},
		},
	}
	applyDefaults(cfg)

	r := cfg.Tools["some_tool"].Rules[0]
	want := "Rate limit of 3 per day reached. Try again later."
	if r.OnDeny != want {
		t.Errorf("on_deny = %q, want %q", r.OnDeny, want)
	}
}

func TestRateLimitOnDenyPreserved(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	r := cfg.Tools["create_issue"].Rules[0]
	if r.OnDeny != "Hourly limit of 5 new issues reached" {
		t.Errorf("on_deny = %q, want %q", r.OnDeny, "Hourly limit of 5 new issues reached")
	}
}

func TestRateLimitWildcardScope(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Tools: map[string]ToolDef{
			"*": {Rules: []Rule{
				{Name: "global limit", RateLimit: "60/minute"},
			}},
		},
	}
	applyDefaults(cfg)

	r := cfg.Tools["*"].Rules[0]
	if len(r.Conditions) != 1 {
		t.Fatalf("condition count = %d, want 1", len(r.Conditions))
	}
	if r.Conditions[0].Path != "state._global._rate_minute" {
		t.Errorf("condition path = %q, want %q", r.Conditions[0].Path, "state._global._rate_minute")
	}
}

func TestValidateDuplicateCounter(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Tools: map[string]ToolDef{
			"some_tool": {Rules: []Rule{
				{Name: "limit1", RateLimit: "5/hour"},
				{Name: "limit2", RateLimit: "10/hour"},
			}},
		},
	}
	applyDefaults(cfg)

	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate state counter") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected duplicate state counter error")
		for _, e := range errs {
			t.Logf("  got: %v", e)
		}
	}
}

func TestDefaultFieldOmittedBecomesAllow(t *testing.T) {
	cfg, err := Load("../../testdata/valid_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Default != "allow" {
		t.Errorf("default = %q, want %q", cfg.Default, "allow")
	}
}

func TestDefaultFieldDenyPreserved(t *testing.T) {
	cfg, err := Load("../../testdata/default_deny_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Default != "deny" {
		t.Errorf("default = %q, want %q", cfg.Default, "deny")
	}
}

func TestValidateInvalidDefault(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Default: "block",
		Tools:   map[string]ToolDef{},
	}
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), `default must be`) {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected validation error for invalid default value")
		for _, e := range errs {
			t.Logf("  got: %v", e)
		}
	}
}

func TestValidateDefaultDenyPolicy(t *testing.T) {
	cfg, err := Load("../../testdata/default_deny_policy.yaml")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	errs := Validate(cfg)
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("unexpected validation error: %v", e)
		}
	}
}

func TestValidateWindows(t *testing.T) {
	tests := []struct {
		window  string
		wantErr bool
	}{
		{"minute", false},
		{"hour", false},
		{"day", false},
		{"week", true},
		{"", true},
	}

	for _, tt := range tests {
		t.Run(tt.window, func(t *testing.T) {
			errs := validateState("test", &StateDef{Counter: "c", Window: tt.window})
			if tt.wantErr && len(errs) == 0 {
				t.Error("expected error, got none")
			}
			if !tt.wantErr && len(errs) > 0 {
				t.Errorf("unexpected errors: %v", errs)
			}
		})
	}
}

func TestValidateHideDuplicateEntries(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Hide:    []string{"tool_a", "tool_b", "tool_a"},
		Tools:   map[string]ToolDef{},
	}
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate entry") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error containing \"duplicate entry\"")
		for _, e := range errs {
			t.Logf("  got: %v", e)
		}
	}
}

func TestValidateHideEmptyString(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Hide:    []string{"tool_a", ""},
		Tools:   map[string]ToolDef{},
	}
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "must not be empty") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected error containing \"must not be empty\"")
		for _, e := range errs {
			t.Logf("  got: %v", e)
		}
	}
}

func TestValidateHideValid(t *testing.T) {
	cfg := &Config{
		Version: "1",
		Default: "allow",
		Hide:    []string{"tool_a", "tool_b"},
		Tools:   map[string]ToolDef{},
	}
	errs := Validate(cfg)
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("unexpected validation error: %v", e)
		}
	}
}
