package shared

// ===== GPU Metrics =====
type GPUMetrics struct {
	Index         int          `json:"index"`
	Name          string       `json:"name"`
	Util          float64      `json:"util"`
	MemUsed       float64      `json:"mem_used"`
	MemTotal      float64      `json:"mem_total"`
	MemFree       float64      `json:"mem_free"`
	MemUtilPct    float64      `json:"mem_util_pct"`
	MemUtil       float64      `json:"mem_util"`
	EncUtil       float64      `json:"enc_util"`
	DecUtil       float64      `json:"dec_util"`
	Temp          float64      `json:"temp"`
	PowerDraw     float64      `json:"power_draw"`
	PowerLimit    float64      `json:"power_limit"`
	PowerPct      float64      `json:"power_pct"`
	FanSpeed      *float64     `json:"fan_speed"`
	Clock         float64      `json:"clock"`
	ClockMax      float64      `json:"clock_max"`
	Driver        string       `json:"driver"`
	PCIE          PCIEInfo     `json:"pcie"`
	Processes     []GPUProcess `json:"processes"`
	Arch          string       `json:"arch"`
	CUDACores     int          `json:"cuda_cores"`
	CoreType      string       `json:"core_type"`
	BusWidth      string       `json:"bus_width"`
	MemType       string       `json:"mem_type"`
	NVLinkBW      string       `json:"nvlink_bw,omitempty"`
	ECC_Errors    int          `json:"ecc_errors,omitempty"`
	Vendor        string       `json:"vendor"`
	VendorDisplay string       `json:"vendor_display"`
	VendorColor   string       `json:"vendor_color"`
	EncoderName   string       `json:"encoder_name"`
	DecoderName   string       `json:"decoder_name"`
	PCIBusID      string       `json:"pci_bus_id"`
	TDPMin        float64      `json:"tdp_min"`
	TDPMax        float64      `json:"tdp_max"`
}

type PCIEInfo struct {
	Bus          string `json:"bus"`
	CurrentGen   string `json:"current_gen"`
	CurrentWidth string `json:"current_width"`
	Gen          string `json:"gen"`
	Width        string `json:"width"`
	Bandwidth    string `json:"bandwidth,omitempty"`
	MaxBandwidth string `json:"max_bandwidth,omitempty"`
}

type GPUProcess struct {
	PID  string `json:"pid"`
	Name string `json:"name"`
	Mem  int    `json:"mem"`
}

type GPUAggregate struct {
	GPUs      []GPUMetrics `json:"gpus"`
	Aggregate *GPUMetrics  `json:"aggregate"`
	History   GPUHistory   `json:"history,omitempty"`
}

type GPUHistory struct {
	Util   []float64 `json:"util"`
	MemPct []float64 `json:"mem_pct"`
	Temp   []float64 `json:"temp"`
	Power  []float64 `json:"power"`
	Clock  []float64 `json:"clock"`
}

// ===== System Metrics =====
type SystemMetrics struct {
	CPUUtil        float64       `json:"cpu_util"`
	CPUPerCore     []float64     `json:"cpu_per_core"`
	CPUModel       string        `json:"cpu_model"`
	CPUPhysical    int           `json:"cpu_physical_cores"`
	CPULogical     int           `json:"cpu_logical_cores"`
	CPUFreqMax     float64       `json:"cpu_max_mhz"`
	CPUFreqCur     float64       `json:"cpu_freq_current"`
	CPUVirt        string        `json:"cpu_virt"`
	CPUL2          string        `json:"cpu_l2"`
	CPUL3          string        `json:"cpu_l3"`
	CPUTemp        float64       `json:"cpu_temp_tctl"`
	MemTotal       uint64        `json:"mem_total"`
	MemUsed        uint64        `json:"mem_used"`
	MemFree        uint64        `json:"mem_free"`
	MemUsedPct     float64       `json:"mem_used_pct"`
	MemAvailable   uint64        `json:"mem_available"`
	MemBuffers     uint64        `json:"mem_buffers"`
	MemCached      uint64        `json:"mem_cached"`
	SwapTotal      uint64        `json:"swap_total"`
	SwapUsed       uint64        `json:"swap_used"`
	SwapFree       uint64        `json:"swap_free"`
	SwapUsedPct    float64       `json:"swap_used_pct"`
	Disks          []DiskMetrics `json:"disks"`
	DiskModel      string        `json:"disk_model"`
	DiskType       string        `json:"disk_type"`
	DiskSize       uint64        `json:"disk_size"`
	NvmeTemp       *float64      `json:"nvme_temp"`
	IOCounters     []DiskIO      `json:"io_counters"`
	DiskReadBps    float64       `json:"disk_read_bps"`
	DiskWriteBps   float64       `json:"disk_write_bps"`
	DiskActivePct  float64       `json:"disk_active_pct"`
	NetAdapter     string        `json:"net_adapter"`
	NetIPv4        string        `json:"net_ipv4"`
	NetLinkSpeed   string        `json:"net_link_speed"`
	NetVendor      string        `json:"net_vendor"`
	NetBytesSent   uint64        `json:"net_bytes_sent"`
	NetBytesRecv   uint64        `json:"net_bytes_recv"`
	NetSentBps     float64       `json:"net_sent_bps"`
	NetRecvBps     float64       `json:"net_recv_bps"`
	NetPacketsSent uint64        `json:"net_packets_sent"`
	NetPacketsRecv uint64        `json:"net_packets_recv"`
	CollectedAt    int64         `json:"collected_at"`
	ProcessCount   int           `json:"process_count"`
	Load1          float64       `json:"load_1"`
	Load5          float64       `json:"load_5"`
	Load15         float64       `json:"load_15"`
}

// SourceFreshness describes the last successful collector snapshot. API and
// SSE consumers use it to distinguish a valid zero from missing or stale data.
type SourceFreshness struct {
	CollectedAtUnixMs int64   `json:"collected_at_ms"`
	AgeMs             int64   `json:"age_ms"`
	DurationMs        float64 `json:"duration_ms"`
	Sequence          uint64  `json:"sequence"`
	Status            string  `json:"status"`
	LastError         string  `json:"last_error,omitempty"`
}

type DiskMetrics struct {
	Device     string  `json:"device"`
	Mountpoint string  `json:"mountpoint"`
	Fstype     string  `json:"fstype"`
	Total      uint64  `json:"total"`
	Used       uint64  `json:"used"`
	Free       uint64  `json:"free"`
	UsedPct    float64 `json:"used_pct"`
}

type DiskIO struct {
	Device     string `json:"device"`
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
	ReadCount  uint64 `json:"read_count"`
	WriteCount uint64 `json:"write_count"`
	IoTime     uint64 `json:"io_time"`
	WeightedIO uint64 `json:"weighted_io"`
}

type LoadAvg struct {
	Load1  float64 `json:"load_1"`
	Load5  float64 `json:"load_5"`
	Load15 float64 `json:"load_15"`
}

// ===== Inference Metrics =====
type InferenceMetrics struct {
	ActiveSlots       int        `json:"active_slots"`
	TotalSlots        int        `json:"total_slots"`
	Slots             []SlotInfo `json:"slots"`
	LastTPS           float64    `json:"last_tps"`
	LastLatencyMs     float64    `json:"last_latency_ms"`
	LastPromptTokens  int        `json:"last_prompt_tokens"`
	LastEvalTokens    int        `json:"last_eval_tokens"`
	LastPromptMs      float64    `json:"last_prompt_ms"`
	LastEvalMs        float64    `json:"last_eval_ms"`
	RequestsPerMin    float64    `json:"requests_per_min"`
	QueueDepth        int        `json:"queue_depth"`
	KVCacheUsedPct    float64    `json:"kv_cache_used_pct"`
	KVCacheUsedTokens int        `json:"kv_cache_used_tokens"`
	KVCacheUsedCells  int        `json:"kv_cache_used_cells"`
}

type SlotInfo struct {
	NDecoded     int  `json:"n_decoded"`
	NRemain      int  `json:"n_remain"`
	NCtx         int  `json:"n_ctx"`
	IsProcessing bool `json:"is_processing,omitempty"`
}

// ===== LLM Observability Metrics =====
type LLMMetrics struct {
	TTFT              float64 `json:"ttft_ms,omitempty"`
	PromptMsPerToken  float64 `json:"prompt_ms_per_token,omitempty"`
	TPOT              float64 `json:"tpot_ms"`
	TTFTP50           float64 `json:"ttft_p50"`
	TTFTP95           float64 `json:"ttft_p95"`
	TTFTP99           float64 `json:"ttft_p99"`
	TPOTP50           float64 `json:"tpot_p50"`
	TPOTP95           float64 `json:"tpot_p95"`
	KVHitRate         float64 `json:"kv_hit_rate"`
	SpecAcceptRate    float64 `json:"spec_accept_rate"`
	SpecAvg           float64 `json:"spec_avg"`
	SpecDraftLen      int     `json:"spec_draft_len"`
	SpecAcceptedCount int     `json:"spec_accepted_count"`
	TokensPerSec      float64 `json:"tokens_per_sec"`
	PromptTokensTotal int     `json:"prompt_tokens_total"`
	EvalTokensTotal   int     `json:"eval_tokens_total"`
	PromptTokensAvg   int     `json:"prompt_tokens_avg"`
	PromptTokensPS    float64 `json:"prompt_tokens_per_sec"`
	SpecSpeedup       float64 `json:"spec_speedup_ratio"`
	SpecDraftLenAvg   float64 `json:"spec_draft_len_avg"`
	ModelCtxUsedPct   float64 `json:"model_ctx_used_pct"`
	KVCacheUsedPct    float64 `json:"kv_cache_used_pct"`
	KVCacheUsedTokens int     `json:"kv_cache_used_tokens"`
}

// ===== KV Cache Metrics =====
type KVSummary struct {
	KVTotalMB             float64 `json:"kv_total_mb"`
	KVUsedMB              float64 `json:"kv_used_mb"`
	KVFreeMB              float64 `json:"kv_free_mb"`
	Pct                   float64 `json:"pct"`
	WorstLevel            string  `json:"worst_level"`
	KVTheoreticalMB       float64 `json:"kv_theoretical_mb"`
	PhysFreeMB            float64 `json:"phys_free_mb"`
	SystemOverheadMB      float64 `json:"system_overhead_mb"`
	AvailableMB           float64 `json:"available_mb"`
	ModelWeightMB         float64 `json:"model_weight_mb"`
	KVTargetMB            float64 `json:"kv_target_mb"`
	KVDraftMB             float64 `json:"kv_draft_mb"`
	KVTokens              int     `json:"kv_tokens"`
	KVTotalTokens         int     `json:"kv_total_tokens"`
	TokensPct             float64 `json:"tokens_pct"`
	FormulaOK             bool    `json:"formula_ok"`
	CacheType             string  `json:"cache_type"`
	CacheTypeK            string  `json:"cache_type_k"`
	CacheTypeV            string  `json:"cache_type_v"`
	DraftCacheTypeK       string  `json:"draft_cache_type_k"`
	DraftCacheTypeV       string  `json:"draft_cache_type_v"`
	CtxSizeUsed           int     `json:"ctx_size_used"`
	CtxSizeTotal          int     `json:"ctx_size_total"`
	Model                 string  `json:"model"`
	KVPerTokenBytes       float64 `json:"kv_per_token_bytes"`
	TargetKVPerTokenBytes float64 `json:"target_kv_per_token_bytes"`
	DraftKVPerTokenBytes  float64 `json:"draft_kv_per_token_bytes"`
	Source                string  `json:"source"`
	Confidence            string  `json:"confidence"`
	VerifyDeltaMB         float64 `json:"verify_delta_mb"`
	SlotsObserved         int     `json:"slots_observed"`
}

type KVCard struct {
	GPUIndex       int     `json:"gpu_index"`
	TotalMB        float64 `json:"total_mb"`
	UsedMB         float64 `json:"used_mb"`
	FreeMB         float64 `json:"free_mb"`
	ModelMB        float64 `json:"model_mb"`
	KVMB           float64 `json:"kv_mb"`
	SystemMB       float64 `json:"system_mb"`
	PctUsed        float64 `json:"pct_used"`
	Name           string  `json:"name"`
	KVTotalMB      float64 `json:"kv_total_mb"`
	KVUsedMB       float64 `json:"kv_used_mb"`
	KVFreeMB       float64 `json:"kv_free_mb"`
	Pct            float64 `json:"pct"`
	Level          string  `json:"level"`
	MemFreeMB      float64 `json:"mem_free_mb"`
	TensorSplitPct float64 `json:"tensor_split_pct"`
	Source         string  `json:"source"`
	Confidence     string  `json:"confidence"`
	PhysKVUsedMB   float64 `json:"phys_kv_used_mb"`
	VerifyDeltaMB  float64 `json:"verify_delta_mb"`
}

type KVMetrics struct {
	Summary  KVSummary `json:"summary"`
	Cards    []KVCard  `json:"cards"`
	Captured bool      `json:"captured"`
}

type KVLayer struct {
	Layer      int    `json:"layer"`
	UsedBytes  uint64 `json:"used_bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

// ===== History Metrics =====
type HistoryMetrics struct {
	GPUUtil    []float64 `json:"gpu_util"`
	GPUMemPct  []float64 `json:"gpu_mem_pct"`
	GPUTemp    []float64 `json:"gpu_temp"`
	GPUPower   []float64 `json:"gpu_power"`
	CPUUsage   []float64 `json:"cpu_usage"`
	CPUFreq    []float64 `json:"cpu_freq"`
	MemUsage   []float64 `json:"mem_usage"`
	MemUsedGB  []float64 `json:"mem_used_gb"`
	NetRecv    []float64 `json:"net_recv"`
	NetSent    []float64 `json:"net_sent"`
	DiskActive []float64 `json:"disk_active"`
	DiskRead   []float64 `json:"disk_read"`
	DiskWrite  []float64 `json:"disk_write"`
}

// ===== Health Score =====
type HealthScore struct {
	Score   int            `json:"score"`
	Reasons []HealthReason `json:"reasons"`
}

type HealthReason struct {
	Item    string `json:"item"`
	Value   string `json:"value"`
	Penalty int    `json:"penalty"`
	Level   string `json:"level"`
}

// ===== SSE Tick =====
type SSETick struct {
	Type           string                     `json:"type"`
	GPUs           *GPUAggregate              `json:"gpus,omitempty"`
	System         *SystemMetrics             `json:"system,omitempty"`
	Inference      *InferenceMetrics          `json:"inference_stats,omitempty"`
	KVCache        *KVMetrics                 `json:"kv_cache,omitempty"`
	LLM            *LLMMetrics                `json:"llm_metrics,omitempty"`
	Logs           []LogEntry                 `json:"logs,omitempty"`
	RequestSources interface{}                `json:"request_sources,omitempty"`
	HealthScore    int                        `json:"health_score"`
	HealthReasons  []HealthReason             `json:"health_reasons,omitempty"`
	Uptime         string                     `json:"uptime"`
	Timestamp      int64                      `json:"ts"`
	Freshness      map[string]SourceFreshness `json:"freshness,omitempty"`
}

type LogEntry struct {
	Timestamp    int64   `json:"timestamp"`
	Time         string  `json:"time"`
	Type         string  `json:"type"`
	Path         string  `json:"path"`
	Status       string  `json:"status"`
	TimeMs       string  `json:"time_ms"`
	TPS          float64 `json:"tps"`
	Tokens       int     `json:"tokens"`
	SourceIP     string  `json:"source_ip"`
	Detail       string  `json:"detail"`
	PromptMs     float64 `json:"prompt_ms"`
	PromptTokens int     `json:"prompt_tokens"`
	EvalTokens   int     `json:"eval_tokens"`
}

// ===== VMem/Swap/Disk/Net/Load types =====
type VMemInfo struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	Available   uint64  `json:"available"`
	UsedPercent float64 `json:"used_percent"`
	Buffers     uint64  `json:"buffers"`
	Cached      uint64  `json:"cached"`
}

type SwapInfo struct {
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"used_percent"`
}

type PartitionInfo struct {
	Device     string `json:"device"`
	Fstype     string `json:"fstype"`
	Mountpoint string `json:"mountpoint"`
}

type DiskUsageInfo struct {
	Path        string  `json:"path"`
	Fstype      string  `json:"fstype"`
	Total       uint64  `json:"total"`
	Used        uint64  `json:"used"`
	Free        uint64  `json:"free"`
	UsedPercent float64 `json:"used_percent"`
}

type NetIOInfo struct {
	BytesRecv uint64 `json:"bytes_recv"`
	BytesSent uint64 `json:"bytes_sent"`
	Interface string `json:"interface,omitempty"`
}
