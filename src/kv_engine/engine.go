package kv_engine

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"inference-hub-v3/src/shared"
)

// Cache type byte sizes (KV cache quantization)
var cacheTypeBytes = map[string]float64{
	"f32": 4.0, "f16": 2.0, "bf16": 2.0,
	"q8_0": 1.0, "q4_0": 0.5, "q4_1": 0.5,
	"q5_0": 0.625, "q5_1": 0.625,
	"iq4_nl": 0.5,
	"turbo2": 0.375, "turbo2_0": 0.375,
	"turbo3": 0.5, "turbo3_0": 0.5,
	"turbo4": 0.625, "turbo4_0": 0.625,
	"iq2_s": 0.3125, "iq3_s": 0.375, "iq4_xs": 0.4375,
}

// GGUFParams holds parsed model parameters
type GGUFParams struct {
	EmbeddingLength    uint64
	HeadCount          uint64
	HeadCountKV        uint64
	BlockCount         uint64
	ContextLength      uint64
	NextNPredictLayers uint64
	Architecture       string
}

// KVCard represents per-GPU KV cache data
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

// KVSummary represents the summary of all KV cache
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

// KVResult is the full KV cache computation result
type KVResult struct {
	Summary  KVSummary `json:"summary"`
	Cards    []KVCard  `json:"cards"`
	Captured bool      `json:"captured"`
}

// KVEngine manages KV cache baseline, computation, and prediction
type KVEngine struct {
	mu              sync.RWMutex
	baseline        map[int]float64
	baselineTS      time.Time
	numGPUs         int
	meta            map[string]interface{}
	httpClient      *shared.HTTPClient
	llamaURL        string
	ggufCache       map[string]*GGUFParams
	ggufCacheMtime  map[string]float64
	lastCapture     time.Time
	captureInterval time.Duration
	autoCapture     bool
	captureCh       chan int
	baselinePath    string
}

func NewKVEngine(httpClient *shared.HTTPClient, llamaURL string) *KVEngine {
	e := &KVEngine{
		baseline:        make(map[int]float64),
		meta:            make(map[string]interface{}),
		httpClient:      httpClient,
		llamaURL:        llamaURL,
		ggufCache:       make(map[string]*GGUFParams),
		ggufCacheMtime:  make(map[string]float64),
		autoCapture:     true,
		captureInterval: 30 * time.Minute,
		captureCh:       make(chan int, 1),
		baselinePath:    "/data/inference-hub-v3/kv_baseline.json",
	}
	if p := strings.TrimSpace(os.Getenv("KV_BASELINE_PATH")); p != "" {
		e.baselinePath = p
	}
	e.loadBaseline()
	go e.captureLoop()
	return e
}

// captureLoop consumes baseline capture requests in the background and
// re-captures on the configured interval.
func (e *KVEngine) captureLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case n := <-e.captureCh:
			e.ForceCapture(n)
		case <-ticker.C:
			e.mu.RLock()
			due := e.autoCapture && (e.baselineTS.IsZero() || time.Since(e.baselineTS) >= e.captureInterval)
			n := e.numGPUs
			e.mu.RUnlock()
			if due {
				if n <= 0 {
					n = 1
				}
				e.ForceCapture(n)
			}
		}
	}
}

// EnsureBaselineAsync requests one background baseline capture when no valid
// baseline exists yet. A second request while one is running is dropped (the
// channel has capacity 1).
func (e *KVEngine) EnsureBaselineAsync(numGPUs int) {
	e.mu.RLock()
	captured := !e.baselineTS.IsZero()
	e.mu.RUnlock()
	if captured {
		return
	}
	select {
	case e.captureCh <- numGPUs:
	default:
	}
}

// computeKVBytesPerToken calculates single-token KV cache bytes
func (e *KVEngine) computeKVBytesPerToken(params GGUFParams, cacheTypeK, cacheTypeV string) (float64, bool) {
	if params.HeadCountKV == 0 || params.HeadCount == 0 || params.EmbeddingLength == 0 || params.BlockCount == 0 {
		return 0, false
	}
	headDim := params.EmbeddingLength / params.HeadCount
	bpeK := cacheTypeBytes[cacheTypeK]
	if bpeK == 0 {
		bpeK = 2.0 // default f16
	}
	bpeV := cacheTypeBytes[cacheTypeV]
	if bpeV == 0 {
		bpeV = bpeK
	}
	bytesPerToken := float64(params.HeadCountKV) * float64(headDim) * (bpeK + bpeV) * float64(params.BlockCount)
	return bytesPerToken, true
}

// readGGUFKVParamsFast quickly parses GGUF file for KV-related parameters
func (e *KVEngine) readGGUFKVParamsFast(ggufPath string) (*GGUFParams, error) {
	mtime := float64(0)
	if info, err := os.Stat(ggufPath); err == nil {
		mtime = float64(info.ModTime().Unix())
	}

	cacheKey := fmt.Sprintf("%s:%.0f", ggufPath, mtime)
	if cached, ok := e.ggufCache[cacheKey]; ok {
		return cached, nil
	}

	f, err := os.Open(ggufPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	result := &GGUFParams{}

	// Read magic
	var magic uint32
	if err := binary.Read(f, binary.LittleEndian, &magic); err != nil {
		return nil, err
	}
	if magic != 0x46554747 { // "GGUF"
		return nil, fmt.Errorf("invalid GGUF magic")
	}

	// Read version
	var version uint32
	if err := binary.Read(f, binary.LittleEndian, &version); err != nil {
		return nil, err
	}

	// Read tensor count and metadata kv count
	var tensorCount, metadataKVCount uint64
	if version >= 3 {
		if err := binary.Read(f, binary.LittleEndian, &tensorCount); err != nil {
			return nil, err
		}
		if err := binary.Read(f, binary.LittleEndian, &metadataKVCount); err != nil {
			return nil, err
		}
	} else {
		var tc32, mk32 uint32
		if err := binary.Read(f, binary.LittleEndian, &tc32); err != nil {
			return nil, err
		}
		tensorCount = uint64(tc32)
		if err := binary.Read(f, binary.LittleEndian, &mk32); err != nil {
			return nil, err
		}
		metadataKVCount = uint64(mk32)
	}

	targetKeys := map[string]bool{
		"llama.embedding_length":     true,
		"llama.head_count":           true,
		"llama.head_count_kv":        true,
		"llama.block_count":          true,
		"llama.context_length":       true,
		"llama.nextn_predict_layers": true,
		"general.architecture":       true,
	}

	arrElSizes := map[int]int{0: 1, 1: 1, 2: 2, 3: 2, 4: 4, 5: 4, 6: 4, 7: 8, 8: 8, 9: 16, 10: 16, 11: 16}

	var arch string

	for i := uint64(0); i < metadataKVCount; i++ {
		var keyLen uint64
		if version >= 3 {
			if err := binary.Read(f, binary.LittleEndian, &keyLen); err != nil {
				return nil, err
			}
		} else {
			var kl32 uint32
			if err := binary.Read(f, binary.LittleEndian, &kl32); err != nil {
				return nil, err
			}
			keyLen = uint64(kl32)
		}

		keyBytes := make([]byte, keyLen)
		if _, err := io.ReadFull(f, keyBytes); err != nil {
			return nil, err
		}
		key := string(keyBytes)

		var valType uint32
		if err := binary.Read(f, binary.LittleEndian, &valType); err != nil {
			return nil, err
		}

		value, skip := e.readGGUFValue(f, valType, version, key, targetKeys, arrElSizes)
		if skip {
			continue
		}

		// Map keys to struct fields
		switch {
		case key == "llama.embedding_length" || strings.HasSuffix(key, ".embedding_length"):
			result.EmbeddingLength = toUint64(value)
		case key == "llama.head_count" || strings.HasSuffix(key, ".attention.head_count"):
			result.HeadCount = toUint64(value)
		case key == "llama.head_count_kv" || strings.HasSuffix(key, ".attention.head_count_kv"):
			result.HeadCountKV = toUint64(value)
		case key == "llama.block_count" || strings.HasSuffix(key, ".block_count"):
			result.BlockCount = toUint64(value)
		case key == "llama.context_length" || strings.HasSuffix(key, ".context_length"):
			result.ContextLength = toUint64(value)
		case key == "llama.nextn_predict_layers" || strings.HasSuffix(key, ".nextn_predict_layers"):
			result.NextNPredictLayers = toUint64(value)
		case key == "general.architecture":
			if v, ok := value.(string); ok {
				arch = v
				result.Architecture = v
			}
		}
		_ = arch
	}

	if result.EmbeddingLength > 0 && result.HeadCount > 0 {
		e.ggufCache[cacheKey] = result
		return result, nil
	}
	return nil, fmt.Errorf("insufficient GGUF parameters")
}

func toUint64(v interface{}) uint64 {
	switch val := v.(type) {
	case uint64:
		return val
	case int64:
		return uint64(val)
	case uint32:
		return uint64(val)
	case int32:
		return uint64(val)
	case float64:
		return uint64(val)
	}
	return 0
}

func (e *KVEngine) readGGUFValue(f *os.File, valType uint32, version uint32, key string, targetKeys map[string]bool, arrElSizes map[int]int) (interface{}, bool) {
	var value interface{}
	skip := false

	switch valType {
	case 0: // UINT8
		var v uint8
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = uint64(v)
		}
	case 1: // INT8
		var v int8
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = int64(v)
		}
	case 2: // UINT16
		var v uint16
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = uint64(v)
		}
	case 3: // INT16
		var v int16
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = int64(v)
		}
	case 4: // UINT32
		var v uint32
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = uint64(v)
		}
	case 5: // INT32
		var v int32
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = int64(v)
		}
	case 6: // FLOAT32
		var v float32
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = float64(v)
		}
	case 7: // BOOL
		var v uint8
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = uint64(v)
		}
	case 8: // STRING
		var slen uint64
		if version >= 3 {
			if err := binary.Read(f, binary.LittleEndian, &slen); err != nil {
				skip = true
			}
		} else {
			var sl32 uint32
			if err := binary.Read(f, binary.LittleEndian, &sl32); err != nil {
				skip = true
			} else {
				slen = uint64(sl32)
			}
		}
		if !skip {
			sb := make([]byte, slen)
			if _, err := io.ReadFull(f, sb); err != nil {
				skip = true
			} else {
				value = string(sb)
			}
		}
	case 9: // ARRAY
		var arrType uint32
		if err := binary.Read(f, binary.LittleEndian, &arrType); err != nil {
			skip = true
		}
		var arrLen uint64
		if version >= 3 {
			if err := binary.Read(f, binary.LittleEndian, &arrLen); err != nil {
				skip = true
			}
		} else {
			var al32 uint32
			if err := binary.Read(f, binary.LittleEndian, &al32); err != nil {
				skip = true
			} else {
				arrLen = uint64(al32)
			}
		}
		if !skip {
			if targetKeys[key] && (arrType == 3 || arrType == 4) {
				if arrLen == 1 {
					if arrType == 3 { // INT16
						var v int16
						if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
							skip = true
						} else {
							value = int64(v)
						}
					} else { // UINT16
						var v uint16
						if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
							skip = true
						} else {
							value = uint64(v)
						}
					}
				} else {
					es := arrElSizes[int(arrType)]
					if es > 0 {
						f.Read(make([]byte, int(arrLen-1)*es))
					}
					value = arrLen
				}
			} else if arrType == 8 { // ARRAY of STRING
				for j := uint64(0); j < arrLen; j++ {
					var slen uint64
					if version >= 3 {
						binary.Read(f, binary.LittleEndian, &slen)
					} else {
						var sl32 uint32
						binary.Read(f, binary.LittleEndian, &sl32)
						slen = uint64(sl32)
					}
					f.Read(make([]byte, slen))
				}
				skip = true
			} else {
				es := arrElSizes[int(arrType)]
				if es > 0 {
					f.Read(make([]byte, int(arrLen)*es))
				}
				skip = true
			}
		}
	case 10: // UINT64
		var v uint64
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = v
		}
	case 11: // INT64
		var v int64
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = v
		}
	case 12: // FLOAT64
		var v float64
		if err := binary.Read(f, binary.LittleEndian, &v); err != nil {
			skip = true
		} else {
			value = v
		}
	default:
		// Skip unknown types
		f.Read(make([]byte, 4))
		skip = true
	}

	return value, skip
}

// getLlamaIdentity gets llama-server process identity
func (e *KVEngine) getLlamaIdentity() (pid string, modelPath string, configSig string) {
	cmd := exec.Command("sh", "-c", "pgrep -f 'llama-server' 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return "", "", ""
	}
	// Try each PID until we find one with a valid cmdline containing llama-server
	for _, pidLine := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		pid = strings.TrimSpace(pidLine)
		if pid == "" {
			continue
		}
		cmdlineData, err := os.ReadFile("/proc/" + pid + "/cmdline")
		if err != nil {
			continue // Skip PIDs that don't exist or can't be read
		}
		exePath, err := os.Readlink("/proc/" + pid + "/exe")
		if err != nil || filepath.Base(exePath) != "llama-server" {
			continue // Reject shells, log readers and probes that only mention the name.
		}
		cmdline := strings.ReplaceAll(string(cmdlineData), "\x00", " ")
		// Found a valid llama-server process
		// Extract model path
		modelRe := regexp.MustCompile(`-m\s+(\S+)`)
		if m := modelRe.FindStringSubmatch(cmdline); len(m) > 1 {
			modelPath = m[1]
		}

		// Build config signature
		var parts []string
		for _, flag := range []string{"--ctx-size", "-ngl", "--tensor-split", "--cache-type-k", "--cache-type-v", "--cache-type-k-draft", "--cache-type-v-draft", "-np", "--spec-type", "--spec-draft-n-max"} {
			re := regexp.MustCompile(regexp.QuoteMeta(flag) + `[=\s]+(\S+)`)
			if m := re.FindStringSubmatch(cmdline); len(m) > 1 {
				parts = append(parts, flag+"="+m[1])
			}
		}
		configSig = strings.Join(parts, "|")

		return pid, modelPath, configSig
	}
	return "", "", ""
}

// isLlamaIdle checks if llama-server has no processing slots
func (e *KVEngine) isLlamaIdle() bool {
	var slots []map[string]interface{}
	if err := e.httpClient.GetJSON(e.llamaURL+"/slots", &slots); err != nil {
		return false
	}
	for _, s := range slots {
		if proc, ok := s["is_processing"].(bool); ok && proc {
			return false
		}
	}
	return true
}

// getGPUMem gets GPU memory info via nvidia-smi
func (e *KVEngine) getGPUMem(gpuIdx int, field string) float64 {
	cmd := exec.Command("sh", "-c",
		fmt.Sprintf("nvidia-smi -i %d --query-gpu=memory.%s --format=csv,noheader,nounits", gpuIdx, field))
	out, err := cmd.Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return v
}

// CaptureBaseline captures VRAM baseline when llama-server is idle
func (e *KVEngine) CaptureBaseline(numGPUs int) bool {
	e.mu.Lock()
	if !e.baselineTS.IsZero() {
		allValid := true
		for i := 0; i < numGPUs; i++ {
			if e.baseline[i] == 0 {
				allValid = false
				break
			}
		}
		if allValid {
			e.mu.Unlock()
			return true
		}
	}
	e.mu.Unlock()

	if !e.isLlamaIdle() {
		return false
	}

	stableThreshold := 100.0
	stableCount := 0
	prevFree := make(map[int]float64)
	var lastCurrentFree map[int]float64

	for attempt := 0; attempt < 20; attempt++ {
		currentFree := make(map[int]float64)
		allOK := true
		for i := 0; i < numGPUs; i++ {
			mt := e.getGPUMem(i, "total")
			mu := e.getGPUMem(i, "used")
			mf := mt - mu
			if mf <= 0 {
				mf = e.getGPUMem(i, "free")
			}
			if mf > 0 {
				currentFree[i] = mf
			} else {
				allOK = false
				break
			}
		}
		if !allOK {
			time.Sleep(2 * time.Second)
			continue
		}

		if len(prevFree) > 0 {
			stable := true
			for i := 0; i < numGPUs; i++ {
				diff := math.Abs(currentFree[i] - prevFree[i])
				if diff >= stableThreshold {
					stable = false
					break
				}
			}
			if stable {
				stableCount++
				if stableCount >= 3 {
					e.mu.Lock()
					for i := 0; i < numGPUs; i++ {
						e.baseline[i] = currentFree[i]
					}
					e.baselineTS = time.Now()
					e.mu.Unlock()
					shared.Infof("[KV Baseline] Captured: %+v", currentFree)
					if err := e.SaveBaseline(); err != nil {
						shared.Errorf("[KV Baseline] persist failed: %v", err)
					}
					return true
				}
			} else {
				stableCount = 0
			}
		}

		for k, v := range currentFree {
			prevFree[k] = v
		}
		lastCurrentFree = currentFree
		time.Sleep(2 * time.Second)
	}

	// Timeout but still capture what we have
	if len(lastCurrentFree) > 0 {
		e.mu.Lock()
		for i := 0; i < numGPUs; i++ {
			e.baseline[i] = lastCurrentFree[i]
		}
		e.baselineTS = time.Now()
		e.mu.Unlock()
		shared.Infof("[KV Baseline] Timeout but captured")
	if err := e.SaveBaseline(); err != nil {
		shared.Errorf("[KV Baseline] persist failed: %v", err)
	}
	return true
	}
	return false
}

// ForceCapture forces a baseline recapture

// loadBaseline restores a previously persisted baseline so a gateway restart
// does not throw away a valid capture.
func (e *KVEngine) loadBaseline() {
	if e.baselinePath == "" {
		return
	}
	data, err := os.ReadFile(e.baselinePath)
	if err != nil {
		return
	}
	var payload struct {
		Baseline   map[int]float64 `json:"baseline"`
		CapturedAt time.Time       `json:"captured_at"`
	}
	if err := json.Unmarshal(data, &payload); err != nil || len(payload.Baseline) == 0 {
		return
	}
	e.mu.Lock()
	e.baseline = payload.Baseline
	e.baselineTS = payload.CapturedAt
	e.mu.Unlock()
	shared.Infof("[KV Baseline] loaded persisted baseline from %s (%d GPU(s))",
		e.baselinePath, len(e.baseline))
}

// SaveBaseline persists the current baseline so it survives gateway restarts.
func (e *KVEngine) SaveBaseline() error {
	if e.baselinePath == "" {
		return nil
	}
	e.mu.RLock()
	payload := struct {
		Baseline   map[int]float64 `json:"baseline"`
		CapturedAt time.Time       `json:"captured_at"`
	}{e.baseline, e.baselineTS}
	e.mu.RUnlock()
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(e.baselinePath, data, 0644)
}

func (e *KVEngine) ForceCapture(numGPUs int) bool {
	e.mu.Lock()
	e.lastCapture = time.Time{}
	e.mu.Unlock()
	return e.CaptureBaseline(numGPUs)
}

// CheckIdentityAndReset checks if llama-server identity changed
func (e *KVEngine) CheckIdentityAndReset(numGPUs int) bool {
	pid, modelPath, configSig := e.getLlamaIdentity()
	if pid == "" {
		return false
	}

	e.mu.RLock()
	metaPID := e.meta["pid"]
	metaModel := e.meta["model"]
	metaConfig := e.meta["config_sig"]
	hasBaseline := !e.baselineTS.IsZero()
	e.mu.RUnlock()

	identityChanged := false
	if hasBaseline && metaPID != nil {
		if metaPID.(string) != pid || metaModel.(string) != modelPath || metaConfig.(string) != configSig {
			identityChanged = true
		}
	}

	if identityChanged {
		e.mu.Lock()
		for i := 0; i < numGPUs; i++ {
			mt := e.getGPUMem(i, "total")
			mu := e.getGPUMem(i, "used")
			mf := mt - mu
			if mf <= 0 {
				mf = e.getGPUMem(i, "free")
			}
			if mf > 0 {
				e.baseline[i] = mf
			} else {
				e.baseline[i] = 0
			}
		}
		e.baselineTS = time.Now()
		e.meta = map[string]interface{}{
			"pid":        pid,
			"model":      modelPath,
			"config_sig": configSig,
		}
		e.mu.Unlock()
		shared.Infof("[KV Baseline] Identity changed, baseline reset")
		// Async recapture
		go func() {
			time.Sleep(5 * time.Second)
			e.CaptureBaseline(numGPUs)
		}()
	} else if !hasBaseline {
		// Check if llama is using GPU
		e.mu.RLock()
		needCapture := false
		for i := 0; i < numGPUs; i++ {
			if e.baseline[i] == 0 && e.getGPUMem(i, "used") > 500 {
				needCapture = true
				break
			}
		}
		e.mu.RUnlock()
		if needCapture {
			go e.CaptureBaseline(numGPUs)
		}
	}

	return identityChanged
}

// getTensorSplit parses llama-server cmdline for tensor-split ratios
func (e *KVEngine) getTensorSplit(numGPUs int) []float64 {
	split := make([]float64, numGPUs)
	if numGPUs == 0 {
		return split
	}
	// Default equal split
	for i := range split {
		split[i] = 1.0 / float64(numGPUs)
	}

	_, _, configSig := e.getLlamaIdentity()
	re := regexp.MustCompile(`--tensor-split[=\s](\d+(?:,\d+)*)`)
	if m := re.FindStringSubmatch(configSig); len(m) > 1 {
		parts := strings.Split(m[1], ",")
		total := 0
		vals := make([]int, len(parts))
		for i, p := range parts {
			v, _ := strconv.Atoi(p)
			vals[i] = v
			total += v
		}
		if total > 0 && len(vals) >= numGPUs {
			for i := 0; i < numGPUs && i < len(vals); i++ {
				split[i] = float64(vals[i]) / float64(total)
			}
		}
	}

	return split
}

// getCtxSizeUsed gets context size from llama-server /props
func (e *KVEngine) getCtxSizeUsed() int {
	var props map[string]interface{}
	if err := e.httpClient.GetJSON(e.llamaURL+"/props", &props); err != nil {
		return 0
	}
	if dgs, ok := props["default_generation_settings"].(map[string]interface{}); ok {
		if nctx, ok := dgs["n_ctx"].(float64); ok {
			return int(nctx)
		}
	}
	return 0
}

func slotUsedTokens(slot map[string]interface{}) int {
	candidates := []string{
		"n_past", "n_tokens", "n_processed", "n_prompt_tokens_processed",
		"n_prompt_tokens_cache", "n_decoded",
	}
	maxVal := 0.0
	for _, key := range candidates {
		if v, ok := slot[key].(float64); ok && v > maxVal {
			maxVal = v
		}
	}
	if nt, ok := slot["next_token"].([]interface{}); ok {
		for _, item := range nt {
			if tok, ok := item.(map[string]interface{}); ok {
				if v, ok := tok["n_decoded"].(float64); ok && v > maxVal {
					maxVal = v
				}
			}
		}
	}
	return int(maxVal)
}

func slotCtxTokens(slot map[string]interface{}, fallback int) int {
	if nc, ok := slot["n_ctx"].(float64); ok && nc > 0 {
		return int(nc)
	}
	return fallback
}

// Compute computes KV cache usage (theory + physical fusion)
func (e *KVEngine) Compute(gpus []shared.GPUMetrics, numGPUs int) KVResult {
	e.mu.Lock()
	e.numGPUs = numGPUs
	e.mu.Unlock()

	// Check identity
	e.CheckIdentityAndReset(numGPUs)

	result := KVResult{
		Captured: e.baselineTS.IsZero() == false,
		Summary: KVSummary{
			WorstLevel: "unknown",
		},
	}

	// Filter real GPUs
	var realGPUs []shared.GPUMetrics
	for _, g := range gpus {
		if g.Name != "" && !strings.Contains(g.Name, "Aggregate") {
			realGPUs = append(realGPUs, g)
		}
	}
	if len(realGPUs) == 0 {
		return result
	}

	// Step 1: Get theoretical KV parameters
	var theoreticalKVTotalMB float64
	var theoreticalKVPerTokenBytes float64
	var theoreticalTargetKVPerTokenBytes float64
	var theoreticalDraftKVPerTokenBytes float64
	var theoreticalCtxSizeUsed int
	var theoreticalModel string
	var theoreticalFormulaOK bool
	var theoreticalCacheType string
	var theoreticalKVTargetMB float64
	var theoreticalKVDraftMB float64

	// Find llama-server process and model path
	_, modelPath, configSig := e.getLlamaIdentity()

	cacheTypeK := "f16"
	cacheTypeV := "f16"
	cacheTypeKDraft := "f16"
	cacheTypeVDraft := "f16"
	npVal := 1
	ctxSizeTotal := 0
	specType := ""
	if modelPath != "" {
		// configSig is in format "flag=value|flag=value"
		for _, part := range strings.Split(configSig, "|") {
			if strings.HasPrefix(part, "--cache-type-k=") {
				cacheTypeK = strings.TrimPrefix(part, "--cache-type-k=")
			} else if strings.HasPrefix(part, "--cache-type-v=") {
				cacheTypeV = strings.TrimPrefix(part, "--cache-type-v=")
			} else if strings.HasPrefix(part, "--cache-type-k-draft=") {
				cacheTypeKDraft = strings.TrimPrefix(part, "--cache-type-k-draft=")
			} else if strings.HasPrefix(part, "--cache-type-v-draft=") {
				cacheTypeVDraft = strings.TrimPrefix(part, "--cache-type-v-draft=")
			} else if strings.HasPrefix(part, "-np=") {
				if v, err := strconv.Atoi(strings.TrimPrefix(part, "-np=")); err == nil && v > 0 {
					npVal = v
				}
			} else if strings.HasPrefix(part, "--spec-type=") {
				specType = strings.TrimPrefix(part, "--spec-type=")
			} else if strings.HasPrefix(part, "--ctx-size=") {
				ctxSizeTotal, _ = strconv.Atoi(strings.TrimPrefix(part, "--ctx-size="))
			}
		}
	}

	ctxSizeUsed := e.getCtxSizeUsed()
	if ctxSizeUsed == 0 && ctxSizeTotal > 0 {
		ctxSizeUsed = ctxSizeTotal / npVal
	}

	if modelPath != "" {
		if _, err := os.Stat(modelPath); err == nil {
			params, err := e.readGGUFKVParamsFast(modelPath)
			if err == nil && ctxSizeUsed > 0 {
				bytesPerToken, ok := e.computeKVBytesPerToken(*params, cacheTypeK, cacheTypeV)
				if ok {
					targetMB := bytesPerToken * float64(ctxSizeUsed) * float64(npVal) / (1024 * 1024)
					draftMB := 0.0
					if specType == "draft-mtp" && params.NextNPredictLayers > 0 {
						draftParams := *params
						draftParams.BlockCount = params.NextNPredictLayers
						if draftBytesPerToken, ok := e.computeKVBytesPerToken(draftParams, cacheTypeKDraft, cacheTypeVDraft); ok {
							draftMB = draftBytesPerToken * float64(ctxSizeUsed) * float64(npVal) / (1024 * 1024)
						}
					}
					theoreticalKVTargetMB = math.Round(targetMB*10) / 10
					theoreticalKVDraftMB = math.Round(draftMB*10) / 10
					theoreticalKVTotalMB = math.Round((targetMB+draftMB)*10) / 10
					theoreticalTargetKVPerTokenBytes = bytesPerToken
					theoreticalDraftKVPerTokenBytes = 0
					if ctxSizeUsed > 0 && npVal > 0 {
						theoreticalDraftKVPerTokenBytes = draftMB * 1024 * 1024 / (float64(ctxSizeUsed) * float64(npVal))
					}
					theoreticalKVPerTokenBytes = theoreticalTargetKVPerTokenBytes + theoreticalDraftKVPerTokenBytes
					theoreticalCtxSizeUsed = ctxSizeUsed
					theoreticalModel = modelPath
					theoreticalFormulaOK = true
					theoreticalCacheType = cacheTypeK
				}
			}
		}
	}

	// Step 2: Per-GPU computation
	e.mu.RLock()
	baseline := make(map[int]float64)
	for k, v := range e.baseline {
		baseline[k] = v
	}
	e.mu.RUnlock()

	totalKV := 0.0
	totalUsed := 0.0
	totalPhysFree := 0.0
	worstPct := 0.0
	systemOverheadPerGPU := 150.0

	splitRatios := e.getTensorSplit(len(realGPUs))
	if len(splitRatios) < len(realGPUs) {
		for len(splitRatios) < len(realGPUs) {
			splitRatios = append(splitRatios, 1.0/float64(len(realGPUs)))
		}
	}

	kvTokens := 0
	kvTotalTokens := 0
	slotsObserved := 0
	var slots []map[string]interface{}
	slotsOK := e.httpClient.GetJSON(e.llamaURL+"/slots", &slots) == nil
	if slotsOK {
		slotsObserved = len(slots)
		for _, s := range slots {
			ctx := slotCtxTokens(s, theoreticalCtxSizeUsed)
			used := slotUsedTokens(s)
			if ctx > 0 {
				kvTotalTokens += ctx
			}
			if used > ctx && ctx > 0 {
				used = ctx
			}
			kvTokens += used
		}
	}
	globalUsageRatio := 0.0
	if kvTotalTokens > 0 {
		globalUsageRatio = math.Min(float64(kvTokens)/float64(kvTotalTokens), 1.0)
	}
	totalPhysKVUsed := 0.0
	totalVerifyDelta := 0.0

	for i := 0; i < numGPUs && i < len(realGPUs); i++ {
		baselineFree := baseline[i]
		memTotal := realGPUs[i].MemTotal
		memUsed := realGPUs[i].MemUsed
		memFree := realGPUs[i].MemFree

		if memTotal <= 0 {
			continue
		}

		totalPhysFree += memFree

		card := KVCard{
			GPUIndex: i,
			TotalMB:  memTotal,
			UsedMB:   memUsed,
			FreeMB:   memFree,
			Name:     realGPUs[i].Name,
			PctUsed:  math.Round(memUsed/memTotal*1000) / 10,
		}

		if theoreticalFormulaOK {
			kvTotal := theoreticalKVTotalMB * splitRatios[i]

			kvUsed := kvTotal * globalUsageRatio

			// Physical difference method (auxiliary verification)
			physKVUsed := 0.0
			if baselineFree > 0 && memFree > 0 && baselineFree > memFree {
				physKVUsed = baselineFree - memFree
			}
			totalPhysKVUsed += physKVUsed

			// Fusion strategy
			kvUsedFinal := kvUsed
			source := "theory+slots"
			confidence := "high"
			if !slotsOK {
				if physKVUsed > 0 {
					kvUsedFinal = physKVUsed
					source = "physical-baseline"
					confidence = "medium"
				} else {
					source = "theory"
					confidence = "low"
				}
			}
			if kvUsedFinal > kvTotal {
				kvUsedFinal = kvTotal
			}
			verifyDelta := 0.0
			if physKVUsed > 0 {
				verifyDelta = physKVUsed - kvUsedFinal
				totalVerifyDelta += math.Abs(verifyDelta)
				if math.Abs(verifyDelta) > math.Max(256, kvTotal*0.1) && confidence == "high" {
					confidence = "medium"
				}
			}

			kvFree := math.Max(0, kvTotal-kvUsedFinal)
			pct := 0.0
			if kvTotal > 0 {
				pct = math.Round(kvUsedFinal/kvTotal*1000) / 10
			}
			if pct > worstPct {
				worstPct = pct
			}

			totalKV += kvTotal
			totalUsed += kvUsedFinal

			card.KVTotalMB = math.Round(kvTotal*10) / 10
			card.KVUsedMB = math.Round(kvUsedFinal*10) / 10
			card.KVFreeMB = math.Round(kvFree*10) / 10
			card.Pct = pct
			card.Level = "healthy"
			if pct > 90 {
				card.Level = "critical"
			} else if pct > 70 {
				card.Level = "warning"
			}
			card.MemFreeMB = math.Round(memFree*10) / 10
			card.TensorSplitPct = math.Round(splitRatios[i]*10000) / 100
			card.Source = source
			card.Confidence = confidence
			card.PhysKVUsedMB = math.Round(physKVUsed*10) / 10
			card.VerifyDeltaMB = math.Round(verifyDelta*10) / 10

			card.ModelMB = 0
			card.KVMB = card.KVUsedMB
			card.SystemMB = math.Max(0, memUsed-card.ModelMB-card.KVTotalMB)
		} else {
			// Fallback: physical VRAM
			kvUsedVal := memTotal - memFree
			pct := 0.0
			if memTotal > 0 {
				pct = math.Round(kvUsedVal/memTotal*1000) / 10
			}
			if pct > worstPct {
				worstPct = pct
			}

			card.KVTotalMB = math.Round(memTotal*10) / 10
			card.KVUsedMB = math.Round(kvUsedVal*10) / 10
			card.KVFreeMB = math.Round(memFree*10) / 10
			card.Pct = pct
			card.Level = "healthy"
			if pct > 90 {
				card.Level = "critical"
			} else if pct > 70 {
				card.Level = "warning"
			}
			card.MemFreeMB = math.Round(memFree*10) / 10
			card.Source = "physical-vram"
			card.Confidence = "low"
		}

		result.Cards = append(result.Cards, card)
	}

	// Compute model weight from GGUF file size
	modelWeightMB := 0.0
	if modelPath != "" {
		if info, err := os.Stat(modelPath); err == nil {
			modelWeightMB = float64(info.Size()) / (1024 * 1024)
		}
	}

	// Summary
	tokensPct := 0.0
	if kvTotalTokens > 0 {
		tokensPct = math.Round(float64(kvTokens)/float64(kvTotalTokens)*1000) / 10
	}

	result.Summary = KVSummary{
		KVTotalMB:             math.Round(totalKV*10) / 10,
		KVUsedMB:              math.Round(totalUsed*10) / 10,
		KVFreeMB:              math.Round((totalKV-totalUsed)*10) / 10,
		Pct:                   0,
		WorstLevel:            "healthy",
		PhysFreeMB:            math.Round(totalPhysFree*10) / 10,
		KVTheoreticalMB:       theoreticalKVTotalMB,
		SystemOverheadMB:      systemOverheadPerGPU * float64(numGPUs),
		AvailableMB:           math.Round((totalPhysFree-systemOverheadPerGPU*float64(numGPUs))*10) / 10,
		ModelWeightMB:         math.Round(modelWeightMB*10) / 10,
		KVTargetMB:            theoreticalKVTargetMB,
		KVDraftMB:             theoreticalKVDraftMB,
		KVTokens:              kvTokens,
		KVTotalTokens:         kvTotalTokens,
		TokensPct:             tokensPct,
		FormulaOK:             theoreticalFormulaOK,
		CacheType:             theoreticalCacheType,
		CacheTypeK:            cacheTypeK,
		CacheTypeV:            cacheTypeV,
		DraftCacheTypeK:       cacheTypeKDraft,
		DraftCacheTypeV:       cacheTypeVDraft,
		CtxSizeUsed:           theoreticalCtxSizeUsed,
		CtxSizeTotal:          theoreticalCtxSizeUsed * npVal,
		Model:                 theoreticalModel,
		KVPerTokenBytes:       theoreticalKVPerTokenBytes,
		TargetKVPerTokenBytes: theoreticalTargetKVPerTokenBytes,
		DraftKVPerTokenBytes:  theoreticalDraftKVPerTokenBytes,
		Source:                "theory+slots",
		Confidence:            "high",
		VerifyDeltaMB:         math.Round(totalVerifyDelta*10) / 10,
		SlotsObserved:         slotsObserved,
	}
	if !theoreticalFormulaOK {
		result.Summary.Source = "physical-vram"
		result.Summary.Confidence = "low"
	} else if !slotsOK {
		result.Summary.Source = "physical-baseline"
		result.Summary.Confidence = "medium"
	}
	if totalPhysKVUsed > 0 && totalVerifyDelta > math.Max(512, totalKV*0.1) && result.Summary.Confidence == "high" {
		result.Summary.Confidence = "medium"
	}

	if totalKV > 0 {
		result.Summary.Pct = math.Round(totalUsed/totalKV*1000) / 10
	}

	if worstPct > 90 {
		result.Summary.WorstLevel = "critical"
	} else if worstPct > 70 {
		result.Summary.WorstLevel = "warning"
	}

	// Distribute model weight to cards based on tensor split ratios
	for i := range result.Cards {
		if i < len(splitRatios) {
			result.Cards[i].ModelMB = math.Round(modelWeightMB*splitRatios[i]*10) / 10
		} else {
			result.Cards[i].ModelMB = 0
		}
		// Recalculate system overhead per card: used - model - kv_total
		result.Cards[i].SystemMB = math.Max(0, result.Cards[i].UsedMB-result.Cards[i].ModelMB-result.Cards[i].KVUsedMB)
		result.Cards[i].SystemMB = math.Round(result.Cards[i].SystemMB*10) / 10
	}

	// System overhead = total_phys_used - model_weight - kv_total
	totalPhysUsedForOverhead := 0.0
	for _, card := range result.Cards {
		totalPhysUsedForOverhead += card.UsedMB
	}
	result.Summary.SystemOverheadMB = math.Round((totalPhysUsedForOverhead-modelWeightMB-totalKV)*10) / 10
	if result.Summary.SystemOverheadMB < 0 {
		result.Summary.SystemOverheadMB = 0
	}

	return result
}

// GetStatus returns baseline status
func (e *KVEngine) GetStatus() map[string]interface{} {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return map[string]interface{}{
		"baseline": e.baseline,
		"meta":     e.meta,
		"captured": !e.baselineTS.IsZero(),
	}
}
