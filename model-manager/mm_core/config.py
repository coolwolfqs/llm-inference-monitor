"""Environment driven paths and runtime settings.

The model manager is a node-local control plane.  Keeping every deployment
path here makes the application relocatable without changing Python source.
"""

from __future__ import annotations

import os
from dataclasses import dataclass
from pathlib import Path


def _path(name: str, default: str) -> Path:
    return Path(os.getenv(name, default)).expanduser().resolve()


@dataclass(frozen=True, slots=True)
class Settings:
    app_dir: Path
    models_dir: Path
    engine_dir: Path
    state_dir: Path
    catalog_database: Path
    llama_env_file: Path
    llama_start_script: Path
    write_start_script: Path
    overrides_file: Path
    catalog_ttl_seconds: float
    inference_host: str
    inference_port: int
    control_port: int
    inference_cgroup_procs: Path
    runtime_poll_seconds: float


settings = Settings(
    app_dir=_path("MODEL_MANAGER_APP_DIR", str(Path(__file__).resolve().parents[1])),
    models_dir=_path("MODEL_MANAGER_MODELS_DIR", "/data/models"),
    engine_dir=_path("MODEL_MANAGER_ENGINE_DIR", "/data/engines"),
    state_dir=_path("MODEL_MANAGER_STATE_DIR", "/data/inference-hub/state"),
    catalog_database=_path(
        "MODEL_MANAGER_CATALOG_DATABASE",
        "/data/model-manager/catalog.sqlite3",
    ),
    llama_env_file=_path(
        "MODEL_MANAGER_LLAMA_ENV_FILE",
        "/data/llama-server-config/llama-server.env",
    ),
    llama_start_script=_path(
        "MODEL_MANAGER_LLAMA_START_SCRIPT",
        "/usr/local/bin/start-llama-server.sh",
    ),
    write_start_script=_path(
        "MODEL_MANAGER_WRITE_START_SCRIPT",
        "/usr/local/bin/write-start-llama-server.sh",
    ),
    overrides_file=_path(
        "MODEL_MANAGER_METADATA_OVERRIDES",
        "/data/model-manager/model-metadata-overrides.json",
    ),
    catalog_ttl_seconds=max(
        1.0, float(os.getenv("MODEL_MANAGER_CATALOG_TTL_SECONDS", "60"))
    ),
    inference_host=os.getenv("MODEL_MANAGER_INFERENCE_HOST", "127.0.0.1"),
    inference_port=int(os.getenv("MODEL_MANAGER_INFERENCE_PORT", "8080")),
    control_port=int(os.getenv("MODEL_MANAGER_CONTROL_PORT", "8093")),
    inference_cgroup_procs=_path(
        "MODEL_MANAGER_INFERENCE_CGROUP_PROCS",
        "/sys/fs/cgroup/system.slice/inference-server.service/cgroup.procs",
    ),
    runtime_poll_seconds=max(
        0.25, float(os.getenv("MODEL_MANAGER_RUNTIME_POLL_SECONDS", "1"))
    ),
)
