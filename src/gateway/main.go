package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"inference-hub-v3/src/alert_manager"
	"inference-hub-v3/src/collectors"
	"inference-hub-v3/src/kv_engine"
	"inference-hub-v3/src/middleware"
	"inference-hub-v3/src/shared"

	"github.com/shirou/gopsutil/v4/net"
	"runtime"
)

// Global KV engine instance
var globalKVEngine *kv_engine.KVEngine
var buildCommit = "unknown"

// Global GPU collector for multi-vendor support
var globalGPUCollector *collectors.GPUCollector

// Collector-owned snapshots are the single source for API and SSE consumers.
var globalGPUBase *collectors.BaseCollector
var globalSystemBase *collectors.BaseCollector
var globalInferenceBase *collectors.BaseCollector
var globalLLMBase *collectors.BaseCollector
var globalKVBase *collectors.BaseCollector

// Global NewAPI log parser for IP token stats
var globalNewAPIParser *shared.NewAPIParser

// v2EventCursor is process-local during the compatibility period. The client
// treats it as a monotonic transport cursor; durable history remains owned by
// the collectors/VictoriaMetrics and is not coupled to this stream.
var v2EventCursor uint64

func main() {
	shared.Infof("InferenceHub v3 starting...")
	globalNewAPIParser = shared.NewNewAPIParser()

	cfg, err := shared.LoadConfig("configs")
	if err != nil {
		shared.Errorf("Failed to load config: %v", err)
		os.Exit(1)
	}
	shared.Infof("Configuration loaded successfully")

	if cfg.Services.Dashboard.Mode != "" {
		gin.SetMode(cfg.Services.Dashboard.Mode)
	}

	vmURL := cfg.Services.VictoriaMetrics.URL + cfg.Services.VictoriaMetrics.WritePath
	store := shared.NewMetricsStore(vmURL, cfg.Services.VictoriaMetrics.TimeoutSec)
	shared.Infof("MetricsStore initialized (VM: %s)", vmURL)

	apiKey := cfg.GetAPIKey()
	httpClient := shared.NewHTTPClient(cfg.Services.LlamaServer.TimeoutSec, apiKey)
	shared.Infof("HTTPClient initialized (timeout=%ds)", cfg.Services.LlamaServer.TimeoutSec)

	// Initialize KV Cache engine
	globalKVEngine = kv_engine.NewKVEngine(httpClient, cfg.Services.LlamaServer.URL)
	shared.Infof("KV Cache engine initialized")

	collectorList := initCollectors(cfg, store, httpClient, globalKVEngine)
	shared.Infof("Collectors initialized (%d collectors)", len(collectorList))

	// Initialize alert engine
	alertEngine := alert_manager.NewAlertEngine(&cfg.Alerts)
	shared.Infof("Alert engine initialized (%d rules)", len(cfg.Alerts.Rules))

	// Start alert evaluation goroutine
	go evaluateAlerts(alertEngine)

	for _, c := range collectorList {
		c.Start()
	}

	router := setupRouter(cfg, store, httpClient, globalKVEngine)

	addr := cfg.DashboardAddr()
	srv := &http.Server{
		Addr:    addr,
		Handler: router,
	}
	var extraSrv *http.Server

	go func() {
		shared.Infof("Dashboard listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			shared.Errorf("Server error: %v", err)
			os.Exit(1)
		}
	}()
	if extraPort := strings.TrimSpace(os.Getenv("DASHBOARD_EXTRA_PORT")); extraPort != "" && extraPort != fmt.Sprint(cfg.Services.Dashboard.Port) {
		extraSrv = &http.Server{Addr: cfg.Services.Dashboard.Listen + ":" + extraPort, Handler: router}
		go func() {
			shared.Infof("Dashboard compatibility listener on %s", extraSrv.Addr)
			if err := extraSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				shared.Errorf("Compatibility server error: %v", err)
			}
		}()
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	shared.Infof("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, c := range collectorList {
		c.Stop()
	}

	if err := srv.Shutdown(ctx); err != nil {
		shared.Errorf("Server forced to shutdown: %v", err)
	}
	if extraSrv != nil {
		if err := extraSrv.Shutdown(ctx); err != nil {
			shared.Errorf("Compatibility server forced to shutdown: %v", err)
		}
	}

	store.Flush()
	shared.Infof("Server exited")
}

// handleNodeIdentity is a read-only registration precursor. It deliberately
// publishes no credential, path or control command; a future control plane can
// discover a node's stable identity and protocol versions before enrollment.
func handleNodeIdentity() gin.HandlerFunc {
	return func(c *gin.Context) {
		hostname, _ := os.Hostname()
		nodeID := strings.TrimSpace(os.Getenv("CONTROL_NODE_ID"))
		if nodeID == "" {
			nodeID = hostname
		}
		nodeName := strings.TrimSpace(os.Getenv("CONTROL_NODE_NAME"))
		if nodeName == "" {
			nodeName = nodeID
		}
		advertiseURL := strings.TrimSpace(os.Getenv("AGENT_ADVERTISE_URL"))
		c.JSON(http.StatusOK, gin.H{
			"schema_version":   "1.0",
			"node_id":          nodeID,
			"node_name":        nodeName,
			"hostname":         hostname,
			"agent_version":    "v3.1.0",
			"build_commit":     buildCommit,
			"advertise_url":    advertiseURL,
			"metric_schema":    "/api/schema/metrics",
			"capabilities_url": "/api/capabilities",
			"control_actions":  []string{"restart_llama", "clear_cache", "collect_baseline"},
		})
	}
}

type CollectorRunner struct {
	name      string
	runner    *collectors.BaseCollector
	collectFn func(ctx context.Context) (interface{}, error)
}

func (cr *CollectorRunner) Start() {
	cr.runner.Run(cr.collectFn)
}

func (cr *CollectorRunner) Stop() {
	cr.runner.Stop()
}

func initCollectors(cfg *shared.Config, store *shared.MetricsStore, httpClient *shared.HTTPClient, kvEng *kv_engine.KVEngine) []*CollectorRunner {
	var runners []*CollectorRunner

	if cfg.Collectors.GPU.Enabled {
		gpuBase := collectors.NewBaseCollector("gpu", true, cfg.Collectors.GPU.IntervalSec, store)
		gpuColl := collectors.NewGPUCollector(gpuBase, httpClient)
		globalGPUCollector = gpuColl
		globalGPUBase = gpuBase
		runners = append(runners, &CollectorRunner{
			name:   "gpu",
			runner: gpuBase,
			collectFn: func(ctx context.Context) (interface{}, error) {
				return gpuColl.Collect(ctx)
			},
		})
	}

	if cfg.Collectors.System.Enabled {
		sysBase := collectors.NewBaseCollector("system", true, cfg.Collectors.System.IntervalSec, store)
		sysColl := collectors.NewSystemCollector(sysBase)
		globalSystemBase = sysBase
		runners = append(runners, &CollectorRunner{
			name:   "system",
			runner: sysBase,
			collectFn: func(ctx context.Context) (interface{}, error) {
				return sysColl.Collect(ctx)
			},
		})
	}

	if cfg.Collectors.Inference.Enabled {
		infBase := collectors.NewBaseCollector("inference", true, cfg.Collectors.Inference.IntervalSec, store)
		infColl := collectors.NewInferenceCollector(infBase, httpClient, cfg)
		globalInferenceBase = infBase
		runners = append(runners, &CollectorRunner{
			name:   "inference",
			runner: infBase,
			collectFn: func(ctx context.Context) (interface{}, error) {
				return infColl.Collect(ctx)
			},
		})
	}

	if cfg.Collectors.LLMMonitor.Enabled {
		llmBase := collectors.NewBaseCollector("llm_monitor", true, cfg.Collectors.LLMMonitor.IntervalSec, store)
		llmColl := collectors.NewLLMMonitor(llmBase, httpClient, cfg)
		globalLLMBase = llmBase
		runners = append(runners, &CollectorRunner{
			name:   "llm_monitor",
			runner: llmBase,
			collectFn: func(ctx context.Context) (interface{}, error) {
				return llmColl.Collect(ctx)
			},
		})
	}

	if cfg.Collectors.KVEngine.Enabled && kvEng != nil {
		kvBase := collectors.NewBaseCollector("kv_cache", true, cfg.Collectors.KVEngine.IntervalSec, store)
		kvColl := collectors.NewKVCollector(kvBase, kvEng, cfg.Services.LlamaServer.URL)
		globalKVBase = kvBase
		runners = append(runners, &CollectorRunner{
			name:   "kv_cache",
			runner: kvBase,
			collectFn: func(ctx context.Context) (interface{}, error) {
				return kvColl.Collect(ctx)
			},
		})
	}

	return runners
}

func setupRouter(cfg *shared.Config, store *shared.MetricsStore, httpClient *shared.HTTPClient, kvEng *kv_engine.KVEngine) *gin.Engine {
	r := gin.Default()

	r.Use(corsMiddleware())

	authMW := middleware.AuthMiddleware()

	r.Static("/assets", "./static/assets")
	r.Static("/static", "/data/dashboard/static")
	r.StaticFile("/favicon.ico", "./static/favicon.ico")
	r.StaticFile("/", "/data/dashboard/index.html")

	api := r.Group("/api")
	{
		// ── 基础设施 ──
		api.GET("/health", func(c *gin.Context) {
			c.JSON(200, gin.H{"status": "ok", "version": "v3.1.0", "build_commit": buildCommit, "time": time.Now().Format("2006-01-02 15:04:05"), "freshness": collectorFreshness()})
		})
		api.GET("/node/identity", handleNodeIdentity())
		api.GET("/schema/metrics", func(c *gin.Context) { c.JSON(200, shared.MetricDefinitions()) })
		api.GET("/sse", handleSSE())
		api.GET("/stream/deploy", handleDeployStream())

		// ── 模型管理 ──
		models := api.Group("/models")
		{
			models.GET("", handleGetModels(cfg, httpClient))
			models.GET("/list", handleLegacyModelsList(cfg, httpClient))
			models.POST("/deploy", authMW, handleProxyDeploy(cfg, httpClient))
			models.POST("/switch", authMW, handleSwitchModel(cfg, httpClient))
			models.POST("/stop", authMW, handleStopModel(cfg, httpClient))
		}

		// ── 引擎管理 ──
		engines := api.Group("/engines")
		{
			engines.GET("", handleGetEngines(cfg, httpClient))
			engines.GET("/active", handleGetActiveEngine(cfg, httpClient))
			engines.POST("/switch", authMW, handleSwitchEngine(cfg, httpClient))
		}
		api.GET("/engine/active", handleGetActiveEngine(cfg, httpClient))
		api.POST("/engine/switch", authMW, handleSwitchEngine(cfg, httpClient))

		// ── 配置管理 ──
		settings := api.Group("/settings")
		{
			settings.GET("/persist", handleGetPersist(cfg, httpClient))
			settings.POST("/persist", authMW, handleSetPersist(cfg, httpClient))
			settings.GET("/params", handleGetLlamaParams(cfg, httpClient))
			settings.POST("/params", authMW, handleSetLlamaParams(cfg, httpClient))
		}

		// ── 快速切换 ──
		qs := api.Group("/quick-switch")
		{
			qs.GET("", handleProxyQuickSwitchGet(cfg, httpClient))
			qs.POST("", authMW, handleProxyQuickSwitchPost(cfg, httpClient))
			qs.POST("/add-recent", authMW, handleProxyAddRecent(cfg, httpClient))
			qs.POST("/toggle-fav", authMW, handleProxyToggleFav(cfg, httpClient))
		}

		// ── GPU 管理 ──
		gpu := api.Group("/gpu")
		{
			gpu.GET("", func(c *gin.Context) {
				gpus := cachedGPUData(cfg)
				if gpus == nil {
					c.JSON(200, gin.H{"gpus": []interface{}{}, "count": 0})
					return
				}
				c.JSON(200, gin.H{"gpus": gpus.GPUs, "count": len(gpus.GPUs)})
			})
			gpu.GET("/info", handleGetGPUInfo(cfg))
			gpu.POST("/power_limit", authMW, handleGPUPowerLimit())
		}

		// ── 算力详情 ──
		compute := api.Group("/compute")
		{
			compute.GET("/procs", handleComputeProcesses())
		}

		// ── KV 基线 ──
		kv := api.Group("/kv-baseline")
		{
			kv.GET("/status", handleKVBaselineStatus())
			kv.POST("/refresh", authMW, handleKVBaselineRefresh())
		}
		api.GET("/kv_baseline/status", handleKVBaselineStatus())
		api.POST("/kv_baseline/refresh", authMW, handleKVBaselineRefresh())

		// ── KV Cache ──
		api.GET("/kv-cache", handleKVCache(cfg))

		// ── 监控面板 ──
		monitor := api.Group("/monitor")
		{
			monitor.GET("/panel/:name", handlePanelData(cfg))
			monitor.GET("/active-requests", handleActiveRequests(cfg, httpClient))
			monitor.GET("/request-sources", handleRequestSources(cfg, httpClient))
		}

		// ── IP Token 统计 ──
		api.GET("/ip-token-stats", handleIPTokenStats())

		// ── 系统操作 ──
		system := api.Group("/system")
		{
			system.POST("/:action", authMW, handleSystemAction(cfg))
		}

		// ── 指标查询 ──
		metrics := api.Group("/metrics")
		{
			metrics.GET("/query", authMW, handleMetricsQuery())
			metrics.GET("/query_range", authMW, handleMetricsQueryRange())
		}

		// ── 基准测试 ──
		benchmark := api.Group("/benchmark")
		{
			benchmark.GET("/history", handleProxyBenchmarkHistory(cfg, httpClient))
			benchmark.GET("/providers", handleProxyBenchmarkProviders(cfg, httpClient))
		}

		// ── 集群 ──
		cluster := api.Group("/cluster")
		{
			cluster.GET("/nodes", handleProxyClusterNodes(cfg, httpClient))
		}

		// Unified aggregation endpoints
		api.GET("/overview", handleOverview(cfg, httpClient))
		api.GET("/status", handleV2Snapshot(cfg, httpClient))
		api.GET("/gpus", func(c *gin.Context) {
			gpus := cachedGPUData(cfg)
			if gpus == nil {
				c.JSON(http.StatusOK, gin.H{"gpus": []interface{}{}, "count": 0})
				return
			}
			c.JSON(http.StatusOK, gin.H{"gpus": gpus.GPUs, "count": len(gpus.GPUs)})
		})
		api.GET("/request_sources", handleRequestSources(cfg, httpClient))
		api.GET("/active_requests", handleActiveRequests(cfg, httpClient))
		api.GET("/hardware", handleHardware(cfg))
		api.GET("/inference", handleInferenceUnified(cfg, httpClient))
		api.GET("/v2/snapshot", handleV2Snapshot(cfg, httpClient))
		api.GET("/v2/events", handleV2Events(cfg, httpClient))
		api.GET("/history", func(c *gin.Context) { c.JSON(http.StatusOK, getHistory(cfg)) })
		// ── 告警 ──
		alerts := api.Group("/alerts")
		{
			alerts.GET("/status", handleAlertStatus())
			alerts.POST("/test", authMW, handleTestAlert())
		}
	}

	proxyRoutes(r, cfg)
	compatProxyRoutes(r, cfg, authMW)

	r.NoRoute(func(c *gin.Context) {
		c.File("/data/dashboard/index.html")
	})

	return r
}
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

// ===== Metrics Query =====

func handleMetricsQuery() gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("query")
		cfg := shared.GetConfig()
		if cfg == nil {
			c.JSON(500, gin.H{"error": "config not loaded"})
			return
		}
		if query == "" {
			c.JSON(400, gin.H{"error": "query parameter required"})
			return
		}
		vmURL := cfg.Services.VictoriaMetrics.URL + cfg.Services.VictoriaMetrics.QueryPath + "?query=" + url.QueryEscape(query)
		proxyToVM(c, vmURL)
	}
}

func handleMetricsQueryRange() gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("query")
		start := c.Query("start")
		end := c.Query("end")
		step := c.Query("step")
		cfg := shared.GetConfig()
		if cfg == nil {
			c.JSON(500, gin.H{"error": "config not loaded"})
			return
		}
		if query == "" {
			c.JSON(400, gin.H{"error": "query parameter required"})
			return
		}
		vmURL := cfg.Services.VictoriaMetrics.URL + cfg.Services.VictoriaMetrics.QueryRangePath +
			fmt.Sprintf("?query=%s&start=%s&end=%s&step=%s",
				url.QueryEscape(query), start, end, step)
		proxyToVM(c, vmURL)
	}
}

func proxyToVM(c *gin.Context, vmURL string) {
	resp, err := http.Get(vmURL)
	if err != nil {
		c.JSON(500, gin.H{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
}

// ===== SSE =====

func handleSSE() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")

		cfg := shared.GetConfig()
		if cfg == nil {
			c.SSEvent("error", gin.H{"error": "config not loaded"})
			return
		}

		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()

		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				tick := buildSSETick(cfg, nil)
				c.SSEvent("tick", tick)
				c.Writer.Flush()
			}
		}
	}
}

func buildSSETick(cfg *shared.Config, hc *shared.HTTPClient) shared.SSETick {
	tick := shared.SSETick{
		Type:      "tick",
		Timestamp: time.Now().Unix(),
		Freshness: collectorFreshness(),
	}

	if cfg.Collectors.GPU.Enabled {
		if globalGPUBase != nil {
			if value, _, ok := globalGPUBase.Latest(15 * time.Second); ok {
				if gpus, typed := value.(shared.GPUAggregate); typed {
					tick.GPUs = &gpus
				}
			}
		}
	}

	if cfg.Collectors.System.Enabled {
		if globalSystemBase != nil {
			if value, _, ok := globalSystemBase.Latest(15 * time.Second); ok {
				if sys, typed := value.(shared.SystemMetrics); typed {
					tick.System = &sys
				}
			}
		}
	}

	if cfg.Collectors.Inference.Enabled {
		if globalInferenceBase != nil {
			if value, _, ok := globalInferenceBase.Latest(10 * time.Second); ok {
				if inf, typed := value.(shared.InferenceMetrics); typed {
					tick.Inference = &inf
				}
			}
		}
	}

	if cfg.Collectors.LLMMonitor.Enabled {
		if globalLLMBase != nil {
			if value, _, ok := globalLLMBase.Latest(10 * time.Second); ok {
				if llm, typed := value.(shared.LLMMetrics); typed {
					tick.LLM = &llm
				}
			}
		}
	}

	// Reuse the collector-owned KV snapshot instead of recomputing per client.
	if cfg.Collectors.KVEngine.Enabled && globalKVBase != nil {
		if value, _, ok := globalKVBase.Latest(15 * time.Second); ok {
			if kv, typed := value.(shared.KVMetrics); typed {
				tick.KVCache = &kv
			}
		}
	}

	tick.HealthScore, tick.HealthReasons = calcHealthScore(tick)
	tick.Uptime = getUptime()

	return tick
}

func collectGPUData(cfg *shared.Config) *shared.GPUAggregate {
	// Use multi-vendor GPU collector if available
	if globalGPUCollector != nil {
		result, err := globalGPUCollector.Collect(context.Background())
		if err != nil {
			shared.Infof("[gpu] collector error: %v", err)
		} else {
			if agg, ok := result.(shared.GPUAggregate); ok {
				if len(agg.GPUs) > 0 {
					shared.Infof("[gpu] collector returned %d GPUs, vendor=%s", len(agg.GPUs), agg.GPUs[0].Vendor)
				}
				return &agg
			}
			shared.Infof("[gpu] type assertion failed, result type=%T", result)
		}
	}

	// Fallback: original nvidia-smi collection
	fields := "name,utilization.gpu,memory.used,memory.total,memory.free,temperature.gpu,power.draw,power.limit,fan.speed,clocks.current.graphics,clocks.max.graphics,driver_version,pcie.link.gen.current,pcie.link.width.current,pcie.link.gen.max,pcie.link.width.max"
	cmd := exec.Command("sh", "-c",
		"nvidia-smi --query-gpu="+fields+" --format=csv,noheader,nounits 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var gpus []shared.GPUMetrics
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")

	for i, line := range lines {
		vals := strings.Split(line, ",")
		if len(vals) < 16 {
			continue
		}
		gpu := shared.GPUMetrics{
			Index:      i,
			Name:       strings.TrimSpace(vals[0]),
			Util:       parseFloat2(vals[1]),
			MemUsed:    parseFloat2(vals[2]),
			MemTotal:   parseFloat2(vals[3]),
			MemFree:    parseFloat2(vals[4]),
			Temp:       parseFloat2(vals[5]),
			PowerDraw:  parseFloat2(vals[6]),
			PowerLimit: parseFloat2(vals[7]),
			FanSpeed:   shared.Float64Ptr(parseFloat2(vals[8])),
			Clock:      parseFloat2(vals[9]),
			ClockMax:   parseFloat2(vals[10]),
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

	if len(gpus) == 0 {
		return nil
	}

	agg := aggregateGPUs2(gpus)
	return &shared.GPUAggregate{GPUs: gpus, Aggregate: agg}
}

// 网络速率计算全局状态
var (
	_netPrevRecv   uint64
	_netPrevSent   uint64
	_netPrevTime   time.Time
	netRecvRateBPS float64 // 接收速率 bytes/sec
	netSentRateBPS float64 // 发送速率 bytes/sec
)

// ===== Data Caches =====
var (
	gpuCacheMu     sync.RWMutex
	gpuCacheData   *shared.GPUAggregate
	gpuCacheTime   time.Time
	gpuCacheTTL    = 5 * time.Second
	propsCacheMu   sync.RWMutex
	propsCacheData map[string]interface{}
	propsCacheTime time.Time
	propsCacheTTL  = 60 * time.Second
)

func cachedGPUData(cfg *shared.Config) *shared.GPUAggregate {
	if globalGPUBase != nil {
		if value, _, ok := globalGPUBase.Latest(15 * time.Second); ok {
			if gpus, typed := value.(shared.GPUAggregate); typed {
				return &gpus
			}
		}
	}
	gpuCacheMu.RLock()
	if gpuCacheData != nil && time.Since(gpuCacheTime) < gpuCacheTTL {
		defer gpuCacheMu.RUnlock()
		return gpuCacheData
	}
	gpuCacheMu.RUnlock()
	gpuCacheMu.Lock()
	defer gpuCacheMu.Unlock()
	if gpuCacheData != nil && time.Since(gpuCacheTime) < gpuCacheTTL {
		return gpuCacheData
	}
	data := collectGPUData(cfg)
	gpuCacheData = data
	gpuCacheTime = time.Now()
	return data
}

func cachedSystemData() *shared.SystemMetrics {
	if globalSystemBase != nil {
		if value, _, ok := globalSystemBase.Latest(15 * time.Second); ok {
			if sys, typed := value.(shared.SystemMetrics); typed {
				return &sys
			}
		}
	}
	return collectSystemData()
}

func cachedInferenceData(cfg *shared.Config, hc *shared.HTTPClient) *shared.InferenceMetrics {
	if globalInferenceBase != nil {
		if value, _, ok := globalInferenceBase.Latest(10 * time.Second); ok {
			if inf, typed := value.(shared.InferenceMetrics); typed {
				return &inf
			}
		}
	}
	return collectInferenceData(cfg, hc)
}

func cachedLLMData(cfg *shared.Config, hc *shared.HTTPClient) *shared.LLMMetrics {
	if globalLLMBase != nil {
		if value, _, ok := globalLLMBase.Latest(10 * time.Second); ok {
			if llm, typed := value.(shared.LLMMetrics); typed {
				return &llm
			}
		}
	}
	return collectLLMData(cfg, hc)
}

func cachedKVData() *shared.KVMetrics {
	if globalKVBase != nil {
		if value, _, ok := globalKVBase.Latest(15 * time.Second); ok {
			if kv, typed := value.(shared.KVMetrics); typed {
				return &kv
			}
		}
	}
	return nil
}

func collectorFreshness() map[string]shared.SourceFreshness {
	result := make(map[string]shared.SourceFreshness)
	if globalGPUBase != nil {
		result["gpus"] = globalGPUBase.Status(15 * time.Second)
	}
	if globalSystemBase != nil {
		result["system"] = globalSystemBase.Status(15 * time.Second)
	}
	if globalInferenceBase != nil {
		result["inference"] = globalInferenceBase.Status(10 * time.Second)
	}
	if globalLLMBase != nil {
		result["llm"] = globalLLMBase.Status(10 * time.Second)
	}
	if globalKVBase != nil {
		result["kv_cache"] = globalKVBase.Status(15 * time.Second)
	}
	return result
}

func cachedPropsData(cfg *shared.Config) map[string]interface{} {
	propsCacheMu.RLock()
	if propsCacheData != nil && time.Since(propsCacheTime) < propsCacheTTL {
		defer propsCacheMu.RUnlock()
		return propsCacheData
	}
	propsCacheMu.RUnlock()
	propsCacheMu.Lock()
	defer propsCacheMu.Unlock()
	if propsCacheData != nil && time.Since(propsCacheTime) < propsCacheTTL {
		return propsCacheData
	}
	apiKey := cfg.GetAPIKey()
	if apiKey == "" {
		return nil
	}
	client := &http.Client{Timeout: 3 * time.Second}
	req, _ := http.NewRequest("GET", "http://127.0.0.1:8080/props", nil)
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var props map[string]interface{}
	if json.NewDecoder(resp.Body).Decode(&props) != nil {
		return nil
	}
	propsCacheData = props
	propsCacheTime = time.Now()
	return props
}

func calcNetRates(recv, sent uint64) {
	now := time.Now()
	if !_netPrevTime.IsZero() {
		dt := now.Sub(_netPrevTime).Seconds()
		if dt > 0 {
			netRecvRateBPS = float64(recv-_netPrevRecv) / dt
			netSentRateBPS = float64(sent-_netPrevSent) / dt
			if netRecvRateBPS < 0 {
				netRecvRateBPS = 0
			}
			if netSentRateBPS < 0 {
				netSentRateBPS = 0
			}
		}
	}
	_netPrevRecv = recv
	_netPrevSent = sent
	_netPrevTime = now
}

func collectSystemData() *shared.SystemMetrics {
	m := &shared.SystemMetrics{}

	// CPU
	cpuPcts, err := getCPUPercent()
	if err == nil && len(cpuPcts) > 0 {
		m.CPUUtil = cpuPcts[0]
	}
	m.CPULogical = runtime.NumCPU()
	if perCPU, err := getCPUPerCore(); err == nil {
		m.CPUPerCore = perCPU
	}
	// CPU model and details
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		seen := map[string]bool{}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "model name") && !seen["model"] {
				m.CPUModel = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
				seen["model"] = true
			}
			if strings.HasPrefix(line, "cpu MHz") {
				if v, e := strconv.ParseFloat(strings.TrimSpace(strings.SplitN(line, ":", 2)[1]), 64); e == nil {
					if v > m.CPUFreqMax {
						m.CPUFreqMax = v
					}
					m.CPUFreqCur = v
				}
			}
			if strings.HasPrefix(line, "flags") {
				flags := strings.SplitN(line, ":", 2)[1]
				if strings.Contains(flags, "svm") {
					m.CPUVirt = "AMD-V"
				}
				if strings.Contains(flags, "vmx") {
					m.CPUVirt = "Intel VT-x"
				}
			}
		}
	}
	// CPU cache from sysfs
	if data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cache/index2/size"); err == nil {
		m.CPUL2 = strings.TrimSpace(string(data))
	}
	if data, err := os.ReadFile("/sys/devices/system/cpu/cpu0/cache/index3/size"); err == nil {
		m.CPUL3 = strings.TrimSpace(string(data))
	}
	// Physical cores from /proc/cpuinfo
	if data, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		coreIDs := map[string]bool{}
		currentPhysID := ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "physical id") {
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					currentPhysID = fields[3]
				}
			}
			if strings.HasPrefix(line, "core id") {
				fields := strings.Fields(line)
				if len(fields) >= 4 {
					coreIDs[currentPhysID+"-"+fields[3]] = true
				}
			}
		}
		if len(coreIDs) > 0 {
			m.CPUPhysical = len(coreIDs)
		}
	}
	// CPU temp: k10temp优先
	cpuTempFound := false
	if dirs, err := os.ReadDir("/sys/class/hwmon"); err == nil {
		for _, d := range dirs {
			nameBytes, _ := os.ReadFile("/sys/class/hwmon/" + d.Name() + "/name")
			name := strings.TrimSpace(string(nameBytes))
			if name == "k10temp" || name == "coretemp" {
				if tempData, err3 := os.ReadFile("/sys/class/hwmon/" + d.Name() + "/temp1_input"); err3 == nil {
					if temp, err4 := strconv.ParseFloat(strings.TrimSpace(string(tempData)), 64); err4 == nil {
						m.CPUTemp = temp / 1000.0
						cpuTempFound = true
					}
				}
				break
			}
		}
	}
	if !cpuTempFound {
		if data, err := os.ReadFile("/sys/class/thermal/thermal_zone0/temp"); err == nil {
			if temp, err := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); err == nil {
				m.CPUTemp = temp / 1000.0
			}
		}
	}

	// Memory
	vmem, err := getVirtualMemory()
	if err == nil {
		m.MemTotal = vmem.Total
		m.MemUsed = vmem.Used
		m.MemFree = vmem.Free
		m.MemUsedPct = vmem.UsedPercent
		m.MemAvailable = vmem.Available
	}
	// Read Buffers/Cached from /proc/meminfo (gopsutil v4 doesn't expose them)
	if data, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && fields[0] == "Buffers:" {
				if v, e := strconv.ParseUint(fields[1], 10, 64); e == nil {
					m.MemBuffers = v * 1024
				}
			}
			if len(fields) >= 2 && fields[0] == "Cached:" {
				if v, e := strconv.ParseUint(fields[1], 10, 64); e == nil {
					m.MemCached = v * 1024
				}
			}
		}
	}

	// Swap
	swap, err := getSwapMemory()
	if err == nil {
		m.SwapTotal = swap.Total
		m.SwapUsed = swap.Used
		m.SwapFree = swap.Free
		m.SwapUsedPct = swap.UsedPercent
	}

	// Disks
	partitions, err := getDiskPartitions()
	if err == nil {
		for _, p := range partitions {
			usage, err := getDiskUsage(p.Mountpoint)
			if err == nil {
				m.Disks = append(m.Disks, shared.DiskMetrics{
					Device: p.Device, Mountpoint: p.Mountpoint, Fstype: p.Fstype,
					Total: usage.Total, Used: usage.Used, Free: usage.Free, UsedPct: usage.UsedPercent,
				})
			}
		}
	}
	// Disk model
	if data, err := os.ReadFile("/sys/block/nvme0n1/device/model"); err == nil {
		m.DiskModel = strings.TrimSpace(string(data))
		m.DiskType = "NVMe SSD"
	}
	if data, err := os.ReadFile("/sys/block/nvme0n1/size"); err == nil {
		if sectors, e := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); e == nil {
			m.DiskSize = sectors * 512
		}
	}
	// NVMe temp (try multiple paths)
	for _, path := range []string{"/sys/class/nvme/nvme0/hwmon0/temp1_input", "/sys/class/hwmon/hwmon0/temp1_input"} {
		if data, err := os.ReadFile(path); err == nil {
			if temp, e := strconv.ParseFloat(strings.TrimSpace(string(data)), 64); e == nil {
				t := temp / 1000.0
				m.NvmeTemp = &t
				break
			}
		}
	}

	// Network
	netIO, err := getNetIO()
	if err == nil && len(netIO) > 0 {
		m.NetBytesSent = netIO[0].BytesSent
		m.NetBytesRecv = netIO[0].BytesRecv
		calcNetRates(m.NetBytesRecv, m.NetBytesSent)
		if netIO[0].Interface != "" {
			m.NetAdapter = netIO[0].Interface
		}
	}
	// Network adapter details
	if interfaces, err := net.Interfaces(); err == nil {
		for _, iface := range interfaces {
			if iface.HardwareAddr == "" || len(iface.Name) < 2 || strings.HasPrefix(iface.Name, "lo") {
				continue
			}
			// 如果已有适配器名(来自getNetIO)，只匹配对应接口
			if m.NetAdapter != "" && iface.Name != m.NetAdapter {
				continue
			}
			if m.NetAdapter == "" {
				m.NetAdapter = iface.Name
			}
			for _, addr := range iface.Addrs {
				ip := strings.Split(addr.Addr, "/")[0]
				if strings.Contains(ip, ".") && !strings.HasPrefix(ip, "127.") {
					m.NetIPv4 = ip
				}
			}
			if m.NetIPv4 != "" {
				break
			}
		}
	}
	if m.NetAdapter != "" {
		if data, err := os.ReadFile("/sys/class/net/" + m.NetAdapter + "/speed"); err == nil {
			if speed, e := strconv.Atoi(strings.TrimSpace(string(data))); e == nil {
				if speed >= 1000 {
					m.NetLinkSpeed = strconv.Itoa(speed/1000) + " Gbps"
				} else {
					m.NetLinkSpeed = strconv.Itoa(speed) + " Mbps"
				}
			}
		}
		vendorMap := map[string]string{"0x10ec": "Realtek", "0x8086": "Intel", "0x14e4": "Broadcom", "0x10de": "NVIDIA"}
		if data, err := os.ReadFile("/sys/class/net/" + m.NetAdapter + "/device/vendor"); err == nil {
			vid := strings.TrimSpace(string(data))
			if v, ok := vendorMap[vid]; ok {
				m.NetVendor = v
			} else {
				m.NetVendor = vid
			}
		}
	}

	procs, err := getProcesses()
	if err == nil {
		m.ProcessCount = len(procs)
	}

	load, err := getLoadAvg()
	if err == nil {
		m.Load1 = load.Load1
		m.Load5 = load.Load5
		m.Load15 = load.Load15
	}

	return m
}

func collectInferenceData(cfg *shared.Config, hc *shared.HTTPClient) *shared.InferenceMetrics {
	m := &shared.InferenceMetrics{}
	baseURL := cfg.Services.LlamaServer.URL

	slotsURL := baseURL + cfg.Services.LlamaServer.SlotsPath
	var slotsData []map[string]interface{}
	if err := hc.GetJSON(slotsURL, &slotsData); err == nil {
		m.TotalSlots = len(slotsData)
		for _, s := range slotsData {
			if proc, ok := s["is_processing"].(bool); ok && proc {
				m.ActiveSlots++
			}
			slot := shared.SlotInfo{}
			if nd, ok := s["n_decoded"].(float64); ok {
				slot.NDecoded = int(nd)
			}
			if nr, ok := s["n_remain"].(float64); ok {
				slot.NRemain = int(nr)
			}
			if nc, ok := s["n_ctx"].(float64); ok {
				slot.NCtx = int(nc)
			}
			m.Slots = append(m.Slots, slot)
		}
	}

	statsURL := baseURL + cfg.Services.LlamaServer.StatsPath
	var statsData map[string]interface{}
	if err := hc.GetJSON(statsURL, &statsData); err == nil {
		if v, ok := statsData["tokens_predicted_per_second"].(float64); ok {
			m.LastTPS = v
		}
		if v, ok := statsData["slots_avg_processing_ms"].(float64); ok {
			m.LastLatencyMs = v
		}
		if v, ok := statsData["tokens_prompted_total"].(float64); ok {
			m.LastPromptTokens = int(v)
		}
		if v, ok := statsData["tokens_predicted_total"].(float64); ok {
			m.LastEvalTokens = int(v)
		}
		if v, ok := statsData["kv_cache_usage_ratio"].(float64); ok {
			m.KVCacheUsedPct = v * 100
		}
		if v, ok := statsData["kv_cache_tokens_count"].(float64); ok {
			m.KVCacheUsedTokens = int(v)
		}
	}

	// 缓存推理数据，无请求时保持上次结果
	if m.ActiveSlots > 0 || m.LastTPS > 0 {
		_lastInfStats = m
	} else if _lastInfStats != nil {
		// 无活动时恢复上次的timing数据，但更新slots状态
		m.LastTPS = _lastInfStats.LastTPS
		m.LastLatencyMs = _lastInfStats.LastLatencyMs
		m.LastPromptTokens = _lastInfStats.LastPromptTokens
		m.LastEvalTokens = _lastInfStats.LastEvalTokens
		m.LastPromptMs = _lastInfStats.LastPromptMs
		m.LastEvalMs = _lastInfStats.LastEvalMs
	}
	// 如果 /stats 失败，从日志文件解析 timing 数据
	if m.LastTPS == 0 {
		m = parseTimingFromLog(m, cfg)
	}
	return m
}

// 推理统计数据缓存
var _lastInfStats *shared.InferenceMetrics

// parseTimingFromLog 从日志文件末尾解析最近的 timing 数据
func parseTimingFromLog(m *shared.InferenceMetrics, cfg *shared.Config) *shared.InferenceMetrics {
	logPath := "/tmp/llama-server.log"
	f, err := os.Open(logPath)
	if err != nil {
		return m
	}
	defer f.Close()

	// 只读末尾 200 行 (足够找到最新 timing 块)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 64*1024)
	var tailLines []string
	const maxTailLines = 200
	for scanner.Scan() {
		tailLines = append(tailLines, scanner.Text())
		if len(tailLines) > maxTailLines {
			tailLines = tailLines[1:]
		}
	}

	// 从后向前搜索最新的 timing 块
	for i := len(tailLines) - 1; i >= 0; i-- {
		line := tailLines[i]
		if !strings.Contains(line, "total time =") {
			continue
		}
		totalRe := regexp.MustCompile(`total time =\s+([\d.]+)\s+ms\s*/\s*(\d+)\s+tokens`)
		totalMatch := totalRe.FindStringSubmatch(line)
		if len(totalMatch) < 3 {
			continue
		}

		durationMs, _ := strconv.ParseFloat(totalMatch[1], 64)
		_, _ = strconv.Atoi(totalMatch[2])

		// 向前搜索 prompt/eval 行
		for j := i - 1; j >= 0 && j > i-5; j-- {
			nextLine := tailLines[j]
			if pm := regexp.MustCompile(`prompt eval time =\s+([\d.]+)\s+ms\s*/\s*(\d+)\s+tokens`).FindStringSubmatch(nextLine); len(pm) >= 3 {
				ms, _ := strconv.ParseFloat(pm[1], 64)
				tok, _ := strconv.Atoi(pm[2])
				m.LastPromptMs = ms
				m.LastPromptTokens = tok
			}
			if !strings.Contains(nextLine, "prompt eval") {
				if em := regexp.MustCompile(`eval time =\s+([\d.]+)\s+ms\s*/\s*(\d+)\s+tokens`).FindStringSubmatch(nextLine); len(em) >= 3 {
					ms, _ := strconv.ParseFloat(em[1], 64)
					tok, _ := strconv.Atoi(em[2])
					m.LastEvalMs = ms
					m.LastEvalTokens = tok
					if ms > 0 {
						m.LastTPS = float64(tok) / ms * 1000
					}
				}
			}
		}
		m.LastLatencyMs = durationMs
		break // 只取最新一条
	}
	return m
}

func collectLLMData(cfg *shared.Config, hc *shared.HTTPClient) *shared.LLMMetrics {
	m := &shared.LLMMetrics{}
	baseURL := cfg.Services.LlamaServer.URL
	metricsURL := baseURL + cfg.Services.LlamaServer.MetricsPath

	resp, err := hc.Get(metricsURL)
	if err != nil {
		return m
	}
	defer resp.Body.Close()

	buf := make([]byte, 131072)
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

	if v, ok := metrics["llamacpp:prompt_tokens_seconds"]; ok && v > 0 {
		m.PromptTokensPS = v
		m.PromptMsPerToken = 1000.0 / v
	}
	if v, ok := metrics["llamacpp:predicted_tokens_seconds"]; ok && v > 0 {
		m.TPOT = 1000.0 / v
	}
	if v, ok := metrics["llamacpp:speculative_accept_rate"]; ok {
		m.SpecAcceptRate = v
	}
	// Fallback: parse draft acceptance from log file
	if m.SpecAcceptRate == 0 {
		da := shared.ParseDraftAcceptance("/tmp/llama-server.log")
		if da.Generated > 0 {
			m.SpecAcceptRate = da.Rate
			m.SpecDraftLen = da.Generated / max(da.Accepted, 1)
			m.SpecAcceptedCount = da.Accepted
			m.SpecSpeedup = 1 + da.Rate*2
		}
	}
	if v, ok := metrics["llamacpp:kv_cache_usage_ratio"]; ok {
		m.KVCacheUsedPct = v * 100
	}
	if v, ok := metrics["llamacpp:prompt_tokens_total"]; ok {
		m.PromptTokensTotal = int(v)
	}
	if v, ok := metrics["llamacpp:tokens_predicted_total"]; ok {
		m.EvalTokensTotal = int(v)
	}

	return m
}

func calcHealthScore(tick shared.SSETick) (int, []shared.HealthReason) {
	score := 100
	var reasons []shared.HealthReason

	if tick.GPUs != nil && tick.GPUs.Aggregate != nil {
		agg := tick.GPUs.Aggregate
		if agg.Temp > 85 {
			score -= 30
			reasons = append(reasons, shared.HealthReason{Item: "GPU Temp", Value: fmt.Sprintf("%.1f°C", agg.Temp), Penalty: 30, Level: "critical"})
		} else if agg.Temp > 75 {
			score -= 10
			reasons = append(reasons, shared.HealthReason{Item: "GPU Temp", Value: fmt.Sprintf("%.1f°C", agg.Temp), Penalty: 10, Level: "warning"})
		}
		if agg.MemUtilPct > 95 {
			score -= 25
			reasons = append(reasons, shared.HealthReason{Item: "GPU Mem", Value: fmt.Sprintf("%.1f%%", agg.MemUtilPct), Penalty: 25, Level: "critical"})
		} else if agg.MemUtilPct > 85 {
			score -= 10
			reasons = append(reasons, shared.HealthReason{Item: "GPU Mem", Value: fmt.Sprintf("%.1f%%", agg.MemUtilPct), Penalty: 10, Level: "warning"})
		}
	}

	if tick.System != nil {
		if tick.System.MemUsedPct > 95 {
			score -= 20
			reasons = append(reasons, shared.HealthReason{Item: "Sys Mem", Value: fmt.Sprintf("%.1f%%", tick.System.MemUsedPct), Penalty: 20, Level: "critical"})
		}
	}

	if tick.Inference != nil {
		if tick.Inference.KVCacheUsedPct > 98 {
			score -= 20
			reasons = append(reasons, shared.HealthReason{Item: "KV Cache", Value: fmt.Sprintf("%.1f%%", tick.Inference.KVCacheUsedPct), Penalty: 20, Level: "critical"})
		}
	}

	if score < 0 {
		score = 0
	}

	return score, reasons
}

func getUptime() string {
	cmd := exec.Command("sh", "-c", "cat /proc/uptime")
	out, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	parts := strings.Fields(string(out))
	if len(parts) < 1 {
		return "unknown"
	}
	secs, _ := strconv.ParseFloat(parts[0], 64)
	d := int(secs) / 86400
	h := (int(secs) % 86400) / 3600
	m := (int(secs) % 3600) / 60
	s := int(secs) % 60
	return fmt.Sprintf("%dd %dh %dm %ds", d, h, m, s)
}

// ===== Panel Data =====

func handlePanelData(cfg *shared.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		hc := shared.NewHTTPClient(cfg.Services.LlamaServer.TimeoutSec, cfg.GetAPIKey())

		switch name {
		case "overview":
			data := buildOverviewData(cfg, hc)
			c.JSON(200, data)
		case "inference":
			data := buildInferencePanelData(cfg, hc)
			c.JSON(200, data)
		case "compute":
			data := buildComputePanelData(cfg)
			c.JSON(200, data)
		case "system":
			data := buildSystemPanelData()
			c.JSON(200, data)
		case "hardware":
			data := buildHardwarePanelData(cfg)
			c.JSON(200, data)
		case "services":
			data := buildServicesPanelData(cfg)
			c.JSON(200, data)
		default:
			c.JSON(404, gin.H{"error": "Unknown panel: " + name})
		}
	}
}

func buildOverviewData(cfg *shared.Config, hc *shared.HTTPClient) gin.H {
	var wg sync.WaitGroup
	var gpus *shared.GPUAggregate
	var sys *shared.SystemMetrics
	var inf *shared.InferenceMetrics
	var llm *shared.LLMMetrics
	wg.Add(4)
	go func() { defer wg.Done(); gpus = cachedGPUData(cfg) }()
	go func() { defer wg.Done(); sys = cachedSystemData() }()
	go func() { defer wg.Done(); inf = cachedInferenceData(cfg, hc) }()
	go func() { defer wg.Done(); llm = cachedLLMData(cfg, hc) }()
	wg.Wait()

	data := gin.H{"api_version": "v1", "timestamp": time.Now().Unix()}

	// === GPU: gpus数组 + 首卡作为gpu ===
	if gpus != nil {
		data["gpus"] = gpus.GPUs
		if len(gpus.GPUs) > 0 {
			data["gpu"] = gpus.GPUs[0]
		}
	}

	// === CPU 嵌套对象 ===
	if sys != nil {
		data["cpu"] = gin.H{
			"usage":          sys.CPUUtil,
			"model":          sys.CPUModel,
			"freq_current":   fmt.Sprintf("%.0f", sys.CPUFreqCur),
			"max_mhz":        fmt.Sprintf("%.4f", sys.CPUFreqMax),
			"temp_tctl":      sys.CPUTemp,
			"physical_cores": sys.CPUPhysical,
			"logical_cores":  sys.CPULogical,
			"virt":           sys.CPUVirt,
			"l2":             formatCacheInfo(sys.CPUL2, "L2"),
			"l3":             formatCacheInfo(sys.CPUL3, "L3"),
			"per_core":       sys.CPUPerCore,
			"load1":          fmt.Sprintf("%.2f", sys.Load1),
			"load5":          fmt.Sprintf("%.2f", sys.Load5),
			"load15":         fmt.Sprintf("%.2f", sys.Load15),
			"processes":      sys.ProcessCount,
			"threads":        sys.CPULogical,
			"handles":        0,
		}

		// === Memory ===
		memUsedGB := float64(sys.MemUsed) / 1073741824
		memTotalGB := float64(sys.MemTotal) / 1073741824
		swapUsedPct := 0.0
		if sys.SwapTotal > 0 {
			swapUsedPct = float64(sys.SwapUsed) / float64(sys.SwapTotal) * 100
		}
		data["memory"] = gin.H{
			"percent":    sys.MemUsedPct,
			"used":       sys.MemUsed,
			"free":       sys.MemAvailable,
			"total":      sys.MemTotal,
			"used_str":   fmt.Sprintf("%.1f GB", memUsedGB),
			"free_str":   fmt.Sprintf("%.1f GB", float64(sys.MemAvailable)/1073741824),
			"total_str":  fmt.Sprintf("%.1f GB", memTotalGB),
			"cached":     sys.MemCached,
			"buffers":    sys.MemBuffers,
			"swap_used":  sys.SwapUsed,
			"swap_total": sys.SwapTotal,
			"swap_pct":   swapUsedPct,
		}

		// === Disk IO ===
		data["disk_io"] = gin.H{
			"active_pct": sys.DiskActivePct,
			"read_str":   fmt.Sprintf("%.1f MB/s", sys.DiskReadBps/1048576),
			"write_str":  fmt.Sprintf("%.1f MB/s", sys.DiskWriteBps/1048576),
		}

		// === Disk Model ===
		data["disk_model"] = gin.H{
			"model": sys.DiskModel,
			"type":  sys.DiskType,
			"size":  sys.DiskSize,
		}

		// === Disks: mountpoint basename as key ===
		disksMap := gin.H{}
		for _, d := range sys.Disks {
			label := d.Mountpoint
			if idx := strings.LastIndex(label, "/"); idx >= 0 {
				label = label[idx+1:]
			}
			if label == "" {
				label = "root"
			}
			disksMap[label] = gin.H{
				"label":   label,
				"mount":   d.Mountpoint,
				"percent": d.UsedPct,
				"used":    d.Used,
				"total":   d.Total,
			}
		}
		data["disks"] = disksMap
		data["nvme_temp"] = sys.NvmeTemp

		// === Network ===
		data["network"] = gin.H{
			"adapter":    sys.NetAdapter,
			"vendor":     sys.NetVendor,
			"link_speed": getNetLinkSpeed(sys.NetAdapter),
			"ipv4":       sys.NetIPv4,
			"recv_str":   fmtNetSpeed(sys.NetRecvBps),
			"sent_str":   fmtNetSpeed(sys.NetSentBps),
			"recv_bps":   sys.NetRecvBps,
			"sent_bps":   sys.NetSentBps,
			"rx_mbps":    round1(sys.NetRecvBps * 8 / 1000000),
			"tx_mbps":    round1(sys.NetSentBps * 8 / 1000000),
		}

		data["cpu_per_core"] = sys.CPUPerCore
	}

	// === Inference ===
	if inf != nil {
		data["inference_stats"] = inf
	}
	if llm != nil {
		data["llm_metrics"] = llm
	}

	// === KV Cache: 完整summary+cards ===
	if globalKVEngine != nil && gpus != nil {
		numGPU := 0
		for _, g := range gpus.GPUs {
			if g.Name != "" && !strings.Contains(g.Name, "Aggregate") {
				numGPU++
			}
		}
		if numGPU > 0 {
			kvResult := globalKVEngine.Compute(gpus.GPUs, numGPU)
			data["kv_cache"] = toSharedKVMetrics(kvResult)
		}
	}
	if data["kv_cache"] == nil {
		data["kv_cache"] = gin.H{"summary": gin.H{}, "cards": []interface{}{}, "captured": false}
	}

	// === History ===
	collectLocalHistory(sys, gpus)
	data["history"] = getHistory(cfg)

	// === Health ===
	score, reasons := calcHealthScore(shared.SSETick{GPUs: gpus, System: sys, Inference: inf})
	data["health_score"] = score
	data["health_reasons"] = reasons
	data["uptime"] = getSystemUptime()

	// === Script/Deploy config ===
	scriptData := parseLlamaScript()
	for k, v := range scriptData {
		data[k] = v
	}

	// === Logs, IP stats, request sources ===
	logMTime := int64(0)
	if fi, err := os.Stat("/tmp/llama-server.log"); err == nil {
		logMTime = fi.ModTime().Unix()
	}
	logs, _, _ := shared.ParseLlamaLogsEx("/tmp/llama-server.log", 20, logMTime)
	if logs == nil {
		logs = []shared.LogEntry{}
	}
	// IP统计和请求来源从 nginx access log 获取（不依赖 new-api）
	nginxIPStats, nginxReqSources, _ := shared.ParseNginxAccessLog("/var/log/nginx/llama_access.log", 500)
	data["logs"] = logs
	data["ip_stats"] = nginxIPStats
	data["request_sources"] = nginxReqSources

	// === Model tags (unified across frontends) ===
	var modelTags []string
	var modelQuantType string
	var modelAlias string
	if models := fetchModelsList(cfg, hc); models != nil {
		data["models"] = models
		if rm, _ := data["running_model"].(string); rm != "" {
			if modelList, _ := models.([]interface{}); modelList != nil {
				for _, m := range modelList {
					if mm, _ := m.(map[string]interface{}); mm != nil {
						if name, _ := mm["name"].(string); name == rm {
							if tags, _ := mm["tags"].([]interface{}); tags != nil {
								for _, t := range tags {
									if ts, ok := t.(string); ok {
										modelTags = append(modelTags, ts)
									}
								}
							}
							if qt, _ := mm["quant_type"].(string); qt != "" {
								modelQuantType = qt
							}
							if al, _ := mm["alias"].(string); al != "" {
								modelAlias = al
							}
							break
						}
					}
				}
			}
		}
	}

	// === Services (兼容层) ===
	svcMap := buildServicesMap(cfg)
	data["services"] = buildServicesStatus(svcMap, cfg)
	data["service_running"] = svcMap["llama-server"] == "healthy"

	// === Model tags (unified across frontends) ===
	if len(modelTags) > 0 {
		data["model_tags"] = modelTags
	}
	if modelQuantType != "" {
		data["model_quant_type"] = modelQuantType
	}
	if modelAlias != "" {
		data["model_alias"] = modelAlias
	}

	return data
}

// Cache docker container list (5s TTL)
var (
	_dockerContainers     map[string]bool
	_dockerContainersTime time.Time
	_dockerContainersTTL  = 30 * time.Second
)

func getCachedDockerContainers() map[string]bool {
	if _dockerContainers != nil && time.Since(_dockerContainersTime) < _dockerContainersTTL {
		return _dockerContainers
	}
	result := map[string]bool{}
	if out, err := exec.Command("sh", "-c", "docker ps --format '{{.Names}}' 2>/dev/null").Output(); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line != "" {
				result[line] = true
			}
		}
	}
	_dockerContainers = result
	_dockerContainersTime = time.Now()
	return result
}

// buildServicesStatus 构建服务状态列表
func buildServicesStatus(svcMap map[string]string, cfg *shared.Config) gin.H {
	result := gin.H{}
	llamaPid := findPid("llama-server")
	dockerPid := findPid("dockerd")
	sshPid := findPid("sshd")
	cfPid := findPid("cloudflared")
	benchmarkPid := findPid("benchmark")
	dockerContainers := getCachedDockerContainers()
	llamaRunning := svcMap["llama-server"] == "healthy" || llamaPid > 0
	inferenceSvc := gin.H{"status": boolStr(llamaRunning), "detail": "", "port": "8080"}
	inferenceSvc = enrichInferenceService(inferenceSvc, cfg)
	result["\u63a8\u7406\u670d\u52a1"] = inferenceSvc
	result["\u8c03\u5ea6\u811a\u672c"] = gin.H{"status": boolStr(llamaPid > 0), "detail": "", "port": "-"}
	result["vLLM"] = gin.H{"status": boolStr(dockerContainers["vllm"]), "detail": "", "port": "-"}
	result["llama.cpp"] = gin.H{"status": boolStr(llamaRunning), "detail": fmt.Sprintf("PID: %d", llamaPid), "port": "8080"}
	result["Docker"] = gin.H{"status": boolStr(dockerPid > 0), "detail": fmt.Sprintf("PID: %d", dockerPid), "port": "-"}
	result["SSH"] = gin.H{"status": boolStr(sshPid > 0), "detail": fmt.Sprintf("PID: %d", sshPid), "port": "22"}
	result["LLM\u6d4b\u901f"] = gin.H{"status": boolStr(benchmarkPid > 0), "detail": fmt.Sprintf("PID: %d", benchmarkPid), "port": "8090"}
	result["Cloudflare\u96a7\u9053"] = gin.H{"status": boolStr(cfPid > 0), "detail": fmt.Sprintf("PID: %d", cfPid), "port": "-"}
	return result
}

// enrichInferenceService enriches the 推理服务 entry with full data from process cmdline, VERSION.json, and /props API
func enrichInferenceService(svc gin.H, cfg *shared.Config) gin.H {
	pid := findPid("llama-server")
	if pid == 0 {
		return svc
	}

	// 1. Read process cmdline from /proc
	cmdline := ""
	if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
		cmdline = strings.ReplaceAll(string(data), "\x00", " ")
	}

	// 2. Extract model_file from cmdline
	modelFile := ""
	if m := regexp.MustCompile(`-m\s+(\S+)`).FindStringSubmatch(cmdline); len(m) > 1 {
		modelFile = m[1]
	}
	svc["model_file"] = modelFile
	svc["pid"] = pid

	// 3. Extract config from cmdline flags
	config := gin.H{}
	flagMap := map[string]string{
		"--ctx-size": "ctx_size", "-ngl": "ngl", "-b": "batch", "-ub": "ubatch",
		"--cache-type-k": "cache_type_k", "--cache-type-v": "cache_type_v",
		"--flash-attn": "flash_attn", "-t": "threads", "--threads-http": "threads_http",
		"--temp": "temp", "-np": "np", "--reasoning": "reasoning",
		"--mmproj": "mmproj", "--spec-type": "spec_type",
		"--spec-draft-n-max": "spec_draft_n_max",
	}
	intKeys := map[string]bool{"ctx_size": true, "ngl": true, "batch": true, "ubatch": true, "threads": true, "threads_http": true, "np": true, "spec_draft_n_max": true}
	for flag, key := range flagMap {
		re := regexp.MustCompile(regexp.QuoteMeta(flag) + `\s+(\S+)`)
		if m := re.FindStringSubmatch(cmdline); len(m) > 1 {
			val := m[1]
			if intKeys[key] {
				if iv, err := strconv.Atoi(val); err == nil {
					config[key] = iv
				} else {
					config[key] = val
				}
			} else {
				config[key] = val
			}
		}
	}
	// flash_attn: check if flag exists (no value)
	if strings.Contains(cmdline, "--flash-attn") {
		if _, ok := config["flash_attn"]; !ok {
			config["flash_attn"] = "on"
		}
	}
	// chunked_batch: check -cb flag
	config["chunked_batch"] = "off"
	if strings.Contains(cmdline, "-cb") || strings.Contains(cmdline, "--chunked-batch") {
		config["chunked_batch"] = "on"
	}

	svc["ngl"] = config["ngl"]
	svc["ctx_size"] = config["ctx_size"]

	// 4. Extract engine info from binary path
	binaryPath := ""
	if parts := strings.Split(cmdline, " "); len(parts) > 0 {
		binaryPath = parts[0]
	}
	if m := regexp.MustCompile(`/engines/([^/]+)/`).FindStringSubmatch(binaryPath); len(m) > 1 {
		engine := m[1]
		svc["engine"] = engine
		svc["engine_type"] = "llama"
		// Read VERSION.json
		vjPath := fmt.Sprintf("/data/engines/%s/VERSION.json", engine)
		if vjData, err := os.ReadFile(vjPath); err == nil {
			var vj map[string]interface{}
			if json.Unmarshal(vjData, &vj) == nil {
				if branch, ok := vj["branch"].(string); ok {
					svc["llama_version"] = branch
				}
				if ver, ok := vj["version"].(string); ok {
					svc["version"] = ver
				}
			}
		}
	}

	// 5. Fetch /props API for runtime params (cached 30s)
	if props := cachedPropsData(cfg); props != nil {
		if dgs, ok := props["default_generation_settings"].(map[string]interface{}); ok {
			if v, ok := dgs["n_ctx"].(float64); ok {
				config["n_ctx_per_slot"] = int(v)
			}
			if v, ok := dgs["temperature"].(float64); ok {
				config["temperature"] = v
			}
			if v, ok := dgs["top_k"].(float64); ok {
				config["top_k"] = int(v)
			}
			if v, ok := dgs["top_p"].(float64); ok {
				config["top_p"] = v
			}
			if v, ok := dgs["min_p"].(float64); ok {
				config["min_p"] = v
			}
		}
		if v, ok := props["total_slots"].(float64); ok {
			config["total_slots"] = int(v)
		}
		if v, ok := props["chat_format"].(string); ok {
			config["chat_format"] = v
		} else {
			config["chat_format"] = "unknown"
		}
		if v, ok := props["hostname"].(string); ok {
			config["host"] = v
		} else {
			config["host"] = "127.0.0.1"
		}
		if v, ok := props["port"].(float64); ok {
			config["port"] = int(v)
		} else {
			config["port"] = 8080
		}

	}

	// 6. Check vision support
	visionLoaded := false
	if modelFile != "" && config["mmproj"] != "off" {
		base := filepath.Dir(modelFile)
		if entries, err := os.ReadDir(base); err == nil {
			for _, e := range entries {
				if strings.Contains(strings.ToLower(e.Name()), "mmproj") && strings.HasSuffix(e.Name(), ".gguf") {
					visionLoaded = true
					break
				}
			}
		}
	}
	svc["vision_loaded"] = visionLoaded
	svc["config"] = config

	return svc
}

// findPid finds a process by name via /proc (no shell spawn)
func findPid(name string) int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, e := range entries {
		if e.Name() == "" || e.Name()[0] < '0' || e.Name()[0] > '9' {
			continue
		}
		data, err := os.ReadFile("/proc/" + e.Name() + "/cmdline")
		if err != nil {
			continue
		}
		cmdline := strings.ReplaceAll(string(data), "\x00", " ")
		parts := strings.Fields(cmdline)
		if len(parts) == 0 {
			continue
		}
		// Check binary name: exact match, path suffix, or colon-suffixed (e.g. "sshd:")
		bin := strings.TrimRight(parts[0], ":")
		if bin == name || strings.HasSuffix(bin, "/"+name) {
			pid, _ := strconv.Atoi(e.Name())
			return pid
		}
	}
	return 0
}

func boolStr(b bool) string {
	if b {
		return "running"
	}
	return "stopped"
}

// Cache lscpu output (CPU info doesn't change at runtime)
var (
	_lscpuCache string
	_lscpuOnce  sync.Once
)

func getLscpuOutput() string {
	_lscpuOnce.Do(func() {
		if out, err := exec.Command("sh", "-c", "LANG=C lscpu 2>/dev/null").Output(); err == nil {
			_lscpuCache = string(out)
		}
	})
	return _lscpuCache
}

func formatCacheInfo(raw string, level string) string {
	if raw == "" {
		return "--"
	}
	instances := 1
	lscpu := getLscpuOutput()
	for _, line := range strings.Split(lscpu, "\n") {
		if strings.Contains(line, level+" cache:") {
			if idx := strings.Index(line, "("); idx >= 0 {
				rest := line[idx+1:]
				if end := strings.Index(rest, ")"); end >= 0 {
					instStr := strings.TrimSpace(rest[:end])
					if strings.Contains(instStr, "instance") {
						parts := strings.Fields(instStr)
						if len(parts) > 0 {
							fmt.Sscanf(parts[0], "%d", &instances)
						}
					}
				}
			}
			break
		}
	}
	// Format the size
	sizeStr := raw
	if strings.HasSuffix(raw, "K") || strings.HasSuffix(raw, "KB") {
		if v, err := strconv.ParseFloat(strings.TrimRight(raw, "Kk"), 64); err == nil {
			if v >= 1024 {
				sizeStr = fmt.Sprintf("%.0f MiB", v/1024)
			} else {
				sizeStr = fmt.Sprintf("%.0f KiB", v)
			}
		}
	} else if strings.HasSuffix(raw, "M") {
		if v, err := strconv.ParseFloat(strings.TrimRight(raw, "Mm"), 64); err == nil {
			sizeStr = fmt.Sprintf("%.0f MiB", v)
		}
	}
	if instances > 1 {
		return fmt.Sprintf("%s (%d instances)", sizeStr, instances)
	}
	return fmt.Sprintf("%s (1 instance)", sizeStr)
}

func getNetLinkSpeed(adapter string) string {
	if adapter == "" {
		return "--"
	}
	if data, err := os.ReadFile("/sys/class/net/" + adapter + "/speed"); err == nil {
		if speed, e := strconv.Atoi(strings.TrimSpace(string(data))); e == nil {
			if speed >= 1000 {
				return fmt.Sprintf("%d Gbps", speed/1000)
			}
			return fmt.Sprintf("%d Mbps", speed)
		}
	}
	return "--"
}

func getSystemUptime() string {
	if up, err := os.ReadFile("/proc/uptime"); err == nil {
		if secs, err := strconv.ParseFloat(strings.Fields(string(up))[0], 64); err == nil {
			d := int(secs) / 86400
			h := (int(secs) % 86400) / 3600
			return fmt.Sprintf("%d\u5929 %d\u65f6", d, h)
		}
	}
	return getUptime()
}

var canonicalHistoryCache = struct {
	sync.RWMutex
	data gin.H
	at   time.Time
}{}

func emptyCanonicalHistory() gin.H {
	return gin.H{
		"gpu_util": []float64{}, "gpu_mem_pct": []float64{}, "gpu_temp": []float64{}, "gpu_power": []float64{},
		"cpu_usage": []float64{}, "cpu_freq": []float64{}, "mem_usage": []float64{}, "mem_used_gb": []float64{},
		"net_recv": []float64{}, "net_sent": []float64{}, "disk_active": []float64{}, "disk_read": []float64{}, "disk_write": []float64{},
	}
}

func queryCanonicalHistory(cfg *shared.Config) gin.H {
	canonicalHistoryCache.RLock()
	if canonicalHistoryCache.data != nil && time.Since(canonicalHistoryCache.at) < 30*time.Second {
		data := canonicalHistoryCache.data
		canonicalHistoryCache.RUnlock()
		return data
	}
	canonicalHistoryCache.RUnlock()

	canonicalHistoryCache.Lock()
	defer canonicalHistoryCache.Unlock()
	if canonicalHistoryCache.data != nil && time.Since(canonicalHistoryCache.at) < 30*time.Second {
		return canonicalHistoryCache.data
	}

	metricToKey := map[string]string{
		"gpu_util": "gpu_util", "gpu_mem_util_pct": "gpu_mem_pct", "gpu_temp": "gpu_temp", "gpu_power_draw": "gpu_power",
		"cpu_util": "cpu_usage", "cpu_freq_current": "cpu_freq", "mem_used_pct": "mem_usage", "mem_used_gb": "mem_used_gb",
		"net_recv_bps": "net_recv", "net_sent_bps": "net_sent", "disk_active_pct": "disk_active", "disk_read_bps": "disk_read", "disk_write_bps": "disk_write",
	}
	result := emptyCanonicalHistory()
	query := `{__name__=~"gpu_util|gpu_mem_util_pct|gpu_temp|gpu_power_draw|cpu_util|cpu_freq_current|mem_used_pct|mem_used_gb|net_recv_bps|net_sent_bps|disk_active_pct|disk_read_bps|disk_write_bps"}`
	end := time.Now().Unix()
	start := end - 3600
	vmURL := fmt.Sprintf("%s%s?query=%s&start=%d&end=%d&step=30", cfg.Services.VictoriaMetrics.URL, cfg.Services.VictoriaMetrics.QueryRangePath, url.QueryEscape(query), start, end)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(vmURL)
	if err != nil {
		if canonicalHistoryCache.data != nil {
			return canonicalHistoryCache.data
		}
		return result
	}
	defer resp.Body.Close()
	var payload struct {
		Status string `json:"status"`
		Data   struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
				Values [][]interface{}   `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if resp.StatusCode >= 300 || json.NewDecoder(resp.Body).Decode(&payload) != nil || payload.Status != "success" {
		if canonicalHistoryCache.data != nil {
			return canonicalHistoryCache.data
		}
		return result
	}
	for _, series := range payload.Data.Result {
		name := series.Metric["__name__"]
		key, ok := metricToKey[name]
		if !ok {
			continue
		}
		if strings.HasPrefix(name, "gpu_") && series.Metric["gpu"] != "aggregate" {
			continue
		}
		values := make([]float64, 0, len(series.Values))
		for _, point := range series.Values {
			if len(point) < 2 {
				continue
			}
			text, ok := point[1].(string)
			if !ok {
				continue
			}
			value, parseErr := strconv.ParseFloat(text, 64)
			if parseErr == nil {
				values = append(values, value)
			}
		}
		if len(values) > 120 {
			values = values[len(values)-120:]
		}
		result[key] = values
	}
	canonicalHistoryCache.data = result
	canonicalHistoryCache.at = time.Now()
	return result
}

// getHistory returns durable VictoriaMetrics history. The local ring remains
// only as a legacy fallback for old panel routes.
func getHistory(cfg *shared.Config) gin.H {
	return queryCanonicalHistory(cfg)
}

func buildInferencePanelData(cfg *shared.Config, hc *shared.HTTPClient) gin.H {
	inf := cachedInferenceData(cfg, hc)
	llm := cachedLLMData(cfg, hc)
	logMTime := int64(0)
	if fi, err := os.Stat("/tmp/llama-server.log"); err == nil {
		logMTime = fi.ModTime().Unix()
	}
	logs, _, _ := shared.ParseLlamaLogsEx("/tmp/llama-server.log", 20, logMTime)
	if logs == nil {
		logs = []shared.LogEntry{}
	}
	nginxIPStats, nginxReqSources, _ := shared.ParseNginxAccessLog("/var/log/nginx/llama_access.log", 500)
	return gin.H{
		"inference":       inf,
		"llm":             llm,
		"logs":            logs,
		"ip_stats":        nginxIPStats,
		"request_sources": nginxReqSources,
		"timestamp":       time.Now().Unix(),
	}
}

func buildComputePanelData(cfg *shared.Config) gin.H {
	gpus := cachedGPUData(cfg)
	return gin.H{
		"gpus":      gpus,
		"timestamp": time.Now().Unix(),
	}
}

func buildSystemPanelData() gin.H {
	sys := cachedSystemData()

	// OS info
	osInfo := gin.H{}
	if h, err := os.Hostname(); err == nil {
		osInfo["hostname"] = h
	}
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "PRETTY_NAME=") {
				osInfo["os_name"] = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"")
				break
			}
		}
	}
	var uts syscall.Utsname
	if err := syscall.Uname(&uts); err == nil {
		osInfo["kernel"] = string(runeSlice(uts.Release[:]))
		osInfo["arch"] = string(runeSlice(uts.Machine[:]))
	}
	if tz, err := time.LoadLocation("Local"); err == nil {
		osInfo["timezone"] = tz.String()
	}
	if up, err := os.ReadFile("/proc/uptime"); err == nil {
		if secs, err := strconv.ParseFloat(strings.Fields(string(up))[0], 64); err == nil {
			d := int(secs) / 86400
			h := (int(secs) % 86400) / 3600
			m := (int(secs) % 3600) / 60
			osInfo["uptime"] = fmt.Sprintf("%dd %dh %dm", d, h, m)
		}
	}
	// Users count
	if out, err := exec.Command("who").Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		if len(lines) == 1 && lines[0] == "" {
			osInfo["users"] = 0
		} else {
			osInfo["users"] = len(lines)
		}
	}

	// Build result with OS info
	result := gin.H{
		"cpu_util": sys.CPUUtil, "cpu_per_core": sys.CPUPerCore,
		"cpu_model": sys.CPUModel, "cpu_physical_cores": sys.CPUPhysical,
		"cpu_logical_cores": sys.CPULogical, "cpu_max_mhz": sys.CPUFreqMax,
		"cpu_freq_current": sys.CPUFreqCur, "cpu_virt": sys.CPUVirt,
		"cpu_l2": sys.CPUL2, "cpu_l3": sys.CPUL3, "cpu_temp_tctl": sys.CPUTemp,
		"mem_total": sys.MemTotal, "mem_used": sys.MemUsed, "mem_free": sys.MemFree,
		"mem_used_pct": sys.MemUsedPct, "mem_available": sys.MemAvailable,
		"mem_buffers": sys.MemBuffers, "mem_cached": sys.MemCached,
		"swap_total": sys.SwapTotal, "swap_used": sys.SwapUsed,
		"swap_free": sys.SwapFree, "swap_used_pct": sys.SwapUsedPct,
		"disks": sys.Disks, "disk_model": sys.DiskModel, "disk_type": sys.DiskType,
		"disk_size": sys.DiskSize, "nvme_temp": sys.NvmeTemp,
		"disk_read_bps": sys.DiskReadBps, "disk_write_bps": sys.DiskWriteBps,
		"disk_active_pct": sys.DiskActivePct,
		"load_1":          sys.Load1, "load_5": sys.Load5, "load_15": sys.Load15,
		"process_count": sys.ProcessCount,
		"net_adapter":   sys.NetAdapter, "net_ipv4": sys.NetIPv4,
		"net_link_speed": sys.NetLinkSpeed, "net_vendor": sys.NetVendor,
		"net_bytes_sent": sys.NetBytesSent, "net_bytes_recv": sys.NetBytesRecv,
	}
	for k, v := range osInfo {
		result[k] = v
	}

	// Services
	svcs := buildServicesList(shared.GetConfig())
	llamaParams := parseLlamaScript()
	for i, svc := range svcs {
		if svc["name"] == "llama-server" {
			svcs[i]["params"] = llamaParams["params"]
			svcs[i]["deploy_config"] = llamaParams["deploy_config"]
			svcs[i]["running_model"] = llamaParams["running_model"]
			svcs[i]["active_engine"] = llamaParams["active_engine"]
			break
		}
	}

	return gin.H{
		"system":    result,
		"services":  svcs,
		"timestamp": time.Now().Unix(),
	}
}

// runeSlice converts []int8 to string (for syscall.Utsname)
func runeSlice(s []int8) string {
	r := make([]rune, 0, len(s))
	for _, c := range s {
		if c == 0 {
			break
		}
		r = append(r, rune(c))
	}
	return string(r)
}

func buildHardwarePanelData(cfg *shared.Config) gin.H {
	gpus := cachedGPUData(cfg)
	sys := cachedSystemData()
	gpuHist := queryGPUHistory(cfg)
	sysHist := querySystemHistory(cfg)
	return gin.H{
		"gpus":        gpus,
		"system":      sys,
		"gpu_history": gpuHist,
		"sys_history": sysHist,
		"timestamp":   time.Now().Unix(),
	}
}

func buildServicesPanelData(cfg *shared.Config) gin.H {
	services := buildServicesList(cfg)
	links := gin.H{
		"inference":       cfg.Services.LlamaServer.URL,
		"new_api":         cfg.Services.NewAPI.URL,
		"model_manager":   cfg.Services.ModelManager.URL,
		"cluster":         cfg.Services.ClusterConfig.URL,
		"benchmark":       cfg.Services.Benchmark.URL,
		"searxng":         "http://127.0.0.1:8888",
		"victoriametrics": cfg.Services.VictoriaMetrics.URL,
	}
	return gin.H{
		"services":  services,
		"links":     links,
		"timestamp": time.Now().Unix(),
	}
}

// ===== Helper Functions for Enriched Panels =====

// Cache parseLlamaScript result (10s TTL)
var (
	_parseScriptCache map[string]interface{}
	_parseScriptTime  time.Time
	_parseScriptTTL   = 60 * time.Second
)

func parseLlamaScript() map[string]interface{} {
	if _parseScriptCache != nil && time.Since(_parseScriptTime) < _parseScriptTTL {
		return _parseScriptCache
	}
	scriptPath := "/usr/local/bin/start-llama-server.sh"
	result := map[string]interface{}{}
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		result["running_model"] = "unknown"
		result["model_path"] = ""
		result["active_engine"] = "unknown"
		result["deploy_config"] = gin.H{}
		result["params"] = gin.H{}
		result["persist"] = gin.H{"mode": "auto", "label": "auto"}
		return result
	}
	script := string(data)
	modelPath := parseScriptValue(script, "-m")
	modelName := ""
	if idx := strings.LastIndex(modelPath, "/"); idx >= 0 {
		modelName = modelPath[idx+1:]
	}
	alias := parseScriptValue(script, "-a")
	engine := "llama-cpp"
	if strings.Contains(script, "turboquant") || strings.Contains(script, "TURBO_") {
		engine = "turboquant"
	}
	params := gin.H{
		"ctx_size":           shared.ParseIntFromScript(script, "--ctx-size", 262144),
		"ngl":                shared.ParseIntFromScript(script, "-ngl", 99),
		"batch":              shared.ParseIntFromScript(script, "-b", 512),
		"ubatch":             shared.ParseIntFromScript(script, "-ub", 256),
		"np":                 shared.ParseIntFromScript(script, "-np", 2),
		"threads":            shared.ParseIntFromScript(script, "-t", 8),
		"threads_http":       shared.ParseIntFromScript(script, "--threads-http", 4),
		"cache_type_k":       shared.ParseStringFromScript(script, "--cache-type-k", "turbo4"),
		"cache_type_v":       shared.ParseStringFromScript(script, "--cache-type-v", "turbo4"),
		"flash_attn":         shared.ParseStringFromScript(script, "--flash-attn", "on"),
		"temp":               shared.ParseFloatFromScript(script, "--temp", 0.6),
		"reasoning":          shared.ParseStringFromScript(script, "--reasoning", "off"),
		"chunked_batch":      "off",
		"split_mode":         shared.ParseStringFromScript(script, "--split-mode", ""),
		"fit":                shared.ParseStringFromScript(script, "--fit", ""),
		"cache_ram":          shared.ParseIntFromScript(script, "--cache-ram", 0),
		"sleep_idle_seconds": shared.ParseIntFromScript(script, "--sleep-idle-seconds", 0),
		"tensor_split":       shared.ParseStringFromScript(script, "--tensor-split", ""),
		"spec_draft_n_max":   shared.ParseIntFromScript(script, "--spec-draft-n-max", 0),
		"draft_k_cache_type": shared.ParseStringFromScript(script, "--cache-type-k-draft", ""),
		"draft_v_cache_type": shared.ParseStringFromScript(script, "--cache-type-v-draft", ""),
	}
	if strings.Contains(script, "-cb") {
		params["chunked_batch"] = "on"
	}
	gpuVal := "all"
	if strings.Contains(script, "CUDA_VISIBLE_DEVICES=0") {
		gpuVal = "3080"
	} else if strings.Contains(script, "CUDA_VISIBLE_DEVICES=1") {
		gpuVal = "3060"
	}
	params["gpu"] = gpuVal
	deployConfig := gin.H{
		"model": modelName, "model_path": modelPath, "alias": alias,
		"ngl": params["ngl"], "ctx_size": params["ctx_size"],
		"concurrency": params["np"], "k_cache_type": params["cache_type_k"],
		"v_cache_type": params["cache_type_v"], "flash_attn": params["flash_attn"],
		"batch": params["batch"], "ubatch": params["ubatch"],
		"threads": params["threads"], "threads_http": params["threads_http"],
		"temp": params["temp"], "reasoning": params["reasoning"],
		"chunked_batch": params["chunked_batch"],
		"split_mode":    params["split_mode"], "fit": params["fit"],
		"cache_ram": params["cache_ram"], "sleep_idle_seconds": params["sleep_idle_seconds"],
		"tensor_split": params["tensor_split"], "gpu": params["gpu"],
		"spec_draft_n_max":   params["spec_draft_n_max"],
		"draft_k_cache_type": params["draft_k_cache_type"],
		"draft_v_cache_type": params["draft_v_cache_type"],
	}
	result["running_model"] = modelName
	result["model_path"] = modelPath
	result["active_engine"] = engine
	result["deploy_config"] = deployConfig
	result["params"] = params
	result["persist"] = gin.H{"mode": "auto", "label": "auto"}
	_parseScriptCache = result
	_parseScriptTime = time.Now()
	return result
}

func parseScriptValue(script, flag string) string {
	idx := strings.Index(script, flag)
	if idx < 0 {
		return ""
	}
	rest := strings.TrimSpace(script[idx+len(flag):])
	end := strings.IndexAny(rest, " \n\r")

	if end < 0 {
		end = len(rest)
	}
	return rest[:end]
}

func fetchModelsList(cfg *shared.Config, hc *shared.HTTPClient) interface{} {
	mmURL := cfg.Services.ModelManager.URL
	resp, err := hc.Get(mmURL + "/api/models")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil
	}
	return result["models"]
}

func handleStopModel(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(strings.TrimRight(mmURL, "/")+"/api/models/stop", nil)
		if err != nil {
			c.JSON(502, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func buildServicesMap(cfg *shared.Config) map[string]string {
	result := map[string]string{}
	checks := serviceChecks(cfg)
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, check := range checks {
		wg.Add(1)
		go func(n, u string) {
			defer wg.Done()
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Get(u)
			if err != nil {
				mu.Lock()
				result[n] = "down"
				mu.Unlock()
				return
			}
			resp.Body.Close()
			mu.Lock()
			if resp.StatusCode < 400 {
				result[n] = "healthy"
			} else {
				result[n] = "degraded"
			}
			mu.Unlock()
		}(check.Name, check.URL)
	}
	wg.Wait()
	return result
}

func buildServicesList(cfg *shared.Config) []gin.H {
	checks := serviceChecks(cfg)
	type sr struct {
		idx  int
		name string
		svc  gin.H
	}
	results := make([]sr, 0, len(checks))
	var wg sync.WaitGroup
	var mu sync.Mutex
	for idx, check := range checks {
		wg.Add(1)
		go func(i int, n, u string) {
			defer wg.Done()
			svc := gin.H{"name": n, "url": u}
			client := &http.Client{Timeout: 3 * time.Second}
			resp, err := client.Get(u)
			if err != nil {
				svc["status"] = "down"
				svc["error"] = err.Error()
			} else {
				resp.Body.Close()
				if resp.StatusCode < 400 {
					svc["status"] = "healthy"
					svc["status_code"] = resp.StatusCode
				} else {
					svc["status"] = "degraded"
					svc["status_code"] = resp.StatusCode
				}
			}
			mu.Lock()
			results = append(results, sr{idx: i, name: n, svc: svc})
			mu.Unlock()
		}(idx, check.Name, check.URL)
	}
	wg.Wait()
	sort.Slice(results, func(i, j int) bool { return results[i].idx < results[j].idx })
	var services []gin.H
	for _, r := range results {
		services = append(services, r.svc)
	}
	return services
}

type serviceCheck struct {
	Name string
	URL  string
}

func serviceChecks(cfg *shared.Config) []serviceCheck {
	return []serviceCheck{
		{"llama-server", serviceHealthURL(cfg.Services.LlamaServer, "/health")},
		{"new-api", serviceHealthURL(cfg.Services.NewAPI, "/health")},
		{"model-manager", serviceHealthURL(cfg.Services.ModelManager, "/")},
		{"cluster-config", serviceHealthURL(cfg.Services.ClusterConfig, "/")},
		{"benchmark", serviceHealthURL(cfg.Services.Benchmark, "/")},
		{"searxng", "http://127.0.0.1:8888/"},
		{"victoriametrics", cfg.Services.VictoriaMetrics.URL + "/-/healthy"},
	}
}

func serviceHealthURL(endpoint shared.ServiceEndpoint, fallbackPath string) string {
	path := endpoint.HealthPath
	if path == "" {
		path = fallbackPath
	}
	return strings.TrimRight(endpoint.URL, "/") + "/" + strings.TrimLeft(path, "/")
}

func queryGPUHistory(cfg *shared.Config) gin.H {
	vmURL := cfg.Services.VictoriaMetrics.URL + cfg.Services.VictoriaMetrics.QueryRangePath
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	history := gin.H{"util": []float64{}, "mem_pct": []float64{}, "temp": []float64{}, "power": []float64{}, "clock": []float64{}}
	metrics := []string{"gpu_util", "gpu_mem_used_pct", "gpu_temp_celsius", "gpu_power_watts", "gpu_clock_mhz"}
	keys := []string{"util", "mem_pct", "temp", "power", "clock"}
	for i, metric := range metrics {
		url := fmt.Sprintf("%s?query=%s{gpu=\"aggregate\"}&start=%d&end=%d&step=60", vmURL, metric, start, end)
		resp, err := http.Get(url)
		if err != nil {
			continue
		}
		var vmResp struct {
			Data struct {
				Result []struct{ Values [][]interface{} }
			}
		}
		if err := json.NewDecoder(resp.Body).Decode(&vmResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		var values []float64
		if len(vmResp.Data.Result) > 0 {
			for _, v := range vmResp.Data.Result[0].Values {
				if len(v) >= 2 {
					if val, ok := v[1].(string); ok {
						if f, err := strconv.ParseFloat(val, 64); err == nil {
							values = append(values, f)
						}
					}
				}
			}
		}
		history[keys[i]] = values
	}
	return history
}

func querySystemHistory(cfg *shared.Config) gin.H {
	vmURL := cfg.Services.VictoriaMetrics.URL + cfg.Services.VictoriaMetrics.QueryRangePath
	now := time.Now()
	start := now.Add(-1 * time.Hour).Unix()
	end := now.Unix()
	history := gin.H{"cpu_usage": []float64{}, "mem_pct": []float64{}}
	metrics := []string{"cpu_util", "mem_used_pct"}
	keys := []string{"cpu_usage", "mem_pct"}
	for i, metric := range metrics {
		url := fmt.Sprintf("%s?query=%s&start=%d&end=%d&step=60", vmURL, metric, start, end)
		resp, err := http.Get(url)
		if err != nil {
			continue
		}
		var vmResp struct {
			Data struct {
				Result []struct{ Values [][]interface{} }
			}
		}
		if err := json.NewDecoder(resp.Body).Decode(&vmResp); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()
		var values []float64
		if len(vmResp.Data.Result) > 0 {
			for _, v := range vmResp.Data.Result[0].Values {
				if len(v) >= 2 {
					if val, ok := v[1].(string); ok {
						if f, err := strconv.ParseFloat(val, 64); err == nil {
							values = append(values, f)
						}
					}
				}
			}
		}
		history[keys[i]] = values
	}
	return history
}

// ===== Persist Mode =====

func handleGetPersist(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Get(mmURL + "/api/service/persist")
		if err != nil {
			c.JSON(200, gin.H{"mode": "auto", "label": "auto"})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleSetPersist(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/service/persist", bytes.NewReader(body))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

// ===== Deploy Stream =====

var (
	deployEvents []gin.H
	deployMu     sync.Mutex
)

// === 本地环形缓冲历史数据 (替代VM查询) ===
const historyLen = 61

type localHistory struct {
	mu            sync.Mutex
	cpuUsage      []float64
	cpuFreq       []float64
	memUsage      []float64
	memUsedGB     []float64
	diskActive    []float64
	diskRead      []float64
	diskWrite     []float64
	netRecv       []float64
	netSent       []float64
	gpuUtil       []float64
	gpuMemPct     []float64
	gpuTemp       []float64
	gpuPower      []float64
	lastDiskRead  uint64
	lastDiskWrite uint64
	lastDiskTime  time.Time
	lastNetRecv   uint64
	lastNetSent   uint64
	lastNetTime   time.Time
}

var _localHist = &localHistory{
	cpuUsage:   make([]float64, 0, historyLen),
	cpuFreq:    make([]float64, 0, historyLen),
	memUsage:   make([]float64, 0, historyLen),
	memUsedGB:  make([]float64, 0, historyLen),
	diskActive: make([]float64, 0, historyLen),
	diskRead:   make([]float64, 0, historyLen),
	diskWrite:  make([]float64, 0, historyLen),
	netRecv:    make([]float64, 0, historyLen),
	netSent:    make([]float64, 0, historyLen),
	gpuUtil:    make([]float64, 0, historyLen),
	gpuMemPct:  make([]float64, 0, historyLen),
	gpuTemp:    make([]float64, 0, historyLen),
	gpuPower:   make([]float64, 0, historyLen),
}

func appendHist(slice []float64, val float64) []float64 {
	slice = append(slice, val)
	if len(slice) > historyLen {
		slice = slice[len(slice)-historyLen:]
	}
	return slice
}

// collectLocalHistory 每次调用采集当前快照
func collectLocalHistory(sys *shared.SystemMetrics, gpus *shared.GPUAggregate) {
	if sys == nil {
		return
	}
	h := _localHist
	h.mu.Lock()
	defer h.mu.Unlock()

	h.cpuUsage = appendHist(h.cpuUsage, round1(sys.CPUUtil))
	h.cpuFreq = appendHist(h.cpuFreq, round1(sys.CPUFreqCur))
	memUsedGB := float64(sys.MemUsed) / 1073741824
	h.memUsage = appendHist(h.memUsage, round1(sys.MemUsedPct))
	h.memUsedGB = appendHist(h.memUsedGB, round1(memUsedGB))

	// Disk I/O rate (delta)
	now := time.Now()
	if !h.lastDiskTime.IsZero() {
		dt := now.Sub(h.lastDiskTime).Seconds()
		if dt > 0 {
			readBps := float64(sys.DiskReadBps)
			writeBps := float64(sys.DiskWriteBps)
			h.diskRead = appendHist(h.diskRead, round1(readBps))
			h.diskWrite = appendHist(h.diskWrite, round1(writeBps))
			h.diskActive = appendHist(h.diskActive, round1(sys.DiskActivePct))
		}
	} else {
		h.diskRead = appendHist(h.diskRead, 0)
		h.diskWrite = appendHist(h.diskWrite, 0)
		h.diskActive = appendHist(h.diskActive, 0)
	}
	h.lastDiskTime = now

	// Network rate — 使用计算好的速率(bytes/sec)
	h.netRecv = appendHist(h.netRecv, round1(netRecvRateBPS))
	h.netSent = appendHist(h.netSent, round1(netSentRateBPS))

	// GPU
	if gpus != nil && len(gpus.GPUs) > 0 {
		var totalUtil, totalMemPct, totalTemp, totalPower float64
		var count int
		for _, g := range gpus.GPUs {
			totalUtil += g.Util
			totalMemPct += g.MemUtilPct
			totalTemp += g.Temp
			totalPower += g.PowerDraw
			count++
		}
		// 聚合GPU数据如果有的话直接使用
		if gpus.Aggregate != nil {
			totalUtil = gpus.Aggregate.Util
			totalMemPct = gpus.Aggregate.MemUtilPct
			totalTemp = gpus.Aggregate.Temp
			totalPower = gpus.Aggregate.PowerDraw
			count = 1
		}
		if count == 0 {
			count = 1
		}
		h.gpuUtil = appendHist(h.gpuUtil, round1(totalUtil/float64(count)))
		h.gpuMemPct = appendHist(h.gpuMemPct, round1(totalMemPct/float64(count)))
		h.gpuTemp = appendHist(h.gpuTemp, round1(totalTemp/float64(count)))
		h.gpuPower = appendHist(h.gpuPower, round1(totalPower/float64(count)))
	} else {
		h.gpuUtil = appendHist(h.gpuUtil, 0)
		h.gpuMemPct = appendHist(h.gpuMemPct, 0)
		h.gpuTemp = appendHist(h.gpuTemp, 0)
		h.gpuPower = appendHist(h.gpuPower, 0)
	}
}

func getLocalHistory() gin.H {
	h := _localHist
	h.mu.Lock()
	defer h.mu.Unlock()
	// 复制切片
	cp := func(s []float64) []float64 {
		r := make([]float64, len(s))
		copy(r, s)
		return r
	}
	return gin.H{
		"cpu_usage":   cp(h.cpuUsage),
		"cpu_freq":    cp(h.cpuFreq),
		"mem_usage":   cp(h.memUsage),
		"mem_used_gb": cp(h.memUsedGB),
		"disk_active": cp(h.diskActive),
		"disk_read":   cp(h.diskRead),
		"disk_write":  cp(h.diskWrite),
		"net_recv":    cp(h.netRecv),
		"net_sent":    cp(h.netSent),
		"gpu_util":    cp(h.gpuUtil),
		"gpu_mem_pct": cp(h.gpuMemPct),
		"gpu_temp":    cp(h.gpuTemp),
		"gpu_power":   cp(h.gpuPower),
	}
}

func fmtNetSpeed(bps float64) string {
	if bps >= 1048576 {
		return fmt.Sprintf("%.1f MB/s", bps/1048576)
	} else if bps >= 1024 {
		return fmt.Sprintf("%.1f KB/s", bps/1024)
	} else {
		return fmt.Sprintf("%.0f B/s", bps)
	}
}

func round1(v float64) float64 {
	return float64(int(v*10)) / 10
}

func handleDeployStream() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Stream(func(w io.Writer) bool {
			deployMu.Lock()
			events := make([]gin.H, len(deployEvents))
			copy(events, deployEvents)
			deployMu.Unlock()

			for _, evt := range events {
				data, _ := json.Marshal(evt)
				fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", evt["type"], string(data))
				c.Writer.Flush()
			}
			time.Sleep(500 * time.Millisecond)
			return true
		})
	}
}

func PushDeployEvent(eventType string, data gin.H) {
	deployMu.Lock()
	deployEvents = append(deployEvents, gin.H{"type": eventType, "data": data, "ts": time.Now().Unix()})
	if len(deployEvents) > 100 {
		deployEvents = deployEvents[1:]
	}
	deployMu.Unlock()
}

// ===== GPU Info =====

func handleGetGPUInfo(cfg *shared.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		gpus := cachedGPUData(cfg)
		c.JSON(200, gpus)
	}
}

// ===== Active Engine =====

func handleGetActiveEngine(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		scriptData := parseLlamaScript()
		active, _ := scriptData["active_engine"].(string)
		if data, err := os.ReadFile("/data/inference-hub/state/active_engine"); err == nil {
			if value := strings.TrimSpace(string(data)); value != "" {
				active = value
			}
		}
		if active == "" {
			active = "unknown"
		}
		engine := loadEngineInfo(active)
		engine["is_running"] = true
		engine["status"] = "running"
		c.JSON(200, gin.H{
			"active":  active,
			"engine":  engine,
			"engines": []gin.H{engine},
		})
	}
}

func loadEngineInfo(key string) gin.H {
	engine := gin.H{
		"key":  key,
		"id":   key,
		"name": key,
		"type": engineType(key),
	}
	if key == "" || key == "unknown" {
		return engine
	}
	vjPath := filepath.Join("/data/engines", key, "VERSION.json")
	data, err := os.ReadFile(vjPath)
	if err != nil {
		return engine
	}
	var vj map[string]interface{}
	if json.Unmarshal(data, &vj) != nil {
		return engine
	}
	for k, v := range vj {
		engine[k] = v
	}
	if engine["key"] == nil || engine["key"] == "" {
		engine["key"] = key
	}
	engine["id"] = engine["key"]
	if engine["name"] == nil || engine["name"] == "" {
		engine["name"] = key
	}
	engine["type"] = engineType(fmt.Sprint(engine["key"]))
	return engine
}

func engineType(key string) string {
	if strings.EqualFold(key, "vllm") || strings.Contains(strings.ToLower(key), "vllm") {
		return "vllm"
	}
	return "llama"
}

// ===== Benchmark/Cluster Proxy =====

func handleProxyBenchmarkHistory(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		bURL := cfg.Services.Benchmark.URL
		resp, err := httpClient.Get(bURL + "/api/history")
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleProxyBenchmarkProviders(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		bURL := cfg.Services.Benchmark.URL
		resp, err := httpClient.Get(bURL + "/api/providers")
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleProxyClusterNodes(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		cURL := cfg.Services.ClusterConfig.URL
		resp, err := httpClient.Get(cURL + "/api/nodes")
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

// ===== Quick Switch Add/Toggle =====

func handleProxyAddRecent(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/quick-switch/add-recent", bytes.NewReader(body))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleProxyToggleFav(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/quick-switch/toggle-fav", bytes.NewReader(body))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

// ===== Control =====

func handleControl(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Action string      `json:"action"`
			Params interface{} `json:"params,omitempty"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}

		baseURL := cfg.Services.LlamaServer.URL

		switch req.Action {
		case "stop_slot":
			if params, ok := req.Params.(map[string]interface{}); ok {
				slotID := params["slot_id"]
				resp, err := httpClient.Post(fmt.Sprintf("%s/slots/%v?action=release", baseURL, slotID), nil)
				if err != nil {
					c.JSON(500, gin.H{"error": err.Error()})
					return
				}
				defer resp.Body.Close()
				c.JSON(200, gin.H{"status": "ok", "action": "stop_slot"})
			}
		case "restart_server":
			c.JSON(200, gin.H{"status": "restart_requested"})
		default:
			c.JSON(400, gin.H{"error": "unknown action: " + req.Action})
		}
	}
}

// ===== Models =====

func handleGetModels(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Get(mmURL + "/api/models")
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleLegacyModelsList(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		resp, err := httpClient.Get(strings.TrimRight(cfg.Services.ModelManager.URL, "/") + "/api/models")
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		var payload struct {
			Models        []map[string]interface{} `json:"models"`
			CurrentModel  string                   `json:"current_model"`
			CurrentConfig map[string]interface{}   `json:"current_config"`
			ServerRunning bool                     `json:"server_running"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		models := make([]gin.H, 0, len(payload.Models))
		for _, model := range payload.Models {
			name := fmt.Sprint(model["name"])
			size := model["size_human"]
			if size == nil {
				size = model["size"]
			}
			models = append(models, gin.H{
				"path": model["path"], "name": name, "alias": model["alias"],
				"size": size, "is_current": model["path"] == payload.CurrentModel,
			})
		}
		c.JSON(http.StatusOK, gin.H{
			"models": models, "current": payload.CurrentModel,
			"current_config": payload.CurrentConfig, "server_running": payload.ServerRunning,
		})
	}
}

func handleProxyDeploy(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}

		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/models/deploy", bytes.NewReader(body))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleSwitchModel(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}

		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/models/switch", bytes.NewReader(body))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleProxyQuickSwitchGet(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Get(mmURL + "/api/quick-switch")
		if err != nil {
			c.JSON(200, gin.H{"favorites": []interface{}{}, "recent": []interface{}{}})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

func handleProxyQuickSwitchPost(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}

		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/quick-switch", bytes.NewReader(body))
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

// ===== System Actions =====

func handleSystemAction(cfg *shared.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		action := c.Param("action")

		switch action {
		case "restart", "restart_llama":
			go exec.Command("sudo", "systemctl", "restart", "inference-server.service").Run()
			c.JSON(200, gin.H{"status": "restart_requested", "service": "inference-server.service"})
		case "reboot":
			go exec.Command("sudo", "systemctl", "reboot").Run()
			c.JSON(200, gin.H{"status": "reboot_requested"})
		case "shutdown":
			go exec.Command("sudo", "systemctl", "poweroff").Run()
			c.JSON(200, gin.H{"status": "shutdown_requested"})
		case "restart_dashboard":
			go func() {
				time.Sleep(1 * time.Second)
				os.Exit(0)
			}()
			c.JSON(200, gin.H{"status": "restart_requested", "service": "dashboard"})
		case "clear_cache":
			vmURL := cfg.Services.VictoriaMetrics.URL + "/internal/resetRollupResultCache"
			http.Post(vmURL, "text/plain", nil)
			c.JSON(200, gin.H{"status": "cache_cleared"})
		case "collect_baseline":
			baselinePath := filepath.Join("/tmp", "kv_baseline.json")
			// Simple baseline collection - store current timestamp
			baseline := map[string]interface{}{
				"captured_at": time.Now().Unix(),
				"status":      "collected",
			}
			data, _ := json.Marshal(baseline)
			os.WriteFile(baselinePath, data, 0644)
			c.JSON(200, gin.H{"status": "baseline_collected", "path": baselinePath})
		default:
			c.JSON(400, gin.H{"error": "unknown action: " + action})
		}
	}
}

// ===== GPU Power Limit =====

func handleGPUPowerLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			GPUID int     `json:"gpu_id"`
			Limit float64 `json:"limit_watts"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}

		cmd := exec.Command("sudo", "nvidia-smi", "-i", strconv.Itoa(req.GPUID), "-pl", fmt.Sprintf("%.0f", req.Limit))
		out, err := cmd.CombinedOutput()
		if err != nil {
			c.JSON(500, gin.H{"error": string(out)})
			return
		}

		c.JSON(200, gin.H{"status": "ok", "output": string(out)})
	}
}

func handleComputeProcesses() gin.HandlerFunc {
	return func(c *gin.Context) {
		fields := "gpu_index,pid,process_name,used_memory"
		cmd := exec.Command("nvidia-smi", "--query-compute-apps="+fields, "--format=csv,noheader,nounits")
		out, err := cmd.Output()
		if err != nil {
			c.JSON(200, gin.H{"procs": []interface{}{}, "count": 0})
			return
		}

		var procs []gin.H
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			parts := strings.Split(line, ",")
			if len(parts) < 4 {
				continue
			}
			gpuIndex, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
			pid, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
			memUsed := parseFloat2(parts[3])
			procs = append(procs, gin.H{
				"gpu_index": gpuIndex,
				"pid":       pid,
				"name":      strings.TrimSpace(parts[2]),
				"mem_used":  memUsed,
			})
		}
		if procs == nil {
			procs = []gin.H{}
		}
		c.JSON(200, gin.H{"procs": procs, "count": len(procs)})
	}
}

// ===== KV Baseline =====

func handleKVBaselineStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		baselinePath := filepath.Join("/tmp", "kv_baseline.json")
		if _, err := os.Stat(baselinePath); os.IsNotExist(err) {
			c.JSON(200, gin.H{"baseline": nil, "captured": false})
			return
		}

		data, err := os.ReadFile(baselinePath)
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}

		var baseline interface{}
		json.Unmarshal(data, &baseline)

		c.JSON(200, gin.H{"baseline": baseline, "captured": true})
	}
}

func handleKVBaselineRefresh() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := shared.GetConfig()
		if cfg == nil {
			c.JSON(500, gin.H{"error": "config not loaded"})
			return
		}

		baselinePath := filepath.Join("/tmp", "kv_baseline.json")
		baseline := map[string]interface{}{
			"captured_at": time.Now().Unix(),
			"status":      "refreshed",
		}
		data, _ := json.Marshal(baseline)
		os.WriteFile(baselinePath, data, 0644)

		c.JSON(200, gin.H{"status": "ok", "path": baselinePath})
	}
}

// ===== Active Requests =====

// ===== Engines =====

func handleGetEngines(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		enginesDir := "/data/engines"
		var engines []gin.H

		// Scan VERSION.json files
		entries, err := os.ReadDir(enginesDir)
		if err == nil {
			for _, entry := range entries {
				if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
					continue
				}
				vjPath := filepath.Join(enginesDir, entry.Name(), "VERSION.json")
				data, err := os.ReadFile(vjPath)
				if err != nil {
					continue
				}
				var vj map[string]interface{}
				if json.Unmarshal(data, &vj) != nil {
					continue
				}
				key, _ := vj["key"].(string)
				if key == "" {
					key = entry.Name()
				}
				eng := gin.H{
					"key":            key,
					"id":             key,
					"name":           vj["name"],
					"type":           engineType(key),
					"binary_path":    vj["binary_path"],
					"version":        vj["version"],
					"features":       vj["features"],
					"version_params": vj["version_params"],
					"branch":         vj["branch"],
					"commit":         vj["commit"],
					"upstream_tag":   vj["upstream_tag"],
					"github_url":     vj["github_url"],
				}
				if eng["name"] == nil || eng["name"] == "" {
					eng["name"] = key
				}
				engines = append(engines, eng)
			}
		}

		// Check which engine is running
		runningBin := ""
		if pid := findPid("llama-server"); pid > 0 {
			if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
				parts := strings.Split(strings.ReplaceAll(string(data), "\x00", " "), " ")
				if len(parts) > 0 {
					runningBin = parts[0]
				}
			}
		}

		for _, eng := range engines {
			bp, _ := eng["binary_path"].(string)
			eng["is_running"] = (runningBin != "" && bp != "" && strings.Contains(runningBin, bp))
		}

		// Get active engine
		active := "llama"
		if data, err := os.ReadFile("/data/inference-hub/state/active_engine"); err == nil {
			active = strings.TrimSpace(string(data))
		}

		c.JSON(200, gin.H{"engines": engines, "active": active})
	}
}

func handleSwitchEngine(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "invalid request"})
			return
		}
		engine := strings.Trim(strings.TrimSpace(string(body)), `"`)
		var req struct {
			Engine string `json:"engine"`
		}
		if json.Unmarshal(body, &req) == nil && strings.TrimSpace(req.Engine) != "" {
			engine = strings.TrimSpace(req.Engine)
		}
		if engine == "" {
			c.JSON(400, gin.H{"error": "engine required"})
			return
		}
		c.JSON(200, gin.H{"status": "switch_requested", "engine": engine})
	}
}

// ===== Settings =====

func handleGetLlamaParams(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		scriptPath := "/usr/local/bin/start-llama-server.sh"
		params := gin.H{}
		data, err := os.ReadFile(scriptPath)
		if err == nil {
			script := string(data)
			// 解析参数
			params["ctx_size"] = parseInt(script, "--ctx-size", 262144)
			params["ngl"] = parseInt(script, "-ngl", 99)
			params["batch"] = parseInt(script, "-b", 512)
			params["ubatch"] = parseInt(script, "-ub", 256)
			params["np"] = parseInt(script, "-np", 2)
			params["threads"] = parseInt(script, "-t", 8)
			params["threads_http"] = parseInt(script, "--threads-http", 4)
			params["spec_draft_n_max"] = parseInt(script, "--spec-draft-n-max", 2)
			params["cache_type_k"] = parseString(script, "--cache-type-k", "turbo4")
			params["cache_type_v"] = parseString(script, "--cache-type-v", "turbo4")
			params["draft_k_cache"] = parseString(script, "--cache-type-k-draft", "turbo2")
			params["draft_v_cache"] = parseString(script, "--cache-type-v-draft", "turbo2")
			params["flash_attn"] = parseString(script, "--flash-attn", "on")
			params["temp"] = parseFloat(script, "--temp", 0.6)
			params["reasoning"] = parseString(script, "--reasoning", "off")
			params["chunked_batch"] = "on"
			if strings.Contains(script, "-cb") {
				params["chunked_batch"] = "on"
			} else {
				params["chunked_batch"] = "off"
			}
			params["mmproj"] = "off"
			if strings.Contains(script, "--mmproj") {
				params["mmproj"] = "on"
			}
		}
		c.JSON(200, gin.H{"status": "ok", "params": params})
	}
}

func handleSetLlamaParams(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(400, gin.H{"error": "failed to read body"})
			return
		}

		// 通过 model-manager 更新参数
		mmURL := cfg.Services.ModelManager.URL
		resp, err := httpClient.Post(mmURL+"/api/settings/params", bytes.NewReader(body))
		if err != nil {
			// 如果 model-manager 不可用，直接返回
			c.JSON(200, gin.H{"status": "params_updated", "note": "需要重启 llama-server 生效"})
			return
		}
		defer resp.Body.Close()
		c.DataFromReader(resp.StatusCode, resp.ContentLength, resp.Header.Get("Content-Type"), resp.Body, nil)
	}
}

// ===== Request Sources =====

func handleRequestSources(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 从 nginx access log 获取请求来源统计
		_, reqSources, _ := shared.ParseNginxAccessLog("/var/log/nginx/llama_access.log", 500)

		// 同时从 NewAPI Docker 日志获取请求来源
		if globalNewAPIParser != nil {
			newAPISources := globalNewAPIParser.GetRequestSources()
			for ip, count := range newAPISources {
				reqSources.Sources[ip] += count
				reqSources.Total += count
			}
		}

		c.JSON(200, reqSources)
	}
}

func handleIPTokenStats() gin.HandlerFunc {
	return func(c *gin.Context) {
		if globalNewAPIParser == nil {
			c.JSON(200, gin.H{"stats": []interface{}{}, "total_requests": 0})
			return
		}
		stats := globalNewAPIParser.GetIPTokenStats()
		c.JSON(200, stats)
	}
}

func handleActiveRequests(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		baseURL := cfg.Services.LlamaServer.URL
		resp, err := httpClient.Get(baseURL + "/slots")
		if err != nil {
			c.JSON(500, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()

		var slots []map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&slots); err != nil {
			c.JSON(500, gin.H{"error": "decode error"})
			return
		}

		active := make([]map[string]interface{}, 0)
		for _, s := range slots {
			if proc, ok := s["is_processing"].(bool); ok && proc {
				active = append(active, s)
			}
		}

		c.JSON(200, gin.H{
			"active":   true,
			"count":    len(active),
			"requests": active,
		})
	}
}

// ===== KV Cache API =====

func toSharedKVMetrics(r kv_engine.KVResult) shared.KVMetrics {
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

func handleKVCache(cfg *shared.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cached := cachedKVData(); cached != nil {
			c.JSON(200, cached)
			return
		}
		kvEng := globalKVEngine
		if kvEng == nil {
			c.JSON(500, gin.H{"error": "KV engine not initialized"})
			return
		}
		gpus := cachedGPUData(cfg)
		if gpus == nil {
			c.JSON(200, gin.H{"error": "no GPU data"})
			return
		}
		// Count real GPUs
		numGPUs := 0
		for _, g := range gpus.GPUs {
			if g.Name != "" && !strings.Contains(g.Name, "Aggregate") {
				numGPUs++
			}
		}
		if numGPUs == 0 {
			c.JSON(200, gin.H{"error": "no real GPUs"})
			return
		}
		result := kvEng.Compute(gpus.GPUs, numGPUs)
		kvMetrics := toSharedKVMetrics(result)
		c.JSON(200, kvMetrics)
	}
}

// ===== V2 contract (Go migration compatibility surface) =====

func canonicalV2GPU(g shared.GPUMetrics, aggregate bool) gin.H {
	b, _ := json.Marshal(g)
	result := gin.H{}
	_ = json.Unmarshal(b, &result)
	name := strings.ToLower(g.Name)
	amd := strings.Contains(name, "amd") || strings.Contains(name, "radeon") || strings.Contains(strings.ToLower(g.Vendor), "amd")
	if amd {
		result["vendor"] = "amd"
		result["vendor_display"] = "AMD"
		result["vendor_color"] = "#ed1c24"
		result["encoder_name"] = "VCN"
		result["decoder_name"] = "VCN"
		coreType := strings.ToLower(strings.TrimSpace(fmt.Sprint(result["core_type"])))
		if coreType == "" || coreType == "nvidia" || coreType == "cuda" {
			result["core_type"] = "Stream"
		}
		result["fan_speed"] = nil
		result["enc_util"] = nil
		result["dec_util"] = nil
	}
	if aggregate {
		result["is_aggregate"] = true
	}
	return result
}

func v2Map(value interface{}) gin.H {
	b, _ := json.Marshal(value)
	result := gin.H{}
	_ = json.Unmarshal(b, &result)
	return result
}

func v2SystemSection(sys *shared.SystemMetrics) gin.H {
	if sys == nil {
		return gin.H{}
	}
	result := v2Map(sys)
	result["uptime"] = getSystemUptime()
	if value, ok := result["cpu_physical_cores"].(float64); ok && value == 0 {
		result["cpu_physical_cores"] = runtime.NumCPU() / 2
	}
	return result
}

func v2DeploymentSection() gin.H {
	script := parseLlamaScript()
	engines := scanEngines()
	active := fmt.Sprint(script["active_engine"])
	if data, err := os.ReadFile("/data/inference-hub/state/active_engine"); err == nil {
		if value := strings.TrimSpace(string(data)); value != "" {
			active = value
		}
	}
	var activeInfo gin.H
	for _, engine := range engines {
		if fmt.Sprint(engine["key"]) == active {
			engine["is_running"] = true
			activeInfo = engine
		} else {
			engine["is_running"] = false
		}
	}
	modelPath := fmt.Sprint(script["model_path"])
	modelName := fmt.Sprint(script["running_model"])
	if modelName == "" || modelName == "unknown" {
		modelName = filepath.Base(modelPath)
	}
	pid := findPid("llama-server")
	config := v2Map(script["params"])
	config["model_path"] = modelPath
	config["pid"] = pid
	config["llama_version"] = active
	config["concurrency"] = config["np"]
	config["k_cache_type"] = config["cache_type_k"]
	config["v_cache_type"] = config["cache_type_v"]
	if pid > 0 {
		if binary, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid)); err == nil {
			config["binary_path"] = binary
		}
	}
	return gin.H{
		"model_path":          modelName,
		"server_running":      pid > 0,
		"pid":                 pid,
		"active_engine":       active,
		"config":              config,
		"engines":             engines,
		"source":              "process-argv+start-script+engine-registry",
		"conflicts":           []interface{}{},
		"engine_branch":       activeInfo["branch"],
		"engine_commit":       activeInfo["commit"],
		"engine_version":      activeInfo["version"],
		"engine_upstream_tag": activeInfo["upstream_tag"],
	}
}

func v2ServicesSection(cfg *shared.Config) gin.H {
	status := buildServicesMap(cfg)
	result := gin.H{}
	result["推理服务"] = gin.H{"status": boolStr(status["llama-server"] == "healthy"), "detail": "", "port": "8080"}
	result["模型管理"] = gin.H{"status": boolStr(status["model-manager"] == "healthy"), "detail": "", "port": "8093"}
	result["SearXNG"] = gin.H{"status": boolStr(status["searxng"] == "healthy"), "detail": "", "port": "8888"}
	result["监控面板"] = gin.H{"status": "running", "detail": "Go migration bridge", "port": fmt.Sprint(cfg.Services.Dashboard.Port)}
	return result
}

func v2RequestsSection() gin.H {
	modTime := int64(0)
	if info, err := os.Stat("/tmp/llama-server.log"); err == nil {
		modTime = info.ModTime().Unix()
	}
	logs, ipStats, sources := shared.ParseLlamaLogsEx("/tmp/llama-server.log", 20, modTime)
	if logs == nil {
		logs = []shared.LogEntry{}
	}
	if ipStats == nil {
		ipStats = []shared.IPStat{}
	}
	items := make([]gin.H, 0, len(sources.Sources))
	for ip, count := range sources.Sources {
		items = append(items, gin.H{"ip": ip, "count": count})
	}
	return gin.H{"sources": gin.H{"sources": items, "total": sources.Total}, "recent": logs, "ip_stats": ipStats}
}

func buildV2Sections(cfg *shared.Config, httpClient *shared.HTTPClient) gin.H {
	gpus := cachedGPUData(cfg)
	sys := cachedSystemData()
	inf := cachedInferenceData(cfg, httpClient)
	llm := cachedLLMData(cfg, httpClient)
	kv := cachedKVData()
	systemSection := v2SystemSection(sys)

	items := []shared.GPUMetrics{}
	var aggregate *shared.GPUMetrics
	if gpus != nil {
		items = gpus.GPUs
		aggregate = gpus.Aggregate
	}
	if aggregate == nil && len(items) == 1 {
		copy := items[0]
		copy.Index = -1
		aggregate = &copy
	}

	llmSection := gin.H{
		"available":           llm != nil,
		"sample_complete":     llm != nil && llm.TPOT > 0,
		"ttft_ms":             nil,
		"prompt_ms_per_token": nil,
		"tpot_ms":             nil,
		"gen_tps":             nil,
		"prompt_tokens":       nil,
		"prompt_tokens_total": nil,
		"eval_tokens":         nil,
		"eval_tokens_total":   nil,
		"prompt_ms":           nil,
		"eval_ms":             nil,
		"spec_accept_rate":    nil,
		"source":              "go inference-hub collector",
		"confidence":          "low",
	}
	if llm != nil {
		llmSection["prompt_ms_per_token"] = llm.PromptMsPerToken
		llmSection["tpot_ms"] = llm.TPOT
		llmSection["spec_accept_rate"] = llm.SpecAcceptRate
		llmSection["prompt_tokens_total"] = llm.PromptTokensTotal
		llmSection["eval_tokens_total"] = llm.EvalTokensTotal
		if llm.TPOT > 0 {
			llmSection["confidence"] = "medium"
		}
	}
	if inf != nil {
		llmSection["gen_tps"] = inf.LastTPS
		llmSection["prompt_tokens"] = inf.LastPromptTokens
		llmSection["eval_tokens"] = inf.LastEvalTokens
	}
	requests := v2RequestsSection()
	if recent, ok := requests["recent"].([]shared.LogEntry); ok && len(recent) > 0 {
		latest := recent[0]
		if latest.PromptMs > 0 && latest.PromptTokens > 0 {
			llmSection["prompt_ms"] = latest.PromptMs
			llmSection["prompt_tokens"] = latest.PromptTokens
			llmSection["prompt_tokens_total"] = latest.PromptTokens
			llmSection["prompt_ms_per_token"] = latest.PromptMs / float64(latest.PromptTokens)
		}
		if latest.TPS > 0 && latest.EvalTokens > 0 {
			llmSection["eval_ms"] = float64(latest.EvalTokens) / latest.TPS * 1000
			llmSection["eval_tokens"] = latest.EvalTokens
			llmSection["eval_tokens_total"] = latest.EvalTokens
			llmSection["tpot_ms"] = float64(latest.EvalTokens) / latest.TPS * 1000 / float64(latest.EvalTokens)
			llmSection["gen_tps"] = latest.TPS
			llmSection["sample_complete"] = true
			llmSection["confidence"] = "high"
		}
	}
	acceptance := shared.ParseDraftAcceptance("/tmp/llama-server.log")
	if acceptance.Generated > 0 {
		llmSection["spec_accept_rate"] = acceptance.Rate
	}

	v2Items := make([]gin.H, 0, len(items))
	for _, item := range items {
		v2Items = append(v2Items, canonicalV2GPU(item, false))
	}
	var v2Aggregate interface{}
	if aggregate != nil {
		a := canonicalV2GPU(*aggregate, true)
		if len(v2Items) == 1 {
			// A single-GPU aggregate is a presentation convenience. Never let a
			// partially populated aggregate hide telemetry that exists on the
			// authoritative device item.
			item := v2Items[0]
			for _, key := range []string{"temp", "power_draw", "clock", "clock_max", "mem_used", "mem_total", "mem_free", "mem_util_pct"} {
				aggregateValue, aggregateOK := a[key].(float64)
				itemValue, itemOK := item[key].(float64)
				if itemOK && itemValue > 0 && (!aggregateOK || aggregateValue <= 0) {
					a[key] = itemValue
				}
			}
		}
		if len(v2Items) > 0 && strings.EqualFold(fmt.Sprint(v2Items[0]["vendor"]), "amd") {
			a["vendor"] = "amd"
			a["vendor_display"] = "AMD"
			a["vendor_color"] = "#ed1c24"
			a["core_type"] = "Stream"
			a["encoder_name"] = "VCN"
			a["decoder_name"] = "VCN"
			a["fan_speed"] = nil
			a["enc_util"] = nil
			a["dec_util"] = nil
		}
		v2Aggregate = a
	}
	sections := gin.H{
		"system": systemSection,
		"gpus": gin.H{
			"items":     v2Items,
			"aggregate": v2Aggregate,
		},
		"inference": gin.H{
			"runtime":  inf,
			"llm":      llmSection,
			"kv_cache": kv,
		},
		"services":   v2ServicesSection(cfg),
		"deployment": v2DeploymentSection(),
		"requests":   requests,
	}
	return sections
}

func handleV2Snapshot(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()
		c.JSON(http.StatusOK, gin.H{
			"schema_version": "2.0",
			"snapshot_id":    now.UnixMilli(),
			"collected_at":   now.Unix(),
			"event_cursor":   atomic.LoadUint64(&v2EventCursor),
			"sections":       buildV2Sections(cfg, httpClient),
			"freshness":      collectorFreshness(),
			"quality":        gin.H{"system": gin.H{"available": true}, "gpus": gin.H{"available": true}, "inference": gin.H{"available": true}},
			"history":        getHistory(cfg),
			"stream":         gin.H{"connected": true, "source": "inference-hub-v3", "error": nil},
		})
	}
}

func handleV2Events(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				id := atomic.AddUint64(&v2EventCursor, 1)
				now := time.Now()
				event := gin.H{
					"id": id, "type": "metrics.fast", "schema_version": "2.0",
					"collected_at": now.Unix(), "data": gin.H{"sections": buildV2Sections(cfg, httpClient)},
				}
				c.SSEvent("metrics.fast", event)
				c.Writer.Flush()
			}
		}
	}
}

// ===== Unified Aggregation Endpoints =====

func handleOverview(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		compact := c.Query("compact") == "1"
		gpus := cachedGPUData(cfg)
		var gpuData []shared.GPUMetrics
		if gpus != nil {
			gpuData = gpus.GPUs
		}
		sys := cachedSystemData()
		inf := cachedInferenceData(cfg, httpClient)

		var models []interface{}
		if !compact {
			if resp, err := httpClient.Get(cfg.Services.ModelManager.URL + "/api/models"); err == nil {
				defer resp.Body.Close()
				var modelResp struct {
					Models []interface{} `json:"models"`
				}
				if json.NewDecoder(resp.Body).Decode(&modelResp) == nil {
					models = modelResp.Models
				}
			}
		}

		var engines []gin.H
		var params map[string]interface{}
		if !compact {
			engines = scanEngines()
			params = parseLlamaParams()
		}
		activeEngine := "main"
		if data, err := os.ReadFile("/data/inference-hub/state/active_engine"); err == nil {
			activeEngine = strings.TrimSpace(string(data))
		}
		modelPath := ""
		if data, err := os.ReadFile("/data/inference-hub/state/persist_config.json"); err == nil {
			var pc map[string]interface{}
			if json.Unmarshal(data, &pc) == nil {
				if mp, ok := pc["model"].(string); ok {
					modelPath = mp
				}
			}
		}
		uptime := getUptime()

		c.JSON(200, gin.H{
			"gpus": gpuData, "system": sys, "inference": inf,
			"models": models, "engines": engines, "params": params,
			"active_engine": activeEngine, "model_path": modelPath, "uptime": uptime,
			"freshness": collectorFreshness(), "timestamp": time.Now().Unix(),
		})
	}
}

func handleHardware(cfg *shared.Config) gin.HandlerFunc {
	return func(c *gin.Context) {
		gpus := cachedGPUData(cfg)
		sys := cachedSystemData()
		c.JSON(200, gin.H{"gpus": gpus, "system": sys, "freshness": collectorFreshness(), "timestamp": time.Now().Unix()})
	}
}

func handleInferenceUnified(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		inf := cachedInferenceData(cfg, httpClient)
		kvResult := shared.KVMetrics{}
		if cached := cachedKVData(); cached != nil {
			kvResult = *cached
		} else if globalKVEngine != nil {
			gpus := cachedGPUData(cfg)
			if gpus != nil {
				numGPUs := 0
				for _, g := range gpus.GPUs {
					if g.Name != "" && !strings.Contains(g.Name, "Aggregate") {
						numGPUs++
					}
				}
				if numGPUs > 0 {
					result := globalKVEngine.Compute(gpus.GPUs, numGPUs)
					kvResult = toSharedKVMetrics(result)
				}
			}
		}
		c.JSON(200, gin.H{
			"kv_cache": kvResult, "inference_stats": inf,
			"freshness": collectorFreshness(), "timestamp": time.Now().Unix(),
		})
	}
}

func scanEngines() []gin.H {
	enginesDir := "/data/engines"
	var engines []gin.H
	entries, err := os.ReadDir(enginesDir)
	if err != nil {
		return engines
	}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		vjPath := filepath.Join(enginesDir, entry.Name(), "VERSION.json")
		data, err := os.ReadFile(vjPath)
		if err != nil {
			continue
		}
		var vj map[string]interface{}
		if json.Unmarshal(data, &vj) != nil {
			continue
		}
		key, _ := vj["key"].(string)
		if key == "" {
			key = entry.Name()
		}
		engines = append(engines, gin.H{
			"key": key, "name": vj["name"], "type": engineType(key),
			"version": vj["version"], "features": vj["features"],
			"branch": vj["branch"], "commit": vj["commit"],
			"upstream_tag": vj["upstream_tag"], "github_url": vj["github_url"],
			"binary_path": vj["binary_path"], "version_params": vj["version_params"],
		})
	}
	return engines
}

func parseLlamaParams() map[string]interface{} {
	params := map[string]interface{}{}
	script := ""
	if pid := findPid("llama-server"); pid > 0 {
		if data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil {
			script = strings.ReplaceAll(string(data), "\x00", " ")
		}
	}
	if script == "" {
		if data, err := os.ReadFile("/usr/local/bin/start-llama-server.sh"); err == nil {
			script = string(data)
		}
	}
	if script == "" {
		return params
	}
	params["ctx_size"] = parseInt(script, "--ctx-size", 262144)
	params["ngl"] = parseInt(script, "-ngl", 99)
	params["batch"] = parseInt(script, "-b", 512)
	params["ubatch"] = parseInt(script, "-ub", 256)
	params["np"] = parseInt(script, "-np", 2)
	params["cache_type_k"] = parseString(script, "--cache-type-k", "turbo4")
	params["cache_type_v"] = parseString(script, "--cache-type-v", "turbo4")
	params["draft_k_cache_type"] = parseString(script, "--cache-type-k-draft", "")
	params["draft_v_cache_type"] = parseString(script, "--cache-type-v-draft", "")
	params["tensor_split"] = parseString(script, "--tensor-split", "")
	params["threads"] = parseInt(script, "-t", 0)
	params["threads_http"] = parseInt(script, "--threads-http", 0)
	params["spec_type"] = parseString(script, "--spec-type", "")
	params["spec_draft_n_max"] = parseInt(script, "--spec-draft-n-max", 0)
	params["flash_attn"] = parseString(script, "--flash-attn", "on")
	params["chunked_batch"] = hasFlag(script, "-cb") || hasFlag(script, "--cont-batching")
	params["cache_ram"] = parseInt(script, "--cache-ram", 0)
	params["sleep_idle_seconds"] = parseInt(script, "--sleep-idle-seconds", 0)
	params["split_mode"] = parseString(script, "--split-mode", "")
	params["fit"] = parseString(script, "--fit", "")
	params["reasoning"] = parseString(script, "--reasoning", "")
	params["temp"] = parseFloat(script, "--temp", 0)
	return params
}

// ===== Proxy Routes =====

func proxyRoutes(r *gin.Engine, cfg *shared.Config) {
	services := map[string]string{
		"cluster":      cfg.Services.ClusterConfig.URL,
		"benchmark":    cfg.Services.Benchmark.URL,
		"llama-server": cfg.Services.LlamaServer.URL,
	}

	// Enhanced model-manager proxy with HTML base href injection
	r.Any("/model-manager/*path", func(c *gin.Context) {
		targetURL := cfg.Services.ModelManager.URL
		path := c.Param("path")
		if path == "" {
			path = "/"
		}
		url := targetURL + path
		req, _ := http.NewRequest(c.Request.Method, url, c.Request.Body)
		for k, v := range c.Request.Header {
			if k != "Host" && k != "Content-Length" {
				req.Header[k] = v
			}
		}
		req.Host = targetURL
		client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
		resp, err := client.Do(req)
		if err != nil {
			c.JSON(502, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		ct := resp.Header.Get("Content-Type")
		if strings.Contains(ct, "text/html") && resp.StatusCode < 400 {
			html := string(body)
			headTag := "<head>"
			if strings.Contains(html, headTag) {
				html = strings.Replace(html, headTag, `<head><base href="/model-manager/">`, 1)
			}
			body = []byte(html)
		}
		for k, v := range resp.Header {
			if k != "Content-Length" && k != "Transfer-Encoding" {
				c.Writer.Header()[k] = v
			}
		}
		c.Writer.WriteHeader(resp.StatusCode)
		c.Writer.Write(body)
	})

	for prefix, target := range services {
		// Only use catch-all route, no separate prefix route
		r.Any(prefix+"/*path", createProxyHandler(target, prefix))
	}
}

// compatProxyRoutes keeps the public aliases used by the Python dashboard
// while the Go gateway becomes the single HTTP entrypoint. Reads stay public;
// all write methods use the same admin-key middleware as native Go handlers.
func compatProxyRoutes(r *gin.Engine, cfg *shared.Config, auth gin.HandlerFunc) {
	route := func(prefix, target, targetPrefix string) {
		proxy := createPathProxyHandler(target, prefix, targetPrefix)
		for _, method := range []string{"GET", "HEAD", "OPTIONS"} {
			r.Handle(method, prefix+"/*path", proxy)
		}
		for _, method := range []string{"POST", "PUT", "DELETE", "PATCH"} {
			r.Handle(method, prefix+"/*path", auth, proxy)
		}
	}
	route("/mm/api", cfg.Services.ModelManager.URL, "/api")
}

func createProxyHandler(target string, prefix string) gin.HandlerFunc {
	targetURL, _ := url.Parse(target)
	timeout := 10 * time.Second
	if prefix == "model-manager" || prefix == "llama-server" {
		timeout = 30 * time.Second
	}
	proxy := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = targetURL.Scheme
			req.URL.Host = targetURL.Host
			req.Host = targetURL.Host
			// Strip prefix from path
			if strings.HasPrefix(req.URL.Path, "/"+prefix) {
				req.URL.Path = strings.TrimPrefix(req.URL.Path, "/"+prefix)
				if req.URL.Path == "" {
					req.URL.Path = "/"
				}
			}
		},
		Transport: &http.Transport{ResponseHeaderTimeout: timeout, IdleConnTimeout: 90 * time.Second, MaxIdleConns: 10, MaxIdleConnsPerHost: 5},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte("Proxy error: " + err.Error()))
		},
	}
	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func createPathProxyHandler(target, prefix, targetPrefix string) gin.HandlerFunc {
	targetURL, _ := url.Parse(strings.TrimRight(target, "/"))
	return func(c *gin.Context) {
		path := c.Param("path")
		if path == "" {
			path = "/"
		}
		upstreamPath := strings.TrimRight(targetPrefix, "/") + "/" + strings.TrimLeft(path, "/")
		upstream := *c.Request.URL
		upstream.Scheme = targetURL.Scheme
		upstream.Host = targetURL.Host
		upstream.Path = upstreamPath
		req := c.Request.Clone(c.Request.Context())
		req.URL = &upstream
		req.Host = targetURL.Host
		resp, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()
		for key, values := range resp.Header {
			for _, value := range values {
				c.Header(key, value)
			}
		}
		c.Status(resp.StatusCode)
		_, _ = io.Copy(c.Writer, resp.Body)
	}
}

// ===== Script parameter parsers =====

func nextToken(script string, after int) (string, int) {
	i := after
	for i < len(script) && (script[i] == ' ' || script[i] == '\t') {
		i++
	}
	if i >= len(script) {
		return "", i
	}
	j := i
	for j < len(script) && script[j] != ' ' && script[j] != '\n' && script[j] != '\t' && script[j] != '\r' {
		j++
	}
	return script[i:j], j
}

func parseInt(script, flag string, defaultVal int) int {
	token, ok := parseArg(script, flag)
	if !ok {
		return defaultVal
	}
	val, err := strconv.Atoi(token)
	if err != nil {
		return defaultVal
	}
	return val
}

func parseString(script, flag, defaultVal string) string {
	token, ok := parseArg(script, flag)
	if !ok {
		return defaultVal
	}
	return token
}

func parseFloat(script, flag string, defaultVal float64) float64 {
	token, ok := parseArg(script, flag)
	if !ok {
		return defaultVal
	}
	val, err := strconv.ParseFloat(token, 64)
	if err != nil {
		return defaultVal
	}
	return val
}

func parseArg(script, flag string) (string, bool) {
	fields := strings.Fields(script)
	for i, token := range fields {
		if token == flag && i+1 < len(fields) {
			return fields[i+1], true
		}
		if strings.HasPrefix(token, flag+"=") {
			return strings.TrimPrefix(token, flag+"="), true
		}
	}
	return "", false
}

func hasFlag(script, flag string) bool {
	for _, token := range strings.Fields(script) {
		if token == flag {
			return true
		}
	}
	return false
}

// ===== gopsutil wrappers =====

var (
	_prevPerCoreTotal []uint64
	_prevPerCoreIdle  []uint64
	_prevPerCoreTime  time.Time
)

func getCPUPerCore() ([]float64, error) {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return nil, err
	}
	var totals, idles []uint64
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "cpu") || strings.HasPrefix(line, "cpu ") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		var vals [10]uint64
		for i := 1; i < len(fields) && i <= 10; i++ {
			vals[i-1], _ = strconv.ParseUint(fields[i], 10, 64)
		}
		total := vals[0] + vals[1] + vals[2] + vals[3] + vals[4] + vals[5] + vals[6] + vals[7] + vals[8] + vals[9]
		idle := vals[3] + vals[4]
		totals = append(totals, total)
		idles = append(idles, idle)
	}
	now := time.Now()
	percents := make([]float64, len(totals))
	if _prevPerCoreTotal != nil && len(_prevPerCoreTotal) == len(totals) {
		dt := now.Sub(_prevPerCoreTime).Seconds()
		if dt > 0.05 {
			for i := range totals {
				dT := float64(totals[i] - _prevPerCoreTotal[i])
				dI := float64(idles[i] - _prevPerCoreIdle[i])
				if dT > 0 {
					percents[i] = 100.0 * (1.0 - dI/dT)
				}
			}
		}
	}
	_prevPerCoreTotal = totals
	_prevPerCoreIdle = idles
	_prevPerCoreTime = now
	return percents, nil
}

var (
	_prevCPUTotal uint64
	_prevCPUIdle  uint64
	_prevCPUTime  time.Time
)

func readCPUSum() (total, idle uint64) {
	data, _ := os.ReadFile("/proc/stat")
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) >= 5 {
				var vals [10]uint64
				for i := 1; i < len(fields) && i <= 10; i++ {
					vals[i-1], _ = strconv.ParseUint(fields[i], 10, 64)
				}
				total = vals[0] + vals[1] + vals[2] + vals[3] + vals[4] + vals[5] + vals[6] + vals[7] + vals[8] + vals[9]
				idle = vals[3] + vals[4]
			}
			break
		}
	}
	return
}

func getCPUPercent() ([]float64, error) {
	total, idle := readCPUSum()
	if total == 0 {
		return []float64{0}, nil
	}
	now := time.Now()
	if !_prevCPUTime.IsZero() {
		dt := now.Sub(_prevCPUTime).Seconds()
		if dt > 0.05 {
			dT := float64(total - _prevCPUTotal)
			dI := float64(idle - _prevCPUIdle)
			_prevCPUTotal = total
			_prevCPUIdle = idle
			_prevCPUTime = now
			if dT > 0 {
				return []float64{100.0 * (1.0 - dI/dT)}, nil
			}
		}
	}
	_prevCPUTotal = total
	_prevCPUIdle = idle
	_prevCPUTime = now
	return []float64{0}, nil
}

func getVirtualMemory() (*shared.VMemInfo, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	vals := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			k := strings.TrimSuffix(f[0], ":")
			v, _ := strconv.ParseUint(f[1], 10, 64)
			vals[k] = v * 1024
		}
	}
	total, avail, free := vals["MemTotal"], vals["MemAvailable"], vals["MemFree"]
	used := total - avail
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return &shared.VMemInfo{Total: total, Used: used, Free: free, Available: avail, UsedPercent: pct}, nil
}

func getSwapMemory() (*shared.SwapInfo, error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return nil, err
	}
	vals := map[string]uint64{}
	for _, line := range strings.Split(string(data), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			k := strings.TrimSuffix(f[0], ":")
			v, _ := strconv.ParseUint(f[1], 10, 64)
			vals[k] = v * 1024
		}
	}
	total, free := vals["SwapTotal"], vals["SwapFree"]
	used := total - free
	pct := 0.0
	if total > 0 {
		pct = float64(used) / float64(total) * 100
	}
	return &shared.SwapInfo{Total: total, Used: used, Free: free, UsedPercent: pct}, nil
}

func getDiskPartitions() ([]shared.PartitionInfo, error) {
	data, err := os.ReadFile("/proc/mounts")
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{"tmpfs": true, "devtmpfs": true, "squashfs": true, "overlay": true, "efivarfs": true, "sysfs": true, "proc": true, "devpts": true, "cgroup": true, "cgroup2": true, "pstore": true, "securityfs": true, "debugfs": true, "hugetlbfs": true, "mqueue": true, "fusectl": true, "configfs": true, "binfmt_misc": true, "autofs": true, "tracefs": true, "bpf": true, "nsfs": true, "ramfs": true, "rpc_pipefs": true, "nfsd": true}
	var parts []shared.PartitionInfo
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		f := strings.Fields(line)
		if len(f) >= 3 && strings.HasPrefix(f[0], "/dev/") && !skip[f[2]] {
			parts = append(parts, shared.PartitionInfo{Device: f[0], Mountpoint: f[1], Fstype: f[2]})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, shared.PartitionInfo{Device: "/", Mountpoint: "/", Fstype: ""})
	}
	return parts, nil
}

func getDiskUsage(mount string) (*shared.DiskUsageInfo, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(mount, &stat); err != nil {
		return nil, err
	}
	total := stat.Blocks * uint64(stat.Bsize)
	avail := stat.Bavail * uint64(stat.Bsize)
	used := total - stat.Bfree*uint64(stat.Bsize)
	pct := 0.0
	if total > 0 {
		pct = float64(total-avail) / float64(total) * 100
	}
	return &shared.DiskUsageInfo{Path: mount, Total: total, Used: used, Free: avail, UsedPercent: pct}, nil
}

func getNetIO() ([]shared.NetIOInfo, error) {
	// 读取所有网卡接口的累计字节数（跳过lo回环）
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var results []shared.NetIOInfo
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum <= 2 {
			continue
		} // 跳过头部2行
		line := scanner.Text()
		fields := strings.Fields(line)
		if len(fields) < 10 {
			continue
		}
		iface := strings.TrimSuffix(fields[0], ":")
		// 跳过回环和虚拟接口
		if iface == "lo" || strings.HasPrefix(iface, "veth") || strings.HasPrefix(iface, "docker") || strings.HasPrefix(iface, "br-") {
			continue
		}
		recv, _ := strconv.ParseUint(fields[1], 10, 64)
		sent, _ := strconv.ParseUint(fields[9], 10, 64)
		results = append(results, shared.NetIOInfo{BytesRecv: recv, BytesSent: sent, Interface: iface})
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("no network interface found")
	}

	// 选择流量最大的接口
	var bestIdx int
	var bestTotal uint64
	for i, r := range results {
		total := r.BytesRecv + r.BytesSent
		if total > bestTotal {
			bestTotal = total
			bestIdx = i
		}
	}
	return []shared.NetIOInfo{results[bestIdx]}, nil
}

func getProcesses() ([]interface{}, error) {
	cmd := exec.Command("sh", "-c", "ps -e --no-headers | wc -l")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	count, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	result := make([]interface{}, count)
	return result, nil
}

func getLoadAvg() (*shared.LoadAvg, error) {
	cmd := exec.Command("sh", "-c", "cat /proc/loadavg | awk '{print $1,$2,$3}'")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 3 {
		return nil, fmt.Errorf("unexpected output")
	}
	l1, _ := strconv.ParseFloat(fields[0], 64)
	l5, _ := strconv.ParseFloat(fields[1], 64)
	l15, _ := strconv.ParseFloat(fields[2], 64)
	return &shared.LoadAvg{Load1: l1, Load5: l5, Load15: l15}, nil
}

func parseFloat2(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "N/A" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func aggregateGPUs2(gpus []shared.GPUMetrics) *shared.GPUMetrics {
	if len(gpus) == 0 {
		return nil
	}
	var agg shared.GPUMetrics
	agg.Name = fmt.Sprintf("Aggregate (%d GPUs)", len(gpus))
	agg.Index = -1
	for _, g := range gpus {
		agg.Util += g.Util
		agg.MemUsed += g.MemUsed
		agg.MemTotal += g.MemTotal
		agg.MemFree += g.MemFree
		agg.MemUtilPct += g.MemUtilPct
		agg.Temp += g.Temp
		agg.PowerDraw += g.PowerDraw
		agg.PowerLimit += g.PowerLimit
		agg.Clock += g.Clock
		if agg.Driver == "" {
			agg.Driver = g.Driver
		}
		if agg.FanSpeed == nil {
			agg.FanSpeed = g.FanSpeed
		}
	}
	n := float64(len(gpus))
	agg.Util /= n
	agg.MemUtilPct /= n
	agg.Temp /= n
	agg.Clock /= n
	return &agg
}

func parseGPUProcesses(gpuIdx int) []shared.GPUProcess {
	cmd := exec.Command("sh", "-c",
		"nvidia-smi --query-compute-apps=pid,name,used_memory --format=csv,noheader,nounits -i "+strconv.Itoa(gpuIdx)+" 2>/dev/null")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var procs []shared.GPUProcess
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Split(line, ",")
		if len(parts) >= 3 {
			mem, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
			procs = append(procs, shared.GPUProcess{
				PID:  strings.TrimSpace(parts[0]),
				Name: strings.TrimSpace(parts[1]),
				Mem:  mem,
			})
		}
	}
	return procs
}

// ===== Alert Handlers =====

func handleAlertStatus() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get alert engine instance from global context
		cfg := shared.GetConfig()
		if cfg == nil {
			c.JSON(500, gin.H{"error": "config not loaded"})
			return
		}

		// Return current alert rules configuration
		c.JSON(200, gin.H{
			"rules":     cfg.Alerts.Rules,
			"notifiers": cfg.Alerts.Notifiers,
		})
	}
}

func handleTestAlert() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Send a test alert to verify notification channels
		cfg := shared.GetConfig()
		if cfg == nil {
			c.JSON(500, gin.H{"error": "config not loaded"})
			return
		}

		// Create alert engine temporarily
		engine := alert_manager.NewAlertEngine(&cfg.Alerts)

		// Test metrics that will trigger alerts
		testMetrics := map[string]float64{
			"gpu_temp_celsius":      90.0,
			"gpu_mem_used_pct":      96.0,
			"service_health_status": 1.0,
			"disk_free_pct":         50.0,
		}

		triggered := engine.Evaluate(testMetrics)

		c.JSON(200, gin.H{
			"status":    "test_complete",
			"triggered": len(triggered),
			"rules":     len(cfg.Alerts.Rules),
		})
	}
}

// evaluateAlerts runs the alert evaluation loop
func evaluateAlerts(engine *alert_manager.AlertEngine) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		metrics := collectCurrentMetrics()
		engine.Evaluate(metrics)
	}
}

// collectCurrentMetrics gathers current values for alert evaluation
func collectCurrentMetrics() map[string]float64 {
	metrics := make(map[string]float64)

	cmd := exec.Command("sh", "-c", "nvidia-smi --query-gpu=temperature.gpu,memory.used,memory.total --format=csv,noheader,nounits 2>/dev/null")
	out, err := cmd.Output()
	if err == nil {
		lines := strings.Split(strings.TrimSpace(string(out)), "\n")
		for i, line := range lines {
			vals := strings.Split(line, ",")
			if len(vals) >= 3 {
				temp, _ := strconv.ParseFloat(strings.TrimSpace(vals[0]), 64)
				memUsed, _ := strconv.ParseFloat(strings.TrimSpace(vals[1]), 64)
				memTotal, _ := strconv.ParseFloat(strings.TrimSpace(vals[2]), 64)
				if memTotal > 0 {
					memPct := memUsed / memTotal * 100
					metrics[fmt.Sprintf("gpu_mem_used_pct_%d", i)] = float64(int(memPct*10)) / 10
				}
				metrics[fmt.Sprintf("gpu_temp_celsius_%d", i)] = temp
			}
		}
	}

	cmd = exec.Command("sh", "-c", "df / | tail -1 | awk '{print $5}'")
	out, err = cmd.Output()
	if err == nil {
		pctStr := strings.TrimSpace(strings.TrimSuffix(string(out), "%"))
		pct, _ := strconv.ParseFloat(pctStr, 64)
		metrics["disk_free_pct"] = 100 - pct
	}

	cfg := shared.GetConfig()
	if cfg != nil {
		healthURL := cfg.Services.LlamaServer.URL + cfg.Services.LlamaServer.HealthPath
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(healthURL)
		if err != nil || resp.StatusCode >= 400 {
			metrics["service_health_status"] = 0
		} else {
			metrics["service_health_status"] = 1
			if resp != nil {
				resp.Body.Close()
			}
		}
	}

	return metrics
}
