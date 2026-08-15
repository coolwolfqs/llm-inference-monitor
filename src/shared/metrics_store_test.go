package shared

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsStoreFlushesWithoutHoldingWritePath(t *testing.T) {
	received := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	store := NewMetricsStore(server.URL, 2)
	store.Write("test_metric", 1, map[string]string{"kind": "unit"}, time.Now())
	defer store.Close()

	select {
	case body := <-received:
		if !strings.Contains(body, "test_metric") {
			t.Fatalf("flush body = %q, missing metric", body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("metrics batch was not flushed asynchronously")
	}
}

func TestMetricsStoreRequeuesFailedBatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte("temporarily unavailable"))
	}))
	defer server.Close()

	store := NewMetricsStore(server.URL, 1)
	store.Write("failed_metric", 1, nil, time.Now())
	if err := store.Flush(); err == nil {
		t.Fatal("Flush returned nil for a 503 response")
	}
	if !strings.Contains(store.DebugFormat(), "1 metrics pending") {
		t.Fatalf("failed batch was not requeued: %s", store.DebugFormat())
	}
	_ = store.Close()
}
