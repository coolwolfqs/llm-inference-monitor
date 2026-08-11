package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"
)

type MetricsStore struct {
	vmURL        string
	client       *http.Client
	buf          bytes.Buffer
	bufMu        sync.Mutex
	writeCount   int
	commonLabels map[string]string
}

// vmLine represents a single metric line in VictoriaMetrics JSON Lines format
type vmLine struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

func NewMetricsStore(vmURL string, timeoutSec int) *MetricsStore {
	hostname, _ := os.Hostname()
	return &MetricsStore{
		vmURL: vmURL,
		client: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
			},
		},
		commonLabels: map[string]string{"node_id": hostname},
	}
}

func (ms *MetricsStore) Write(name string, value float64, labels map[string]string, ts time.Time) {
	ms.bufMu.Lock()
	defer ms.bufMu.Unlock()

	// Build metric labels map with __name__
	m := make(map[string]string)
	m["__name__"] = name
	for k, v := range ms.commonLabels {
		if v != "" {
			m[k] = v
		}
	}
	for k, v := range labels {
		m[k] = v
	}

	line := vmLine{
		Metric:     m,
		Values:     []float64{value},
		Timestamps: []int64{ts.UnixMilli()},
	}

	data, err := json.Marshal(line)
	if err != nil {
		Errorf("[MetricsStore] json marshal error: %v", err)
		return
	}

	ms.buf.Write(data)
	ms.buf.WriteString("\n")
	ms.writeCount++

	// Flush every 20 writes or when buffer > 4096 bytes
	if ms.writeCount >= 20 || ms.buf.Len() > 4096 {
		ms.flushLocked()
	}
}

func (ms *MetricsStore) Flush() error {
	ms.bufMu.Lock()
	defer ms.bufMu.Unlock()
	n := ms.buf.Len()
	err := ms.flushLocked()
	if n > 0 {
		Infof("[MetricsStore] flushed %d bytes (%d metrics) to %s", n, ms.writeCount, ms.vmURL)
	}
	return err
}

func (ms *MetricsStore) flushLocked() error {
	if ms.buf.Len() == 0 {
		return nil
	}

	data := append([]byte(nil), ms.buf.Bytes()...)
	count := ms.writeCount

	resp, err := ms.client.Post(ms.vmURL, "application/json", bytes.NewReader(data))
	if err != nil {
		Errorf("[MetricsStore] flush error: %v", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		Errorf("[MetricsStore] VM write failed: status=%d body=%s (%d bytes, %d metrics)", resp.StatusCode, string(body), len(data), count)
		return fmt.Errorf("VictoriaMetrics write status %d", resp.StatusCode)
	} else {
		ms.buf.Reset()
		ms.writeCount = 0
		Infof("[MetricsStore] flushed %d bytes (%d metrics) to %s -> status=%d", len(data), count, ms.vmURL, resp.StatusCode)
	}

	return nil
}

// DebugFormat prints the current buffer content for debugging
func (ms *MetricsStore) DebugFormat() string {
	ms.bufMu.Lock()
	defer ms.bufMu.Unlock()
	return fmt.Sprintf("buffer: %d bytes, %d metrics pending", ms.buf.Len(), ms.writeCount)
}
