#!/usr/bin/env bash
set -euo pipefail

# Release through the supervised units. This script deliberately does not
# start a second nohup process or touch VictoriaMetrics lifecycle.
project=/data/inference-hub-v3
dashboard=/data/dashboard
release_dir=/data/rollback/inference-release-$(date +%Y%m%d-%H%M%S)
mkdir -p "$release_dir"

echo "[1/5] Backing up the running artifacts..."
cp -a "$project/inference-hub-v3" "$release_dir/inference-hub-v3.before"
cp -a /data/dashboard/index.html "$release_dir/dashboard-index.before"

echo "[2/5] Building the Go binary with embedded provenance..."
docker run --rm --network host \
  -v "$project:/src" -w /src golang:1.24-bookworm \
  bash -lc 'make build && test -x inference-hub-v3'

echo "[3/5] Building the active dashboard source..."
docker run --rm --network host \
  -v "$dashboard:/app" -w /app/frontend node:20-bookworm \
  bash -lc '/app/frontend/node_modules/.bin/vite --version >/dev/null && /app/frontend/node_modules/.bin/vue-tsc --noEmit && /app/frontend/node_modules/.bin/vitest run && node scripts/generate-layout.mjs && /app/frontend/node_modules/.bin/vite build && node scripts/promote.mjs'

echo "[4/5] Restarting the supervised gateway..."
sudo -n systemctl restart inference-hub-v3.service
for attempt in $(seq 1 30); do
  if curl -fsS --max-time 2 http://127.0.0.1:8081/api/health >/tmp/inference-hub-health.json; then
    break
  fi
  sleep 1
done
test -s /tmp/inference-hub-health.json
systemctl is-active --quiet inference-hub-v3.service

echo "[5/5] Verifying the release artifact..."
curl -fsS --max-time 5 http://127.0.0.1:8081/api/health
echo
echo "Release complete; rollback snapshot: $release_dir"
