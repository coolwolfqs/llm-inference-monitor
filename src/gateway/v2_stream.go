package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"

	"inference-hub-v3/src/shared"
)

// v2EventBroadcaster owns one ticker and one snapshot serialization for all
// connected clients. Each client receives a small bounded queue so a slow
// browser cannot block collectors or other clients; when its queue is full we
// discard the oldest frame and keep the newest state.
type v2EventBroadcaster struct {
	mu      sync.Mutex
	clients map[uint64]chan []byte
	nextID  uint64
	running bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
	cfg     *shared.Config
	hc      *shared.HTTPClient
}

var globalV2EventBroadcaster = &v2EventBroadcaster{clients: make(map[uint64]chan []byte)}

func startV2EventBroadcaster(cfg *shared.Config, hc *shared.HTTPClient) {
	b := globalV2EventBroadcaster
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return
	}
	b.cfg = cfg
	b.hc = hc
	b.stopCh = make(chan struct{})
	b.running = true
	b.wg.Add(1)
	b.mu.Unlock()
	go b.run()
}

func stopV2EventBroadcaster() {
	b := globalV2EventBroadcaster
	b.mu.Lock()
	if !b.running {
		b.mu.Unlock()
		return
	}
	close(b.stopCh)
	b.running = false
	for id, ch := range b.clients {
		close(ch)
		delete(b.clients, id)
	}
	b.mu.Unlock()
	b.wg.Wait()
}

func (b *v2EventBroadcaster) run() {
	defer b.wg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			b.publish()
		case <-b.stopCh:
			return
		}
	}
}

func (b *v2EventBroadcaster) publish() {
	b.mu.Lock()
	cfg, hc := b.cfg, b.hc
	b.mu.Unlock()
	if cfg == nil {
		return
	}
	frame, err := buildV2EventFrame(cfg, hc, atomic.AddUint64(&v2EventCursor, 1))
	if err != nil {
		shared.Errorf("[v2 events] encode failed: %v", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, ch := range b.clients {
		select {
		case ch <- frame:
		default:
			// Keep the newest state. The event cursor lets clients detect that
			// an intermediate frame was intentionally skipped.
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- frame:
			default:
			}
		}
	}
}

func (b *v2EventBroadcaster) subscribe() (uint64, <-chan []byte, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	id := b.nextID
	ch := make(chan []byte, 2)
	b.clients[id] = ch
	return id, ch, func() {
		b.mu.Lock()
		if existing, ok := b.clients[id]; ok {
			delete(b.clients, id)
			close(existing)
		}
		b.mu.Unlock()
	}
}

func buildV2EventFrame(cfg *shared.Config, hc *shared.HTTPClient, id uint64) ([]byte, error) {
	envelope := map[string]interface{}{
		"id":             id,
		"type":           "metrics.fast",
		"schema_version": "2.0",
		"collected_at":   time.Now().Unix(),
		"data":           map[string]interface{}{"sections": cachedV2Sections(cfg, hc)},
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return nil, err
	}
	frame := []byte(fmt.Sprintf("id: %d\nevent: metrics.fast\ndata: ", id))
	frame = append(frame, payload...)
	frame = append(frame, '\n', '\n')
	return frame, nil
}

func handleV2Events(cfg *shared.Config, httpClient *shared.HTTPClient) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Header("X-Event-Cursor", strconv.FormatUint(atomic.LoadUint64(&v2EventCursor), 10))

		startV2EventBroadcaster(cfg, httpClient)
		_, frames, unsubscribe := globalV2EventBroadcaster.subscribe()
		defer unsubscribe()

		initial, err := buildV2EventFrame(cfg, httpClient, atomic.LoadUint64(&v2EventCursor))
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "event encoding failed"})
			return
		}
		if _, err := c.Writer.Write(initial); err != nil {
			return
		}
		c.Writer.Flush()

		ctx := c.Request.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case frame, ok := <-frames:
				if !ok {
					return
				}
				if _, err := c.Writer.Write(frame); err != nil {
					return
				}
				c.Writer.Flush()
			}
		}
	}
}
