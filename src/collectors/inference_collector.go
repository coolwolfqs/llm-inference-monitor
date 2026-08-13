package collectors

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"inference-hub-v3/src/shared"
)

type InferenceCollector struct {
	base    *BaseCollector
	http    *shared.HTTPClient
	cfg     *shared.Config
	logPath string
}

func NewInferenceCollector(base *BaseCollector, httpClient *shared.HTTPClient, cfg *shared.Config) *InferenceCollector {
	logPath := "/tmp/llama-server.log"
	if cfg.Collectors.Inference.LogPath != "" {
		logPath = cfg.Collectors.Inference.LogPath
	}
	return &InferenceCollector{
		base:    base,
		http:    httpClient,
		cfg:     cfg,
		logPath: logPath,
	}
}

func (c *InferenceCollector) Collect(ctx context.Context) (interface{}, error) {
	result := shared.InferenceMetrics{}
	baseURL := c.cfg.Services.LlamaServer.URL

	// Slots
	slotsURL := baseURL + c.cfg.Services.LlamaServer.SlotsPath
	var slotsData []map[string]interface{}
	if err := c.http.GetJSONContext(ctx, slotsURL, &slotsData); err == nil {
		result.TotalSlots = len(slotsData)
		for _, s := range slotsData {
			slot := shared.SlotInfo{}
			if proc, ok := s["is_processing"].(bool); ok && proc {
				result.ActiveSlots++
				slot.IsProcessing = true
			}
			if nextTok, ok := s["next_token"].([]interface{}); ok && len(nextTok) > 0 {
				if tok, ok := nextTok[0].(map[string]interface{}); ok {
					if nd, ok := tok["n_decoded"].(float64); ok {
						slot.NDecoded = int(nd)
					}
					if nr, ok := tok["n_remain"].(float64); ok {
						slot.NRemain = int(nr)
					}
				}
			} else if nd, ok := s["n_decoded"].(float64); ok {
				slot.NDecoded = int(nd)
			}
			if nc, ok := s["n_ctx"].(float64); ok {
				slot.NCtx = int(nc)
			}
			result.Slots = append(result.Slots, slot)
		}
	}

	// Stats
	statsURL := baseURL + c.cfg.Services.LlamaServer.StatsPath
	var statsData map[string]interface{}
	if err := c.http.GetJSONContext(ctx, statsURL, &statsData); err == nil {
		if v, ok := statsData["tokens_predicted_per_second"].(float64); ok {
			result.LastTPS = v
		}
		if v, ok := statsData["slots_avg_processing_ms"].(float64); ok {
			result.LastLatencyMs = v
		}
		if v, ok := statsData["tokens_prompted_total"].(float64); ok {
			result.LastPromptTokens = int(v)
		}
		if v, ok := statsData["tokens_predicted_total"].(float64); ok {
			result.LastEvalTokens = int(v)
		}
		if v, ok := statsData["kv_cache_usage_ratio"].(float64); ok {
			result.KVCacheUsedPct = v * 100
		}
		if v, ok := statsData["kv_cache_tokens_count"].(float64); ok {
			result.KVCacheUsedTokens = int(v)
		}
		if v, ok := statsData["kv_cache_cells_count"].(float64); ok {
			result.KVCacheUsedCells = int(v)
		}
	} else {
		c.parsePrometheusMetrics(ctx, baseURL, &result)
	}

	// Write metrics
	c.writeMetrics(&result, time.Now())

	return result, nil
}

func (c *InferenceCollector) parsePrometheusMetrics(ctx context.Context, baseURL string, result *shared.InferenceMetrics) {
	metricsURL := baseURL + c.cfg.Services.LlamaServer.MetricsPath
	resp, err := c.http.GetContext(ctx, metricsURL)
	if err != nil {
		return
	}
	defer resp.Body.Close()

	buf := make([]byte, 65536)
	n, _ := resp.Body.Read(buf)
	text := string(buf[:n])

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

	result.LastPromptTokens = int(metrics["llamacpp:prompt_tokens_total"])
	result.LastEvalTokens = int(metrics["llamacpp:tokens_predicted_total"])
	result.LastTPS = metrics["llamacpp:predicted_tokens_seconds"]
	if result.LastTPS > 0 {
		result.LastLatencyMs = 1000.0 / result.LastTPS
	}
	result.KVCacheUsedPct = metrics["llamacpp:kv_cache_usage_ratio"] * 100
}

func (c *InferenceCollector) writeMetrics(m *shared.InferenceMetrics, ts time.Time) {
	c.write("inference_tps", m.LastTPS, nil, ts)
	c.write("inference_latency_ms", m.LastLatencyMs, nil, ts)
	c.write("inference_prompt_tokens_total", float64(m.LastPromptTokens), nil, ts)
	c.write("inference_eval_tokens_total", float64(m.LastEvalTokens), nil, ts)
	c.write("inference_active_slots", float64(m.ActiveSlots), nil, ts)
	c.write("inference_total_slots", float64(m.TotalSlots), nil, ts)
	c.write("inference_kv_cache_used_pct", m.KVCacheUsedPct, nil, ts)
	c.write("inference_kv_cache_used_tokens", float64(m.KVCacheUsedTokens), nil, ts)
}

func (c *InferenceCollector) write(name string, value float64, labels map[string]string, ts time.Time) {
	if c.base != nil && c.base.store != nil {
		c.base.store.Write(name, value, labels, ts)
	}
}

func (c *InferenceCollector) parseLogs(maxEntries int) []shared.LogEntry {
	var logs []shared.LogEntry
	content, err := os.ReadFile(c.logPath)
	if err != nil {
		return logs
	}

	lines := strings.Split(string(content), "\n")
	timingRe := regexp.MustCompile(`slot\s+print_timing:.*?task\s+(\d+)`)
	elapsedRe := regexp.MustCompile(`(\d+)\.(\d{2})\.(\d{2,3})\.(\d{3})`)
	timeRe := regexp.MustCompile(`=\s+([\d.]+)\s+ms\s+/\s+(\d+)\s+tokens.*?([\d.]+)\s+tokens per second`)

	var entries []map[string]interface{}
	i := 0
	for i < len(lines) {
		line := lines[i]
		if timingRe.MatchString(line) {
			if strings.Contains(line, "progress =") || strings.Contains(line, "n_decoded =") {
				i++
				continue
			}
			entry := make(map[string]interface{})
			m := timingRe.FindStringSubmatch(line)
			if len(m) > 1 {
				entry["task_id"] = m[1]
			}
			if em := elapsedRe.FindStringSubmatch(line); len(em) > 1 {
				days, _ := strconv.Atoi(em[1])
				hours, _ := strconv.Atoi(em[2])
				mins, _ := strconv.Atoi(em[3])
				secs, _ := strconv.Atoi(em[4][:3])
				entry["elapsed"] = days*86400 + hours*3600 + mins*60 + secs
			}
			if strings.Contains(line, "prompt eval time") {
				if tm := timeRe.FindStringSubmatch(line); len(tm) > 1 {
					val, _ := strconv.ParseFloat(tm[1], 64)
					entry["prompt_ms"] = val
					tokens, _ := strconv.Atoi(tm[2])
					entry["prompt_tokens"] = tokens
					tps, _ := strconv.ParseFloat(tm[3], 64)
					entry["prompt_tps"] = tps
				}
			} else if strings.Contains(line, "eval time") && !strings.Contains(line, "prompt") {
				if tm := timeRe.FindStringSubmatch(line); len(tm) > 1 {
					val, _ := strconv.ParseFloat(tm[1], 64)
					entry["eval_ms"] = val
					tokens, _ := strconv.Atoi(tm[2])
					entry["eval_tokens"] = tokens
					tps, _ := strconv.ParseFloat(tm[3], 64)
					entry["eval_tps"] = tps
				}
			}
			hasEval := false
			if _, ok := entry["eval_tps"]; ok {
				hasEval = true
			}
			if _, ok := entry["eval_ms"]; ok {
				hasEval = true
			}
			if hasEval {
				entries = append(entries, entry)
			}
		}
		i++
	}

	for _, e := range entries {
		le := shared.LogEntry{}
		if v, ok := e["task_id"].(string); ok {
			le.Time = v
		}
		if v, ok := e["total_ms"].(float64); ok {
			le.TimeMs = strconv.FormatFloat(v, 'f', 0, 64)
		} else if v, ok := e["eval_ms"].(float64); ok {
			le.TimeMs = strconv.FormatFloat(v, 'f', 0, 64)
		}
		if v, ok := e["eval_tps"].(float64); ok {
			le.TPS = v
		}
		if v, ok := e["total_tokens"].(int); ok {
			le.Tokens = v
		} else {
			pt, _ := e["prompt_tokens"].(int)
			et, _ := e["eval_tokens"].(int)
			le.Tokens = pt + et
		}
		if v, ok := e["prompt_ms"].(float64); ok {
			le.PromptMs = v
		}
		if v, ok := e["prompt_tokens"].(int); ok {
			le.PromptTokens = v
		}
		if v, ok := e["eval_tokens"].(int); ok {
			le.EvalTokens = v
		}
		logs = append(logs, le)
	}

	return logs
}

func (c *InferenceCollector) GetActiveRequests() []map[string]interface{} {
	baseURL := c.cfg.Services.LlamaServer.URL
	resp, err := c.http.Get(baseURL + "/active-tasks")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var data interface{}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil
	}

	if tasks, ok := data.([]interface{}); ok {
		result := make([]map[string]interface{}, 0, len(tasks))
		for _, t := range tasks {
			if m, ok := t.(map[string]interface{}); ok {
				result = append(result, m)
			}
		}
		return result
	}
	if m, ok := data.(map[string]interface{}); ok {
		if tasks, ok := m["tasks"].([]interface{}); ok {
			result := make([]map[string]interface{}, 0, len(tasks))
			for _, t := range tasks {
				if tm, ok := t.(map[string]interface{}); ok {
					result = append(result, tm)
				}
			}
			return result
		}
	}
	return nil
}
