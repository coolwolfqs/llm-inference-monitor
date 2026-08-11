package collectors

import (
	"math"
	"testing"

	"inference-hub-v3/src/shared"
)

func TestAggregateUsesCapacityWeightedMemoryAndRawFree(t *testing.T) {
	c := &GPUCollector{}
	gpus := []shared.GPUMetrics{
		{MemUsed: 80, MemTotal: 100, MemFree: 15, Temp: 60, Clock: 1000, ClockMax: 1800, PowerDraw: 100, PowerLimit: 200},
		{MemUsed: 50, MemTotal: 200, MemFree: 140, Temp: 70, Clock: 1200, ClockMax: 2100, PowerDraw: 150, PowerLimit: 300},
	}
	agg := c.aggregate(gpus)
	if agg == nil {
		t.Fatal("aggregate is nil")
	}
	wantPct := 130.0 / 300.0 * 100
	if math.Abs(agg.MemUtilPct-wantPct) > 0.001 {
		t.Fatalf("mem pct=%f want=%f", agg.MemUtilPct, wantPct)
	}
	if agg.MemFree != 155 {
		t.Fatalf("raw free=%f want=155", agg.MemFree)
	}
	if agg.Temp != 70 {
		t.Fatalf("temp=%f want max=70", agg.Temp)
	}
	if agg.ClockMax != 2100 {
		t.Fatalf("clock max=%f want=2100", agg.ClockMax)
	}
}
