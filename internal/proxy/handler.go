// Package proxy sits between the transport layer and the policy engine. It
// evaluates each tools/call request, manages stateful rate-limit reservations,
// and builds denied responses for blocked calls.
package proxy

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/policylayer/intercept/internal/config"
	"github.com/policylayer/intercept/internal/conv"
	"github.com/policylayer/intercept/internal/engine"
	"github.com/policylayer/intercept/internal/events"
	"github.com/policylayer/intercept/internal/state"
	"github.com/policylayer/intercept/internal/transport"
)

// Handler evaluates tool calls against policy rules, manages stateful
// reservations, and produces denied responses for blocked calls.
type Handler struct {
	engine  *engine.Engine
	store   state.StateStore
	cfg     atomic.Pointer[config.Config]
	emitter events.EventEmitter
}

// New creates a Handler. store may be nil if no stateful rules are configured.
// emitter may be nil to disable event emission.
func New(eng *engine.Engine, store state.StateStore, cfg *config.Config, emitter events.EventEmitter) *Handler {
	h := &Handler{engine: eng, store: store, emitter: emitter}
	h.cfg.Store(cfg)
	return h
}

// SetConfig replaces the active configuration for hot reload.
func (h *Handler) SetConfig(cfg *config.Config) {
	h.cfg.Store(cfg)
}

// Handle evaluates a tools/call request and returns a ToolCallResult.
func (h *Handler) Handle(req transport.ToolCallRequest) transport.ToolCallResult {
	cfg := h.cfg.Load()

	var result string
	var ruleName, denyMsg string

	defer func() {
		if h.emitter == nil {
			return
		}
		ev := events.Event{
			Type:     "tool_call",
			Tool:     req.Name,
			Result:   result,
			ArgsHash: events.HashArgs(req.Arguments),
		}
		if result == "denied" {
			ev.Rule = ruleName
			ev.Message = denyMsg
		}
		h.emitter.Emit(ev)
	}()

	call := engine.ToolCall{
		Name:      req.Name,
		Arguments: req.Arguments,
	}

	decision := h.engine.Evaluate(call)
	if !decision.Allowed {
		denyMsg = decision.Message
		if denyMsg == "" {
			denyMsg = fmt.Sprintf("Denied by rule %q", decision.Rule)
		}
		result = "denied"
		ruleName = decision.Rule
		return transport.ToolCallResult{
			Handled:  true,
			Response: buildDeniedResponse(req.ID, denyMsg),
		}
	}

	stateful := collectStatefulRules(cfg, req.Name)
	if len(stateful) == 0 {
		result = "allowed"
		return transport.ToolCallResult{Handled: false}
	}

	if h.store == nil {
		slog.Warn("stateful rules present but no state store configured, skipping reservations")
		result = "allowed"
		return transport.ToolCallResult{Handled: false}
	}

	reservations, reserveDenyMsg, err := h.reserveAll(req.Name, req.Arguments, stateful)
	if err != nil {
		slog.Error("reservation error", "error", err)
		result = "denied"
		denyMsg = "Internal policy error"
		return transport.ToolCallResult{
			Handled:  true,
			Response: buildDeniedResponse(req.ID, "Internal policy error"),
		}
	}
	if reserveDenyMsg != "" {
		result = "denied"
		denyMsg = reserveDenyMsg
		return transport.ToolCallResult{
			Handled:  true,
			Response: buildDeniedResponse(req.ID, reserveDenyMsg),
		}
	}

	result = "allowed"
	return transport.ToolCallResult{
		Handled: false,
		OnResponse: func(data json.RawMessage) {
			if isErrorResponse(data) {
				h.rollbackAll(reservations)
			}
		},
	}
}

// statefulRule pairs a rule that has a state block with the scope (tool name
// or "_global") used to construct the counter key.
type statefulRule struct {
	rule  config.Rule
	scope string
}

// collectStatefulRules returns all rules with a state block that apply to the
// given tool, including wildcard ("*") rules.
func collectStatefulRules(cfg *config.Config, toolName string) []statefulRule {
	var rules []statefulRule

	if tool, ok := cfg.Tools[toolName]; ok {
		for _, r := range tool.Rules {
			if r.State != nil {
				rules = append(rules, statefulRule{rule: r, scope: toolName})
			}
		}
	}

	if wildcard, ok := cfg.Tools["*"]; ok {
		for _, r := range wildcard.Rules {
			if r.State != nil {
				rules = append(rules, statefulRule{rule: r, scope: "_global"})
			}
		}
	}

	return rules
}

// reservation records a successful state counter increment so it can be
// rolled back if the upstream call fails.
type reservation struct {
	Key    string
	Amount int64
}

// reserveAll atomically reserves capacity for all stateful rules. If any
// reservation fails or is denied, all prior reservations are rolled back.
// Returns the reservations, a deny message (empty on success), and any error.
func (h *Handler) reserveAll(toolName string, args map[string]any, rules []statefulRule) ([]reservation, string, error) {
	var reservations []reservation

	for _, sr := range rules {
		key := sr.scope + "." + sr.rule.State.Counter

		amount, err := resolveIncrement(sr.rule.State, args)
		if err != nil {
			h.rollbackAll(reservations)
			return nil, "", fmt.Errorf("resolving increment for %s: %w", key, err)
		}

		limit, hasLimit := extractLimit(sr.rule, sr.scope)
		if !hasLimit {
			continue
		}

		window := state.WindowDuration(sr.rule.State.Window)
		allowed, _, err := h.store.Reserve(key, amount, limit, window)
		if err != nil {
			h.rollbackAll(reservations)
			return nil, "", fmt.Errorf("reserving %s: %w", key, err)
		}

		if !allowed {
			h.rollbackAll(reservations)
			msg := sr.rule.OnDeny
			if msg == "" {
				msg = fmt.Sprintf("Rate limit exceeded for %s", sr.rule.Name)
			}
			return nil, msg, nil
		}

		reservations = append(reservations, reservation{Key: key, Amount: amount})
	}

	return reservations, "", nil
}

// rollbackAll decrements all previously reserved counters, logging any errors.
func (h *Handler) rollbackAll(reservations []reservation) {
	for _, r := range reservations {
		if err := h.store.Rollback(r.Key, r.Amount); err != nil {
			slog.Error("rollback failed", "key", r.Key, "error", err)
		}
	}
}

// resolveIncrement returns the counter increment amount for a stateful rule.
// If increment_from is set, the value is read from the tool call arguments;
// otherwise the static Increment value is used.
func resolveIncrement(sd *config.StateDef, args map[string]any) (int64, error) {
	if sd.IncrementFrom == "" {
		return int64(sd.Increment), nil
	}

	path := strings.TrimPrefix(sd.IncrementFrom, "args.")
	val, ok := engine.ResolveArgs(args, path)
	if !ok {
		return 0, fmt.Errorf("increment_from path %q not found in arguments", sd.IncrementFrom)
	}

	return conv.ToInt64(val)
}

// extractLimit finds the numeric limit from the rule's conditions by looking for
// a "lt" or "lte" comparison on the rule's own state counter path. Returns the
// effective limit and true, or (0, false) if no limit condition is found.
func extractLimit(r config.Rule, scope string) (int64, bool) {
	counterPath := "state." + scope + "." + r.State.Counter

	for _, c := range r.Conditions {
		if c.Path != counterPath {
			continue
		}
		numVal, ok := conv.ToFloat64(c.Value)
		if !ok {
			continue
		}
		switch c.Op {
		case "lt":
			return int64(numVal) - 1, true
		case "lte":
			return int64(numVal), true
		}
	}

	return 0, false
}
