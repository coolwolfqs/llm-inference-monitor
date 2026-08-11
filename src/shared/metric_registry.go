package shared

// MetricDefinition is the stable contract shared by collectors, APIs and UI.
// ZeroValid prevents callers from treating an idle value as missing.
type MetricDefinition struct {
	ID          string `json:"id"`
	Unit        string `json:"unit"`
	Kind        string `json:"kind"`
	Authority   string `json:"authority"`
	TTLSeconds  int    `json:"ttl_seconds"`
	Aggregation string `json:"aggregation"`
	ZeroValid   bool   `json:"zero_valid"`
	Nullable    bool   `json:"nullable"`
	Visibility  string `json:"visibility"`
}

func MetricDefinitions() map[string]interface{} {
	definitions := []MetricDefinition{
		{"system.cpu_util", "percent", "gauge", "system-collector", 15, "host", true, false, "visible"},
		{"system.cpu_freq_current", "MHz", "gauge", "system-collector", 15, "host", true, false, "visible"},
		{"system.mem_used", "bytes", "gauge", "system-collector", 15, "host", true, false, "visible"},
		{"system.mem_available", "bytes", "gauge", "system-collector", 15, "host", true, false, "visible"},
		{"system.disk_read_bps", "bytes/s", "rate", "system-collector", 15, "physical-devices-sum", true, false, "visible"},
		{"system.disk_write_bps", "bytes/s", "rate", "system-collector", 15, "physical-devices-sum", true, false, "visible"},
		{"system.net_recv_bps", "bytes/s", "rate", "system-collector", 15, "default-route-interface", true, false, "visible"},
		{"system.net_sent_bps", "bytes/s", "rate", "system-collector", 15, "default-route-interface", true, false, "visible"},
		{"system.net_packets_recv", "packets", "counter", "system-collector", 15, "default-route-interface", true, false, "api"},
		{"system.net_packets_sent", "packets", "counter", "system-collector", 15, "default-route-interface", true, false, "api"},
		{"gpus.items[].util", "percent", "gauge", "gpu-driver", 15, "per-device", true, false, "visible"},
		{"gpus.items[].mem_used", "MiB", "gauge", "gpu-driver", 15, "per-device", true, false, "visible"},
		{"gpus.items[].mem_free", "MiB", "gauge", "gpu-driver", 15, "driver-reported-free", true, false, "visible"},
		{"gpus.aggregate.mem_util_pct", "percent", "gauge", "gpu-collector", 15, "capacity-weighted", true, false, "visible"},
		{"gpus.aggregate.temp", "celsius", "gauge", "gpu-collector", 15, "max", true, false, "visible"},
		{"gpus.aggregate.power_draw", "watts", "gauge", "gpu-collector", 15, "sum", true, false, "visible"},
		{"inference.last_tps", "tokens/s", "last-completed", "llama-server", 10, "last-request", true, false, "visible"},
		{"inference.active_slots", "slots", "gauge", "llama-server", 10, "sum", true, false, "visible"},
		{"inference.requests_per_min", "requests/min", "rate", "inference-collector", 10, "rolling-window", true, false, "visible"},
		{"inference.llm.ttft_ms", "ms", "gauge", "unsupported", 0, "none", false, true, "hidden"},
		{"inference.llm.kv_hit_rate", "ratio", "gauge", "unsupported", 0, "none", false, true, "hidden"},
		{"inference.kv_cache.phys_free_mb", "MiB", "gauge", "gpu-driver", 15, "sum-driver-free", true, false, "visible"},
		{"deployment.config", "mixed", "config", "process-argv", 30, "runtime-authority", false, false, "visible"},
	}
	return map[string]interface{}{"schema_version": "1.0", "definitions": definitions}
}
