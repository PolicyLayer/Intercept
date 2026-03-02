// Package conv provides numeric type conversion helpers used across the
// engine and proxy packages.
package conv

import "fmt"

// ToFloat64 converts a numeric value (float32, float64, int, int64) to float64.
// Returns (0, false) if v is not a recognised numeric type.
func ToFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// ToInt64 converts a numeric value (float32, float64, int, int64) to int64.
// Returns an error if v is not a recognised numeric type.
func ToInt64(v any) (int64, error) {
	switch n := v.(type) {
	case float64:
		return int64(n), nil
	case float32:
		return int64(n), nil
	case int:
		return int64(n), nil
	case int64:
		return n, nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int64", v)
	}
}
