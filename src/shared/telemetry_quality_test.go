package shared

import (
	"math"
	"testing"
)

func TestIsReliableTPS(t *testing.T) {
	tests := []struct {
		name  string
		value float64
		want  bool
	}{
		{name: "positive sample", value: 117.95, want: true},
		{name: "upper bound", value: MaxReliableTPS, want: true},
		{name: "zero", value: 0, want: false},
		{name: "negative", value: -1, want: false},
		{name: "rounded zero artifact", value: 1_000_000, want: false},
		{name: "nan", value: math.NaN(), want: false},
		{name: "infinity", value: math.Inf(1), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsReliableTPS(test.value); got != test.want {
				t.Fatalf("IsReliableTPS(%v) = %v, want %v", test.value, got, test.want)
			}
		})
	}
}

func TestIsReliableEvalSampleRejectsRoundedZeroTiming(t *testing.T) {
	if IsReliableEvalSample(0, 1, 1_000_000) {
		t.Fatal("expected rounded zero-duration sample to be rejected")
	}
	if !IsReliableEvalSample(84.78, 10, 117.95) {
		t.Fatal("expected valid eval timing to be accepted")
	}
}
