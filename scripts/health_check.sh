#!/bin/bash
# Health check for InferenceHub v3

PASS=0
FAIL=0

check() {
    local name="$1"
    local cmd="$2"
    if eval "$cmd" > /dev/null 2>&1; then
        echo "  ✓ $name"
        PASS=$((PASS+1))
    else
        echo "  ✗ $name"
        FAIL=$((FAIL+1))
    fi
}

echo "=== InferenceHub v3 Health Check ==="
echo ""

echo "Services:"
check "Dashboard (9092)" "curl -s http://127.0.0.1:9092/api/health"
check "VictoriaMetrics (8428)" "curl -s http://127.0.0.1:8428/-/healthy"
check "llama-server (8080)" "curl -s http://127.0.0.1:8080/health"
check "model-manager (8093)" "curl -s http://127.0.0.1:8093/api/models"
check "new-api (3010)" "curl -s http://127.0.0.1:3010/api/status"
check "benchmark (8090)" "curl -s http://127.0.0.1:8090/"

echo ""
echo "Files:"
check "config/services.yaml" "test -f /data/inference-hub-v3/configs/services.yaml"
check "config/collectors.yaml" "test -f /data/inference-hub-v3/configs/collectors.yaml"
check "config/alerts.yaml" "test -f /data/inference-hub-v3/configs/alerts.yaml"

echo ""
echo "Results: $PASS passed, $FAIL failed"
