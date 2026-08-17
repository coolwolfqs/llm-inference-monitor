package collectors

import (
	"math"
	"testing"

	"inference-hub-v3/src/shared"
)

func TestParseRocmSMIShowAllInfoAndVram(t *testing.T) {
	c := &GPUCollector{}
	got := c.parseRocmSMI(`GPU[0] : Temperature (Sensor edge) (C): 38.0
GPU[0] : sclk clock level: 1: (603Mhz)
GPU[0] : Current Socket Graphics Package Power (W): 9.079
GPU[0] : GPU use (%): 0
GPU[0] : GPU Memory Allocated (VRAM%): 52
GPU[0] : PCI Bus: 0000:C6:00.0
GPU[0] : Valid sclk range: 600Mhz - 2900Mhz
GPU[0] : VRAM Total Memory (B): 68719476736
GPU[0] : VRAM Total Used Memory (B): 36041191424`)
	if got == nil {
		t.Fatal("parsed GPU is nil")
	}
	if got.MemTotal < 65535 || got.MemUsed < 34370 || got.MemFree < 31164 {
		t.Fatalf("memory=%+v want approximately 65536/34371/31165 MB", got)
	}
	if math.Abs(got.MemUtilPct-52.4468) > 0.01 {
		t.Fatalf("mem pct=%f want approximately 52.4468", got.MemUtilPct)
	}
	if got.PowerDraw < 9 || got.Clock != 603 || got.ClockMax != 2900 || got.PCIBusID != "0000:C6:00.0" {
		t.Fatalf("metrics=%+v want power/clock/PCI parsed", got)
	}
}

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
