#!/bin/bash
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$SCRIPT_DIR"
python3 -m venv venv 2>/dev/null || true
source venv/bin/activate 2>/dev/null || true
pip install --upgrade pip
pip install -r requirements.txt
if [ ! -f config.yaml ]; then cp config.yaml.example config.yaml; fi
echo "Setup complete! Run ./scripts/start.sh"
