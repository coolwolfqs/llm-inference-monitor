#!/bin/bash
set -e

echo "=== InferenceHub v3 Deploy Script ==="

# Check if VictoriaMetrics is running
if ! curl -s http://127.0.0.1:8428/-/healthy > /dev/null 2>&1; then
    echo "[WARN] VictoriaMetrics not running on 8428, starting..."
    docker run -d --name inference-hub-vm \
        -p 8428:8428 \
        -v vm-data:/victoria-metrics-data \
        victoriametrics/victoria-metrics:latest \
        --storageDataPath=/victoria-metrics-data \
        --retentionPeriod=30d
fi

# Build
echo "[1/3] Building Go binary..."
cd /data/inference-hub-v3
make build

# Build frontend
echo "[2/3] Building frontend..."
cd frontend
npm install --silent
npm run build
cp -r dist/* ../static/ 2>/dev/null || mkdir -p ../static && cp -r dist/* ../static/

# Start
echo "[3/3] Starting InferenceHub v3 on port 9092..."
cd /data/inference-hub-v3
nohup ./inference-hub-v3 > /var/log/inference-hub-v3.log 2>&1 &

echo "=== Deploy complete ==="
echo "Dashboard: http://10.1.1.4:9092"
echo "VictoriaMetrics: http://10.1.1.4:8428"
