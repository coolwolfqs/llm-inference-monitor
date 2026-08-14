package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"inference-hub-v3/src/middleware"
	"inference-hub-v3/src/shared"
)

func TestUpdatedEngineMetadataResolvesRegistryKeyAndBinary(t *testing.T) {
	enginesDir := t.TempDir()
	registryDir := filepath.Join(enginesDir, "rocm")
	binary := filepath.Join(enginesDir, "llama", "build-rocm", "bin", "llama-server")
	if err := os.MkdirAll(filepath.Dir(binary), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	metadata := map[string]interface{}{"backend": "rocm", "version": "b10333"}
	key := engineRegistryKey(metadata, "fallback")
	if key != "rocm" {
		t.Fatalf("expected backend key rocm, got %q", key)
	}
	if got := engineBinaryPath(enginesDir, registryDir, key, metadata); got != binary {
		t.Fatalf("expected binary %q, got %q", binary, got)
	}
}

func TestExplicitEngineBinaryPathRemainsAuthoritative(t *testing.T) {
	metadata := map[string]interface{}{"binary_path": "/opt/custom/llama-server"}
	if got := engineBinaryPath(t.TempDir(), t.TempDir(), "custom", metadata); got != "/opt/custom/llama-server" {
		t.Fatalf("unexpected binary path %q", got)
	}
}

func TestV2ServicesIncludesAllUserFacingBusinessLines(t *testing.T) {
	services := v2ServicesFromStatus(map[string]string{
		"llama-server": "healthy", "model-manager": "healthy", "benchmark": "healthy",
		"cluster-config": "down", "searxng": "healthy",
	}, 9092)
	for _, name := range []string{"推理服务", "模型管理", "LLM测速", "集群配置", "监控面板"} {
		if _, ok := services[name]; !ok {
			t.Fatalf("missing business service %q", name)
		}
	}
	cluster := services["集群配置"].(gin.H)
	if cluster["status"] == "running" {
		t.Fatal("unavailable cluster service must not be reported as running")
	}
}

func TestProxyWritesRequireAdminKeyWhileReadsStayPublic(t *testing.T) {
	t.Setenv("ADMIN_KEY", "test-admin-key")
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerReadWriteProxy(router, "/benchmark/*path", middleware.AuthMiddleware(), func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"proxied": true})
	})

	read := httptest.NewRecorder()
	router.ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/benchmark/api/providers", nil))
	if read.Code != http.StatusOK {
		t.Fatalf("public read returned %d", read.Code)
	}
	write := httptest.NewRecorder()
	router.ServeHTTP(write, httptest.NewRequest(http.MethodDelete, "/benchmark/api/providers/0", nil))
	if write.Code != http.StatusForbidden {
		t.Fatalf("unauthorized write returned %d", write.Code)
	}
}

func TestCORSAdvertisesAdminKeyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(corsMiddleware())

	request := httptest.NewRequest(http.MethodOptions, "/api/engine/switch", nil)
	request.Header.Set("Origin", "http://console.example")
	request.Header.Set("Access-Control-Request-Headers", "X-Admin-Key")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight returned %d", response.Code)
	}
	if headers := response.Header().Get("Access-Control-Allow-Headers"); !strings.Contains(headers, "X-Admin-Key") {
		t.Fatalf("admin key header missing from CORS response: %q", headers)
	}
}

func TestModelManagerMutationForwardsInternalAdminKey(t *testing.T) {
	t.Setenv("MODEL_MANAGER_ADMIN_KEY", "internal-control-key")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("X-Admin-Key"); got != "internal-control-key" {
			t.Errorf("upstream admin key = %q, want internal control key", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		if string(body) != `{"mode":"auto"}` {
			t.Errorf("upstream body = %q", body)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest(http.MethodPost, "/api/settings/persist", strings.NewReader(`{"mode":"auto"}`))
	resp, err := postModelManagerJSON(ctx, shared.NewHTTPClient(5, ""), upstream.URL, []byte(`{"mode":"auto"}`))
	if err != nil {
		t.Fatalf("post model-manager request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("upstream status = %d", resp.StatusCode)
	}
}
