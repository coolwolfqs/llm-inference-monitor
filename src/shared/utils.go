package shared

import "fmt"

// Sprintf is a convenience wrapper around fmt.Sprintf
func Sprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}

// Float64Ptr returns a pointer to the given float64 value
func Float64Ptr(v float64) *float64 {
	return &v
}

// Float64Value safely dereferences a *float64, returning 0 if nil
func Float64Value(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}
