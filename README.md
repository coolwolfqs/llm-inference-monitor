# InferenceHub v3

LLM Inference Monitoring Dashboard - Complete rewrite in Go + Vue 3

## Architecture

```
┌──────────────┐     ┌──────────────┐     ┌──────────────┐
│   Vue 3 UI   │────▶│ Go Gateway   │────▶│  Collectors  │
│  (port 9092) │     │  (FastAPI→Gin)│     │ (goroutines) │
└──────────────┘     └──────┬───────┘     └──────┬───────┘
                            │                    │
                     ┌──────▼───────┐     ┌──────▼───────┐
                     │ Proxy Layer  │     │ Victoria     │
                     │ (embedded svcs)│    │ Metrics      │
                     └──────────────┘     └──────────────┘
```

## Modules

| Module | Description |
|--------|-------------|
| `collectors/gpu` | GPU metrics (NVIDIA/AMD) |
| `collectors/system` | CPU/Memory/Disk/Network |
| `collectors/inference` | llama-server stats/slots/logs |
| `collectors/llm_monitor` | TTFT/TPOT/Spec Decoding observability |
| `kv_engine` | KV Cache baseline + computation |
| `gateway` | Gin HTTP server + SSE + API routes |
| `alert_manager` | Rule engine + Feishu/Wechat/Telegram |
| `config_center` | YAML hot-reload |
| `shared` | Common types, HTTP client, metrics store |

## Quick Start

```bash
# Build Go backend
make build

# Build frontend
cd frontend && npm install && npm run build

# Run with VictoriaMetrics
make docker

# Dev mode
make dev
```

## Configuration

All config in `configs/`:
- `services.yaml` - Service endpoints
- `collectors.yaml` - Collector intervals
- `alerts.yaml` - Alert rules + notifiers

## Monitoring Metrics

See docs/ARCHITECTURE.md for full metrics catalog.

## License

Internal use only.
