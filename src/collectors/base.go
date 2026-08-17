package collectors

import (
	"context"
	"sync"
	"time"

	"inference-hub-v3/src/shared"
)

// Collector defines the interface all collectors must implement
type Collector interface {
	// Name returns the collector name
	Name() string

	// Enabled returns whether this collector is active
	Enabled() bool

	// Collect performs one collection cycle and returns the result
	Collect(ctx context.Context) (interface{}, error)

	// Interval returns the collection interval
	Interval() time.Duration
}

// BaseCollector provides common functionality for all collectors
type BaseCollector struct {
	name         string
	enabled      bool
	interval     time.Duration
	store        *shared.MetricsStore
	stopCh       chan struct{}
	stopOnce     sync.Once
	wg           sync.WaitGroup
	latestMu     sync.RWMutex
	latest       interface{}
	latestAt     time.Time
	lastDuration time.Duration
	sequence     uint64
	lastError    string
}

func NewBaseCollector(name string, enabled bool, intervalSec int, store *shared.MetricsStore) *BaseCollector {
	if intervalSec <= 0 {
		intervalSec = 1
	}
	return &BaseCollector{
		name:     name,
		enabled:  enabled,
		interval: time.Duration(intervalSec) * time.Second,
		store:    store,
		stopCh:   make(chan struct{}),
	}
}

func (b *BaseCollector) Name() string            { return b.name }
func (b *BaseCollector) Enabled() bool           { return b.enabled }
func (b *BaseCollector) Interval() time.Duration { return b.interval }

// Run starts the collection loop in a goroutine
func (b *BaseCollector) Run(collectFn func(ctx context.Context) (interface{}, error)) {
	if !b.enabled {
		shared.Infof("[%s] collector disabled, skipping", b.name)
		return
	}

	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		shared.Infof("[%s] collector started (interval=%v)", b.name, b.interval)

		ticker := time.NewTicker(b.interval)
		defer ticker.Stop()

		collectOnce := func() {
			started := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), b.interval)
			result, err := collectFn(ctx)
			cancel()
			if err != nil {
				b.latestMu.Lock()
				b.lastDuration = time.Since(started)
				b.lastError = err.Error()
				b.latestMu.Unlock()
				// Keep the last successful sample for continuity, but make the
				// failure observable in both freshness and time-series health.
				// Returning no sample as collect_ok=1 was the source of false-green
				// inference and LLM panels after an upstream partial failure.
				b.store.Write(b.name+"_collect_ok", 0.0, nil, time.Now())
				shared.Errorf("[%s] collect error: %v", b.name, err)
				return
			}
			b.latestMu.Lock()
			b.latest = result
			b.latestAt = time.Now()
			b.lastDuration = time.Since(started)
			b.sequence++
			b.lastError = ""
			b.latestMu.Unlock()
			b.store.Write(b.name+"_collect_ok", 1.0, nil, time.Now())
		}

		collectOnce()
		for {
			select {
			case <-b.stopCh:
				shared.Infof("[%s] collector stopped", b.name)
				return
			case <-ticker.C:
				collectOnce()
			}
		}
	}()
}

func (b *BaseCollector) Status(maxAge time.Duration) shared.SourceFreshness {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	status := "unavailable"
	age := int64(0)
	collectedAt := int64(0)
	if !b.latestAt.IsZero() {
		collectedAt = b.latestAt.UnixMilli()
		age = time.Since(b.latestAt).Milliseconds()
		status = "ok"
		if time.Since(b.latestAt) > maxAge {
			status = "stale"
		}
	}
	if b.lastError != "" && status != "stale" {
		status = "degraded"
	}
	return shared.SourceFreshness{
		CollectedAtUnixMs: collectedAt,
		AgeMs:             age,
		DurationMs:        float64(b.lastDuration.Microseconds()) / 1000,
		Sequence:          b.sequence,
		Status:            status,
		LastError:         b.lastError,
	}
}

// Latest returns the last successful collection if it is still fresh enough.
func (b *BaseCollector) Latest(maxAge time.Duration) (interface{}, time.Time, bool) {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	if b.latest == nil || b.latestAt.IsZero() || time.Since(b.latestAt) > maxAge {
		return nil, b.latestAt, false
	}
	return b.latest, b.latestAt, true
}

// LatestAny returns the most recent successful sample even when it is stale.
// Presentation APIs should prefer a stale sample plus freshness metadata over
// synchronously collecting from a busy inference service.
func (b *BaseCollector) LatestAny() (interface{}, time.Time, bool) {
	b.latestMu.RLock()
	defer b.latestMu.RUnlock()
	if b.latest == nil || b.latestAt.IsZero() {
		return nil, b.latestAt, false
	}
	return b.latest, b.latestAt, true
}

// Stop gracefully stops the collector
func (b *BaseCollector) Stop() {
	b.stopOnce.Do(func() { close(b.stopCh) })
	b.wg.Wait()
}

// WriteMetric helper to write a metric with the collector prefix
func (b *BaseCollector) WriteMetric(name string, value float64, labels map[string]string, ts time.Time) {
	b.store.Write(b.name+"_"+name, value, labels, ts)
}
