package shared

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const maxHTTPResponseBytes = 8 << 20

// HTTPClient wraps http.Client with connection pooling and auth
type HTTPClient struct {
	client  *http.Client
	apiKey  string
	timeout time.Duration
	mu      sync.RWMutex
}

// NewHTTPClient creates a pooled HTTP client
func NewHTTPClient(timeoutSec int, apiKey string) *HTTPClient {
	return &HTTPClient{
		client: &http.Client{
			Timeout: time.Duration(timeoutSec) * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:        100,
				MaxIdleConnsPerHost: 20,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		apiKey:  apiKey,
		timeout: time.Duration(timeoutSec) * time.Second,
	}
}

func (c *HTTPClient) UpdateAPIKey(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.apiKey = key
}

func (c *HTTPClient) getAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.apiKey
}

func (c *HTTPClient) Get(url string) (*http.Response, error) {
	return c.GetContext(context.Background(), url)
}

// GetContext performs a GET request that is cancelled with ctx. Collectors
// pass their cycle context here so a slow inference endpoint cannot outlive
// the collector deadline and hold up the next cycle.
func (c *HTTPClient) GetContext(ctx context.Context, url string) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

func (c *HTTPClient) GetJSON(url string, result interface{}) error {
	return c.GetJSONContext(context.Background(), url, result)
}

// GetJSONContext is the context-aware JSON variant used by collectors.
func (c *HTTPClient) GetJSONContext(ctx context.Context, url string, result interface{}) error {
	resp, err := c.GetContext(ctx, url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes))
		return fmt.Errorf("GET %s: status %d, body: %s", url, resp.StatusCode, string(body))
	}
	if resp.ContentLength > maxHTTPResponseBytes {
		return fmt.Errorf("GET %s: response too large (%d bytes)", url, resp.ContentLength)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
	if err != nil {
		return err
	}
	if int64(len(body)) > maxHTTPResponseBytes {
		return fmt.Errorf("GET %s: response exceeds %d bytes", url, maxHTTPResponseBytes)
	}
	return json.Unmarshal(body, result)
}

func (c *HTTPClient) PostJSON(url string, payload interface{}, result interface{}) error {
	return c.PostJSONContext(context.Background(), url, payload, result)
}

func (c *HTTPClient) PostJSONContext(ctx context.Context, url string, payload interface{}, result interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes))
		return fmt.Errorf("POST %s: status %d, body: %s", url, resp.StatusCode, string(responseBody))
	}
	if result != nil {
		if resp.ContentLength > maxHTTPResponseBytes {
			return fmt.Errorf("POST %s: response too large (%d bytes)", url, resp.ContentLength)
		}
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, maxHTTPResponseBytes+1))
		if readErr != nil {
			return readErr
		}
		if int64(len(responseBody)) > maxHTTPResponseBytes {
			return fmt.Errorf("POST %s: response exceeds %d bytes", url, maxHTTPResponseBytes)
		}
		return json.Unmarshal(responseBody, result)
	}
	return nil
}

func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	key := c.getAPIKey()
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	return c.client.Do(req)
}

func (c *HTTPClient) Post(url string, body io.Reader) (*http.Response, error) {
	return c.PostContext(context.Background(), url, body)
}

func (c *HTTPClient) PostContext(ctx context.Context, url string, body io.Reader) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, body)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
