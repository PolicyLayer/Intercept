package engine

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/policylayer/intercept/internal/conv"
)

// regexCache stores compiled *regexp.Regexp values keyed by pattern string,
// avoiding repeated compilation for the same pattern across evaluations.
var regexCache sync.Map

// evalOp dispatches a condition operator and returns whether actual satisfies
// the comparison against expected.
func evalOp(op string, actual, expected any) bool {
	switch op {
	case "eq":
		return evalEq(actual, expected)
	case "neq":
		return !evalEq(actual, expected)
	case "in":
		return evalIn(actual, expected)
	case "not_in":
		return !evalIn(actual, expected)
	case "lt":
		return evalNumericCmp(actual, expected, func(a, b float64) bool { return a < b })
	case "lte":
		return evalNumericCmp(actual, expected, func(a, b float64) bool { return a <= b })
	case "gt":
		return evalNumericCmp(actual, expected, func(a, b float64) bool { return a > b })
	case "gte":
		return evalNumericCmp(actual, expected, func(a, b float64) bool { return a >= b })
	case "regex":
		return evalRegex(actual, expected)
	case "contains":
		return evalContains(actual, expected)
	default:
		return false
	}
}

// evalEq compares two values. Numeric coercion is attempted first,
// then string comparison as fallback.
func evalEq(actual, expected any) bool {
	af, aOk := conv.ToFloat64(actual)
	ef, eOk := conv.ToFloat64(expected)
	if aOk && eOk {
		return af == ef
	}
	return fmt.Sprintf("%v", actual) == fmt.Sprintf("%v", expected)
}

// evalIn returns true if actual equals any element in the expected list.
func evalIn(actual, expected any) bool {
	list, ok := expected.([]any)
	if !ok {
		return false
	}
	for _, item := range list {
		if evalEq(actual, item) {
			return true
		}
	}
	return false
}

// evalNumericCmp coerces both values to float64 and applies the comparison function.
// Returns false if either value cannot be coerced.
func evalNumericCmp(actual, expected any, cmp func(float64, float64) bool) bool {
	af, aOk := conv.ToFloat64(actual)
	ef, eOk := conv.ToFloat64(expected)
	if !aOk || !eOk {
		return false
	}
	return cmp(af, ef)
}

// evalRegex compiles the expected pattern and tests it against the actual string.
// Compiled patterns are cached in regexCache for reuse.
func evalRegex(actual, expected any) bool {
	s, ok := actual.(string)
	if !ok {
		return false
	}
	pattern, ok := expected.(string)
	if !ok {
		return false
	}
	if cached, ok := regexCache.Load(pattern); ok {
		return cached.(*regexp.Regexp).MatchString(s)
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return false
	}
	regexCache.Store(pattern, re)
	return re.MatchString(s)
}

// evalContains checks if actual contains expected.
// For strings, uses substring matching. For slices, checks element equality.
func evalContains(actual, expected any) bool {
	if s, ok := actual.(string); ok {
		es, ok := expected.(string)
		if !ok {
			return false
		}
		return strings.Contains(s, es)
	}
	if list, ok := actual.([]any); ok {
		for _, item := range list {
			if evalEq(item, expected) {
				return true
			}
		}
	}
	return false
}
