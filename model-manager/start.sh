#!/bin/bash
set -euo pipefail
APP_DIR="${MODEL_MANAGER_APP_DIR:-/home/draco/model-manager}"
cd "$APP_DIR"
exec "$APP_DIR/venv/bin/python3" -m uvicorn app:app --workers 1 --host 127.0.0.1 --port 8093 --proxy-headers --forwarded-allow-ips=127.0.0.1 --log-level info
