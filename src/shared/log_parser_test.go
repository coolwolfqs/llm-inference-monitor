package shared

import (
	"testing"
	"time"
)

func TestParseLogMonotonic(t *testing.T) {
	got, ok := parseLogMonotonic("1083.11.912.794")
	if !ok {
		t.Fatal("expected timestamp to parse")
	}
	want := 1083*60 + 11.912794
	if diff := got - want; diff < -0.000001 || diff > 0.000001 {
		t.Fatalf("got %.6f, want %.6f", got, want)
	}
}

func TestLogWallTimeUsesAnchorDelta(t *testing.T) {
	const modTime = int64(1786505423)
	got, ok := logWallTime("1078.32.845.956", "1083.11.912.794", modTime)
	if !ok {
		t.Fatal("expected wall time conversion to succeed")
	}
	want := time.Unix(modTime, 0).Add(-279*time.Second - 66838*time.Microsecond)
	if delta := got.Sub(want); delta < -time.Microsecond || delta > time.Microsecond {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestLogWallTimeRejectsInvalidAnchor(t *testing.T) {
	if _, ok := logWallTime("1083.11.912.794", "", 1786505423); ok {
		t.Fatal("expected conversion without an anchor to fail")
	}
}
