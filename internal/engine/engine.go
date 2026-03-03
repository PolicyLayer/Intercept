// Package engine evaluates MCP tool calls against policy rules. It supports
// argument conditions, stateful counter checks, and wildcard rules.
package engine

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/policylayer/intercept/internal/config"
)

// ToolCall represents an incoming tool invocation to evaluate.
type ToolCall struct {
	Name      string
	Arguments map[string]any
}

// Decision is the result of evaluating a tool call against policy rules.
type Decision struct {
	Allowed bool
	Rule    string
	Message string
}

// StateResolver resolves state paths to their current values.
type StateResolver interface {
	Resolve(path string) (value any, exists bool, err error)
}

// Engine evaluates tool calls against a loaded policy configuration.
type Engine struct {
	cfg   atomic.Pointer[config.Config]
	state StateResolver
}

// New creates a new Engine. stateResolver may be nil, in which case
// state conditions automatically pass.
func New(cfg *config.Config, stateResolver StateResolver) *Engine {
	e := &Engine{state: stateResolver}
	e.cfg.Store(cfg)
	return e
}

// SetConfig replaces the active configuration for hot reload.
func (e *Engine) SetConfig(cfg *config.Config) {
	e.cfg.Store(cfg)
}

// Evaluate checks a tool call against all matching rules and returns a decision.
// When default is "deny", unlisted tools are rejected before rule evaluation.
// Rules are checked in order: tool-specific first, then wildcard ("*").
// A "deny" action immediately denies. For "evaluate" actions, all conditions
// must pass (AND logic); the first failing condition denies.
// If no rules match and default is "allow", the call is allowed.

// HiddenTools returns a set of hidden tool names from the current config.
// Returns nil if the hide list is empty.
func (e *Engine) HiddenTools() map[string]bool {
	cfg := e.cfg.Load()
	if len(cfg.Hide) == 0 {
		return nil
	}
	m := make(map[string]bool, len(cfg.Hide))
	for _, name := range cfg.Hide {
		m[name] = true
	}
	return m
}

func (e *Engine) Evaluate(call ToolCall) Decision {
	cfg := e.cfg.Load()

	// Check hide list before rule evaluation.
	for _, name := range cfg.Hide {
		if name == call.Name || name == "*" {
			return Decision{
				Allowed: false,
				Rule:    "(hidden)",
				Message: fmt.Sprintf("Tool %q is hidden by policy", call.Name),
			}
		}
	}

	if cfg.Default == "deny" {
		if _, listed := cfg.Tools[call.Name]; !listed {
			return Decision{
				Allowed: false,
				Rule:    "(default deny)",
				Message: fmt.Sprintf("Tool %q is not permitted by policy", call.Name),
			}
		}
	}

	rules := matchingRules(cfg, call.Name)

	for _, r := range rules {
		if r.Action == "deny" {
			return Decision{Allowed: false, Rule: r.Name, Message: r.OnDeny}
		}

		if r.Action == "evaluate" {
			if !e.evaluateConditions(call, r.Conditions) {
				return Decision{Allowed: false, Rule: r.Name, Message: r.OnDeny}
			}
		}
	}

	return Decision{Allowed: true}
}

// matchingRules returns tool-specific rules followed by wildcard rules.
func matchingRules(cfg *config.Config, toolName string) []config.Rule {
	var rules []config.Rule

	if tool, ok := cfg.Tools[toolName]; ok {
		rules = append(rules, tool.Rules...)
	}

	if toolName != "*" {
		if wildcard, ok := cfg.Tools["*"]; ok {
			rules = append(rules, wildcard.Rules...)
		}
	}

	return rules
}

// evaluateConditions checks that all conditions pass (AND logic).
func (e *Engine) evaluateConditions(call ToolCall, conditions []config.Condition) bool {
	for _, c := range conditions {
		if !e.evaluateCondition(call, c) {
			return false
		}
	}
	return true
}

// evaluateCondition evaluates a single condition against a tool call.
func (e *Engine) evaluateCondition(call ToolCall, cond config.Condition) bool {
	// State conditions auto-pass when no state resolver is present.
	if strings.HasPrefix(cond.Path, "state.") && e.state == nil {
		return true
	}

	val, exists := e.resolvePath(call, cond.Path)

	if cond.Op == "exists" {
		expectedBool, ok := cond.Value.(bool)
		if !ok {
			return false
		}
		actual := exists && val != nil
		return actual == expectedBool
	}

	if !exists {
		return false
	}

	return evalOp(cond.Op, val, cond.Value)
}
