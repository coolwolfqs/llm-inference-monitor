package config_center

import (
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"inference-hub-v3/src/shared"
)

type ConfigLoader struct {
	cfg    *shared.Config
	mu     sync.RWMutex
	watch  chan struct{}
	stopCh chan struct{}
}

func NewConfigLoader(configDir string) (*ConfigLoader, error) {
	cfg, err := shared.LoadConfig(configDir)
	if err != nil {
		return nil, err
	}
	return &ConfigLoader{
		cfg:    cfg,
		watch:  make(chan struct{}, 1),
		stopCh: make(chan struct{}),
	}, nil
}

func (cl *ConfigLoader) GetConfig() *shared.Config {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	return cl.cfg
}

func (cl *ConfigLoader) StartWatch(configDir string, interval time.Duration) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-cl.stopCh:
				return
			case <-ticker.C:
				cl.reloadIfChanged(configDir)
			}
		}
	}()
}

func (cl *ConfigLoader) reloadIfChanged(configDir string) {
	cfg, err := shared.LoadConfig(configDir)
	if err != nil {
		shared.Errorf("[ConfigCenter] reload error: %v", err)
		return
	}
	cl.mu.Lock()
	cl.cfg = cfg
	cl.mu.Unlock()
	shared.Infof("[ConfigCenter] configuration reloaded")
}

func (cl *ConfigLoader) Stop() {
	close(cl.stopCh)
}

func LoadYAML(path string, v interface{}) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, v)
}
