package collectors

import (
	"context"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"inference-hub-v3/src/shared"
)

type LLMMonitor struct {
	base        *BaseCollector
	http        *shared.HTTPClient
	cfg         *shared.Config
	historySize int
	ttftHistory []float64
	tpotHistory []float64
	specHistory []float64
}

func NewLLMMonitor(base *BaseCollector, httpClient *shared.HTTPClient, cfg *shared.Config) *LLMMonitor {
	histSize := cfg.Collectors.LLMMonitor.HistorySize
	if histSize <= 0 {
		histSize = 300
	}
	return &LLMMonitor{
		base:        base,
		http:        httpClient,
		cfg:         cfg,
		historySize: histSize,
	}
}

func (c *LLMMonitor) Collect(ctx context.Context) (interface{}, error) {
	baseURL := c.cfg.Services.LlamaServer.URL
	metricsURL := baseURL + c.cfg.Services.LlamaServer.MetricsPath

	result := shared.LLMMetrics{}

	resp, err := c.http.GetContext(ctx, metricsURL)
	if err != nil {
		c.write("llm_collect_error", 1.0, nil, time.Now())
		return result, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		c.write("llm_collect_error", 1.0, nil, time.Now())
		return result, err
	}
	text := string(body)

	metrics := make(map[string]float64)
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) >= 2 {
			val, _ := strconv.ParseFloat(parts[1], 64)
			metrics[parts[0]] = val
		}
	}

	// llama.cpp exposes prompt throughput, not time-to-first-token. Keep TTFT
	// unset and report the observable prompt cost under its correct name.
	if v, ok := metrics["llamacpp:prompt_tokens_seconds"]; ok && v > 0 {
		result.PromptTokensPS = v
		result.PromptMsPerToken = 1000.0 / v
	}

	if v, ok := metrics["llamacpp:predicted_tokens_seconds"]; ok && shared.IsReliableTPS(v) {
		tpot := 1000.0 / v
		result.TPOT = tpot
		c.tpotHistory = append(c.tpotHistory, tpot)
		if len(c.tpotHistory) > c.historySize {
			c.tpotHistory = c.tpotHistory[1:]
		}
	}

	// Speculative decoding
	if v, ok := metrics["llamacpp:speculative_accept_rate"]; ok {
		result.SpecAcceptRate = v
		c.specHistory = append(c.specHistory, v)
		if len(c.specHistory) > c.historySize {
			c.specHistory = c.specHistory[1:]
		}
	}
	if v, ok := metrics["llamacpp:speculative_n_draft"]; ok {
		result.SpecDraftLen = int(v)
	}
	if v, ok := metrics["llamacpp:speculative_n_accepted"]; ok {
		result.SpecAcceptedCount = int(v)
	}

	// KV Cache from metrics
	if v, ok := metrics["llamacpp:kv_cache_usage_ratio"]; ok {
		result.KVCacheUsedPct = v * 100
	}
	if v, ok := metrics["llamacpp:kv_cache_tokens_count"]; ok {
		result.KVCacheUsedTokens = int(v)
	}

	// Prompt / Eval throughput
	if v, ok := metrics["llamacpp:prompt_tokens_total"]; ok {
		result.PromptTokensTotal = int(v)
	}
	if v, ok := metrics["llamacpp:tokens_predicted_total"]; ok {
		result.EvalTokensTotal = int(v)
	}

	// Calculate percentiles
	result.TPOTP50 = c.percentile(c.tpotHistory, 50)
	result.TPOTP95 = c.percentile(c.tpotHistory, 95)
	result.SpecAvg = c.avg(c.specHistory)

	// Write metrics
	c.writeMetrics(&result, time.Now())

	return result, nil
}

func (c *LLMMonitor) percentile(data []float64, p float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sorted := make([]float64, len(data))
	copy(sorted, data)
	sort.Float64s(sorted)
	idx := float64(len(sorted)-1) * p / 100.0
	low := int(math.Floor(idx))
	high := int(math.Ceil(idx))
	if low == high {
		return sorted[low]
	}
	frac := idx - float64(low)
	return sorted[low]*(1-frac) + sorted[high]*frac
}

func (c *LLMMonitor) avg(data []float64) float64 {
	if len(data) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range data {
		sum += v
	}
	return sum / float64(len(data))
}

func (c *LLMMonitor) writeMetrics(m *shared.LLMMetrics, ts time.Time) {
	c.write("llm_prompt_ms_per_token", m.PromptMsPerToken, nil, ts)
	c.write("llm_tpot_ms", m.TPOT, nil, ts)
	// llama.cpp metrics do not expose request-level TTFT. Do not publish
	// zero-valued TTFT percentiles as if they were measured data.
	c.write("llm_ttft_available", 0, nil, ts)
	c.write("llm_tpot_p50", m.TPOTP50, nil, ts)
	c.write("llm_tpot_p95", m.TPOTP95, nil, ts)
	c.write("llm_spec_accept_rate", m.SpecAcceptRate, nil, ts)
	c.write("llm_spec_avg_len", float64(m.SpecDraftLen), nil, ts)
	c.write("llm_kv_cache_used_pct", m.KVCacheUsedPct, nil, ts)
	c.write("llm_kv_cache_used_tokens", float64(m.KVCacheUsedTokens), nil, ts)
	c.write("llm_prompt_tokens_total", float64(m.PromptTokensTotal), nil, ts)
	c.write("llm_eval_tokens_total", float64(m.EvalTokensTotal), nil, ts)
}

func (c *LLMMonitor) write(name string, value float64, labels map[string]string, ts time.Time) {
	if c.base != nil && c.base.store != nil {
		c.base.store.Write(name, value, labels, ts)
	}
}
