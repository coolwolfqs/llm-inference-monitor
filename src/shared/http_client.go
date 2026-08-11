package shared

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

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
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}

func (c *HTTPClient) GetJSON(url string, result interface{}) error {
	resp, err := c.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: status %d, body: %s", url, resp.StatusCode, string(body))
	}
	return json.NewDecoder(resp.Body).Decode(result)
}

func (c *HTTPClient) PostJSON(url string, payload interface{}, result interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
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
	req, err := http.NewRequest("POST", url, body)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
