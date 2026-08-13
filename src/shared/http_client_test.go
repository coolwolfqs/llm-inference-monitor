package shared

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientGetContextCancelsSlowRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewHTTPClient(30, "")
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()

	_, err := client.GetContext(ctx, server.URL)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetContext error = %v, want context deadline exceeded", err)
	}
}
