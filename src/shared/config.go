package shared

import (
	"fmt"
	"os"
	"sync"

	"gopkg.in/yaml.v3"
)

// Config holds all application configuration
type Config struct {
	Services   ServiceConfig   `yaml:"services"`
	Collectors CollectorConfig `yaml:"collectors"`
	Alerts     AlertConfig     `yaml:"alerts"`
}

type ServiceConfig struct {
	LlamaServer     ServiceEndpoint `yaml:"llama_server"`
	ModelManager    ServiceEndpoint `yaml:"model_manager"`
	NewAPI          ServiceEndpoint `yaml:"new_api"`
	OpenWebUI       ServiceEndpoint `yaml:"open_webui"`
	Benchmark       ServiceEndpoint `yaml:"benchmark"`
	ClusterConfig   ServiceEndpoint `yaml:"cluster_config"`
	VictoriaMetrics VMEndpoint      `yaml:"victoria_metrics"`
	Dashboard       DashboardConfig `yaml:"dashboard"`
}

type ServiceEndpoint struct {
	URL         string `yaml:"url"`
	APIKeyEnv   string `yaml:"api_key_env"`
	TimeoutSec  int    `yaml:"timeout_sec"`
	HealthPath  string `yaml:"health_path"`
	MetricsPath string `yaml:"metrics_path"`
	StatsPath   string `yaml:"stats_path"`
	SlotsPath   string `yaml:"slots_path"`
	DockerName  string `yaml:"docker_name"`
}

type VMEndpoint struct {
	URL            string `yaml:"url"`
	WritePath      string `yaml:"write_path"`
	QueryPath      string `yaml:"query_path"`
	QueryRangePath string `yaml:"query_range_path"`
	TimeoutSec     int    `yaml:"timeout_sec"`
}

type DashboardConfig struct {
	Listen string `yaml:"listen"`
	Port   int    `yaml:"port"`
	Mode   string `yaml:"mode"`
}

type CollectorConfig struct {
	GPU        CollectorEntry `yaml:"gpu"`
	System     CollectorEntry `yaml:"system"`
	Inference  CollectorEntry `yaml:"inference"`
	LLMMonitor CollectorEntry `yaml:"llm_monitor"`
	KVEngine   CollectorEntry `yaml:"kv_engine"`
}

type CollectorEntry struct {
	Enabled                    bool   `yaml:"enabled"`
	IntervalSec                int    `yaml:"interval_sec"`
	Vendor                     string `yaml:"vendor"`
	LogPath                    string `yaml:"log_path"`
	MaxLogEntries              int    `yaml:"max_log_entries"`
	HistorySize                int    `yaml:"history_size"`
	BaselineAutoCapture        bool   `yaml:"baseline_auto_capture"`
	BaselineCaptureIntervalMin int    `yaml:"baseline_capture_interval_min"`
}

type AlertConfig struct {
	Rules     map[string]AlertRule   `yaml:"alerts"`
	Notifiers map[string]NotifierCfg `yaml:"notifiers"`
}

type AlertRule struct {
	Enabled     bool     `yaml:"enabled"`
	Metric      string   `yaml:"metric"`
	Condition   string   `yaml:"condition"`
	Threshold   float64  `yaml:"threshold"`
	DurationSec int      `yaml:"duration_sec"`
	Severity    string   `yaml:"severity"`
	Message     string   `yaml:"message"`
	Channels    []string `yaml:"channels"`
}

type NotifierCfg struct {
	WebhookURLEnv string `yaml:"webhook_url_env"`
	BotTokenEnv   string `yaml:"bot_token_env"`
	ChatIDEnv     string `yaml:"chat_id_env"`
}

var (
	globalConfig *Config
	configMu     sync.RWMutex
)

// LoadConfig reads and parses YAML config files
func LoadConfig(configDir string) (*Config, error) {
	cfg := &Config{}

	// Load services.yaml
	data, err := os.ReadFile(configDir + "/services.yaml")
	if err != nil {
		return nil, fmt.Errorf("read services.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse services.yaml: %w", err)
	}

	// Load collectors.yaml
	data, err = os.ReadFile(configDir + "/collectors.yaml")
	if err != nil {
		return nil, fmt.Errorf("read collectors.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse collectors.yaml: %w", err)
	}

	// Load alerts.yaml - unmarshal into cfg.Alerts directly
	data, err = os.ReadFile(configDir + "/alerts.yaml")
	if err != nil {
		return nil, fmt.Errorf("read alerts.yaml: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg.Alerts); err != nil {
		return nil, fmt.Errorf("parse alerts.yaml: %w", err)
	}

	// Resolve env vars
	cfg.resolveEnvVars()

	configMu.Lock()
	globalConfig = cfg
	configMu.Unlock()

	Infof("Config loaded: VM=%s Dashboard=%s GPU=%v Sys=%v Inf=%v LLM=%v Alerts=%d",
		cfg.Services.VictoriaMetrics.URL,
		cfg.DashboardAddr(),
		cfg.Collectors.GPU.Enabled,
		cfg.Collectors.System.Enabled,
		cfg.Collectors.Inference.Enabled,
		cfg.Collectors.LLMMonitor.Enabled,
		len(cfg.Alerts.Rules))

	return cfg, nil
}

func (c *Config) resolveEnvVars() {
	if c.Services.LlamaServer.APIKeyEnv != "" {
		_ = os.Getenv(c.Services.LlamaServer.APIKeyEnv)
	}
	for k, v := range c.Alerts.Notifiers {
		if v.WebhookURLEnv != "" {
			_ = os.Getenv(v.WebhookURLEnv)
		}
		_ = k
	}
}

func GetConfig() *Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return globalConfig
}

func (c *Config) GetAPIKey() string {
	if c.Services.LlamaServer.APIKeyEnv != "" {
		return os.Getenv(c.Services.LlamaServer.APIKeyEnv)
	}
	return ""
}

func (c *Config) DashboardAddr() string {
	return fmt.Sprintf("%s:%d", c.Services.Dashboard.Listen, c.Services.Dashboard.Port)
}
