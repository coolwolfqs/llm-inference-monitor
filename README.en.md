# LLM Inference Monitor

A real-time monitoring dashboard for LLM inference services (llama.cpp / vllm) with GPU, CPU, memory, disk, and network metrics.

**Security-first design:** All sensitive data (IPs, hostnames, admin keys, disk models, PIDs) is automatically redacted via configurable middleware. Safe to share screenshots publicly.

## Features

### System Monitoring
- **GPU**: Utilization, VRAM usage, temperature, power draw, fan speed, clock frequency, PCIe link status, encoder/decoder utilization, per-GPU detail cards
- **CPU**: Per-core utilization, frequency, temperature, cache info, load averages, process/thread counts
- **Memory**: Usage %, used/free/cached/buffers, swap usage
- **Disk**: Read/write speed, activity %, partition usage, NVMe temperature, disk model
- **Network**: Throughput (Rx/Tx), adapter details, link speed, IPv4

### Inference Monitoring
- Real-time token generation speed (TPS)
- KV Cache utilization with VRAM breakdown
- LLM Performance: TTFT, TPOT, KV hit rate, MTP speculative decoding stats
- Service validation cross-checking (process, GPU, metrics, KV)
- IP-based token usage statistics (with IP redaction)
- Live request terminal view

### Engine Management
- Multi-engine support (llama.cpp, vllm)
- Engine switching with restart
- Version display and GitHub source links

### System Control
- Persist mode toggle (auto/manual)
- GPU power limit control (40%-100%)
- System reboot/shutdown (admin-protected)
- Dark/Light theme with auto-switch

### Data Security
- **IP Address Redaction**: Configurable (partial/full/none)
- **GPU UUID/Serial Redaction**: Always removed
- **Process PID Redaction**: Zeroed out
- **Disk Model Redaction**: Generic naming
- **Admin Key Protection**: HMAC-verified admin header
- **All config via env vars**: No hardcoded secrets

## Quick Start

### Prerequisites
- Python 3.10+
- NVIDIA GPU with nvidia-smi (for GPU metrics)
- Linux (for full system metrics)
- llama.cpp server or vllm (for inference metrics)

### Installation

```bash
git clone https://github.com/YOUR_USERNAME/llm-inference-monitor.git
cd llm-inference-monitor
./scripts/setup.sh
```

### Configuration

Copy and edit the config:

```bash
cp config.yaml.example config.yaml
# Edit config.yaml with your settings
```

Key environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `MONITOR_HOST` | `0.0.0.0` | Server bind address |
| `MONITOR_PORT` | `8081` | Server port |
| `ADMIN_KEY` | `changeme` | Admin key for sensitive ops |
| `INFERENCE_HOST` | `127.0.0.1` | Inference service host |
| `INFERENCE_PORT` | `8080` | Inference service port |
| `IP_REDACT_MODE` | `partial` | IP redaction: partial/full/none |

### Running

```bash
./scripts/start.sh
# Or with custom port:
MONITOR_PORT=9090 ./scripts/start.sh
```

Open http://localhost:8081 in your browser.

## Project Structure

```
llm-inference-monitor/
├── backend/
│   ├── server.py                 # FastAPI server + API routes
│   ├── config.py                 # Configuration (env-based)
│   ├── collectors/
│   │   ├── gpu_collector.py      # NVIDIA GPU metrics (nvidia-smi)
│   │   ├── cpu_collector.py      # CPU metrics (psutil)
│   │   ├── memory_collector.py   # Memory metrics (psutil)
│   │   ├── disk_collector.py     # Disk I/O metrics (psutil)
│   │   ├── network_collector.py  # Network metrics (psutil)
│   │   ├── system_collector.py   # Uptime & system info
│   │   └── inference_collector.py# Inference service metrics
│   └── api/
│       └── middleware.py         # Admin auth + data desensitization
├── frontend/
│   ├── index.html                # Main dashboard page
│   └── static/
│       ├── css/                  # Stylesheets
│       │   ├── base.css          # Variables, reset, themes
│       │   ├── layout.css        # Header, nav, sections
│       │   ├── components.css    # Cards, buttons, tables
│       │   ├── monitor.css       # Charts, gauges, GPU panels
│       │   ├── models.css        # Model banner, tags
│       │   └── optimize.css      # Responsive media queries
│       └── js/                   # JavaScript modules
│           ├── utils.js          # Utilities (formatting, escaping)
│           ├── charts.js         # Canvas chart rendering
│           ├── system.js         # Theme, persist, power limit
│           ├── monitor.js        # Main data fetch + rendering
│           ├── inference.js      # KV cache, IP stats, LLM metrics
│           ├── gpu.js            # GPU cards, details, power tabs
│           ├── models.js         # Model filename parsing
│           └── deploy-prefs.js   # Per-model localStorage prefs
├── config.yaml.example           # Configuration template
├── requirements.txt              # Python dependencies
└── scripts/
    ├── start.sh                  # Start server
    └── setup.sh                  # Install dependencies
```

## API Endpoints

| Method | Path | Description | Auth Required |
|--------|------|-------------|:---:|
| GET | `/api/status` | Full system status snapshot | No |
| GET | `/api/sse` | SSE real-time data stream | No |
| GET | `/api/engines` | List inference engines | No |
| POST | `/api/engine/switch` | Switch active engine | Yes |
| POST | `/api/gpu/power_limit` | Set GPU power limit | Yes |
| GET | `/api/settings/persist` | Get persist mode | No |
| POST | `/api/settings/persist` | Set persist mode | No |

## Data Desensitization

The middleware automatically redacts the following before sending to the frontend:

| Data Type | Method | Example |
|-----------|--------|---------|
| IP Addresses | Configurable: partial/full/none | `10.1.1.4` -> `192.168.1.4` |
| GPU UUID/Serial | Always removed | `GPU-abc123...` -> `REDACTED` |
| Process PIDs | Zeroed out | `12345` -> `0` |
| Disk Models | Generic prefix | `Seagate ZP1000...` -> `Seagate SSD ***` |
| Network Adapters | Generic name | `eno1` -> `eth0` |
| Admin Keys | Env-only, never exposed | N/A |

## License

MIT License - see LICENSE file.

## Contributing

Contributions welcome! Please open an issue first for major changes.
