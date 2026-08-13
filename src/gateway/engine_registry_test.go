package main

import (
	"os"
	"path/filepath"
	"testing"
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
