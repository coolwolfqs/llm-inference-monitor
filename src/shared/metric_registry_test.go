package shared

import "testing"

func TestMetricRegistryDeclaresAccuracyCriticalFields(t *testing.T) {
	payload := MetricDefinitions()
	items, ok := payload["definitions"].([]MetricDefinition)
	if !ok || len(items) < 20 {
		t.Fatalf("definitions missing or too small: %T %d", payload["definitions"], len(items))
	}
	byID := make(map[string]MetricDefinition)
	for _, item := range items {
		byID[item.ID] = item
	}
	for _, id := range []string{"system.net_recv_bps", "system.disk_read_bps", "gpus.items[].mem_free", "gpus.aggregate.mem_util_pct", "inference.kv_cache.phys_free_mb"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing definition %s", id)
		}
	}
	if byID["gpus.aggregate.mem_util_pct"].Aggregation != "capacity-weighted" {
		t.Fatal("GPU memory aggregation contract drift")
	}
	if byID["inference.llm.ttft_ms"].Visibility != "hidden" {
		t.Fatal("unsupported TTFT must remain hidden")
	}
}
