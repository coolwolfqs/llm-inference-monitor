package collectors

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"inference-hub-v3/src/kv_engine"
	"inference-hub-v3/src/shared"
)

type KVCollector struct {
	base     *BaseCollector
	engine   *kv_engine.KVEngine
	llamaURL string
}

func NewKVCollector(base *BaseCollector, engine *kv_engine.KVEngine, llamaURL string) *KVCollector {
	return &KVCollector{
		base:     base,
		engine:   engine,
		llamaURL: llamaURL,
	}
}

func (c *KVCollector) Collect(ctx context.Context) (interface{}, error) {
	gpus := c.parseNvidiaSmi()

	numGPUs := 0
	for _, g := range gpus {
		if g.Name != "" && !strings.Contains(g.Name, "Aggregate") {
			numGPUs++
		}
	}

	if numGPUs == 0 {
		return shared.KVMetrics{Captured: false}, nil
	}

	// Check if llama-server identity changed (model/config/pid) — auto-reset baseline
	c.engine.CheckIdentityAndReset(numGPUs)

	// Auto-capture baseline in the background if not yet captured
	// (CaptureBaseline samples GPU memory over tens of seconds and must not
	// block the collector cycle).
	c.engine.EnsureBaselineAsync(numGPUs)

	result := c.engine.Compute(gpus, numGPUs)
	return toKVMetrics(result), nil
}

func toKVMetrics(r kv_engine.KVResult) shared.KVMetrics {
	cards := make([]shared.KVCard, len(r.Cards))
	for i, c := range r.Cards {
		cards[i] = shared.KVCard(c)
	}
	return shared.KVMetrics{
		Summary:  shared.KVSummary(r.Summary),
		Cards:    cards,
		Captured: r.Captured,
	}
}

func (c *KVCollector) parseNvidiaSmi() []shared.GPUMetrics {
	var gpus []shared.GPUMetrics
	fields := "name,utilization.gpu,memory.used,memory.total,memory.free,temperature.gpu,power.draw,power.limit,fan.speed,clocks.current.graphics,clocks.max.graphics,driver_version,pcie.link.gen.current,pcie.link.width.current,pcie.link.gen.max,pcie.link.width.max"
	cmd := exec.Command("sh", "-c", "nvidia-smi --query-gpu="+fields+" --format=csv,noheader,nounits 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return gpus
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for i, line := range lines {
		vals := strings.Split(line, ",")
		if len(vals) < 16 {
			continue
		}
		gpu := shared.GPUMetrics{
			Index:      i,
			Name:       strings.TrimSpace(vals[0]),
			Util:       pf(vals[1]),
			MemUsed:    pf(vals[2]),
			MemTotal:   pf(vals[3]),
			MemFree:    pf(vals[4]),
			Temp:       pf(vals[5]),
			PowerDraw:  pf(vals[6]),
			PowerLimit: pf(vals[7]),
			FanSpeed:   shared.Float64Ptr(pf(vals[8])),
			Clock:      pf(vals[9]),
			ClockMax:   pf(vals[10]),
			Driver:     strings.TrimSpace(vals[11]),
			PCIE: shared.PCIEInfo{
				CurrentGen:   strings.TrimSpace(vals[12]),
				CurrentWidth: strings.TrimSpace(vals[13]),
				Gen:          strings.TrimSpace(vals[14]),
				Width:        strings.TrimSpace(vals[15]),
			},
		}
		if gpu.MemTotal > 0 {
			gpu.MemUtilPct = gpu.MemUsed / gpu.MemTotal * 100
		}
		if gpu.PowerLimit > 0 {
			gpu.PowerPct = gpu.PowerDraw / gpu.PowerLimit * 100
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

func pf(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}
