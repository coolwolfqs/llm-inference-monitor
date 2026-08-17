package shared

import "math"

// MaxReliableTPS is deliberately conservative for the deployed inference
// node. llama.cpp prints timings rounded to two decimals; a zero-duration
// sample can otherwise become an enormous but meaningless throughput value.
const MaxReliableTPS = 10_000.0

// IsReliableTPS accepts only finite, positive values within the range that can
// be represented as a useful request-level throughput sample on this node.
func IsReliableTPS(value float64) bool {
	return value > 0 && value <= MaxReliableTPS && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// IsReliableEvalSample validates the timing inputs used to derive tokens/s.
// A two-decimal "0.00 ms" measurement is rounded and must not be used for a
// division that turns one token into a fake million-token/s result.
func IsReliableEvalSample(evalMs float64, evalTokens int, tps float64) bool {
	return evalMs >= 0.1 && evalTokens > 0 && IsReliableTPS(tps)
}
