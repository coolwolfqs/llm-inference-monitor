package shared

import (
	"os"
	"path/filepath"
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

func TestParseLlamaLogsRejectsRoundedZeroEvalTiming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llama.log")
	content := "416.14.434.672 I slot print_timing: id 1 | prompt eval time = 1212.07 ms / 555 tokens (2.18 ms per token, 457.82 tokens per second)\n" +
		"416.14.434.681 I slot print_timing: id 1 | eval time = 0.00 ms / 1 tokens (0.00 ms per token, 1000000.00 tokens per second)\n" +
		"416.14.434.683 I slot print_timing: id 1 | total time = 1212.08 ms / 556 tokens\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	logs, _, _ := ParseLlamaLogsEx(path, 10, time.Now().Unix())
	if len(logs) != 1 {
		t.Fatalf("got %d log entries, want 1", len(logs))
	}
	if logs[0].EvalTokens != 1 {
		t.Fatalf("got eval tokens %d, want 1", logs[0].EvalTokens)
	}
	if logs[0].TPS != 0 {
		t.Fatalf("got unreliable TPS %.2f, want 0", logs[0].TPS)
	}
}

func TestParseLlamaLogsKeepsReliableEvalTiming(t *testing.T) {
	path := filepath.Join(t.TempDir(), "llama.log")
	content := "417.14.434.672 I slot print_timing: id 1 | prompt eval time = 1212.07 ms / 555 tokens (2.18 ms per token, 457.82 tokens per second)\n" +
		"417.14.443.590 I slot print_timing: id 1 | eval time = 8917.64 ms / 534 tokens (16.70 ms per token, 59.88 tokens per second)\n" +
		"417.14.443.592 I slot print_timing: id 1 | total time = 10129.73 ms / 1089 tokens\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	logs, _, _ := ParseLlamaLogsEx(path, 10, time.Now().Unix())
	if len(logs) != 1 {
		t.Fatalf("got %d log entries, want 1", len(logs))
	}
	if diff := logs[0].TPS - 59.88; diff < -0.000001 || diff > 0.000001 {
		t.Fatalf("got TPS %.5f, want 59.88", logs[0].TPS)
	}
}
