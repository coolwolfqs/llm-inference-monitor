package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	metricsFlushInterval = time.Second
	metricsFlushBytes    = 4096
	metricsFlushCount    = 20
	metricsMaxBuffer     = 1 << 20
)

// MetricsStore is a bounded, asynchronous write buffer for VictoriaMetrics.
// Collectors must never hold their sample lock while a remote metrics backend
// is slow or unavailable. Failed batches are requeued up to the configured
// bound; excess samples are counted and dropped rather than growing without
// limit.
type MetricsStore struct {
	vmURL        string
	client       *http.Client
	buf          bytes.Buffer
	bufMu        sync.Mutex
	flushMu      sync.Mutex
	writeCount   int
	commonLabels map[string]string

	flushCh   chan struct{}
	stopCh    chan struct{}
	closeOnce sync.Once
	wg        sync.WaitGroup
	dropped   atomic.Uint64

	// backoff state for VictoriaMetrics failures (see flushLoop).
	flushMu2      sync.Mutex
	failStreak    int
	backoffUntil  time.Time
	consecutiveOK int
}

// vmLine represents a single metric line in VictoriaMetrics JSON Lines format
type vmLine struct {
	Metric     map[string]string `json:"metric"`
	Values     []float64         `json:"values"`
	Timestamps []int64           `json:"timestamps"`
}

func NewMetricsStore(vmURL string, timeoutSec int) *MetricsStore {
	if timeoutSec <= 0 {
		timeoutSec = 5
	}
	nodeID := strings.TrimSpace(os.Getenv("CONTROL_NODE_ID"))
	if nodeID == "" {
		nodeID, _ = os.Hostname()
	}
	ms := &MetricsStore{
		vmURL: vmURL,
		client: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        20,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		commonLabels: map[string]string{"node_id": nodeID},
		flushCh:      make(chan struct{}, 1),
		stopCh:       make(chan struct{}),
	}
	ms.wg.Add(1)
	go ms.flushLoop()
	return ms
}

func (ms *MetricsStore) flushLoop() {
	defer ms.wg.Done()
	ticker := time.NewTicker(metricsFlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ms.flushCh:
			ms.flushWithBackoff()
		case <-ticker.C:
			ms.flushWithBackoff()
		case <-ms.stopCh:
			// stopCh is already closed by Close(); drain once and exit.
			if err := ms.Flush(); err != nil {
				Errorf("[MetricsStore] final flush failed: %v", err)
			}
			return
		}
	}
}

// flushWithBackoff tracks consecutive VictoriaMetrics failures and drops the
// flush cadence to 1s/2s/5s/10s (capped) while the backend is down, so a long
// outage no longer produces an error-log storm or a 1s hot retry loop.
func (ms *MetricsStore) flushWithBackoff() {
	ms.flushMu2.Lock()
	now := time.Now()
	if now.Before(ms.backoffUntil) {
		ms.flushMu2.Unlock()
		return
	}
	ms.flushMu2.Unlock()

	if err := ms.Flush(); err != nil {
		ms.flushMu2.Lock()
		ms.failStreak++
		ms.consecutiveOK = 0
		delay := time.Second
		if ms.failStreak > 3 {
			delay = 2 * time.Second
		}
		if ms.failStreak > 6 {
			delay = 5 * time.Second
		}
		if ms.failStreak > 10 {
			delay = 10 * time.Second
		}
		ms.backoffUntil = time.Now().Add(delay)
		streak := ms.failStreak
		ms.flushMu2.Unlock()
		// Log every failure when fresh, then only the first failure of each
		// streak segment to avoid a log storm during a long outage.
		if streak <= 3 || streak%5 == 1 {
			Errorf("[MetricsStore] VM write failed (streak=%d): %v", streak, err)
		}
		return
	}
	ms.flushMu2.Lock()
	if ms.failStreak > 0 && ms.consecutiveOK == 0 {
		Infof("[MetricsStore] VM write recovered after %d failures", ms.failStreak)
	}
	ms.failStreak = 0
	ms.consecutiveOK++
	ms.flushMu2.Unlock()
}

func (ms *MetricsStore) signalFlush() {
	select {
	case ms.flushCh <- struct{}{}:
	default:
	}
}

func (ms *MetricsStore) Write(name string, value float64, labels map[string]string, ts time.Time) {
	// Build metric labels map with __name__.
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
	data = append(data, '\n')

	ms.bufMu.Lock()
	if ms.buf.Len()+len(data) > metricsMaxBuffer {
		ms.dropped.Add(1)
		ms.bufMu.Unlock()
		Errorf("[MetricsStore] buffer full; dropping metric %s (dropped=%d)", name, ms.dropped.Load())
		return
	}
	ms.buf.Write(data)
	ms.writeCount++
	shouldFlush := ms.writeCount >= metricsFlushCount || ms.buf.Len() >= metricsFlushBytes
	ms.bufMu.Unlock()
	if shouldFlush {
		ms.signalFlush()
	}
}

// Flush snapshots the pending batch before doing network I/O. New writes can
// continue while VictoriaMetrics is processing the previous batch.
func (ms *MetricsStore) Flush() error {
	ms.flushMu.Lock()
	defer ms.flushMu.Unlock()

	data, count := ms.takeBatch()
	if len(data) == 0 {
		return nil
	}
	resp, err := ms.client.Post(ms.vmURL, "application/json", bytes.NewReader(data))
	if err != nil {
		ms.requeue(data, count)
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		ms.requeue(data, count)
		return fmt.Errorf("VictoriaMetrics write status %d: %s", resp.StatusCode, string(body))
	}
	Infof("[MetricsStore] flushed %d bytes (%d metrics) to %s -> status=%d", len(data), count, ms.vmURL, resp.StatusCode)
	return nil
}

func (ms *MetricsStore) takeBatch() ([]byte, int) {
	ms.bufMu.Lock()
	defer ms.bufMu.Unlock()
	if ms.buf.Len() == 0 {
		return nil, 0
	}
	data := append([]byte(nil), ms.buf.Bytes()...)
	count := ms.writeCount
	ms.buf.Reset()
	ms.writeCount = 0
	return data, count
}

func (ms *MetricsStore) requeue(data []byte, count int) {
	ms.bufMu.Lock()
	defer ms.bufMu.Unlock()
	if len(data)+ms.buf.Len() > metricsMaxBuffer {
		ms.dropped.Add(uint64(count))
		Errorf("[MetricsStore] failed batch discarded at buffer limit (dropped=%d)", ms.dropped.Load())
		return
	}
	current := append([]byte(nil), ms.buf.Bytes()...)
	ms.buf.Reset()
	ms.buf.Write(data)
	ms.buf.Write(current)
	ms.writeCount += count
}

// Close stops the background worker and performs one final bounded flush.
func (ms *MetricsStore) Close() error {
	ms.closeOnce.Do(func() { close(ms.stopCh) })
	ms.wg.Wait()
	return ms.Flush()
}

func (ms *MetricsStore) DroppedCount() uint64 { return ms.dropped.Load() }

// DebugFormat prints the current buffer content for debugging
func (ms *MetricsStore) DebugFormat() string {
	ms.bufMu.Lock()
	defer ms.bufMu.Unlock()
	return fmt.Sprintf("buffer: %d bytes, %d metrics pending, %d dropped", ms.buf.Len(), ms.writeCount, ms.dropped.Load())
}
