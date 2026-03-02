package engine

import "strings"

// resolvePath resolves a dotted path to a value from the tool call or state.
func (e *Engine) resolvePath(call ToolCall, path string) (any, bool) {
	switch {
	case strings.HasPrefix(path, "args."):
		return ResolveArgs(call.Arguments, strings.TrimPrefix(path, "args."))
	case strings.HasPrefix(path, "state."):
		if e.state == nil {
			return nil, false
		}
		val, exists, err := e.state.Resolve(path)
		if err != nil {
			return nil, false
		}
		return val, exists
	default:
		return nil, false
	}
}

// ResolveArgs walks a dotted path (e.g. "owner" or "config.nested.key") through
// the arguments map and returns the value at that path, or (nil, false) if any
// segment is missing.
func ResolveArgs(args map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var current any = args

	for _, part := range parts {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}

	return current, true
}
