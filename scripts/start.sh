#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SCRIPT_DIR"
python3 -c "import fastapi, uvicorn, psutil" 2>/dev/null || pip install -r requirements.txt
PORT=${MONITOR_PORT:-${1:-8081}}
HOST=${MONITOR_HOST:-${2:-0.0.0.0}}
echo "Starting LLM Inference Monitor on $HOST:$PORT..."
export MONITOR_PORT=$PORT
export MONITOR_HOST=$HOST
python3 -m uvicorn backend.server:app --host $HOST --port $PORT --log-level info
