package config

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// validOps is the set of recognised condition operators.
var validOps = map[string]bool{
	"eq": true, "neq": true,
	"in": true, "not_in": true,
	"lt": true, "lte": true,
	"gt": true, "gte": true,
	"regex": true, "contains": true, "exists": true,
}

// numericOps is the subset of operators that require numeric values.
var numericOps = map[string]bool{
	"lt": true, "lte": true, "gt": true, "gte": true,
}

// sliceOps is the subset of operators that require list values.
var sliceOps = map[string]bool{
	"in": true, "not_in": true,
}

// validWindows is the set of recognised time window names for rate limiting.
var validWindows = map[string]bool{
	"minute": true, "hour": true, "day": true,
}

// Validate checks a parsed Config for all schema and semantic errors,
// returning every error found (not just the first).
func Validate(cfg *Config) []error {
	var errs []error

	if cfg.Version != "1" {
		errs = append(errs, fmt.Errorf("version must be %q, got %q", "1", cfg.Version))
	}

	if cfg.Default != "allow" && cfg.Default != "deny" {
		errs = append(errs, fmt.Errorf("default must be %q or %q, got %q", "allow", "deny", cfg.Default))
	}

	toolNames := make([]string, 0, len(cfg.Tools))
	for name := range cfg.Tools {
		toolNames = append(toolNames, name)
	}
	sort.Strings(toolNames)

	for _, toolName := range toolNames {
		tool := cfg.Tools[toolName]

		// Track counter names per tool to detect duplicates.
		countersSeen := map[string]int{}
		for i, rule := range tool.Rules {
			if rule.State != nil {
				if prev, ok := countersSeen[rule.State.Counter]; ok {
					errs = append(errs, fmt.Errorf(
						"tools.%s.rules[%d]: duplicate state counter %q (also used by rules[%d])",
						toolName, i, rule.State.Counter, prev,
					))
				} else {
					countersSeen[rule.State.Counter] = i
				}
			}
		}

		for i, rule := range tool.Rules {
			prefix := fmt.Sprintf("tools.%s.rules[%d]", toolName, i)
			errs = append(errs, validateRule(prefix, toolName, rule, cfg)...)
		}
	}

	return errs
}

// validateRule checks a single rule for structural and semantic errors.
func validateRule(prefix, toolName string, rule Rule, cfg *Config) []error {
	var errs []error

	// If RateLimit is still set, desugaring was skipped due to conflicts or parse error.
	if rule.RateLimit != "" {
		if _, _, err := parseRateLimit(rule.RateLimit); err != nil {
			errs = append(errs, fmt.Errorf("%s: %v", prefix, err))
		}
		if len(rule.Conditions) > 0 || rule.State != nil {
			errs = append(errs, fmt.Errorf(
				"%s: rate_limit cannot be combined with conditions or state; use separate rules or the full syntax instead",
				prefix,
			))
		}
		if rule.Action == "deny" {
			errs = append(errs, fmt.Errorf(
				"%s: rate_limit cannot be used with action \"deny\"",
				prefix,
			))
		}
		return errs
	}

	if rule.Name == "" {
		errs = append(errs, fmt.Errorf("%s: rule must have a name", prefix))
	}

	if rule.Action != "evaluate" && rule.Action != "deny" {
		errs = append(errs, fmt.Errorf("%s: action must be %q or %q, got %q", prefix, "evaluate", "deny", rule.Action))
	}

	if rule.Action == "deny" && len(rule.Conditions) > 0 {
		errs = append(errs, fmt.Errorf("%s: deny rules must not have conditions", prefix))
	}

	if rule.Action == "evaluate" && len(rule.Conditions) == 0 {
		errs = append(errs, fmt.Errorf("%s: evaluate rules must have at least one condition", prefix))
	}

	for j, cond := range rule.Conditions {
		condPrefix := fmt.Sprintf("%s.conditions[%d]", prefix, j)
		errs = append(errs, validateCondition(condPrefix, cond)...)
	}

	if rule.State != nil {
		errs = append(errs, validateState(prefix, rule.State)...)
	}

	// Counter consistency: conditions referencing state.<tool>.<counter> should
	// have a matching state block somewhere in the rules for that tool.
	for _, cond := range rule.Conditions {
		if !strings.HasPrefix(cond.Path, "state.") {
			continue
		}
		parts := strings.SplitN(cond.Path, ".", 3)
		if len(parts) < 3 {
			continue
		}
		stateTool := parts[1]
		counterName := parts[2]

		if !hasMatchingStateBlock(cfg, stateTool, counterName) {
			errs = append(errs, fmt.Errorf(
				"%s: condition references state.%s.%s but no matching state block found",
				prefix, stateTool, counterName,
			))
		}
	}

	return errs
}

// validateCondition checks a single condition for valid path, operator, and value types.
func validateCondition(prefix string, cond Condition) []error {
	var errs []error

	if !strings.HasPrefix(cond.Path, "args.") && !strings.HasPrefix(cond.Path, "state.") {
		errs = append(errs, fmt.Errorf("%s: path must start with %q or %q, got %q", prefix, "args.", "state.", cond.Path))
	}

	if !validOps[cond.Op] {
		errs = append(errs, fmt.Errorf("%s: unknown operator %q", prefix, cond.Op))
	}

	if sliceOps[cond.Op] {
		if !isSlice(cond.Value) {
			errs = append(errs, fmt.Errorf("%s: operator %q requires a list value", prefix, cond.Op))
		}
	}

	if numericOps[cond.Op] {
		if !isNumeric(cond.Value) {
			errs = append(errs, fmt.Errorf("%s: operator %q requires a numeric value", prefix, cond.Op))
		}
	}

	if cond.Op == "exists" {
		if _, ok := cond.Value.(bool); !ok {
			errs = append(errs, fmt.Errorf("%s: operator %q requires a boolean value", prefix, cond.Op))
		}
	}

	if cond.Op == "regex" {
		s, ok := cond.Value.(string)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: regex value must be a string", prefix))
		} else if _, err := regexp.Compile(s); err != nil {
			errs = append(errs, fmt.Errorf("%s: invalid regex %q: %v", prefix, s, err))
		}
	}

	return errs
}

// validateState checks a state block for valid counter, window, and increment_from values.
func validateState(prefix string, state *StateDef) []error {
	var errs []error

	if state.Counter == "" {
		errs = append(errs, fmt.Errorf("%s.state: counter must not be empty", prefix))
	}

	if !validWindows[state.Window] {
		errs = append(errs, fmt.Errorf("%s.state: window must be %q, %q, or %q, got %q",
			prefix, "minute", "hour", "day", state.Window))
	}

	if state.IncrementFrom != "" && !strings.HasPrefix(state.IncrementFrom, "args.") {
		errs = append(errs, fmt.Errorf("%s.state: increment_from must start with %q, got %q",
			prefix, "args.", state.IncrementFrom))
	}

	return errs
}

// hasMatchingStateBlock returns true if any rule for the given tool defines a
// state block with the specified counter name. This validates that conditions
// referencing state paths have a corresponding state block to populate them.
func hasMatchingStateBlock(cfg *Config, toolName, counterName string) bool {
	tool, ok := cfg.Tools[toolName]
	if !ok {
		// For wildcard rules, the tool name in the state path is "_global"
		// and the tool key in config is "*".
		if toolName == "_global" {
			tool, ok = cfg.Tools["*"]
			if !ok {
				return false
			}
		} else {
			return false
		}
	}

	for _, rule := range tool.Rules {
		if rule.State != nil && rule.State.Counter == counterName {
			return true
		}
	}
	return false
}

// isSlice returns true if v is a []any (the type YAML lists unmarshal to).
func isSlice(v any) bool {
	if v == nil {
		return false
	}
	_, ok := v.([]any)
	return ok
}

// isNumeric returns true if v is a numeric type (int, int64, float32, float64).
func isNumeric(v any) bool {
	if v == nil {
		return false
	}
	switch v.(type) {
	case int, int64, float32, float64:
		return true
	default:
		return false
	}
}
