#!/usr/bin/env python3
"""Model Manager - 所有模型在 /data/models，部署改配置+重启"""
import os
import re
import time
import subprocess
import hmac
import ipaddress
import shlex
import asyncio
import json
import socket
import uuid
import urllib.error
import urllib.parse
import urllib.request
import shutil
from concurrent.futures import ThreadPoolExecutor
from contextlib import suppress
from pathlib import Path
from contextlib import asynccontextmanager
from typing import Any

from fastapi import FastAPI, HTTPException, UploadFile, File, Request
from fastapi.responses import HTMLResponse, FileResponse, JSONResponse, StreamingResponse
from fastapi.staticfiles import StaticFiles
from fastapi.middleware.cors import CORSMiddleware

from mm_core import CatalogService, settings
from mm_core.operations import OperationStore
from mm_core.tasks import DeploymentTaskStore
from mm_core.downloads import DownloadTaskStore
from mm_core.runtime import SingleFlightTTLCache, service_pids
from mm_core.deployment import (
    DeploymentPlanError,
    match_model_engine,
    model_requirements,
    resolve_deployment_plan,
)

APP_DIR = str(settings.app_dir)
DATA_DIR = str(settings.models_dir)
ENV_FILE = str(settings.llama_env_file)
START_SCRIPT = str(settings.llama_start_script)
catalog_service = CatalogService(
    settings.models_dir,
    overrides_file=settings.overrides_file,
    database=settings.catalog_database,
    ttl_seconds=settings.catalog_ttl_seconds,
)
operation_store = OperationStore(settings.catalog_database)
deployment_tasks = DeploymentTaskStore(settings.catalog_database)
download_tasks = DownloadTaskStore(settings.catalog_database)

try:
    _TASK_WORKERS = max(2, min(4, int(os.getenv("MODEL_MANAGER_TASK_WORKERS", "2"))))
except ValueError:
    _TASK_WORKERS = 2
background_executor = ThreadPoolExecutor(
    max_workers=_TASK_WORKERS,
    thread_name_prefix="model-manager-task",
)

HF_API = "https://huggingface.co/api"
HF_REPO_RE = re.compile(r"^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$")
MAX_UPLOAD_BYTES = 512 * 1024**3


def _write_upload_chunk(fd: int, chunk: bytes) -> None:
    view = memoryview(chunk)
    while view:
        written = os.write(fd, view)
        if written <= 0:
            raise OSError("upload write made no progress")
        view = view[written:]


def _hf_json(url: str) -> object:
    headers = {"Accept": "application/json", "User-Agent": "model-manager/1.0"}
    token = os.environ.get("HF_TOKEN", "").strip()
    if token:
        headers["Authorization"] = f"Bearer {token}"
    try:
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=20) as response:
            return json.load(response)
    except urllib.error.HTTPError as exc:
        if exc.code in {401, 403}:
            raise HTTPException(403, "该模型需要授权，请在服务器配置 HF_TOKEN")
        if exc.code == 404:
            raise HTTPException(404, "Hugging Face 模型不存在")
        raise HTTPException(502, f"Hugging Face 请求失败 ({exc.code})")
    except (urllib.error.URLError, TimeoutError, json.JSONDecodeError) as exc:
        raise HTTPException(502, f"Hugging Face 连接失败: {exc}")


def _validate_hf_file(repo_id: str, filename: str) -> tuple[str, str]:
    if not HF_REPO_RE.fullmatch(repo_id or ""):
        raise HTTPException(400, "无效的 Hugging Face 模型 ID")
    clean = str(filename or "").strip()
    parts = Path(clean).parts
    if not clean.lower().endswith(".gguf") or not parts or clean.startswith("/") or ".." in parts:
        raise HTTPException(400, "只允许下载模型仓库中的 GGUF 文件")
    return repo_id, clean


def _run_hf_download(task_id: str, repo_id: str, filename: str, destination: Path) -> None:
    temporary = destination.with_name(f".{destination.name}.{task_id}.downloading")
    downloaded = total = 0
    try:
        download_tasks.update(task_id, state="running", phase="connecting", progress=1)
        url = f"https://huggingface.co/{repo_id}/resolve/main/{urllib.parse.quote(filename, safe='/')}?download=true"
        headers = {"User-Agent": "model-manager/1.0"}
        token = os.environ.get("HF_TOKEN", "").strip()
        if token:
            headers["Authorization"] = f"Bearer {token}"
        with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=60) as response:
            total = int(response.headers.get("Content-Length") or 0)
            if total and shutil.disk_usage(destination.parent).free < total + 2 * 1024**3:
                raise RuntimeError("磁盘空间不足，需额外保留 2 GB 安全空间")
            with open(temporary, "xb") as output:
                while True:
                    chunk = response.read(8 * 1024 * 1024)
                    if not chunk:
                        break
                    output.write(chunk)
                    downloaded += len(chunk)
                    progress = min(99, int(downloaded * 100 / total)) if total else 1
                    download_tasks.update(task_id, state="running", phase="downloading", progress=progress,
                                          downloaded_bytes=downloaded, total_bytes=total)
        os.replace(temporary, destination)
        catalog_service.invalidate()
        catalog_service.list_models(force=True)
        download_tasks.update(task_id, state="succeeded", phase="completed", progress=100,
                              downloaded_bytes=downloaded, total_bytes=total or downloaded)
    except Exception as exc:
        temporary.unlink(missing_ok=True)
        download_tasks.update(task_id, state="failed", phase="failed", progress=100,
                              downloaded_bytes=downloaded, total_bytes=total, error=str(exc))


def _safe_model_path(name, must_exist=False):
    """Resolve a model path and prove it stays inside DATA_DIR."""
    if not isinstance(name, str) or not name.strip() or "\x00" in name:
        raise HTTPException(400, "无效的模型路径")
    base = Path(DATA_DIR).resolve()
    candidate = (base / name).resolve()
    try:
        candidate.relative_to(base)
    except ValueError:
        raise HTTPException(400, "模型路径超出允许目录")
    if must_exist and not candidate.exists():
        # Deployment requests use stable catalog IDs for both the main model
        # and optional projectors. Resolve those IDs (and legacy basenames)
        # back to the catalog's relative path before checking the filesystem.
        record = catalog_service.find(name)
        relative_path = record.get("relative_path") if record else None
        if isinstance(relative_path, str) and relative_path.strip():
            candidate = (base / relative_path).resolve()
            try:
                candidate.relative_to(base)
            except ValueError:
                raise HTTPException(400, "模型路径超出允许目录")
        if not candidate.exists():
            raise HTTPException(404, "文件不存在")
    return candidate


def _bounded_int(value, name, minimum, maximum):
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        raise HTTPException(400, f"{name} 必须是整数")
    if not minimum <= parsed <= maximum:
        raise HTTPException(400, f"{name} 必须在 {minimum}-{maximum} 之间")
    return parsed


def _bounded_float(value, name, minimum, maximum):
    try:
        parsed = float(value)
    except (TypeError, ValueError):
        raise HTTPException(400, f"{name} 必须是数字")
    if not minimum <= parsed <= maximum:
        raise HTTPException(400, f"{name} 必须在 {minimum}-{maximum} 之间")
    return parsed

# === Engine Registry (动态读取 /data/engines/*/VERSION.json) ===
_ENGINES_CACHE = {"engines": [], "ts": 0}
_ENGINES_DIR = str(settings.engine_dir)
_ENGINE_CAPABILITIES_CACHE: dict[str, tuple[int, int, dict[str, Any]]] = {}

# llama.cpp's current server exposes these values for both the target and
# speculative/draft KV caches.  Engine help probing below remains the source
# of truth; this list is only the safe compatibility fallback for an older
# VERSION.json/binary that cannot be probed during a request.
_LLAMA_CACHE_TYPES = (
    "f32", "f16", "bf16", "q8_0", "q4_0", "q4_1", "iq4_nl", "q5_0", "q5_1",
)
_LLAMA_SPEC_TYPES = (
    "none", "draft-simple", "draft-eagle3", "draft-mtp", "draft-dflash",
    "draft-dspark", "ngram-simple", "ngram-map-k", "ngram-map-k4v",
    "ngram-mod", "ngram-cache",
)

_ENGINE_PARAMETER_FILE_NAMES = (
    "deployment-parameters.json",
    "parameters.json",
)

# Fields that are safe to carry from an engine parameter file to the public
# schema.  Values such as binary paths and environment variables are kept in
# the registry record and are never accepted from a deployment request.
_PARAMETER_METADATA_FIELDS = (
    "label", "type", "description", "flag", "false_flag", "env", "group",
    "common", "managed", "default", "min", "max", "step", "values",
    "placeholder", "visible_when", "requires", "conflicts", "source",
    "secret", "deprecated", "load_phase",
)

# One deployment contract is shared by every llama.cpp engine.  A VERSION.json
# may override labels/defaults or add a genuinely new flag, but it must not
# make common controls disappear from the model-manager drawer.
_COMMON_ENGINE_PARAMETER_DEFINITIONS = (
    {"key": "ctx_size", "label": "上下文大小", "type": "integer", "flag": "--ctx-size", "min": 4096, "max": 1048576, "default": 131072, "group": "基础"},
    {"key": "concurrency", "label": "并发数", "type": "integer", "flag": "-np", "min": 1, "max": 32, "default": 1, "group": "基础"},
    {"key": "batch", "label": "Batch", "type": "integer", "flag": "-b", "min": 1, "max": 65536, "default": 1024, "group": "性能"},
    {"key": "ubatch", "label": "Ubatch", "type": "integer", "flag": "-ub", "min": 1, "max": 65536, "default": 512, "group": "性能"},
    {"key": "threads", "label": "CPU 线程", "type": "integer", "flag": "-t", "min": 1, "max": 512, "default": 8, "group": "性能"},
    {"key": "flash_attn", "label": "Flash Attention", "type": "select", "flag": "--flash-attn", "values": ["on", "off", "auto"], "default": "on", "group": "性能"},
    {"key": "device", "label": "设备", "type": "select", "flag": "--device", "values": [], "default": "", "group": "设备"},
    {"key": "fit", "label": "Fit 模式", "type": "select", "flag": "--fit", "values": ["on", "off"], "default": "", "group": "设备"},
    {"key": "k_cache_type", "label": "K Cache", "type": "select", "flag": "--cache-type-k", "values": [], "default": "q8_0", "group": "KV Cache"},
    {"key": "v_cache_type", "label": "V Cache", "type": "select", "flag": "--cache-type-v", "values": [], "default": "q8_0", "group": "KV Cache"},
    {"key": "draft_k_cache_type", "label": "Draft K Cache", "type": "select", "flag": "--cache-type-k-draft", "values": [], "default": "q8_0", "group": "KV Cache"},
    {"key": "draft_v_cache_type", "label": "Draft V Cache", "type": "select", "flag": "--cache-type-v-draft", "values": [], "default": "q8_0", "group": "KV Cache"},
    {"key": "kv_unified", "label": "统一 KV Cache", "type": "boolean", "flag": "--kv-unified", "default": False, "group": "KV Cache"},
    {"key": "cache_reuse", "label": "Cache reuse", "type": "integer", "flag": "--cache-reuse", "min": 0, "max": 1048576, "default": None, "group": "KV Cache"},
    {"key": "spec_type", "label": "推测解码类型", "type": "multi-select", "flag": "--spec-type", "values": [], "default": "none", "group": "推测解码"},
    {"key": "spec_draft_n_max", "label": "Draft 预测数", "type": "integer", "flag": "--spec-draft-n-max", "min": 0, "max": 32, "default": 3, "group": "推测解码"},
    {"key": "spec_draft_p_min", "label": "Draft 接受阈值", "type": "number", "flag": "--spec-draft-p-min", "min": 0, "max": 1, "step": 0.01, "default": None, "group": "推测解码"},
    {"key": "draft_model", "label": "外置草稿模型", "type": "model", "flag": "--model-draft", "default": "", "group": "推测解码"},
)

# Keep the fallback useful when an older node has not received the catalog
# files yet.  New nodes load the same descriptors from common/deployment-
# parameters.json and can extend this list without a frontend code change.
_COMMON_ENGINE_PARAMETER_DEFINITIONS += (
    {"key": "ngl", "label": "GPU 层数", "type": "integer", "flag": "-ngl", "min": 0, "max": 999, "default": 99, "group": "加载", "managed": True},
    {"key": "threads_batch", "label": "批处理线程", "type": "integer", "flag": "--threads-batch", "min": 1, "max": 512, "default": None, "group": "性能"},
    {"key": "threads_http", "label": "HTTP 线程", "type": "integer", "flag": "--threads-http", "min": 1, "max": 512, "default": 4, "group": "服务", "managed": True},
    {"key": "chunked_batch", "label": "连续批处理", "type": "boolean", "flag": "--cont-batching", "default": True, "group": "性能", "managed": True},
    {"key": "poll_batch", "label": "批处理轮询", "type": "boolean", "flag": "--poll-batch", "default": False, "group": "性能", "managed": True},
    {"key": "cache_ram", "label": "Cache RAM (MiB)", "type": "integer", "flag": "--cache-ram", "min": 0, "max": 1048576, "default": 8192, "group": "KV Cache", "managed": True},
    {"key": "sleep_idle_seconds", "label": "空闲休眠（秒）", "type": "integer", "flag": "--sleep-idle-seconds", "min": 0, "max": 86400, "default": 300, "group": "服务", "managed": True},
    {"key": "n_cpu_moe", "label": "CPU MoE 层数", "type": "integer", "flag": "--n-cpu-moe", "min": 0, "max": 512, "default": 0, "group": "加载", "managed": True},
    {"key": "split_mode", "label": "多卡切分模式", "type": "select", "flag": "--split-mode", "values": ["none", "layer", "row", "tensor"], "default": "layer", "group": "设备"},
    {"key": "tensor_split", "label": "Tensor Split", "type": "string", "flag": "--tensor-split", "default": "", "group": "设备", "managed": True},
    {"key": "main_gpu", "label": "主 GPU", "type": "integer", "flag": "--main-gpu", "min": 0, "max": 64, "default": 0, "group": "设备"},
    {"key": "fit_target", "label": "Fit 显存余量（MiB）", "type": "string", "flag": "--fit-target", "default": "", "group": "设备"},
    {"key": "fit_ctx", "label": "Fit 最小上下文", "type": "integer", "flag": "--fit-ctx", "min": 0, "max": 1048576, "default": 4096, "group": "设备"},
    {"key": "load_mode", "label": "模型加载策略", "type": "select", "flag": "--load-mode", "values": ["none", "mmap", "mlock", "mmap+mlock", "dio"], "default": "mmap", "group": "加载"},
    {"key": "no_mmap", "label": "禁用 mmap", "type": "boolean", "flag": "--no-mmap", "default": False, "group": "加载", "managed": True},
    {"key": "use_mlock", "label": "锁定模型内存", "type": "boolean", "flag": "--mlock", "default": False, "group": "加载", "managed": True},
    {"key": "numa", "label": "NUMA 策略", "type": "select", "flag": "--numa", "values": ["", "distribute", "isolate", "numactl"], "default": "", "group": "加载", "managed": True},
    {"key": "spec_draft_n_min", "label": "Draft 最小预测数", "type": "integer", "flag": "--spec-draft-n-min", "min": 0, "max": 32, "default": 0, "group": "推测解码"},
    {"key": "spec_draft_p_split", "label": "Draft 分裂概率", "type": "number", "flag": "--spec-draft-p-split", "min": 0, "max": 1, "step": 0.01, "default": 0.1, "group": "推测解码"},
    {"key": "draft_device", "label": "Draft 设备", "type": "string", "flag": "--spec-draft-device", "default": "", "group": "推测解码"},
    {"key": "draft_layers", "label": "Draft GPU 层数", "type": "string", "flag": "--spec-draft-ngl", "default": "auto", "group": "推测解码"},
    {"key": "draft_threads", "label": "Draft CPU 线程", "type": "integer", "flag": "--spec-draft-threads", "min": 1, "max": 512, "default": None, "group": "推测解码"},
    {"key": "ngram_mod_n_min", "label": "n-gram 最小长度", "type": "integer", "flag": "--spec-ngram-mod-n-min", "min": 1, "max": 256, "default": 48, "group": "n-gram", "managed": True},
    {"key": "ngram_mod_n_max", "label": "n-gram 最大长度", "type": "integer", "flag": "--spec-ngram-mod-n-max", "min": 1, "max": 256, "default": 64, "group": "n-gram", "managed": True},
    {"key": "ngram_mod_n_match", "label": "n-gram 匹配长度", "type": "integer", "flag": "--spec-ngram-mod-n-match", "min": 1, "max": 256, "default": 24, "group": "n-gram", "managed": True},
    {"key": "ngram_simple_size_n", "label": "n-gram simple N", "type": "integer", "flag": "--spec-ngram-simple-size-n", "min": 1, "max": 256, "default": 16, "group": "n-gram"},
    {"key": "ngram_simple_size_m", "label": "n-gram simple M", "type": "integer", "flag": "--spec-ngram-simple-size-m", "min": 1, "max": 256, "default": 4, "group": "n-gram"},
    {"key": "ngram_simple_min_hits", "label": "n-gram simple 最小命中", "type": "integer", "flag": "--spec-ngram-simple-min-hits", "min": 1, "max": 256, "default": 1, "group": "n-gram"},
    {"key": "ngram_map_k_size_n", "label": "map-k N", "type": "integer", "flag": "--spec-ngram-map-k-size-n", "min": 1, "max": 256, "default": 16, "group": "n-gram"},
    {"key": "ngram_map_k_size_m", "label": "map-k M", "type": "integer", "flag": "--spec-ngram-map-k-size-m", "min": 1, "max": 256, "default": 4, "group": "n-gram"},
    {"key": "ngram_map_k_min_hits", "label": "map-k 最小命中", "type": "integer", "flag": "--spec-ngram-map-k-min-hits", "min": 1, "max": 256, "default": 1, "group": "n-gram"},
    {"key": "ngram_map_k4v_size_n", "label": "map-k4v N", "type": "integer", "flag": "--spec-ngram-map-k4v-size-n", "min": 1, "max": 256, "default": 16, "group": "n-gram"},
    {"key": "ngram_map_k4v_size_m", "label": "map-k4v M", "type": "integer", "flag": "--spec-ngram-map-k4v-size-m", "min": 1, "max": 256, "default": 4, "group": "n-gram"},
    {"key": "ngram_map_k4v_min_hits", "label": "map-k4v 最小命中", "type": "integer", "flag": "--spec-ngram-map-k4v-min-hits", "min": 1, "max": 256, "default": 1, "group": "n-gram"},
    {"key": "mmproj", "label": "视觉投影组件", "type": "artifact", "flag": "--mmproj", "default": False, "group": "多模态", "managed": True},
    {"key": "mmproj_offload", "label": "视觉组件 GPU offload", "type": "boolean", "flag": "--mmproj-offload", "false_flag": "--no-mmproj-offload", "default": True, "group": "多模态"},
    {"key": "reasoning", "label": "Reasoning", "type": "select", "flag": "--reasoning", "values": ["on", "off", "auto"], "default": "auto", "group": "生成", "managed": True},
    {"key": "reasoning_format", "label": "Reasoning 格式", "type": "select", "flag": "--reasoning-format", "values": ["auto", "none", "deepseek", "deepseek-legacy"], "default": "auto", "group": "生成"},
    {"key": "reasoning_budget", "label": "Reasoning 预算", "type": "integer", "flag": "--reasoning-budget", "min": -1, "max": 1048576, "default": -1, "group": "生成"},
    {"key": "reasoning_preserve", "label": "保留 Reasoning 轨迹", "type": "boolean", "flag": "--reasoning-preserve", "false_flag": "--no-reasoning-preserve", "default": False, "group": "生成"},
    {"key": "temp", "label": "Temperature", "type": "number", "flag": "--temp", "min": 0, "max": 5, "step": 0.01, "default": 0.7, "group": "生成", "managed": True},
    {"key": "metrics", "label": "Metrics", "type": "boolean", "flag": "--metrics", "default": True, "group": "服务", "managed": True},
    {"key": "ui", "label": "内置 Web UI", "type": "boolean", "flag": "--ui", "default": False, "group": "服务", "managed": True},
)

_COMMON_ENGINE_PARAMETER_ALIASES = {
    "ctx_size": ("--ctx-size",),
    "concurrency": ("-np",),
    "batch": ("-b", "--batch-size"),
    "ubatch": ("-ub", "--ubatch-size"),
    "threads": ("-t",),
    "flash_attn": ("--flash-attn",),
    "device": ("--device",),
    "fit": ("--fit",),
    "k_cache_type": ("--cache-type-k",),
    "v_cache_type": ("--cache-type-v",),
    "draft_k_cache_type": ("--cache-type-k-draft", "--spec-draft-type-k"),
    "draft_v_cache_type": ("--cache-type-v-draft", "--spec-draft-type-v"),
    "kv_unified": ("--kv-unified",),
    "cache_reuse": ("--cache-reuse",),
    "spec_type": ("--spec-type",),
    "spec_draft_n_max": ("--spec-draft-n-max",),
    "spec_draft_p_min": ("--spec-draft-p-min", "--draft-p-min"),
    "draft_model": ("--model-draft", "--spec-draft-model"),
}
_COMMON_ENGINE_PARAMETER_ALIASES.update({
    "ngl": ("-ngl", "--gpu-layers", "--n-gpu-layers"),
    "threads_batch": ("-tb", "--threads-batch"),
    "threads_http": ("--threads-http",),
    "chunked_batch": ("-cb", "--cont-batching"),
    "poll_batch": ("--poll-batch",),
    "cache_ram": ("--cache-ram",),
    "sleep_idle_seconds": ("--sleep-idle-seconds",),
    "n_cpu_moe": ("-ncmoe", "--n-cpu-moe"),
    "split_mode": ("-sm", "--split-mode"),
    "tensor_split": ("-ts", "--tensor-split"),
    "main_gpu": ("-mg", "--main-gpu"),
    "fit_target": ("-fitt", "--fit-target"),
    "fit_ctx": ("-fitc", "--fit-ctx"),
    "load_mode": ("-lm", "--load-mode"),
    "no_mmap": ("--no-mmap",),
    "use_mlock": ("--mlock",),
    "numa": ("--numa",),
    "spec_draft_n_min": ("--spec-draft-n-min",),
    "spec_draft_p_split": ("--spec-draft-p-split", "--draft-p-split"),
    "draft_device": ("--spec-draft-device", "--device-draft"),
    "draft_layers": ("--spec-draft-ngl", "--gpu-layers-draft", "--n-gpu-layers-draft"),
    "draft_threads": ("--spec-draft-threads", "--threads-draft"),
    "ngram_mod_n_min": ("--spec-ngram-mod-n-min",),
    "ngram_mod_n_max": ("--spec-ngram-mod-n-max",),
    "ngram_mod_n_match": ("--spec-ngram-mod-n-match",),
    "ngram_simple_size_n": ("--spec-ngram-simple-size-n",),
    "ngram_simple_size_m": ("--spec-ngram-simple-size-m",),
    "ngram_simple_min_hits": ("--spec-ngram-simple-min-hits",),
    "ngram_map_k_size_n": ("--spec-ngram-map-k-size-n",),
    "ngram_map_k_size_m": ("--spec-ngram-map-k-size-m",),
    "ngram_map_k_min_hits": ("--spec-ngram-map-k-min-hits",),
    "ngram_map_k4v_size_n": ("--spec-ngram-map-k4v-size-n",),
    "ngram_map_k4v_size_m": ("--spec-ngram-map-k4v-size-m",),
    "ngram_map_k4v_min_hits": ("--spec-ngram-map-k4v-min-hits",),
    "mmproj": ("-mm", "--mmproj"),
    "mmproj_offload": ("--mmproj-offload",),
    "reasoning": ("-rea", "--reasoning"),
    "reasoning_format": ("--reasoning-format",),
    "reasoning_budget": ("--reasoning-budget",),
    "reasoning_preserve": ("--reasoning-preserve",),
    "temp": ("--temp",),
    "metrics": ("--metrics",),
    "ui": ("--ui",),
})


def _merge_parameter_lists(base: list[Any], override: list[Any]) -> list[dict[str, Any]]:
    """Merge parameter descriptors by key while preserving catalog order."""
    merged: list[dict[str, Any]] = []
    positions: dict[str, int] = {}
    for item in [*base, *override]:
        if not isinstance(item, dict):
            continue
        key = str(item.get("key") or "").strip()
        if not key:
            continue
        value = dict(item)
        if key in positions:
            merged[positions[key]].update(value)
        else:
            positions[key] = len(merged)
            merged.append(value)
    return merged


def _merge_engine_parameter_config(base: dict[str, Any], override: dict[str, Any]) -> dict[str, Any]:
    """Merge a common parameter catalog with one engine's overrides."""
    result = dict(base)
    for key, value in override.items():
        if key in {"parameters", "parameter_schema"}:
            continue
        if key == "profiles" and isinstance(value, dict) and isinstance(result.get(key), dict):
            profiles = dict(result[key])
            for profile_id, profile in value.items():
                if isinstance(profile, dict) and isinstance(profiles.get(profile_id), dict):
                    merged_profile = dict(profiles[profile_id])
                    merged_profile.update(profile)
                    if isinstance(profiles[profile_id].get("parameters"), dict) and isinstance(profile.get("parameters"), dict):
                        merged_profile["parameters"] = {
                            **profiles[profile_id]["parameters"],
                            **profile["parameters"],
                        }
                    profiles[profile_id] = merged_profile
                else:
                    profiles[profile_id] = profile
            result[key] = profiles
            continue
        if isinstance(value, dict) and isinstance(result.get(key), dict):
            nested = dict(result[key])
            nested.update(value)
            result[key] = nested
        else:
            result[key] = value
    base_parameters = base.get("parameters") or base.get("parameter_schema") or []
    override_parameters = override.get("parameters") or override.get("parameter_schema") or []
    result["parameters"] = _merge_parameter_lists(
        base_parameters if isinstance(base_parameters, list) else [],
        override_parameters if isinstance(override_parameters, list) else [],
    )
    return result


def _load_engine_parameter_config(version_file: str, raw_engine: dict[str, Any]) -> tuple[dict[str, Any], str]:
    """Load the optional parameter file beside VERSION.json.

    An engine directory owns its deployment defaults and capability notes.  A
    small ``extends`` file lets all engines share the same llama.cpp catalog
    while keeping engine-specific profiles, load strategy and environment
    settings next to the binary.  Missing or invalid files are intentionally
    non-fatal so old VERSION.json deployments keep working.
    """
    registry_dir = Path(version_file).resolve().parent
    candidates: list[Path] = []
    configured = raw_engine.get("parameter_file")
    if isinstance(configured, str) and configured.strip():
        candidates.append((registry_dir / configured).resolve())
    candidates.extend((registry_dir / name).resolve() for name in _ENGINE_PARAMETER_FILE_NAMES)
    parameter_file = next((path for path in candidates if path.is_file()), None)
    if parameter_file is None:
        return {}, ""

    engines_root = Path(_ENGINES_DIR).resolve()
    loaded: set[Path] = set()

    def read_file(path: Path) -> dict[str, Any]:
        path = path.resolve()
        if path in loaded:
            raise ValueError(f"循环 extends: {path}")
        try:
            path.relative_to(engines_root)
        except ValueError:
            raise ValueError(f"参数文件超出引擎目录: {path}")
        loaded.add(path)
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError) as exc:
            raise ValueError(f"无法读取参数文件 {path}: {exc}")
        if not isinstance(payload, dict):
            raise ValueError(f"参数文件必须是 JSON 对象: {path}")
        parent: dict[str, Any] = {}
        extends = payload.get("extends")
        if isinstance(extends, str) and extends.strip():
            parent = read_file((path.parent / extends).resolve())
        current = dict(payload)
        current.pop("extends", None)
        return _merge_engine_parameter_config(parent, current)

    try:
        return read_file(parameter_file), str(parameter_file)
    except ValueError as exc:
        print(f"[WARN] Failed to load engine parameter file {parameter_file}: {exc}")
        return {}, str(parameter_file)


def _engine_parameter_definitions(engine: dict[str, Any]) -> list[dict[str, Any]]:
    """Return the code fallback plus descriptors supplied by the registry."""
    definitions = [dict(item) for item in _COMMON_ENGINE_PARAMETER_DEFINITIONS]
    configured = engine.get("_parameter_file_parameters")
    if isinstance(configured, list):
        definitions = _merge_parameter_lists(definitions, configured)
    return definitions


def _normalize_engine_parameter_metadata(
    engine: dict[str, Any], probed: dict[str, Any] | None = None,
) -> tuple[list[dict[str, Any]], list[str], dict[str, Any], list[str], dict[str, Any]]:
    """Build one common deployment schema with per-engine recommendations.

    ``parameter_schema`` remains accepted as a registry override for
    compatibility. The returned schema always starts with the same common
    controls, then appends genuinely engine-specific keys. ``recommended``
    and ``differences`` describe tuning choices, not a second incompatible
    request format.
    """
    probed = probed or {}
    definitions = _engine_parameter_definitions(engine)
    raw_schema = engine.get("parameter_schema")
    explicit: dict[str, dict[str, Any]] = {}
    if isinstance(raw_schema, list):
        for item in raw_schema:
            if not isinstance(item, dict):
                continue
            key = str(item.get("key") or "").strip()
            if key:
                explicit[key] = dict(item)

    notes: list[str] = []
    raw_notes = engine.get("parameter_notes")
    if isinstance(raw_notes, list):
        notes = [str(note).strip() for note in raw_notes if str(note).strip()]
    elif isinstance(raw_notes, str) and raw_notes.strip():
        notes = [raw_notes.strip()]

    recommended: dict[str, Any] = {}
    raw_recommended = engine.get("recommended_params")
    if isinstance(raw_recommended, dict):
        recommended = dict(raw_recommended)
    backend = str(engine.get("backend") or "").lower()
    if "device" not in recommended:
        if backend == "rocm":
            recommended["device"] = "ROCm0"
        elif backend == "vulkan":
            recommended["device"] = "Vulkan0"
    if "spec_draft_n_max" not in recommended and engine.get("version_params", {}).get("spec_draft_n_max") is not None:
        recommended["spec_draft_n_max"] = engine.get("version_params", {}).get("spec_draft_n_max")

    support = probed.get("parameter_support") if isinstance(probed.get("parameter_support"), dict) else {}
    cache_values = probed.get("cache_types") if isinstance(probed.get("cache_types"), list) else list(_LLAMA_CACHE_TYPES)
    draft_cache_values = probed.get("draft_cache_types") if isinstance(probed.get("draft_cache_types"), list) else cache_values
    spec_values = probed.get("spec_types") if isinstance(probed.get("spec_types"), list) else list(_LLAMA_SPEC_TYPES)
    common_keys = {definition["key"] for definition in definitions}
    schema: list[dict[str, Any]] = []
    differences: dict[str, Any] = {}
    for definition in definitions:
        key = definition["key"]
        item = dict(definition)
        override = explicit.get(key, {})
        for field in _PARAMETER_METADATA_FIELDS:
            if field in override:
                item[field] = override[field]
        if key in {"k_cache_type", "v_cache_type"}:
            item["values"] = list(cache_values)
        elif key in {"draft_k_cache_type", "draft_v_cache_type"}:
            item["values"] = list(draft_cache_values)
        elif key == "spec_type":
            item["values"] = list(spec_values)
        elif key == "device":
            item["values"] = list(probed.get("device_values") or override.get("values") or [])
        configured_supported = override.get("supported", definition.get("supported"))
        if configured_supported is True:
            supported = True
        elif configured_supported is False:
            supported = False
        else:
            supported = bool(key in explicit or support.get(key, False))
        item["supported"] = supported
        item["common"] = bool(item.get("common", definition.get("common", True)))
        item["recommended"] = recommended.get(key, item.get("default"))
        if key in recommended and recommended[key] != item.get("default"):
            differences[key] = {
                "recommended": recommended[key],
                "default": item.get("default"),
                "reason": str(override.get("description") or "该引擎的实测推荐值"),
            }
        elif key in explicit and "default" in override and override.get("default") != definition.get("default"):
            differences[key] = {
                "recommended": override.get("default"),
                "default": definition.get("default"),
                "reason": str(override.get("description") or "引擎注册表推荐值"),
            }
        schema.append(item)

    exclusive: list[str] = [
        str(item.get("key"))
        for item in schema
        if item.get("common") is False and str(item.get("key") or "").strip()
    ]
    for key, override in explicit.items():
        if key in common_keys:
            continue
        item = {"key": key, "common": bool(override.get("common", False)), "supported": True}
        for field in _PARAMETER_METADATA_FIELDS:
            if field in override:
                item[field] = override[field]
        item["recommended"] = recommended.get(key, item.get("default"))
        schema.append(item)
        exclusive.append(key)
    raw_exclusive = engine.get("exclusive_parameters")
    if isinstance(raw_exclusive, list):
        exclusive = list(dict.fromkeys([*exclusive, *[str(key) for key in raw_exclusive if str(key).strip()]]))
    raw_differences = engine.get("parameter_differences")
    if isinstance(raw_differences, dict):
        differences.update(raw_differences)
    return schema, notes, recommended, exclusive, differences


def _engine_supports_parameter(engine: dict[str, Any] | None, key: str) -> bool:
    if not engine:
        return False
    schema = engine.get("deployment_parameters") or engine.get("parameter_schema")
    if not isinstance(schema, list):
        return False
    return any(
        isinstance(item, dict)
        and str(item.get("key") or "") == key
        and item.get("supported", True) is not False
        for item in schema
    )


_LEGACY_MANAGED_PARAMETER_KEYS = {
    "ctx_size", "ngl", "concurrency", "batch", "ubatch", "threads",
    "flash_attn", "device", "fit", "k_cache_type", "v_cache_type",
    "draft_k_cache_type", "draft_v_cache_type", "kv_unified", "cache_reuse",
    "spec_type", "spec_draft_n_max", "spec_draft_p_min", "draft_model",
    "threads_http", "chunked_batch", "temp", "ui", "mmproj", "cache_ram",
    "sleep_idle_seconds", "no_mmap", "use_mlock", "numa", "poll_batch",
    "n_cpu_moe", "tensor_split", "ngram_mod_n_min", "ngram_mod_n_max",
    "ngram_mod_n_match", "reasoning", "metrics",
}


def _engine_parameter_map(engine: dict[str, Any] | None) -> dict[str, dict[str, Any]]:
    if not engine:
        return {}
    return {
        str(item.get("key")): item
        for item in (engine.get("deployment_parameters") or engine.get("parameter_schema") or [])
        if isinstance(item, dict) and str(item.get("key") or "").strip()
    }


def _parameter_is_managed(item: dict[str, Any]) -> bool:
    return bool(item.get("managed") or str(item.get("key") or "") in _LEGACY_MANAGED_PARAMETER_KEYS)


def _apply_canonical_parameter_payload(data: dict[str, Any]) -> tuple[dict[str, Any], dict[str, Any]]:
    """Accept the v2 ``parameters`` map while retaining the flat API.

    Flat keys win when both forms are present, which keeps old clients and
    browser retries deterministic during the migration.
    """
    canonical = data.get("parameters")
    if not isinstance(canonical, dict):
        canonical = data.get("parameter_values")
    if not isinstance(canonical, dict):
        canonical = {}
    merged = dict(data)
    for key, value in canonical.items():
        if str(key).strip() and key not in merged:
            merged[key] = value
    return merged, {str(key): value for key, value in canonical.items() if str(key).strip()}


class _JsonBodyRequest:
    """Small internal adapter for routing engine switches through deploy_model."""

    def __init__(self, payload: dict[str, Any]):
        self.payload = payload

    async def json(self) -> dict[str, Any]:
        return self.payload


def _validate_canonical_parameters(engine: dict[str, Any], values: dict[str, Any]) -> None:
    schema = _engine_parameter_map(engine)
    for key, value in values.items():
        item = schema.get(key)
        if item is None:
            raise HTTPException(400, f"所选引擎参数未注册: {key}")
        if item.get("supported") is False:
            raise HTTPException(400, f"所选引擎不支持参数: {key}")
        if value in (None, ""):
            continue
        values_allowed = item.get("values")
        if isinstance(values_allowed, list) and values_allowed and item.get("type") in {"select", "multi-select"}:
            candidates = value if isinstance(value, list) else [value]
            invalid = [str(entry) for entry in candidates if str(entry) not in {str(v) for v in values_allowed}]
            if invalid:
                raise HTTPException(400, f"参数 {key} 不受支持: {', '.join(invalid)}")
        if item.get("type") == "integer":
            _bounded_int(value, key, int(item.get("min", -2147483648)), int(item.get("max", 2147483647)))
        elif item.get("type") == "number":
            _bounded_float(value, key, float(item.get("min", -1e12)), float(item.get("max", 1e12)))
        elif item.get("type") == "boolean" and not isinstance(value, (bool, int, str)):
            raise HTTPException(400, f"参数 {key} 必须是布尔值")


def _generic_parameter_flags(
    engine: dict[str, Any] | None,
    values: dict[str, Any],
    *,
    skip: set[str] | None = None,
) -> tuple[list[str], list[str]]:
    """Render registry-declared non-legacy flags and environment variables."""
    parameter_map = _engine_parameter_map(engine)
    skipped = skip or set()
    args: list[str] = []
    exports: list[str] = []
    for key, value in values.items():
        item = parameter_map.get(key)
        if not item or item.get("supported") is False or _parameter_is_managed(item) or key in skipped:
            continue
        if value in (None, ""):
            continue
        value_type = str(item.get("type") or "string")
        if value_type == "boolean":
            enabled = value is True or str(value).lower() in {"1", "true", "on", "yes"}
            flag = str(item.get("flag") or "").strip()
            false_flag = str(item.get("false_flag") or "").strip()
            if item.get("env"):
                exports.append(f"export {item['env']}={'1' if enabled else '0'}")
            elif enabled and flag:
                args.append(flag)
            elif not enabled and false_flag and item.get("default") is True:
                args.append(false_flag)
            continue
        if isinstance(value, list):
            rendered = ",".join(str(entry) for entry in value)
        else:
            rendered = str(value)
        flag = str(item.get("flag") or "").strip()
        if item.get("env"):
            exports.append(f"export {item['env']}={shlex.quote(rendered)}")
        elif flag:
            args.extend([flag, shlex.quote(rendered)])
    return args, exports


def _engine_environment_exports(engine: dict[str, Any] | None) -> list[str]:
    """Render static, non-secret environment defaults from an engine file."""
    if not engine:
        return []
    raw = engine.get("engine_environment")
    if not isinstance(raw, dict):
        return []
    defaults = raw.get("defaults") if isinstance(raw.get("defaults"), dict) else raw
    exports: list[str] = []
    for key, value in defaults.items():
        name = str(key).strip()
        if not re.fullmatch(r"[A-Z][A-Z0-9_]{1,127}", name) or "KEY" in name or "TOKEN" in name or "PASSWORD" in name:
            continue
        if isinstance(value, (str, int, float, bool)):
            exports.append(f"export {name}={shlex.quote(str(value).lower() if isinstance(value, bool) else str(value))}")
    return exports


def _help_allowed_values(help_text: str, flag: str) -> list[str]:
    """Extract an argparse ``allowed values`` line following one exact flag."""
    lines = (help_text or "").splitlines()
    for index, line in enumerate(lines):
        # Matching the exact long flag avoids treating --cache-type-k-draft as
        # the target cache option when probing --cache-type-k.
        if not re.search(rf"(?<![A-Za-z0-9_-]){re.escape(flag)}(?:\s|,|$)", line):
            continue
        block = "\n".join(lines[index:index + 8])
        match = re.search(r"allowed values:\s*([^\n]+)", block, re.IGNORECASE)
        if not match:
            continue
        return list(dict.fromkeys(re.findall(r"[A-Za-z][A-Za-z0-9_]*", match.group(1))))
    return []


def _help_spec_types(help_text: str) -> list[str]:
    match = re.search(r"--spec-type\s+([^\n]+)", help_text or "", re.IGNORECASE)
    if not match:
        return []
    values = re.split(r"[,\s]+", match.group(1).strip())
    return [item for item in dict.fromkeys(values) if re.fullmatch(r"[a-z0-9-]+", item)]


def _probe_engine_capabilities(binary_path: str) -> dict[str, Any]:
    """Probe capabilities that are not reliably recorded in VERSION.json.

    VERSION.json describes how an engine was built, but older registry files
    do not list speculative-decoding flags.  The binary's own help output is
    the authoritative compatibility surface for the UI and deployment guard.
    Only binaries under the configured engine root are executed.
    """
    if not binary_path:
        return {}
    try:
        binary = Path(binary_path).resolve()
        engine_root = Path(_ENGINES_DIR).resolve()
        binary.relative_to(engine_root)
        stat = binary.stat()
    except (OSError, ValueError):
        return {}
    if not binary.is_file() or not os.access(binary, os.X_OK):
        return {}

    cache_key = str(binary)
    cached = _ENGINE_CAPABILITIES_CACHE.get(cache_key)
    fingerprint = (int(stat.st_mtime_ns), int(stat.st_size))
    if cached and cached[:2] == fingerprint:
        return dict(cached[2])

    capabilities: dict[str, Any] = {}
    try:
        result = subprocess.run(
            [str(binary), "--help"],
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            text=True,
            timeout=5,
            check=False,
        )
        help_text = result.stdout or ""
        cache_types = _help_allowed_values(help_text, "--cache-type-k")
        draft_cache_types = _help_allowed_values(help_text, "--cache-type-k-draft")
        spec_types = _help_spec_types(help_text)
        parameter_support = {}
        for parameter_key, aliases in _COMMON_ENGINE_PARAMETER_ALIASES.items():
            parameter_support[parameter_key] = any(
                re.search(rf"(?<![A-Za-z0-9_-]){re.escape(alias)}(?:\s|,|$)", help_text)
                for alias in aliases
            )
        device_values: list[str] = []
        try:
            device_result = subprocess.run(
                [str(binary), "--list-devices"],
                stdout=subprocess.PIPE,
                stderr=subprocess.STDOUT,
                text=True,
                timeout=5,
                check=False,
            )
            device_values = list(dict.fromkeys(
                match.group(1)
                for match in re.finditer(r"^\s*([A-Za-z][A-Za-z0-9_-]*\d+):", device_result.stdout or "", re.MULTILINE)
            ))
        except (OSError, subprocess.SubprocessError):
            device_values = []
        capabilities = {
            "cache_types": cache_types or list(_LLAMA_CACHE_TYPES),
            "draft_cache_types": draft_cache_types or cache_types or list(_LLAMA_CACHE_TYPES),
            "spec_types": spec_types or list(_LLAMA_SPEC_TYPES),
            "parameter_support": parameter_support,
            "device_values": device_values,
            "supports_mtp": bool(re.search(r"\bdraft-mtp\b", help_text, re.IGNORECASE)),
            "supports_draft_model": bool(re.search(r"(?:--model-draft|--spec-draft-model|\s-md[ ,])", help_text, re.IGNORECASE)),
            "supports_ngram": bool(re.search(r"\bngram-(?:simple|map-k|mod|cache)\b", help_text, re.IGNORECASE)),
        }
        if capabilities["supports_mtp"]:
            capabilities["spec_draft_n_max"] = 3
    except (OSError, subprocess.SubprocessError):
        capabilities = {}

    _ENGINE_CAPABILITIES_CACHE[cache_key] = (fingerprint[0], fingerprint[1], dict(capabilities))
    return capabilities

def _normalize_engine_record(eng, version_file):
    """兼容新旧 VERSION.json，并补齐模型管理所需的注册字段。"""
    registry_dir = os.path.dirname(version_file)
    registry_key = os.path.basename(registry_dir)
    raw_key = eng.get("key") or eng.get("id") or eng.get("backend") or registry_key
    key = str(raw_key).strip()
    if not key or not re.fullmatch(r"[A-Za-z0-9._-]+", key):
        raise ValueError(f"invalid engine key: {raw_key!r}")

    parameter_config, parameter_file = _load_engine_parameter_config(version_file, eng)
    effective = dict(parameter_config)
    effective.update(eng)
    file_parameters = parameter_config.get("parameters") if isinstance(parameter_config, dict) else None
    if isinstance(file_parameters, list):
        effective["_parameter_file_parameters"] = file_parameters
    # VERSION.json remains the highest-priority compatibility override for
    # older nodes that already declare a small parameter_schema.
    if isinstance(eng.get("parameter_schema"), list):
        effective["parameter_schema"] = list(eng["parameter_schema"])
    profiles = parameter_config.get("profiles") if isinstance(parameter_config, dict) else {}
    profile_defaults: dict[str, Any] = {}
    if isinstance(profiles, dict):
        default_profile = profiles.get("default")
        if isinstance(default_profile, dict):
            candidate = default_profile.get("parameters") or default_profile.get("values")
            if isinstance(candidate, dict):
                profile_defaults.update(candidate)
    load_defaults = parameter_config.get("load_strategy", {}).get("default", {}) if isinstance(parameter_config.get("load_strategy"), dict) else {}
    if isinstance(load_defaults, dict):
        for default_key in ("load_mode", "fit", "kv_unified", "device"):
            if default_key in load_defaults and default_key not in profile_defaults:
                profile_defaults[default_key] = load_defaults[default_key]
        if "kv_cache" in load_defaults:
            profile_defaults.setdefault("k_cache_type", load_defaults["kv_cache"])
            profile_defaults.setdefault("v_cache_type", load_defaults["kv_cache"])
    config_recommended = parameter_config.get("recommended_params") if isinstance(parameter_config, dict) else {}
    version_recommended = eng.get("recommended_params")
    recommended: dict[str, Any] = {}
    for candidate in (profile_defaults, config_recommended, version_recommended):
        if isinstance(candidate, dict):
            recommended.update(candidate)
    if recommended:
        effective["recommended_params"] = recommended
    if parameter_file:
        effective["_parameter_config_path"] = parameter_file

    normalized = dict(effective)
    normalized["key"] = key
    normalized["name"] = str(effective.get("name") or key)
    normalized["type"] = str(effective.get("type") or effective.get("runtime_type") or "llama")
    normalized["features"] = list(effective.get("features") or [])
    normalized["version_params"] = dict(effective.get("version_params") or {})
    normalized["_dir"] = registry_dir

    binary = effective.get("binary") if isinstance(effective.get("binary"), dict) else {}
    binary_path = str(effective.get("binary_path") or binary.get("path") or "").strip()
    if not binary_path:
        candidates = (
            os.path.join(_ENGINES_DIR, "llama", f"build-{key}", "bin", "llama-server"),
            os.path.join(registry_dir, "build", "bin", "llama-server"),
            os.path.join(registry_dir, "bin", "llama-server"),
        )
        binary_path = next((path for path in candidates if os.path.isfile(path)), "")
    normalized["binary_path"] = binary_path
    probed = _probe_engine_capabilities(binary_path)
    parameter_schema, parameter_notes, recommended_params, exclusive_parameters, parameter_differences = _normalize_engine_parameter_metadata(effective, probed)
    normalized["parameter_schema"] = parameter_schema
    normalized["deployment_parameters"] = parameter_schema
    normalized["parameter_notes"] = parameter_notes
    normalized["recommended_params"] = recommended_params
    normalized["exclusive_parameters"] = exclusive_parameters
    normalized["parameter_differences"] = parameter_differences
    supports_mtp = bool(
        effective.get("supports_mtp")
        if "supports_mtp" in effective
        else normalized["version_params"].get("spec_draft_n_max", False)
        if "spec_draft_n_max" in normalized["version_params"]
        else probed.get("supports_mtp", False)
    )
    normalized["supports_mtp"] = supports_mtp
    normalized["cache_types"] = list(
        effective.get("cache_types") or probed.get("cache_types") or _LLAMA_CACHE_TYPES
    )
    normalized["draft_cache_types"] = list(
        effective.get("draft_cache_types") or probed.get("draft_cache_types") or normalized["cache_types"]
    )
    normalized["spec_types"] = list(
        effective.get("spec_types") or probed.get("spec_types") or _LLAMA_SPEC_TYPES
    )
    normalized["supports_draft_model"] = bool(
        effective.get("supports_draft_model")
        if "supports_draft_model" in effective
        else probed.get("supports_draft_model", False)
    )
    normalized["supports_ngram"] = bool(
        effective.get("supports_ngram")
        if "supports_ngram" in effective
        else probed.get("supports_ngram", True)
    )
    normalized["parameter_file"] = parameter_file
    normalized["parameter_config_version"] = parameter_config.get("schema_version") if parameter_config else None
    normalized["profiles"] = profiles if isinstance(profiles, dict) else {}
    normalized["load_strategy"] = parameter_config.get("load_strategy", {}) if isinstance(parameter_config, dict) else {}
    normalized["engine_environment"] = parameter_config.get("environment", {}) if isinstance(parameter_config, dict) else {}
    normalized["parameter_config_notes"] = parameter_config.get("notes", []) if isinstance(parameter_config, dict) else []
    if supports_mtp:
        normalized["features"] = list(dict.fromkeys([*normalized["features"], "MTP"]))
        normalized["version_params"].setdefault("spec_draft_n_max", 3)
    return normalized

def _scan_engines():
    """扫描 /data/engines 下所有 VERSION.json"""
    import glob, json, time as _time
    now = _time.time()
    if _ENGINES_CACHE["engines"] and (now - _ENGINES_CACHE["ts"]) < 10:
        return _ENGINES_CACHE["engines"]
    engines = []
    for vf in sorted(glob.glob(os.path.join(_ENGINES_DIR, "*/VERSION.json"))):
        try:
            with open(vf) as f:
                eng = json.load(f)
            engines.append(_normalize_engine_record(eng, vf))
        except Exception as e:
            print(f"[WARN] Failed to load {vf}: {e}")
    _ENGINES_CACHE["engines"] = engines
    _ENGINES_CACHE["ts"] = now
    return engines

def _get_engine_by_key(key):
    """按 key 查找引擎配置"""
    for e in _scan_engines():
        if e.get("key") == key:
            return e
    return None

_ACTIVE_ENGINE_STATE = "/data/inference-hub/state/active_engine"

def _get_active_engine():
    """读取当前激活引擎 key：优先 active_engine 状态文件，回退 VERSION.json 首个引擎"""
    try:
        with open(_ACTIVE_ENGINE_STATE) as f:
            key = f.read().strip()
        if key and _get_engine_by_key(key):
            return key
    except Exception:
        pass
    # 回退：扫描 VERSION.json，取第一个有效引擎
    engs = _scan_engines()
    if engs:
        return engs[0].get("key", "")
    return ""


def _engine_cache_types(engine: dict[str, Any] | None, *, draft: bool = False) -> list[str]:
    if not engine:
        return list(_LLAMA_CACHE_TYPES)
    key = "draft_cache_types" if draft else "cache_types"
    values = engine.get(key)
    return list(values) if isinstance(values, list) and values else list(_LLAMA_CACHE_TYPES)


def _engine_spec_types(engine: dict[str, Any] | None) -> list[str]:
    if not engine:
        return list(_LLAMA_SPEC_TYPES)
    values = engine.get("spec_types")
    return list(values) if isinstance(values, list) and values else list(_LLAMA_SPEC_TYPES)

def _engine_key_from_binary(bin_path):
    """从 binary 路径反查引擎 key（遍历 VERSION.json），找不到返回空串"""
    if not bin_path:
        return ""
    bp = str(bin_path)
    for e in _scan_engines():
        if e.get("binary_path") and bp.rstrip("/") == e["binary_path"].rstrip("/"):
            return e.get("key", "")
    # 模糊匹配：build-xxx 目录
    m = re.search(r"build-([\w-]+)/bin/llama-server", bp)
    if m:
        for e in _scan_engines():
            if e.get("key") == m.group(1):
                return e.get("key", "")
        return m.group(1)
    return ""

def _resolve_binary_path(key):
    """根据 key 解析二进制路径（从 VERSION.json 读取）。
    找不到 key 时动态兜底：取当前激活引擎或扫描到的第一个引擎——永不回退到硬编码旧路径。"""
    if not key:
        key = _get_active_engine()
    eng = _get_engine_by_key(key)
    if eng and eng.get("binary_path"):
        return eng["binary_path"]
    # An explicit unknown key must never silently select a different engine.
    return ""

def _supports_mtp(key):
    """Check the registered engine's actual MTP parser capability."""
    eng = _get_engine_by_key(key)
    if eng:
        if "supports_mtp" in eng:
            return bool(eng.get("supports_mtp"))
        vp = eng.get("version_params", {})
        return bool(vp.get("spec_draft_n_max", False))
    return False


def _supports_draft_model(key: str) -> bool:
    engine = _get_engine_by_key(key)
    return bool(engine and engine.get("supports_draft_model"))


def _draft_spec_type(record: dict[str, Any]) -> str:
    """Choose the llama.cpp speculative type for a catalog draft artifact."""
    name = str(record.get("name") or record.get("relative_path") or "").lower()
    if "dflash" in name:
        return "draft-dflash"
    if "eagle" in name:
        return "draft-eagle3"
    if "dspark" in name:
        return "draft-dspark"
    return "draft-simple"
WRITE_START_SCRIPT = str(settings.write_start_script)
def validate_start_script():
    """写入前验证 START_SCRIPT 路径安全性"""
    # 安全检查：拒绝软链接
    if os.path.islink(START_SCRIPT):
        target = os.readlink(START_SCRIPT)
        raise HTTPException(400, f"安全拦截: {START_SCRIPT} 是软链接 -> {target}，已拒绝写入。请检查文件完整性。")
    # 安全检查：必须是常规文件
    if os.path.exists(START_SCRIPT) and not os.path.isfile(START_SCRIPT):
        raise HTTPException(400, f"安全拦截: {START_SCRIPT} 不是常规文件，已拒绝写入。")


MODEL_ALIASES = {
    "gemma-4-26B-A4B-it-UD-IQ4_NL.gguf": "Gemma4-26B",
    "gemma-4-26B-A4B-it-MXFP4_MOE.gguf": "Gemma4-26B",
    "gemma-4-31B-it-IQ4_NL.gguf": "Gemma4-31B",
    "supergemma4-26b-uncensored-fast-v2-Q4_K_M.gguf": "Gemma4-26B",
    "Qwen3.6-35B-A3B-UD-IQ4_NL.gguf": "Qwen3.6-35B-A3B",
    "Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-IQ4_XS.gguf": "Qwen3.6-35B-A3B",
    "Qwopus3.6-27B-v1-preview-Q4_K_M.gguf": "Qwen3.6-27B",
    "Qwen3.5-27B-UD-IQ3_XXS.gguf": "Qwen3.5-27B",
    "Qwen3.6-27B-IQ4_NL.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-Uncensored-HauhauCS-Aggressive-IQ4_XS.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-IQ4_XS.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-Uncensored-HauhauCS-Balanced-IQ4_XS.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-Q4_K_M.gguf": "Qwen3.6-27B",
    "carnice-v2-27b-Q4_K_M.gguf": "Qwen3.6-27B",
    "Qwen3.6-35B-A3B-UD-Q4_K_M.gguf": "Qwen3.6-35B-A3B",
    "Qwen3.6-35B-A3B-Claude-4.7-Opus-Reasoning-Distilled.i1-IQ4_XS.gguf": "Qwen3.6-35B-A3B",
    "Qwopus3.6-35B-A3B-v1-IQ4_XS.gguf": "Qwen3.6-35B-A3B",
    "Qwen3.5-9B-DeepSeek-V4-Flash-Q5_K_M.gguf": "Qwen3.5-9B",
    "Qwen3.6-27B.i1-IQ4_XS-attn_qkv-IQ4_XS.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-Omnimerge-v4-IQ4_NL.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-TQ3_4S.gguf": "Qwen3.6-27B",
    "Qwen3.6-27B-UD-Q4_K_XL.gguf": "Qwen3.6-27B",
    "Hermes3.6-35B-A3B-Uncensored-Genesis-V6-APEX-Compact.gguf": "Hermes3.6-35B-A3B",
}

# === MMPROJ 视觉模块管理 ===
def _list_mmproj_files():
    """Return catalogued projection artifacts, including nested bundles."""
    files = []
    for item in catalog_service.list_models():
        if item.get("role") != "projection":
            continue
        files.append(
            {
                "id": item["id"],
                "name": item["name"],
                "path": item["path"],
                "size": item["size"],
                "family": item.get("family"),
                "relative_dir": item.get("relative_dir", ""),
            }
        )
    return files


def _list_draft_models_for_model(model: dict[str, Any] | None) -> list[dict[str, Any]]:
    """Return draft artifacts that belong to the selected model bundle.

    A draft model is an auxiliary artifact, not a deployable main model.  It
    must stay in the same bundle (or at least the same catalog family) as the
    target model so the drawer never offers an unrelated draft file merely
    because it happens to exist under /data/models.
    """
    if not model:
        return []
    relative_dir = model.get("relative_dir")
    family = model.get("family")
    candidates = []
    for item in catalog_service.list_models():
        if item.get("role") != "draft":
            continue
        if relative_dir and item.get("relative_dir") == relative_dir:
            candidates.append(item)
        elif family and item.get("family") == family:
            candidates.append(item)
    candidates.sort(key=lambda item: (item.get("name", "").lower(), item.get("id", "")))
    return candidates

def _match_mmproj_for_model(model_name_or_alias):
    """Match a projector only within the same catalog bundle.

    Never select an unrelated global fallback: a wrong projector can make a
    deployment fail after a long model load.
    """
    mmproj_files = _list_mmproj_files()
    if not mmproj_files:
        return None
    model = catalog_service.find(str(model_name_or_alias or ""))
    if model:
        same_bundle = [
            item
            for item in mmproj_files
            if item.get("relative_dir") == model.get("relative_dir")
        ]
        if len(same_bundle) == 1:
            return same_bundle[0]["id"]
    return None




# === KV Cache 推荐计算 ===
import struct as _struct

_CACHE_TYPE_BYTES = {
    "f32": 4.0, "f16": 2.0, "bf16": 2.0, "q8_0": 1.0, "q4_0": 0.5,
    "q4_1": 0.5, "q5_0": 0.625, "q5_1": 0.625,
    "turbo2": 0.375, "turbo3": 0.5, "turbo4": 0.625,
    "iq2_s": 0.3125, "iq3_s": 0.375, "iq4_nl": 0.5, "iq4_xs": 0.4375,
}

def _skip_gguf_value(f, vt):
    if vt == 8:
        sl = _struct.unpack('<Q', f.read(8))[0]
        f.read(sl)
    elif vt == 9:
        at = _struct.unpack('<I', f.read(4))[0]
        al = _struct.unpack('<Q', f.read(8))[0]
        if at == 4:
            f.read(4 * al)
        elif at == 8:
            for _ in range(al):
                s2 = _struct.unpack('<Q', f.read(8))[0]
                f.read(s2)
        else:
            sizes = {0:1,1:1,2:2,3:2,5:4,6:4,7:1,10:8,11:8,12:8}
            f.read(al * sizes.get(at, 1))
    else:
        sizes = {0:1,1:1,2:2,3:2,4:4,5:4,6:4,7:1,10:8,11:8,12:8}
        f.read(sizes.get(vt, 4))

def _read_gguf_params(gguf_path):
    needed = {"block_count", "head_count", "head_count_kv", "embedding_length", "context_length"}
    result = {}
    try:
        with open(gguf_path, 'rb') as f:
            header = f.read(24)
            kv_count = _struct.unpack('<Q', header[16:24])[0]
            for _ in range(kv_count):
                kl = _struct.unpack('<Q', f.read(8))[0]
                if kl > 1000:
                    break
                key = f.read(kl).decode('utf-8', errors='replace')
                short = key.split('.')[-1]
                vt = _struct.unpack('<I', f.read(4))[0]
                if short in needed:
                    if vt == 4:
                        result[short] = _struct.unpack('<I', f.read(4))[0]
                    elif vt == 5:
                        result[short] = _struct.unpack('<i', f.read(4))[0]
                    else:
                        _skip_gguf_value(f, vt)
                    if len(result) >= len(needed):
                        break
                else:
                    _skip_gguf_value(f, vt)
    except Exception as e:
        print(f"[GGUF Read Error] {gguf_path}: {e}")
    return result


_GPU_CACHE = {"items": [], "ts": 0.0}


def _detect_gpus():
    """Auto-detect available GPUs via nvidia-smi. Returns list of {index, name, short_name, total_mb, free_mb}."""
    now = time.monotonic()
    if _GPU_CACHE["items"] and now - _GPU_CACHE["ts"] < 60:
        return [dict(item) for item in _GPU_CACHE["items"]]
    try:
        r = subprocess.run(
            ["nvidia-smi", "--query-gpu=index,name,memory.total,memory.free",
             "--format=csv,noheader,nounits"],
            capture_output=True, text=True, timeout=5
        )
        gpus = []
        for line in r.stdout.strip().split("\n"):
            if not line.strip():
                continue
            parts = [p.strip() for p in line.split(",")]
            if len(parts) >= 4:
                name = parts[1]
                gpus.append({
                    "index": int(parts[0]),
                    "name": name,
                    "short_name": name.replace("NVIDIA GeForce ", "").replace("NVIDIA ", ""),
                    "total_mb": int(parts[2]),
                    "free_mb": int(parts[3]),
                })
        _GPU_CACHE["items"] = gpus
        _GPU_CACHE["ts"] = now
        return [dict(item) for item in gpus]
    except:
        return []

def _get_default_ngl():
    """Return sensible default -ngl based on available GPU VRAM."""
    gpus = _detect_gpus()
    if not gpus:
        return 99
    total_vram_mb = sum(g["total_mb"] for g in gpus)
    # For MoE models, ngl should cover attention layers (~48 for 35B-A3B)
    # For dense models, all layers should fit
    # Conservative: use all GPU layers by default for single GPU
    if total_vram_mb < 8192:
        return 24  # 8GB GPU
    elif total_vram_mb < 16384:
        return 48  # 12GB GPU (RTX 3060) - enough for MoE attention layers
    else:
        return 99  # 16GB+ GPU

def _get_gpu_mem_free_mb(index=0):
    for gpu in _detect_gpus():
        if int(gpu.get("index", -1)) == int(index):
            return int(gpu.get("free_mb", 0) or 0)
    return 0

def _calc_recommendation(filename, cache_type="turbo3", gpu_mode="all", v_cache_type=""):
    try:
        filepath = _safe_model_path(filename, must_exist=True)
    except HTTPException:
        return {"error": "file_not_found"}

    params = _read_gguf_params(str(filepath))
    if not params:
        return {"error": "gguf_parse_failed"}

    head_count_kv = params.get("head_count_kv", 0)
    head_count = params.get("head_count", 0)
    embedding_length = params.get("embedding_length", 0)
    block_count = params.get("block_count", 0)
    context_length = params.get("context_length", 262144)

    if not all([head_count_kv, head_count, embedding_length, block_count]):
        return {"error": "missing_params"}

    head_dim = embedding_length // head_count
    v_cache_type = v_cache_type or cache_type
    if cache_type not in _CACHE_TYPE_BYTES or v_cache_type not in _CACHE_TYPE_BYTES:
        return {"error": "unsupported_cache_type"}
    k_bpe = _CACHE_TYPE_BYTES[cache_type]
    v_bpe = _CACHE_TYPE_BYTES[v_cache_type]
    bytes_per_token = head_count_kv * head_dim * (k_bpe + v_bpe) * block_count

    detected_gpus = _detect_gpus()
    gpu_count = len(detected_gpus)
    is_unified_memory = any("amd" in str(g.get("name", "")).lower() for g in detected_gpus)
    model_size_mb = filepath.stat().st_size / (1024 * 1024)

    if is_unified_memory:
        # AMD Strix Halo exposes unified system memory through the driver.  It
        # is not 64 GiB of independent free VRAM: reserve room for the OS,
        # runtime workspaces and the target model before budgeting KV.
        mem_total_mb = 0.0
        try:
            for line in Path("/proc/meminfo").read_text().splitlines():
                if line.startswith("MemTotal:"):
                    mem_total_mb = int(line.split()[1]) / 1024
                    break
        except (OSError, ValueError, IndexError):
            pass
        total_budget_mb = mem_total_mb or sum(float(g.get("total_mb", 0)) for g in detected_gpus)
        os_reserve_mb = max(8192.0, total_budget_mb * 0.125)
        runtime_overhead_mb = 2048.0
        available_kv = max(0.0, total_budget_mb - os_reserve_mb - runtime_overhead_mb - model_size_mb)
        total_free = total_budget_mb
        system_overhead = os_reserve_mb + runtime_overhead_mb
        memory_model = "unified"
    else:
        total_free = sum(float(g.get("free_mb", 0)) for g in detected_gpus)
        system_overhead = 512.0
        available_kv = max(0.0, total_free - system_overhead - model_size_mb)
        memory_model = "discrete"

    tiers = {}
    for label, ctx_val in [("light", 32768), ("standard", 65536), ("high", 131072), ("full", context_length)]:
        kv_mb = bytes_per_token * ctx_val / (1024 * 1024)
        tiers[label] = {
            "ctx_size": ctx_val,
            "kv_mb": round(kv_mb, 1),
            "feasible": ctx_val <= context_length and kv_mb <= available_kv,
            "pct_of_available": round(kv_mb / available_kv * 100, 1) if available_kv > 0 else 0,
        }

    calculated_max_ctx = int(available_kv * 1024 * 1024 / bytes_per_token) if available_kv > 0 and bytes_per_token > 0 else 0
    max_ctx = min(int(context_length), calculated_max_ctx)
    conservative_ctx = min(max_ctx, max(4096, int(max_ctx * 0.8))) if max_ctx else 0
    max_kv_mb = bytes_per_token * max_ctx / (1024 * 1024)
    conservative_kv_mb = bytes_per_token * conservative_ctx / (1024 * 1024)
    tiers["max"] = {
        "ctx_size": max_ctx,
        "kv_mb": round(max_kv_mb, 1),
        "feasible": max_ctx > 0,
        "pct_of_available": round(max_kv_mb / available_kv * 100, 1) if available_kv else 0,
    }
    tiers["conservative"] = {
        "ctx_size": conservative_ctx,
        "kv_mb": round(conservative_kv_mb, 1),
        "feasible": conservative_ctx > 0,
        "pct_of_available": round(conservative_kv_mb / available_kv * 100, 1) if available_kv else 0,
    }

    if gpu_count <= 1 or gpu_mode != "all":
        try:
            gpu_idx = int(gpu_mode) if gpu_mode != "all" else 0
        except ValueError:
            gpu_idx = next(
                (int(g["index"]) for g in detected_gpus if g.get("short_name") == gpu_mode),
                0,
            )
        gpu_info = next(
            (g for g in detected_gpus if int(g.get("index", -1)) == gpu_idx),
            {"short_name": "GPU", "total_mb": total_free, "free_mb": total_free},
        )
        gpu_alloc = {
            "mode": "single",
            "gpu": gpu_info.get("short_name") or gpu_info.get("name") or "GPU",
            "free_mb": round(total_free, 1),
            "kv_cap_mb": round(available_kv, 1),
            "max_ctx": max_ctx,
            "memory_model": memory_model,
            "reason": "AMD 统一内存预算" if is_unified_memory else "单 GPU 显存预算",
        }
    else:
        model_total_mb = bytes_per_token * context_length / (1024 * 1024)
        gpu_caps = [max(0, float(g.get("free_mb", 0)) - 256) for g in detected_gpus]
        total_cap = sum(gpu_caps)
        if total_cap > 0:
            split_values = [max(1, round(cap / total_cap * 100)) for cap in gpu_caps]
            split_values[-1] += 100 - sum(split_values)
        else:
            split_values = [round(100 / gpu_count)] * gpu_count
            split_values[-1] += 100 - sum(split_values)

        gpu_devices = []
        for position, g in enumerate(detected_gpus):
            cap = gpu_caps[position] if position < len(gpu_caps) else 0
            gpu_devices.append({
                "index": g["index"],
                "name": g["short_name"],
                "total_mb": g["total_mb"],
                "free_mb": g["free_mb"],
                "kv_cap_mb": round(cap, 1),
                "split": split_values[position],
            })
        gpu_alloc = {
            "mode": "multi",
            "split": ",".join(str(value) for value in split_values),
            "reason": "按各 GPU 独立可用显存容量比例分配",
            "model_kv_mb": round(model_total_mb, 1),
            "memory_model": memory_model,
            "devices": gpu_devices,
        }

    return {
        "model": filename,
        "params": {
            "head_count_kv": head_count_kv,
            "head_count": head_count,
            "embedding_length": embedding_length,
            "block_count": block_count,
            "context_length": context_length,
            "head_dim": head_dim,
            "bytes_per_token": round(bytes_per_token, 1),
            "kb_per_token": round(bytes_per_token / 1024, 2),
            "k_cache_type": cache_type,
            "v_cache_type": v_cache_type,
        },
        "available_kv_mb": round(available_kv, 1),
        "phys_free_mb": total_free,
        "system_overhead_mb": round(system_overhead, 1),
        "model_size_mb": round(model_size_mb, 1),
        "memory_model": memory_model,
        "estimate": True,
        "confidence": "medium" if is_unified_memory else "high",
        "recommended_ctx": tiers,
        "gpu_allocation": gpu_alloc,
    }

MODEL_CTX_SETTINGS = {
    "gemma-4-31B-it-IQ4_NL.gguf": 32768,
    "gemma-4-26B-A4B-it-MXFP4_MOE.gguf": 32768,
    "Qwen3.6-35B-A3B-UD-IQ4_NL.gguf": 131072,
    "Qwen3.6-35B-A3B-Uncensored-HauhauCS-Aggressive-IQ4_XS.gguf": 131072,
    "Qwen3.6-27B-IQ4_NL.gguf": 131072,
    "Qwopus3.6-27B-v1-preview-Q4_K_M.gguf": 131072,
    "Qwen3.6-27B-Q4_K_M.gguf": 131072,
    "carnice-v2-27b-Q4_K_M.gguf": 131072,
    "Qwen3.6-35B-A3B-UD-Q4_K_M.gguf": 131072,
    "Qwen3.6-35B-A3B-Claude-4.7-Opus-Reasoning-Distilled.i1-IQ4_XS.gguf": 131072,
    "Qwopus3.6-35B-A3B-v1-IQ4_XS.gguf": 131072,
    "Qwen3.6-27B.i1-IQ4_XS-attn_qkv-IQ4_XS.gguf": 131072,
    "Qwen3.6-27B-Omnimerge-v4-IQ4_NL.gguf": 131072,
    "Qwen3.6-27B-TQ3_4S.gguf": 131072,
    "Qwen3.6-27B-UD-Q4_K_XL.gguf": 131072,
}

def read_start_script():
    if not os.path.exists(START_SCRIPT):
        return ""
    with open(START_SCRIPT, "r") as f:
        return f.read()

def _read_env_config():
    """从 env 文件读取配置"""
    config = {}
    try:
        with open(ENV_FILE, 'r') as f:
            for line in f:
                line = line.strip()
                if not line or line.startswith('#'):
                    continue
                if '=' in line:
                    key, val = line.split('=', 1)
                    config[key.strip()] = val.strip()
    except:
        pass
    return config

def _write_env_config(config_updates):
    """更新 env 文件中的配置"""
    lines = []
    try:
        with open(ENV_FILE, 'r') as f:
            existing_lines = f.readlines()
    except:
        existing_lines = []

    updated_keys = set()
    for line in existing_lines:
        stripped = line.strip()
        if stripped and not stripped.startswith('#') and '=' in line:
            key = line.split('=', 1)[0].strip()
            if key in config_updates:
                new_line = key + chr(61) + config_updates[key] + chr(10)
                lines.append(new_line)
                updated_keys.add(key)
            else:
                lines.append(line)
        else:
            lines.append(line)

    # 追加新 key
    for key, val in config_updates.items():
        if key not in updated_keys:
            new_line = key + chr(61) + val + chr(10)
            lines.append(new_line)

    with open(ENV_FILE, 'w') as f:
        f.writelines(lines)


# === Runtime state: cgroup-bounded discovery + single-flight cache ===
_CMDLINE_TTL = 10.0
_runtime_scan_source = "uninitialized"


def _invalidate_cmdline_cache():
    """Deploy/switch/restart/stop invalidates the observed state."""
    _runtime_cache.invalidate()


def _get_running_cmdline_config(force=False):
    """Return authoritative argv state with coalesced refreshes."""
    return _runtime_cache.get(force=force)


def _get_running_cmdline_config_scan():
    """Inspect only inference-server cgroup PIDs; host scan is last-resort."""
    global _runtime_scan_source
    try:
        owned_pids = service_pids(settings.inference_cgroup_procs)
        if owned_pids is None:
            # Bounded fallback for non-cgroup deployments. Never enumerate the
            # host process table from a request path.
            result = subprocess.run(
                ["systemctl", "show", "inference-server", "-p", "MainPID", "--value"],
                capture_output=True, text=True, timeout=2,
            )
            main_pid = result.stdout.strip()
            _runtime_scan_source = "systemd-mainpid"
            entries = [main_pid] if result.returncode == 0 and main_pid.isdigit() and main_pid != "0" else []
        else:
            _runtime_scan_source = "systemd-cgroup"
            entries = [str(pid) for pid in owned_pids]
        for entry in entries:
            try:
                cmdline_raw = open(f"/proc/{entry}/cmdline", "rb").read()
            except (PermissionError, FileNotFoundError):
                continue
            cmdline = cmdline_raw.decode("utf-8", errors="ignore").split(chr(0))
            cmdline = [c for c in cmdline if c]
            if not cmdline:
                continue
            # Match the executable itself, not any process whose arguments happen
            # to mention "llama-server" (log tails, shell scripts and diagnostic
            # commands otherwise produce a plausible-looking default config).
            try:
                exe_path = os.readlink(f"/proc/{entry}/exe")
            except (PermissionError, FileNotFoundError, OSError):
                exe_path = cmdline[0]
            if os.path.basename(exe_path) != "llama-server":
                continue
            cmd_str = " ".join(cmdline)
            config = {}
            config["pid"] = int(entry)
            def _arg(*flags):
                for i, arg in enumerate(cmdline):
                    if arg in flags and i + 1 < len(cmdline):
                        return cmdline[i + 1]
                return None
            def _flag(*flags):
                for arg in cmdline:
                    if arg in flags:
                        return True
                return False
            m_model = re.search(r"-m\s+(\S+)", cmd_str)
            config["model"] = m_model.group(1).split("/")[-1] if m_model else ""
            config["model_path"] = m_model.group(1) if m_model else ""
            config["draft_model_path"] = _arg("--model-draft", "--spec-draft-model", "-md") or ""
            config["draft_model"] = Path(config["draft_model_path"]).name if config["draft_model_path"] else ""
            m_alias = re.search(r"-a\s+(\S+)", cmd_str)
            config["alias"] = m_alias.group(1) if m_alias else ""
            config["ngl"] = int(_arg("-ngl") or _get_default_ngl())
            config["ctx_size"] = int(_arg("--ctx-size") or 131072)
            config["concurrency"] = int(_arg("-np") or 2)
            config["mmproj"] = _flag("--mmproj")
            m_mmproj_run = re.search(r"--mmproj\s+(\S+\.gguf)", cmd_str)
            config["mmproj_file"] = os.path.basename(m_mmproj_run.group(1)) if m_mmproj_run else None
            config["k_cache_type"] = _arg("--cache-type-k") or "q8_0"
            config["v_cache_type"] = _arg("--cache-type-v") or "q8_0"
            config["draft_k_cache_type"] = _arg("--cache-type-k-draft") or "q8_0"
            config["draft_v_cache_type"] = _arg("--cache-type-v-draft") or "q8_0"
            config["batch"] = int(_arg("-b") or 1024)
            config["ubatch"] = int(_arg("-ub") or 512)
            config["flash_attn"] = (_arg("--flash-attn") or "off").lower() == "on"
            config["chunked_batch"] = _flag("-cb")
            config["threads"] = int(_arg("-t") or 8)
            config["threads_http"] = int(_arg("--threads-http") or 4)
            config["temp"] = float(_arg("--temp") or 0.7)
            config["reasoning"] = _arg("--reasoning") or "off"
            config["ui"] = _flag("--ui")
            config["host"] = _arg("--host") or "0.0.0.0"
            config["port"] = int(_arg("--port") or 8080)
            gpu_env = ""
            try:
                process_env = open(f"/proc/{entry}/environ", "rb").read().split(b"\0")
                env_map = {
                    item.split(b"=", 1)[0].decode(errors="ignore"): item.split(b"=", 1)[1].decode(errors="ignore")
                    for item in process_env
                    if b"=" in item
                }
                gpu_env = (
                    env_map.get("HIP_VISIBLE_DEVICES")
                    or env_map.get("ROCR_VISIBLE_DEVICES")
                    or env_map.get("CUDA_VISIBLE_DEVICES")
                    or ""
                )
            except (PermissionError, FileNotFoundError, OSError):
                pass
            # Do not let the one-second runtime observer trigger a slow GPU
            # inventory command. It may only consume an already-cached mapping.
            cached_gpus = _GPU_CACHE.get("items") or []
            gpu_names = {str(g["index"]): g["short_name"] for g in cached_gpus}
            config["gpu"] = gpu_names.get(gpu_env, gpu_env or "all")
            config["kv_offload"] = "off"
            config["n_cpu_moe"] = int(_arg("--n-cpu-moe") or 0)
            config["tensor_split"] = _arg("--tensor-split") or None
            config["spec_type"] = _arg("--spec-type") or None
            config["spec_draft_n_max"] = int(_arg("--spec-draft-n-max") or 0)
            config["ngram_mod_n_min"] = int(_arg("--spec-ngram-mod-n-min") or 0) or None
            config["ngram_mod_n_max"] = int(_arg("--spec-ngram-mod-n-max") or 0) or None
            config["ngram_mod_n_match"] = int(_arg("--spec-ngram-mod-n-match") or 0) or None
            config["cache_ram"] = int(_arg("--cache-ram") or 2048)
            config["sleep_idle_seconds"] = int(_arg("--sleep-idle-seconds") or 300)
            config["device"] = _arg("--device") or ""
            config["fit"] = _arg("--fit") or ""
            config["kv_unified"] = _flag("--kv-unified")
            config["cache_reuse"] = int(_arg("--cache-reuse") or 0) or None
            spec_p_min = _arg("--spec-draft-p-min")
            config["spec_draft_p_min"] = float(spec_p_min) if spec_p_min is not None else None
            bin_path = cmdline[0] if cmdline else ""
            config["binary_path"] = bin_path
            config["llama_version"] = _engine_key_from_binary(bin_path) or _get_active_engine() or "unknown"
            return config
    except Exception:
        pass
    return None


_runtime_cache = SingleFlightTTLCache(_get_running_cmdline_config_scan, _CMDLINE_TTL)


def parse_script_config(content=""):
    """解析配置：优先级 1)运行中进程 cmdline > 2)start脚本内容 > 3)env文件"""
    # 优先级1: 从实际运行进程获取（最权威）
    running_cfg = _get_running_cmdline_config()
    if running_cfg:
        return running_cfg
    # 优先级2: 从 start 脚本内容解析
    if content:
        config = {}
        m = re.search(r"-ngl\s+(\d+)", content)
        config["ngl"] = int(m.group(1)) if m else _get_default_ngl()
        m = re.search(r"--ctx-size\s+(\d+)", content)
        config["ctx_size"] = int(m.group(1)) if m else 131072
        m = re.search(r"-np\s+(\d+)", content)
        config["concurrency"] = int(m.group(1)) if m else 2
        config["mmproj"] = "--mmproj" in content
        m_mmproj = re.search(r"--mmproj\s+(\S+\.gguf)", content)
        config["mmproj_file"] = os.path.basename(m_mmproj.group(1)) if m_mmproj else None
        m = re.search(r"-m\s+(\S+\.gguf)", content)
        config["model"] = m.group(1) if m else ""
        m_draft_model = re.search(r"(?:--model-draft|--spec-draft-model|-md)\s+(\S+\.gguf)", content)
        config["draft_model_path"] = m_draft_model.group(1) if m_draft_model else ""
        config["draft_model"] = Path(config["draft_model_path"]).name if config["draft_model_path"] else ""
        m_alias = re.search(r"-a\s+(\S+)", content)
        config["alias"] = m_alias.group(1) if m_alias else ""
        m_ct_k = re.search(r"--cache-type-k\s+(\S+)", content)
        config["k_cache_type"] = m_ct_k.group(1) if m_ct_k else "q8_0"
        m_ct_v = re.search(r"--cache-type-v\s+(\S+)", content)
        config["v_cache_type"] = m_ct_v.group(1) if m_ct_v else "q8_0"
        m_dk = re.search(r"--cache-type-k-draft\s+(\S+)", content)
        config["draft_k_cache_type"] = m_dk.group(1) if m_dk else "q8_0"
        m_dv = re.search(r"--cache-type-v-draft\s+(\S+)", content)
        config["draft_v_cache_type"] = m_dv.group(1) if m_dv else "q8_0"
        m_b = re.search(r"-b\s+(\d+)", content)
        config["batch"] = int(m_b.group(1)) if m_b else 1024
        m_ub = re.search(r"-ub\s+(\d+)", content)
        config["ubatch"] = int(m_ub.group(1)) if m_ub else 512
        config["flash_attn"] = "on" in content
        config["chunked_batch"] = "-cb" in content
        m_t = re.search(r"-t\s+(\d+)", content)
        config["threads"] = int(m_t.group(1)) if m_t else 8
        m_th = re.search(r"--threads-http\s+(\d+)", content)
        config["threads_http"] = int(m_th.group(1)) if m_th else 4
        m_tmp = re.search(r"--temp\s+(\S+)", content)
        config["temp"] = float(m_tmp.group(1)) if m_tmp else 0.7
        m_reas = re.search(r"--reasoning\s+(\S+)", content)
        config["reasoning"] = m_reas.group(1) if m_reas else "off"
        m_host = re.search(r"--host\s+(\S+)", content)
        m_ui = re.search(r"--ui\b", content)
        config["ui"] = bool(m_ui)
        config["host"] = m_host.group(1) if m_host else "0.0.0.0"
        m_port = re.search(r"--port\s+(\d+)", content)
        config["port"] = int(m_port.group(1)) if m_port else 8080
        config["kv_offload"] = "off"
        m_ncm = re.search(r"--n-cpu-moe\s+(\d+)", content)
        config["n_cpu_moe"] = int(m_ncm.group(1)) if m_ncm else 0
        m_ts = re.search(r"--tensor-split\s+(\S+)", content)
        config["tensor_split"] = m_ts.group(1) if m_ts else None
        m_bin = re.search(r"exec\s+(\S+llama-server)", content)
        config["llama_version"] = _engine_key_from_binary(m_bin.group(1) if m_bin else "") or _get_active_engine() or "unknown"
        m_spec = re.search(r"--spec-draft-n-max\s+(\d+)", content)
        config["spec_draft_n_max"] = int(m_spec.group(1)) if m_spec else 0
        m_spec_type = re.search(r"--spec-type\s+(\S+)", content)
        config["spec_type"] = m_spec_type.group(1) if m_spec_type else None
        m_ng_min = re.search(r"--spec-ngram-mod-n-min\s+(\d+)", content)
        config["ngram_mod_n_min"] = int(m_ng_min.group(1)) if m_ng_min else None
        m_ng_max = re.search(r"--spec-ngram-mod-n-max\s+(\d+)", content)
        config["ngram_mod_n_max"] = int(m_ng_max.group(1)) if m_ng_max else None
        m_ng_match = re.search(r"--spec-ngram-mod-n-match\s+(\d+)", content)
        config["ngram_mod_n_match"] = int(m_ng_match.group(1)) if m_ng_match else None
        m_cache_ram = re.search(r"--cache-ram\s+(\d+)", content)
        config["cache_ram"] = int(m_cache_ram.group(1)) if m_cache_ram else 2048
        m_sleep = re.search(r"--sleep-idle-seconds\s+(\d+)", content)
        config["sleep_idle_seconds"] = int(m_sleep.group(1)) if m_sleep else 300
        m_device = re.search(r"--device\s+(\S+)", content)
        config["device"] = m_device.group(1) if m_device else ""
        m_fit = re.search(r"--fit\s+(on|off)", content)
        config["fit"] = m_fit.group(1) if m_fit else ""
        config["kv_unified"] = bool(re.search(r"--kv-unified\b", content))
        m_reuse = re.search(r"--cache-reuse\s+(\d+)", content)
        config["cache_reuse"] = int(m_reuse.group(1)) if m_reuse else None
        m_p_min = re.search(r"--spec-draft-p-min\s+(\S+)", content)
        config["spec_draft_p_min"] = float(m_p_min.group(1)) if m_p_min else None
        return config
    # 优先级3: 回退到 env 文件
    env_cfg = _read_env_config()
    if not env_cfg:
        return {"ngl": 99, "ctx_size": 131072, "concurrency": 2, "mmproj": False, "mmproj_file": None, "model": "", "draft_model": "", "draft_model_path": "", "k_cache_type": "q8_0", "v_cache_type": "q8_0", "batch": 1024, "ubatch": 512, "flash_attn": True, "chunked_batch": True, "threads": 8, "threads_http": 4, "temp": 0.7, "reasoning": "off", "host": "0.0.0.0", "port": 8080, "kv_offload": "off", "draft_k_cache_type": "q8_0", "draft_v_cache_type": "q8_0", "n_cpu_moe": 0, "tensor_split": None, "gpu": "all", "cache_ram": 2048, "sleep_idle_seconds": 300, "device": "", "fit": "", "kv_unified": False, "cache_reuse": None, "spec_draft_p_min": None}
    return {
        "ngl": int(env_cfg.get("N_GPU_LAYERS", _get_default_ngl())),
        "ctx_size": int(env_cfg.get("CTX_SIZE", 131072)),
        "concurrency": int(env_cfg.get("NP", 2)),
        "mmproj": False,
        "model": env_cfg.get("MODEL_PATH", "").split("/")[-1],
        "k_cache_type": env_cfg.get("CACHE_TYPE_K", "q8_0"),
        "v_cache_type": env_cfg.get("CACHE_TYPE_V", "q8_0"),
        "draft_k_cache_type": env_cfg.get("CACHE_TYPE_K_DRAFT", "q8_0"),
        "draft_v_cache_type": env_cfg.get("CACHE_TYPE_V_DRAFT", "q8_0"),
        "batch": int(env_cfg.get("BATCH", 1024)),
        "ubatch": int(env_cfg.get("UBATCH", 512)),
        "flash_attn": env_cfg.get("FLASH_ATTN", "on").lower() == "on",
        "chunked_batch": env_cfg.get("CONT_BATCH", "on").lower() == "on",
        "threads": int(env_cfg.get("THREADS", 8)),
        "threads_http": int(env_cfg.get("THREADS_HTTP", 4)),
        "temp": float(env_cfg.get("TEMP", 0.7)),
        "reasoning": env_cfg.get("REASONING", "off"),
        "ui": env_cfg.get("UI", "off").lower() in ("on", "true", "1"),
        "host": env_cfg.get("HOST", "0.0.0.0"),
        "port": int(env_cfg.get("PORT", 8080)),
        "kv_offload": env_cfg.get("KV_OFFLOAD", "off"),
        "n_cpu_moe": int(env_cfg.get("N_CPU_MOE", 0)),
    }

# SUDO_PW removed: using sudoers NOPASSWD whitelist

def sudo_run(cmd, timeout=15):
    """Run sudo commands (NOPASSWD whitelist configured)"""
    return subprocess.run(
        ["sudo"] + cmd,
        capture_output=True, text=True, timeout=timeout
    )


def _checked_sudo(cmd, timeout=30):
    result = sudo_run(cmd, timeout=timeout)
    if result.returncode != 0:
        detail = (result.stderr or result.stdout or "unknown control error").strip()
        raise RuntimeError(f"{' '.join(cmd)} failed: {detail}")
    return result


def _atomic_write_text(path, content, mode=0o600):
    target = Path(path)
    target.parent.mkdir(parents=True, exist_ok=True)
    temporary = target.with_name(f".{target.name}.{os.getpid()}.tmp")
    try:
        temporary.write_text(content, encoding="utf-8")
        os.chmod(temporary, mode)
        os.replace(temporary, target)
    finally:
        temporary.unlink(missing_ok=True)

def write_start_script(script_content: str):
    """通过 sudo wrapper 安全写入 start-llama-server.sh"""
    result = subprocess.run(
        ["sudo", WRITE_START_SCRIPT],
        input=script_content, capture_output=True, text=True, timeout=10
    )
    if result.returncode != 0:
        raise HTTPException(500, f"写入脚本失败: {result.stderr.strip()}")
    return result.stdout.strip()



def update_persist_state(engine="llama", binary="", model="", args="", parameters=None, profile_id="default"):
    """更新持久化状态文件"""
    safe_args = re.sub(
        r"(--api-key\s+)(?:'[^']*'|\"[^\"]*\"|\S+)",
        r"\1[REDACTED]",
        str(args),
    )
    config = {
        "engine": engine,
        "binary": binary,
        "model": model,
        "args": safe_args,
        "profile_id": profile_id or "default",
        "parameters": parameters if isinstance(parameters, dict) else {},
    }
    config_path = "/data/inference-hub/state/persist_config.json"
    _atomic_write_text(config_path, json.dumps(config, indent=2) + "\n")

def _inference_systemd_state() -> dict[str, str]:
    """Read the supervised inference unit without scanning processes."""
    try:
        result = subprocess.run(
            [
                "systemctl", "show", "inference-server.service",
                "-p", "ActiveState", "-p", "SubState", "-p", "Result",
                "-p", "MainPID",
            ],
            capture_output=True,
            text=True,
            timeout=2,
        )
    except (OSError, subprocess.SubprocessError):
        return {}
    state: dict[str, str] = {}
    for line in (result.stdout or "").splitlines():
        key, separator, value = line.partition("=")
        if separator:
            state[key] = value
    return state

def _inference_health_ready() -> bool:
    """Require llama-server's loaded-model health, not just an open socket."""
    url = f"http://{settings.inference_host}:{settings.inference_port}/health"
    try:
        with urllib.request.urlopen(urllib.request.Request(url), timeout=2) as response:
            return response.status == 200
    except (urllib.error.HTTPError, urllib.error.URLError, TimeoutError, OSError):
        return False

def _wait_for_inference(expected_model_path="", expected_engine="", timeout=180):
    deadline = time.monotonic() + timeout
    started = time.monotonic()
    next_state_check = 0.0
    expected = str(Path(expected_model_path).resolve()) if expected_model_path else ""
    while time.monotonic() < deadline:
        now = time.monotonic()
        runtime = _get_running_cmdline_config(force=True)
        if runtime:
            observed = runtime.get("model_path") or ""
            try:
                observed = str(Path(observed).resolve()) if observed else ""
            except OSError:
                pass
            model_ok = not expected or observed == expected
            engine_ok = not expected_engine or runtime.get("llama_version") == expected_engine
            if model_ok and engine_ok:
                try:
                    with socket.create_connection(
                        (settings.inference_host, settings.inference_port), timeout=1
                    ):
                        if _inference_health_ready():
                            return runtime
                except OSError:
                    pass
        if now >= next_state_check:
            state = _inference_systemd_state()
            active_state = state.get("ActiveState", "")
            if now - started >= 5 and active_state not in {"active", "activating", "reloading"}:
                detail = ", ".join(
                    f"{key}={state[key]}"
                    for key in ("ActiveState", "SubState", "Result", "MainPID")
                    if state.get(key)
                ) or "systemd 状态不可用"
                raise RuntimeError(f"推理服务启动失败（{detail}）")
            next_state_check = now + 2
        time.sleep(0.5)
    raise RuntimeError("推理服务未在限定时间内达到目标模型/引擎就绪状态")


def restart_llama_server(expected_model_path="", expected_engine=""):
    """Restart the supervised unit and return only after observed readiness."""
    _checked_sudo(["systemctl", "restart", "inference-server"], timeout=30)
    return _wait_for_inference(expected_model_path, expected_engine)

def _switch_and_restart(llama_version, expected_model_path=""):
    """切换引擎并重启推理服务"""
    state_file = "/data/inference-hub/state/active_engine"
    # 写入引擎状态
    _atomic_write_text(state_file, llama_version + "\n")
    return restart_llama_server(expected_model_path, llama_version)

def stop_llama_server():
    """Stop the systemd unit so Restart=always cannot immediately revive it."""
    try:
        result = sudo_run(["systemctl", "stop", "inference-server"], timeout=30)
        if result.returncode != 0:
            return False
        for _ in range(20):
            if _get_running_cmdline_config(force=True) is None:
                return True
            time.sleep(0.25)
        return False
    except (OSError, subprocess.SubprocessError):
        return False

def is_llama_running():
    return _get_running_cmdline_config() is not None


_runtime_event_condition = asyncio.Condition()
_runtime_event_sequence = 0
_runtime_event_payload = None
_runtime_event_signature = None


async def _refresh_runtime_event(*, force: bool = False):
    """One producer samples runtime/catalog state for every SSE consumer."""
    global _runtime_event_sequence, _runtime_event_payload, _runtime_event_signature
    runtime = await asyncio.to_thread(_get_running_cmdline_config, force)
    catalog_meta = catalog_service.metadata()
    operation_sequence, deployment_signal = await asyncio.gather(
        asyncio.to_thread(operation_store.latest_sequence),
        asyncio.to_thread(deployment_tasks.latest_signal),
    )
    signature = (
        catalog_meta.get("generation"),
        runtime.get("pid") if runtime else None,
        runtime.get("model_path") if runtime else None,
        operation_sequence,
        deployment_signal,
    )
    if signature == _runtime_event_signature and _runtime_event_payload is not None:
        return
    payload = {
        "type": "state",
        "catalog_generation": signature[0],
        "runtime_pid": signature[1],
        "runtime_model": signature[2],
        "runtime_source": _runtime_scan_source,
        "observed_at": time.time(),
        "operation_sequence": signature[3],
        "deployment_sequence": signature[4][0],
        "deployment_state": signature[4][1],
        "deployment_phase": signature[4][2],
        "deployment_progress": signature[4][3],
    }
    async with _runtime_event_condition:
        _runtime_event_signature = signature
        _runtime_event_payload = payload
        _runtime_event_sequence += 1
        _runtime_event_condition.notify_all()


async def _runtime_event_watcher():
    while True:
        try:
            await _refresh_runtime_event(force=True)
        except asyncio.CancelledError:
            raise
        except Exception as exc:
            print(f"[runtime-observer] {type(exc).__name__}")
        await asyncio.sleep(settings.runtime_poll_seconds)

@asynccontextmanager
async def lifespan(app: FastAPI):
    Path(DATA_DIR).mkdir(parents=True, exist_ok=True)
    await asyncio.to_thread(catalog_service.list_models)
    await _refresh_runtime_event(force=True)
    watcher = asyncio.create_task(_runtime_event_watcher(), name="runtime-observer")
    try:
        yield
    finally:
        watcher.cancel()
        with suppress(asyncio.CancelledError):
            await watcher
        background_executor.shutdown(wait=False, cancel_futures=True)

app = FastAPI(
    title="Model Manager",
    version="3.3.0",
    description="Node-local model catalog and deployment control plane",
    lifespan=lifespan,
)
catalog_mutation_lock = asyncio.Lock()
runtime_mutation_lock = asyncio.Lock()
settings_mutation_lock = asyncio.Lock()


def mutation_lock_for(path: str) -> asyncio.Lock:
    """Serialize only conflicting resources instead of the whole control plane."""
    if path.startswith("/api/deployments") or path.startswith("/api/models/deploy"):
        return runtime_mutation_lock
    if path.startswith(("/api/models/stop", "/api/models/toggle-mmproj", "/api/service/", "/api/settings/", "/api/engine")):
        return settings_mutation_lock
    if path.startswith(("/api/models/", "/api/upload", "/api/catalog", "/api/hub/")):
        return catalog_mutation_lock
    return settings_mutation_lock

ADMIN_API_KEYS = [
    k.strip()
    for k in (
        os.getenv("MODEL_MANAGER_ADMIN_KEY", "").strip(),
        os.getenv("DASHBOARD_ADMIN_KEY", "").strip(),
    )
    if k.strip()
]
ADMIN_API_KEY = ADMIN_API_KEYS[0] if ADMIN_API_KEYS else ""


@app.middleware("http")
async def require_admin_for_mutations(request, call_next):
    if request.method.upper() in {"POST", "PUT", "PATCH", "DELETE"}:
        if not ADMIN_API_KEYS:
            return JSONResponse(status_code=503, content={"error": "Model manager admin key is not configured"})
        supplied = request.headers.get("X-Admin-Key", "")
        if not supplied or not any(hmac.compare_digest(supplied, key) for key in ADMIN_API_KEYS):
            return JSONResponse(status_code=403, content={"error": "Need admin key"})
        client = request.client.host if request.client else "unknown"
        operation_id = await asyncio.to_thread(
            operation_store.start, request.method.upper(), request.url.path, client
        )
        # This is a node-local control plane: state-changing operations must be
        # serialized so config, engine state and the observed process cannot
        # be changed by two requests at once.
        async def execute_mutation():
            try:
                response = await call_next(request)
                await asyncio.to_thread(operation_store.finish, operation_id, response.status_code)
                response.headers["X-Operation-Id"] = operation_id
                return response
            except Exception as exc:
                await asyncio.to_thread(operation_store.finish, operation_id, 500, type(exc).__name__)
                raise
        # Cancellation only inspects task state and must return immediately
        # while the atomic deploy route owns the global system mutation lock.
        if request.url.path.startswith("/api/deployments/") and request.url.path.endswith("/cancel"):
            return await execute_mutation()
        async with mutation_lock_for(request.url.path):
            return await execute_mutation()
    return await call_next(request)

# === Static files ===
app.mount("/static", StaticFiles(directory=str(settings.app_dir / "static")), name="static")


# 全局异常处理器：确保所有未捕获异常返回 JSON（前端不会因为 HTML 500 崩溃）
from fastapi import HTTPException as _HTTPException

@app.exception_handler(Exception)
async def global_exception_handler(request, exc):
    if isinstance(exc, _HTTPException):
        raise exc
    import traceback
    traceback.print_exc()
    return JSONResponse(status_code=500, content={"error": "Internal server error"})

allowed_origins = [
    item.strip()
    for item in os.getenv(
        "MODEL_MANAGER_ALLOWED_ORIGINS",
        "http://10.1.1.4:8081,http://10.1.1.4:8093",
    ).split(",")
    if item.strip()
]
app.add_middleware(
    CORSMiddleware,
    allow_origins=allowed_origins,
    allow_credentials=False,
    allow_methods=["GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"],
    allow_headers=["Content-Type", "X-Admin-Key"],
)

def _find_model_dir(directory):
    if (directory / "config.json").exists():
        return directory
    subs = [d for d in directory.iterdir() if d.is_dir()]
    if len(subs) == 1:
        sub_cfg = subs[0] / "config.json"
        if sub_cfg.exists():
            return subs[0]
    return None

def _resolve_dir_alias(dirname, config):
    name_lower = dirname.lower()
    if "35a3b" in name_lower or "35b" in name_lower:
        return "Qwen3.6-35B-A3B"
    if "27b" in name_lower:
        return "Qwen3.6-27B"
    if "vox" in name_lower:
        return "VoxCPM2"
    arch = config.get("architectures", [])
    arch_str = " ".join(arch).lower()
    if "moe" in arch_str:
        return "Qwen3.6-35B-A3B"
    return ""

def scan_models(force=False):
    """Compatibility wrapper around the versioned cached catalog."""
    return catalog_service.list_models(force=force)

    # Legacy scanner retained below for one release as a rollback reference.
    import json as _json, subprocess as _subprocess
    models = []
    p = Path(DATA_DIR)
    if not p.exists():
        return models
    for f in sorted(p.rglob("*.gguf"), key=lambda x: x.stat().st_mtime, reverse=True):
        # 跳过软链接（避免同一模型重复显示）
        if f.is_symlink():
            continue
        try:
            stat = f.stat()
        except (OSError, FileNotFoundError):
            continue
        # 过滤 <100MB 的小文件（macOS ._ 元数据、.DS_Store 等垃圾）
        if stat.st_size < 100 * 1024 * 1024:
            continue
        # 跳过 DFlash 草稿模型（非独立主模型）
        if "dflash" in f.name.lower():
            continue
        # mmproj 视觉插件：统一识别为 "视觉插件"
        if "mmproj" in f.name.lower():
            models.append({
                "name": f.name,
                "alias": "视觉插件",
                "path": str(f),
                "size": stat.st_size,
                "size_human": format_size(stat.st_size),
                "modified": stat.st_mtime,
                "ctx_default": None,
                "format": "GGUF",
                "quant_type": "mmproj",
                "tags": [],
            })
            continue
        alias = MODEL_ALIASES.get(f.name,
            "Gemma4-26B" if f.name.lower().startswith(("gemma-4-26b", "supergemma4-26b")) else
            "Gemma4-31B" if f.name.lower().startswith(("gemma-4-31b",)) else
            "Qwen3.6-27B" if f.name.startswith(("Qwen3.6-27B-", "Qwen3.6-27B.", "Qwopus3.6-27B-", "Qwen3.6-40B-")) else
            "Qwen3.6-35B-A3B" if f.name.startswith(("Qwen3.6-35B-A3B-", "Qwen3.6-MoE-35B-A3B-")) else
            "Hermes3.6-35B-A3B" if f.name.startswith(("Hermes3.6-35B-A3B-",)) else "")
        ctx = MODEL_CTX_SETTINGS.get(f.name)
        tags = _extract_tags_gguf(f.name, alias)
        models.append({
            "name": f.name,
            "alias": alias,
            "path": str(f),
            "size": stat.st_size,
            "size_human": format_size(stat.st_size),
            "modified": stat.st_mtime,
            "ctx_default": ctx,
            "format": "GGUF",
            "quant_type": _extract_gguf_quant(f.name),
            "tags": tags,
        })
    for d in sorted(p.iterdir()):
        if not d.is_dir():
            continue
        model_dir = _find_model_dir(d)
        if model_dir is None:
            continue
        cfg_file = model_dir / "config.json"
        if not cfg_file.exists():
            continue
        try:
            with open(cfg_file) as cf:
                cfg = _json.load(cf)
        except Exception:
            continue
        try:
            size = int(_subprocess.check_output(["du", "-sb", str(d)], stderr=_subprocess.DEVNULL).split()[0])
        except Exception:
            size = 0
        # 过滤 <100MB 的目录（测试目录等垃圾）
        if size < 100 * 1024 * 1024:
            continue
        quant_cfg = cfg.get("quantization_config", {})
        quant_method = quant_cfg.get("quant_method", "")
        name_lower = d.name.lower()
        if "awq" in quant_method.lower() or "awq" in name_lower:
            quant_type = "AWQ"
        elif "gptq" in quant_method.lower() or "gptq" in name_lower:
            quant_type = "GPTQ"
        elif quant_method:
            quant_type = quant_method.upper()
        else:
            quant_type = "BF16"
        alias = _resolve_dir_alias(d.name, cfg)
        try:
            mtime = d.stat().st_mtime
        except Exception:
            mtime = 0
        if "35" in d.name or "moe" in str(cfg.get("architectures", [])).lower():
            ctx_default = 131072
        else:
            ctx_default = 32768
        hf_tags = _extract_tags_hf(d.name, cfg, quant_type)
        models.append({
            "name": d.name,
            "alias": alias,
            "path": str(d),
            "size": size,
            "size_human": format_size(size),
            "modified": mtime,
            "ctx_default": ctx_default,
            "format": "HF",
            "quant_type": quant_type,
            "tags": hf_tags,
        })
    return models

def _extract_gguf_quant(name):
    import re
    # Match known quant types, in order of specificity
    patterns = [
        r"MXFP4_MOE",
        r"MXFP4",
        r"NVFP4",
        r"IQ[0-9_]+[A-Z]*",
        r"Q[0-9]_[A-Z]+[A-Z_]*",
        r"BF16",
    ]
    for p in patterns:
        m = re.search(p, name, re.IGNORECASE)
        if m:
            return m.group(0).upper()
    return ""

# === Network lookup for model architecture ===
_MODEL_ARCH_CACHE = {}

def _lookup_model_arch_from_hf(model_name, alias):
    import requests
    # Proxy seems to be down - skip network lookups entirely
    return None
    candidates = []
    if alias:
        candidates.append(alias)
        if "qwen" in alias.lower():
            candidates.append("Qwen/" + alias)
        if "gemma" in alias.lower():
            candidates.append("google/" + alias)
    if model_name:
        base = model_name.replace(".gguf", "").replace("-MTP", "").replace("-UD", "")
        candidates.append(base)
    for candidate in candidates[:5]:
        cache_key = candidate.strip().lower()
        if cache_key in _MODEL_ARCH_CACHE:
            return _MODEL_ARCH_CACHE[cache_key]
        for prefix in ["", "Qwen/", "google/"]:
            hf_id = prefix + candidate.strip()
            try:
                url = "https://huggingface.co/api/models/" + hf_id
                resp = requests.get(url, timeout=1, proxies={'http': 'http://10.1.1.252:7893', 'https': 'http://10.1.1.252:7893'})
                if resp.status_code == 200:
                    data = resp.json()
                    architectures = data.get("config", {}).get("architectures", [])
                    arch_str = " ".join(architectures).lower()
                    is_moe = False
                    if "moe" in arch_str:
                        is_moe = True
                    if re.search(r"a[2-9]b", hf_id.lower()):
                        is_moe = True
                    result = {"is_moe": is_moe, "source": "hf_api"}
                    _MODEL_ARCH_CACHE[cache_key] = result
                    return result
            except Exception:
                pass
    return None


def _extract_tags_gguf(name, alias):
    tags = []
    name_lower = name.lower()
    if re.search(r'[-_]MTP', name, re.IGNORECASE):
        tags.append('MTP')

    is_moe = None

    # Rule 1: Explicit MoE in filename
    if re.search(r'(?i)moe', name):
        is_moe = True

    # Rule 2: Active parameter notation (A3B, A4B, etc.) = MoE
    if alias and re.search(r'A[2-9]B', alias):
        is_moe = True

    # Rule 3: Known Dense models (no need for network lookup)
    if is_moe is None:
        if alias:
            alias_lower = alias.lower()
            # Qwen Dense: 9B, 27B (no A in name)
            if 'qwen' in alias_lower and re.search(r'(?:^|[^A])9B', alias):
                is_moe = False
            if 'qwen' in alias_lower and re.search(r'(?:^|[^A])27B', alias):
                is_moe = False
            # Gem4 Dense: 31B
            if 'gemma' in alias_lower and re.search(r'(?:^|[^A])31B', alias):
                is_moe = False
            # Gemma4 MoE: 26B = A4B (active 4B)
            if 'gemma' in alias_lower and re.search(r'(?:^|[^A])26B', alias):
                is_moe = True

    # Rule 4: Network lookup only if still uncertain
    if is_moe is None:
        arch_info = _lookup_model_arch_from_hf(name, alias)
        if arch_info:
            is_moe = arch_info['is_moe']

    if is_moe is True:
        tags.append('MoE')
    elif is_moe is False:
        tags.append('Dense')

    if 'uncensored' in name_lower or 'abliterated' in name_lower:
        tags.append('Uncensored')
    if 'reasoning' in name_lower or 'opus' in name_lower:
        tags.append('Reasoning')
    if 'tq' in name_lower:
        tags.append('TurboQuant')
    if 'mxfp4' in name_lower or 'nvfp4' in name_lower:
        tags.append('MXFP4')
    return tags



def _extract_tags_hf(d_name, config, quant_type):
    tags = []
    name_lower = d_name.lower()
    is_moe = False
    arch = config.get('architectures', [])
    arch_str = ' '.join(arch).lower()
    if 'moe' in arch_str or 'a3b' in name_lower or 'a4b' in name_lower:
        is_moe = True
    if is_moe:
        tags.append('MoE')
    else:
        tags.append('Dense')
    if 'uncensored' in name_lower or 'abliterated' in name_lower:
        tags.append('Uncensored')
    if 'reasoning' in name_lower or 'opus' in name_lower:
        tags.append('Reasoning')
    if 'mtp' in name_lower:
        tags.append('MTP')
    if 'tq' in quant_type.lower():
        tags.append('TurboQuant')
    if 'mxfp4' in quant_type.lower():
        tags.append('MXFP4')
    return tags


def format_size(size):
    for unit in ["B", "KB", "MB", "GB", "TB"]:
        if size < 1024:
            return f"{size:.1f} {unit}"
        size /= 1024
    return f"{size:.1f} PB"

@app.get("/", response_class=HTMLResponse)
async def index():
    vue_index = settings.app_dir / "static" / "vue" / "index.html"
    html_path = str(vue_index if vue_index.exists() else settings.app_dir / "index.html")
    static_dir = str(settings.app_dir / "static")
    with open(html_path, "r") as f:
        html = f.read()
    latest_ts = int(os.path.getmtime(html_path))
    for root, dirs, files in os.walk(static_dir):
        for fn in files:
            if fn.endswith((".css", ".js")):
                fp = os.path.join(root, fn)
                ts = int(os.path.getmtime(fp))
                if ts > latest_ts:
                    latest_ts = ts
    cache_ver = str(latest_ts)
    # 替换已有 ?v=版本号
    import re as _re
    html = _re.sub(r"([?&]v=)\d{10,}", r"\g<1>" + cache_ver, html)
    # 对没有 ?v= 的静态资源链接追加版本号
    html = _re.sub(r'(href="([^"]*\.css))"(?!.*\?v=)', r'\1?v=' + cache_ver + '"', html)
    html = _re.sub(r'(src="([^"]*\.js))"(?!.*\?v=)', r'\1?v=' + cache_ver + '"', html)
    return HTMLResponse(html)

@app.get("/js/deploy-prefs.js")
async def serve_js():
    return FileResponse(str(settings.app_dir / "js" / "deploy-prefs.js"))


@app.get("/api/models/mmproj-files")
@app.get("/api/models/mmproj-files/{model_id}")
async def list_mmproj_files_api(model_id: str = ""):
    """返回所有可用的视觉模块文件"""
    files = await asyncio.to_thread(_list_mmproj_files)
    if model_id:
        model = await asyncio.to_thread(catalog_service.find, model_id)
        files = [
            item
            for item in files
            if model and item.get("relative_dir") == model.get("relative_dir")
        ]
    return {"files": files}


@app.get("/api/models/preflight")
@app.get("/api/models/preflight/{model_id}")
async def model_preflight(model_id: str):
    model = await asyncio.to_thread(catalog_service.find, model_id)
    if not model:
        raise HTTPException(404, "模型不存在或 model id 不唯一")
    registered, projectors_all = await asyncio.gather(
        asyncio.to_thread(_scan_engines),
        asyncio.to_thread(_list_mmproj_files),
    )
    registered_types = {
        "vllm" if item.get("type") == "vllm" or item.get("key") == "vllm" else "llama"
        for item in registered
    }
    # vLLM is intentionally unavailable until it has a dedicated supervised
    # launcher; the legacy branch only wrote args then restarted llama.cpp.
    registered_types.discard("vllm")
    supported = set(model.get("supported_engines") or [])
    llama_engines = [
        item for item in registered
        if item.get("type", "llama") == "llama"
    ]
    blockers = []
    if not model.get("deployable"):
        if model.get("format") == "EXTENSOR":
            blockers.append("EXTENSOR GGUF 需要专用推理引擎，当前 llama.cpp 不支持")
        else:
            blockers.append(f"{model.get('category')} 不能独立部署")
    projectors = [
        item
        for item in projectors_all
        if item.get("relative_dir") == model.get("relative_dir")
    ]
    classification = model.get("classification") or {}
    model_capabilities = {
        str(value).strip().lower()
        for value in (classification.get("capabilities") or [])
        if str(value).strip()
    }
    if "vision" in model_capabilities and not projectors and model.get("format") == "GGUF":
        blockers.append("模型声明视觉能力，但没有找到同包视觉投影组件")
    draft_models = _list_draft_models_for_model(model)
    engine_matches = []
    for item in llama_engines:
        match = match_model_engine(model, item, projectors=projectors, draft_models=draft_models)
        profile_summaries = {}
        profiles = item.get("profiles") if isinstance(item.get("profiles"), dict) else {}
        for profile_key, profile in profiles.items():
            try:
                plan = resolve_deployment_plan(
                    model,
                    item,
                    profile_id=str(profile_key),
                    projectors=projectors,
                    draft_models=draft_models,
                )
                profile_summaries[str(profile_key)] = {
                    "label": profile.get("label") if isinstance(profile, dict) else str(profile_key),
                    "compatible": True,
                    "parameters": plan.get("parameters", {}),
                    "limits": plan.get("limits", {}),
                }
            except DeploymentPlanError as exc:
                profile_summaries[str(profile_key)] = {
                    "label": profile.get("label") if isinstance(profile, dict) else str(profile_key),
                    "compatible": False,
                    "reasons": [str(exc)],
                }
        engine_matches.append({
            "key": item.get("key"),
            "name": item.get("name"),
            "version": item.get("version"),
            "compatible": match.compatible,
            "reasons": list(match.reasons),
            "warnings": list(match.warnings),
            "capabilities": list(match.capabilities),
            "recommended_profile": "default" if "default" in profile_summaries else (next(iter(profile_summaries), None)),
            "profiles": profile_summaries,
        })
    compatible_versions = [item["key"] for item in engine_matches if item.get("compatible") and item.get("key")]
    ready = sorted({"llama"} if compatible_versions else set())
    if supported and not ready:
        blockers.append(f"节点没有与该模型能力匹配的引擎；模型需要 {sorted(supported)}")
    active_engine = _get_active_engine()
    default_llama_version = next(
        (
            item.get("key") for item in engine_matches
            if item.get("compatible") and item.get("key") == active_engine
        ),
        None,
    ) or (compatible_versions[0] if compatible_versions else None)
    return {
        "model": model,
        "compatible_engines": ready,
        "compatible_llama_versions": compatible_versions,
        "engine_matches": engine_matches,
        "requirements": model_requirements(model, projectors=projectors, draft_models=draft_models),
        "registered_engines": registered,
        "default_engine": ready[0] if ready else None,
        "default_llama_version": default_llama_version,
        "draft_models": draft_models,
        "projectors": projectors,
        "blockers": blockers,
        "can_deploy": bool(model.get("deployable") and ready and not blockers),
    }


@app.get("/api/models/deployment-plan/{model_id}/{engine_key}")
@app.get("/api/models/deployment-plan/{model_id}/{engine_key}/{profile_id}")
async def model_deployment_plan(model_id: str, engine_key: str, profile_id: str = "default"):
    """Return the server-resolved defaults for one model/engine pair."""

    model = await asyncio.to_thread(catalog_service.find, model_id)
    if not model:
        raise HTTPException(404, "模型不存在或 model id 不唯一")
    engine = _get_engine_by_key(engine_key)
    if not engine:
        raise HTTPException(404, f"引擎不存在: {engine_key}")
    projectors_all = await asyncio.to_thread(_list_mmproj_files)
    projectors = [
        item for item in projectors_all
        if item.get("relative_dir") == model.get("relative_dir")
    ]
    draft_models = _list_draft_models_for_model(model)
    try:
        return resolve_deployment_plan(
            model,
            engine,
            profile_id=profile_id,
            projectors=projectors,
            draft_models=draft_models,
        )
    except DeploymentPlanError as exc:
        status = 409 if exc.code in {"engine_model_mismatch", "mtp_not_supported", "vision_not_supported"} else 400
        raise HTTPException(status, str(exc))

@app.get("/api/models/recommend")
async def recommend_model(filename: str = "", cache_type: str = "turbo3", v_cache_type: str = "", gpu_mode: str = "all"):
    """返回推荐上下文大小和 GPU 分配比例"""
    if not filename:
        raise HTTPException(400, "filename is required")
    result = await asyncio.to_thread(
        _calc_recommendation, filename, cache_type, gpu_mode, v_cache_type or cache_type
    )
    if "error" in result:
        raise HTTPException(404, result["error"])
    return result

@app.get("/api/models")
async def list_models(force: bool = False):
    models = await asyncio.to_thread(scan_models, force)
    total_size = sum(m["size"] for m in models)
    st = await asyncio.to_thread(os.statvfs, DATA_DIR)
    disk_total = st.f_blocks * st.f_frsize
    disk_free = st.f_bavail * st.f_frsize

    _, observed_config = _runtime_cache.peek()
    content = await asyncio.to_thread(read_start_script)
    current_config = observed_config or (parse_script_config(content) if content else {})
    persisted_config: dict[str, Any] = {}
    try:
        persisted_path = Path("/data/inference-hub/state/persist_config.json")
        if persisted_path.is_file():
            raw_persisted = json.loads(persisted_path.read_text(encoding="utf-8"))
            if isinstance(raw_persisted, dict):
                persisted_config = raw_persisted
    except (OSError, json.JSONDecodeError):
        persisted_config = {}
    if persisted_config:
        current_config = dict(current_config)
        current_config.setdefault("profile_id", persisted_config.get("profile_id", "default"))
        current_config.setdefault("parameters", persisted_config.get("parameters", {}))
    running = observed_config is not None
    current_model_id = ""
    model_path = current_config.get("model_path", "")
    if model_path:
        try:
            relative_path = str(Path(model_path).resolve().relative_to(Path(DATA_DIR).resolve()))
            current_model_id = catalog_service.uid_for_path(relative_path) or relative_path
        except (ValueError, OSError):
            current_model_id = current_config.get("model", "")
    catalog_meta, catalog_summary, mmproj_files, registered_engines = await asyncio.gather(
        asyncio.to_thread(catalog_service.metadata),
        asyncio.to_thread(catalog_service.summary, models),
        asyncio.to_thread(_list_mmproj_files),
        asyncio.to_thread(_scan_engines),
    )
    runtime_cache_meta = _runtime_cache.metadata()

    return {
        "schema_version": catalog_service.schema_version,
        "models": models,
        "current_model": current_config.get("model", "") if running else "",
        "current_model_id": current_model_id if running else "",
        "server_running": running,
        "runtime_source": _runtime_scan_source if observed_config else "desired-config",
        "runtime_observed_at": (
            time.time() - float(runtime_cache_meta.get("age_seconds", 0))
            if observed_config else None
        ),
        "runtime_cache": runtime_cache_meta,
        "current_config": {
            "pid": current_config.get("pid"),
            "model_path": current_config.get("model_path"),
            "binary_path": current_config.get("binary_path"),
            "draft_model": current_config.get("draft_model"),
            "draft_model_path": current_config.get("draft_model_path"),
            "ctx_size": current_config.get("ctx_size", 131072),
            "ngl": current_config.get("ngl", 99),
            "concurrency": current_config.get("concurrency", 2),
            "mmproj": current_config.get("mmproj", False),
            "mmproj_file": current_config.get("mmproj_file"),
            "k_cache_type": current_config.get("k_cache_type", "q8_0"),
            "v_cache_type": current_config.get("v_cache_type", "q8_0"),
            "llama_version": current_config.get("llama_version") or _get_active_engine() or "unknown",
            "profile_id": current_config.get("profile_id", persisted_config.get("profile_id", "default")),
            "draft_k_cache_type": current_config.get("draft_k_cache_type") if _supports_mtp(current_config.get("llama_version") or _get_active_engine() or "unknown") else None,
            "draft_v_cache_type": current_config.get("draft_v_cache_type") if _supports_mtp(current_config.get("llama_version") or _get_active_engine() or "unknown") else None,
            "batch": current_config.get("batch", 1024),
            "ubatch": current_config.get("ubatch", 512),
            "flash_attn": current_config.get("flash_attn", True),
            "chunked_batch": current_config.get("chunked_batch", True),
            "threads": current_config.get("threads", 8),
            "threads_http": current_config.get("threads_http", 4),
            "temp": current_config.get("temp", 0.7),
            "reasoning": current_config.get("reasoning", "off"),
            "ui": current_config.get("ui", False),
            "host": current_config.get("host", "0.0.0.0"),
            "port": current_config.get("port", 8080),
            "kv_offload": current_config.get("kv_offload", "off"),
            "tensor_split": current_config.get("tensor_split", None),
            "n_cpu_moe": current_config.get("n_cpu_moe", 0),
            "gpu": current_config.get("gpu", "all"),
            "spec_type": current_config.get("spec_type") if _supports_mtp(current_config.get("llama_version") or _get_active_engine() or "unknown") else None,
            "spec_draft_n_max": current_config.get("spec_draft_n_max", 0) if _supports_mtp(current_config.get("llama_version") or _get_active_engine() or "unknown") else 0,
            "ngram_mod_n_min": current_config.get("ngram_mod_n_min"),
            "ngram_mod_n_max": current_config.get("ngram_mod_n_max"),
            "ngram_mod_n_match": current_config.get("ngram_mod_n_match"),
            "cache_ram": current_config.get("cache_ram", 2048),
            "sleep_idle_seconds": current_config.get("sleep_idle_seconds", 300),
            "device": current_config.get("device", ""),
            "fit": current_config.get("fit", ""),
            "kv_unified": current_config.get("kv_unified", False),
            "cache_reuse": current_config.get("cache_reuse"),
            "spec_draft_p_min": current_config.get("spec_draft_p_min"),
            "parameters": current_config.get("parameters") or persisted_config.get("parameters", {}),
        },
        "mmproj_enabled": current_config.get("mmproj", False),
        "mmproj_file": current_config.get("mmproj_file"),
        "mmproj_files": mmproj_files,
        "total_size": format_size(total_size),
        "disk_total": format_size(disk_total),
        "disk_free": format_size(disk_free),
        "engines": registered_engines,
        "catalog": catalog_meta,
        "summary": catalog_summary,
    }


@app.get("/api/health")
async def health():
    _, runtime = _runtime_cache.peek()
    return {
        "status": "ok",
        "version": app.version,
        "catalog": await asyncio.to_thread(catalog_service.metadata),
        "inference": {
            "running": runtime is not None,
            "pid": runtime.get("pid") if runtime else None,
            "source": _runtime_scan_source,
            "cache": _runtime_cache.metadata(),
        },
    }


@app.get("/api/runtime")
async def runtime_state():
    """Lightweight authoritative runtime state; never scans the model catalog."""
    _, runtime = _runtime_cache.peek()
    cache_meta = _runtime_cache.metadata()
    return {
        "server_running": runtime is not None,
        "runtime_source": _runtime_scan_source,
        "runtime_observed_at": (
            time.time() - float(cache_meta.get("age_seconds", 0)) if runtime else None
        ),
        "runtime_cache": cache_meta,
        "current_config": runtime or {},
    }


@app.get("/api/events")
async def service_events(request: Request):
    """Fan out one observer stream; consumers never perform runtime scans."""
    async def stream():
        last_sequence = -1
        while not await request.is_disconnected():
            async with _runtime_event_condition:
                if last_sequence == _runtime_event_sequence:
                    try:
                        await asyncio.wait_for(_runtime_event_condition.wait(), timeout=15)
                    except asyncio.TimeoutError:
                        pass
                sequence = _runtime_event_sequence
                payload = dict(_runtime_event_payload or {})
            if sequence == last_sequence:
                yield ": keepalive\n\n"
                continue
            last_sequence = sequence
            yield f"id: {sequence}\nevent: state\ndata: {json.dumps(payload, ensure_ascii=False)}\n\n"

    return StreamingResponse(
        stream(),
        media_type="text/event-stream",
        headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"},
    )


@app.get("/api/operations")
async def operation_history(limit: int = 50):
    """Return sanitized control-plane history; request bodies and keys are never stored."""
    rows = await asyncio.to_thread(operation_store.list, limit)
    public_fields = {
        "sequence", "operation_id", "method", "path", "state",
        "status_code", "started_at", "finished_at", "duration_ms",
    }
    operations = [{key: value for key, value in row.items() if key in public_fields} for row in rows]
    return {"operations": operations, "latest_sequence": operations[0]["sequence"] if operations else 0}


def _run_deployment_task(task_id: str, payload: dict):
    """Call the checked synchronous deployment route through loopback.

    This deliberately reuses the sole production mutation path, including its
    systemd checks, readiness wait, rollback and global request lock.
    """
    current = deployment_tasks.get(task_id)
    if not current or current.get("state") == "cancelled":
        return
    deployment_tasks.update(task_id, state="running", phase="deploying", progress=35)
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    request = urllib.request.Request(
        f"http://127.0.0.1:{settings.control_port}/api/models/deploy",
        data=body,
        method="POST",
        headers={"Content-Type": "application/json", "X-Admin-Key": ADMIN_API_KEY},
    )
    try:
        with urllib.request.urlopen(request, timeout=360) as response:
            result = json.loads(response.read().decode("utf-8"))
            deployment_tasks.update(
                task_id, state="succeeded", phase="ready", progress=100,
                status_code=response.status, result=result,
            )
    except urllib.error.HTTPError as exc:
        raw = exc.read().decode("utf-8", errors="replace")
        try:
            detail = json.loads(raw).get("detail") or json.loads(raw).get("error") or raw
        except (ValueError, TypeError):
            detail = raw
        deployment_tasks.update(
            task_id, state="failed", phase="rolled_back", progress=100,
            status_code=exc.code, error=str(detail),
        )
    except Exception as exc:
        deployment_tasks.update(
            task_id, state="failed", phase="failed", progress=100,
            status_code=500, error=f"{type(exc).__name__}: {exc}",
        )


@app.post("/api/deployments", status_code=202)
async def submit_deployment(request: Request):
    if not ADMIN_API_KEY:
        raise HTTPException(503, "管理密钥未配置")
    active = await asyncio.to_thread(deployment_tasks.active)
    if active:
        raise HTTPException(409, f"已有部署任务执行中: {active['task_id']}")
    payload = await request.json()
    model_ref = str(payload.get("filename", ""))
    model = await asyncio.to_thread(catalog_service.find, model_ref)
    if not model or not model.get("deployable"):
        raise HTTPException(404, "可部署模型不存在")
    engine = str(payload.get("engine_type", payload.get("engine", "llama")))
    if engine not in model.get("supported_engines", []):
        raise HTTPException(409, "所选引擎与模型格式不兼容")
    payload["filename"] = model["id"]
    task = await asyncio.to_thread(deployment_tasks.create, model["id"], engine)
    background_executor.submit(_run_deployment_task, task["task_id"], payload)
    return task


@app.get("/api/deployments")
async def list_deployments(limit: int = 30):
    return {"deployments": await asyncio.to_thread(deployment_tasks.list, limit)}


@app.get("/api/deployments/{task_id}")
async def get_deployment(task_id: str):
    task = await asyncio.to_thread(deployment_tasks.get, task_id)
    if not task:
        raise HTTPException(404, "部署任务不存在")
    return task


@app.post("/api/deployments/{task_id}/cancel")
async def cancel_deployment(task_id: str):
    task = await asyncio.to_thread(deployment_tasks.get, task_id)
    if not task:
        raise HTTPException(404, "部署任务不存在")
    if task["state"] == "queued":
        await asyncio.to_thread(
            deployment_tasks.update, task_id,
            state="cancelled", phase="cancelled", progress=100,
            status_code=409, error="用户在执行前取消",
        )
        return await asyncio.to_thread(deployment_tasks.get, task_id)
    if task["state"] == "running":
        raise HTTPException(409, "部署已进入原子切换阶段，不能安全取消；失败时会自动回滚")
    return task


@app.post("/api/catalog/rescan")
async def rescan_catalog():
    models = await asyncio.to_thread(catalog_service.list_models, force=True)
    catalog_meta, summary = await asyncio.gather(
        asyncio.to_thread(catalog_service.metadata),
        asyncio.to_thread(catalog_service.summary, models),
    )
    return {"catalog": catalog_meta, "summary": summary}


@app.get("/api/gpus")
async def list_gpus():
    """Return detected GPUs for the frontend to build dropdown."""
    gpus = await asyncio.to_thread(_detect_gpus)
    return {
        "gpus": gpus,
        "count": len(gpus),
        "default": gpus[0]["short_name"] if len(gpus) == 1 else "all",
        "is_multi": len(gpus) > 1,
    }

@app.post("/api/models/deploy")
async def deploy_model(request: Request):
    raw_data = await request.json()
    if not isinstance(raw_data, dict):
        raise HTTPException(400, "部署参数必须是 JSON 对象")
    data, canonical_parameters = _apply_canonical_parameter_payload(raw_data)
    filename = data.get("filename", "")
    model_record = await asyncio.to_thread(catalog_service.find, filename)
    if not model_record:
        raise HTTPException(404, "模型不存在或 basename 不唯一，请使用 catalog model id")
    filename = model_record.get("relative_path", model_record["id"])
    filepath = await asyncio.to_thread(_safe_model_path, filename, True)
    if model_record.get("format") == "EXTENSOR":
        raise HTTPException(409, "EXTENSOR GGUF 需要专用推理引擎，当前 llama.cpp 不支持")
    if not model_record.get("deployable"):
        raise HTTPException(409, f"{model_record.get('category', '该组件')} 不能作为主模型独立部署")

    # 全参数支持 — 有默认值，面板可按需覆盖
    ctx_size = data.get("ctx_size")
    ngl = data.get("ngl", 99)
    concurrency = data.get("concurrency", 2)
    mmproj_raw = data.get("mmproj")
    mmproj = mmproj_raw is True or str(mmproj_raw).lower() in {"1", "true", "on", "yes"}
    mmproj_file = str(data.get("mmproj_file", "") or "").strip()
    if mmproj_raw is None:
        # Legacy callers that do not send the visual checkbox get the safe
        # same-bundle default, never the previous model's projector path.
        mmproj_file = await asyncio.to_thread(_match_mmproj_for_model, model_record["id"])
        mmproj = bool(mmproj_file)
    # A drawer may retain the previous runtime's checkbox state, but a
    # projector value is only meaningful when the visual option is selected.
    # Drop a stale cross-model filename before any path or bundle validation.
    if not mmproj:
        mmproj_file = ""
    k_cache_type = data.get("k_cache_type", "q8_0")
    v_cache_type = data.get("v_cache_type", "q8_0")
    host = data.get("host", "0.0.0.0")
    port = data.get("port", 8080)
    api_key = (
        data.get("api_key")
        or os.getenv("LLAMA_API_KEY", "").strip()
        or _read_env_config().get("API_KEY", "").strip()
    )
    flash_attn = data.get("flash_attn", True)
    batch = data.get("batch", 1024)
    ubatch = data.get("ubatch", 512)
    chunked_batch = data.get("chunked_batch", True)
    threads = data.get("threads", 8)
    threads_http = data.get("threads_http", 4)
    temp = data.get("temp", 0.7)
    reasoning = data.get("reasoning", "off")
    ui = data.get("ui", False)
    kv_offload = data.get("kv_offload", "off")
    n_cpu_moe = data.get("n_cpu_moe", 0)
    gpu = data.get("gpu", "all")
    tensor_split = data.get("tensor_split", None)
    engine = data.get("engine", "llama")
    llama_version = data.get("llama_version") or await asyncio.to_thread(_get_active_engine) or ""
    profile_id = str(data.get("profile_id") or "default").strip() or "default"
    spec_draft_n_max = data.get("spec_draft_n_max", 1)
    spec_type = data.get("spec_type", "")  # 如 "draft-mtp,ngram-mod"，空=自动
    ngram_mod_n_min = data.get("ngram_mod_n_min", 8)
    ngram_mod_n_max = data.get("ngram_mod_n_max", 32)
    ngram_mod_n_match = data.get("ngram_mod_n_match", 16)
    draft_k_cache_type = data.get("draft_k_cache_type", "q8_0")
    draft_v_cache_type = data.get("draft_v_cache_type", "q8_0")
    draft_model_ref = str(
        data.get("draft_model_id") or data.get("draft_model") or ""
    ).strip()
    cache_ram = data.get("cache_ram", 2048)
    sleep_idle_seconds = data.get("sleep_idle_seconds", 300)
    no_mmap = data.get("no_mmap", False)
    use_mlock = data.get("use_mlock", False)
    numa = data.get("numa", "")
    poll_batch = data.get("poll_batch", 0)
    # Optional common engine settings. They are emitted only after the
    # selected VERSION.json/help probe advertises the corresponding key.
    device = str(data.get("device", "") or "").strip()
    fit = str(data.get("fit", "") or "").strip().lower()
    kv_unified_raw = data.get("kv_unified", False)
    kv_unified = kv_unified_raw is True or str(kv_unified_raw).lower() in {"1", "true", "on", "yes"}
    cache_reuse_raw = data.get("cache_reuse", None)
    spec_draft_p_min_raw = data.get("spec_draft_p_min", None)

    alias = model_record.get("family") or ""

    # Apply the selected engine's recommended profile only when a caller did
    # not send a value. This keeps the API and drawer behavior identical while
    # preserving explicit user overrides.
    pre_engine_record = _get_engine_by_key(llama_version) if engine == "llama" else None
    profiles = (pre_engine_record or {}).get("profiles", {}) if isinstance(pre_engine_record, dict) else {}
    if profiles and profile_id not in profiles:
        raise HTTPException(400, f"引擎 profile 不存在: {profile_id}")
    profile_record = profiles.get(profile_id, {}) if isinstance(profiles, dict) else {}
    profile_values = profile_record.get("parameters") or profile_record.get("values") or {}
    pre_recommended = dict((pre_engine_record or {}).get("recommended_params", {}))
    if isinstance(profile_values, dict):
        pre_recommended.update(profile_values)
    if isinstance(pre_recommended, dict):
        if "ctx_size" not in data:
            ctx_size = pre_recommended.get("ctx_size", ctx_size)
        if "ngl" not in data:
            ngl = pre_recommended.get("ngl", ngl)
        if "concurrency" not in data:
            concurrency = pre_recommended.get("concurrency", concurrency)
        if "batch" not in data:
            batch = pre_recommended.get("batch", batch)
        if "ubatch" not in data:
            ubatch = pre_recommended.get("ubatch", ubatch)
        if "spec_draft_n_max" not in data:
            spec_draft_n_max = pre_recommended.get("spec_draft_n_max", spec_draft_n_max)
        if "device" not in data:
            device = str(pre_recommended.get("device", device) or "").strip()
        if "fit" not in data:
            fit = str(pre_recommended.get("fit", fit) or "").strip().lower()
        if "kv_unified" not in data:
            kv_unified = bool(pre_recommended.get("kv_unified", kv_unified))
        if "cache_reuse" not in data:
            cache_reuse_raw = pre_recommended.get("cache_reuse", cache_reuse_raw)
        if "spec_draft_p_min" not in data:
            spec_draft_p_min_raw = pre_recommended.get("spec_draft_p_min", spec_draft_p_min_raw)
        if "threads" not in data:
            threads = pre_recommended.get("threads", threads)
        if "flash_attn" not in data and "flash_attn" in pre_recommended:
            flash_attn = pre_recommended["flash_attn"]
        if "temp" not in data:
            temp = pre_recommended.get("temp", temp)
        if "k_cache_type" not in data:
            k_cache_type = pre_recommended.get("k_cache_type", k_cache_type)
        if "v_cache_type" not in data:
            v_cache_type = pre_recommended.get("v_cache_type", v_cache_type)

    if not ctx_size:
        ctx_size = model_record.get("ctx_default") or MODEL_CTX_SETTINGS.get(filepath.name, 131072)

    ctx_size = _bounded_int(ctx_size, "ctx_size", 4096, 1048576)
    ngl = _bounded_int(ngl, "ngl", 0, 999)
    concurrency = _bounded_int(concurrency, "concurrency", 1, 32)
    port = _bounded_int(port, "port", 1, 65535)
    batch = _bounded_int(batch, "batch", 1, 65536)
    ubatch = _bounded_int(ubatch, "ubatch", 1, 65536)
    threads = _bounded_int(threads, "threads", 1, 512)
    threads_http = _bounded_int(threads_http, "threads_http", 1, 512)
    n_cpu_moe = _bounded_int(n_cpu_moe, "n_cpu_moe", 0, 512)
    spec_draft_n_max = _bounded_int(spec_draft_n_max, "spec_draft_n_max", 0, 32)
    ngram_mod_n_min = _bounded_int(ngram_mod_n_min, "ngram_mod_n_min", 1, 256)
    ngram_mod_n_max = _bounded_int(ngram_mod_n_max, "ngram_mod_n_max", 1, 256)
    ngram_mod_n_match = _bounded_int(ngram_mod_n_match, "ngram_mod_n_match", 1, 256)
    if ngram_mod_n_min > ngram_mod_n_max:
        ngram_mod_n_min, ngram_mod_n_max = ngram_mod_n_max, ngram_mod_n_min
    poll_batch = _bounded_int(poll_batch, "poll_batch", 0, 1)
    if numa not in ("", "isolated", "numactl"):
        numa = ""
    cache_ram = _bounded_int(cache_ram, "cache_ram", 0, 1048576)
    sleep_idle_seconds = _bounded_int(sleep_idle_seconds, "sleep_idle_seconds", 0, 86400)
    temp = _bounded_float(temp, "temp", 0, 5)
    if isinstance(flash_attn, bool):
        flash_attn = "on" if flash_attn else "off"
    else:
        flash_attn = str(flash_attn or "auto").strip().lower()
    if flash_attn not in {"on", "off", "auto"}:
        raise HTTPException(400, "flash_attn 必须是 on、off 或 auto")
    if fit not in ("", "on", "off"):
        raise HTTPException(400, "fit 必须是 on 或 off")
    cache_reuse = None
    if cache_reuse_raw not in (None, ""):
        cache_reuse = _bounded_int(cache_reuse_raw, "cache_reuse", 0, 1048576)
    spec_draft_p_min = None
    if spec_draft_p_min_raw not in (None, ""):
        spec_draft_p_min = _bounded_float(spec_draft_p_min_raw, "spec_draft_p_min", 0, 1)

    try:
        ipaddress.ip_address(str(host))
    except ValueError:
        raise HTTPException(400, "host 必须是合法 IP 地址")
    if not api_key or len(str(api_key)) > 256 or re.search(r"[\r\n\x00]", str(api_key)):
        raise HTTPException(503, "LLAMA_API_KEY 未配置或格式无效")

    if engine not in {"llama", "vllm"}:
        raise HTTPException(400, "engine 不受支持")
    if engine not in set(model_record.get("supported_engines") or []):
        raise HTTPException(
            409,
            f"{model_record.get('format')} 模型不兼容 {engine} 引擎；支持: {model_record.get('supported_engines') or '无'}",
        )
    if engine == "vllm":
        raise HTTPException(409, "当前节点尚未配置独立的 vLLM systemd launcher，已阻止错误成功状态")
    detected_gpus = await asyncio.to_thread(_detect_gpus)
    valid_gpus = {"all"} | {g["short_name"] for g in detected_gpus} | {str(g["index"]) for g in detected_gpus}
    if gpu not in valid_gpus:
        raise HTTPException(400, "gpu 不受支持")
    if tensor_split is not None:
        tensor_split = str(tensor_split).strip()
        if not re.fullmatch(r"\d+(?:\.\d+)?(?:,\d+(?:\.\d+)?)*", tensor_split):
            raise HTTPException(400, "tensor_split 格式无效")
    if engine == "llama" and not _get_engine_by_key(llama_version):
        engines = await asyncio.to_thread(_scan_engines)
        raise HTTPException(400, f"llama_version 不存在或未注册: {llama_version!r}（可用引擎: {[e.get('key') for e in engines]}）")
    engine_record = _get_engine_by_key(llama_version) if engine == "llama" else None
    if engine == "llama":
        # The resolver is the single source of truth for model/engine
        # compatibility and profile defaults.  The legacy code below still
        # renders the start script, but it receives the resolved values so a
        # new engine does not need another set of frontend/backend rules.
        plan_projectors_all, plan_draft_models = await asyncio.gather(
            asyncio.to_thread(_list_mmproj_files),
            asyncio.to_thread(_list_draft_models_for_model, model_record),
        )
        plan_projectors = [
            item for item in plan_projectors_all
            if item.get("relative_dir") == model_record.get("relative_dir")
        ]
        plan_parameter_keys = {
            str(item.get("key"))
            for item in (engine_record or {}).get("deployment_parameters", [])
            if isinstance(item, dict) and str(item.get("key") or "").strip()
        }
        plan_overrides = {
            key: data[key]
            for key in plan_parameter_keys
            if key in data
        }
        # These two selectors are catalog artifacts rather than llama.cpp
        # flags, so they intentionally sit beside (not inside) the parameter
        # file schema.
        if "mmproj_file" in data:
            plan_overrides["mmproj_file"] = data.get("mmproj_file")
        if "draft_model_id" in data:
            plan_overrides["draft_model_id"] = data.get("draft_model_id")
        elif data.get("draft_model"):
            plan_overrides["draft_model_id"] = data.get("draft_model")
        try:
            deployment_plan = resolve_deployment_plan(
                model_record,
                engine_record or {},
                profile_id=profile_id,
                overrides=plan_overrides,
                projectors=plan_projectors,
                draft_models=plan_draft_models,
            )
        except DeploymentPlanError as exc:
            status = 409 if exc.code in {
                "engine_model_mismatch", "mtp_not_supported", "vision_not_supported",
                "draft_not_supported", "invalid_draft_model", "invalid_projector",
                "spec_type_not_supported", "ngram_not_supported",
            } else 400
            raise HTTPException(status, str(exc))
        resolved_parameters = deployment_plan.get("parameters", {})
        if isinstance(resolved_parameters, dict):
            # Do not overwrite explicit request values.  Defaults/profile
            # values are inserted only for fields omitted by the caller.
            for key, value in resolved_parameters.items():
                data.setdefault(key, value)
            # Re-read every value that the script builder consumes.  This is
            # what makes the resolver authoritative even for legacy callers.
            ctx_size = data.get("ctx_size", ctx_size)
            ngl = data.get("ngl", ngl)
            concurrency = data.get("concurrency", concurrency)
            mmproj_raw = data.get("mmproj")
            mmproj = mmproj_raw is True or str(mmproj_raw).lower() in {"1", "true", "on", "yes"}
            mmproj_file = str(data.get("mmproj_file", "") or "").strip()
            k_cache_type = data.get("k_cache_type", k_cache_type)
            v_cache_type = data.get("v_cache_type", v_cache_type)
            flash_attn = data.get("flash_attn", flash_attn)
            batch = data.get("batch", batch)
            ubatch = data.get("ubatch", ubatch)
            threads = data.get("threads", threads)
            spec_draft_n_max = data.get("spec_draft_n_max", spec_draft_n_max)
            spec_type = data.get("spec_type", spec_type)
            ngram_mod_n_min = data.get("ngram_mod_n_min", ngram_mod_n_min)
            ngram_mod_n_max = data.get("ngram_mod_n_max", ngram_mod_n_max)
            ngram_mod_n_match = data.get("ngram_mod_n_match", ngram_mod_n_match)
            draft_k_cache_type = data.get("draft_k_cache_type", draft_k_cache_type)
            draft_v_cache_type = data.get("draft_v_cache_type", draft_v_cache_type)
            draft_model_ref = str(data.get("draft_model_id") or data.get("draft_model") or "").strip()
            cache_ram = data.get("cache_ram", cache_ram)
            sleep_idle_seconds = data.get("sleep_idle_seconds", sleep_idle_seconds)
            no_mmap = data.get("no_mmap", no_mmap)
            use_mlock = data.get("use_mlock", use_mlock)
            numa = data.get("numa", numa)
            poll_batch = data.get("poll_batch", poll_batch)
            device = str(data.get("device", device) or "").strip()
            fit = str(data.get("fit", fit) or "").strip().lower()
            kv_unified_raw = data.get("kv_unified", kv_unified)
            kv_unified = kv_unified_raw is True or str(kv_unified_raw).lower() in {"1", "true", "on", "yes"}
            cache_reuse_raw = data.get("cache_reuse", cache_reuse_raw)
            spec_draft_p_min_raw = data.get("spec_draft_p_min", spec_draft_p_min_raw)
        _validate_canonical_parameters(engine_record or {}, canonical_parameters)
        # The normalized deployment schema is the allow-list for optional
        # engine flags. Common flags are available to all compatible binaries;
        # truly custom flags remain isolated to their declaring engine.
        branch_values = {
            "device": device,
            "fit": fit,
            "kv_unified": kv_unified,
            "cache_reuse": cache_reuse,
            "spec_draft_p_min": spec_draft_p_min,
        }
        for parameter_name, parameter_value in branch_values.items():
            is_set = parameter_value not in (None, "", False)
            if is_set and not _engine_supports_parameter(engine_record, parameter_name):
                raise HTTPException(
                    400,
                    f"参数 {parameter_name} 不是所选引擎的可用参数；请切换到支持该参数的引擎",
                )
        parameter_values = {
            item.get("key"): item.get("values")
            for item in (engine_record.get("deployment_parameters") or [])
            if isinstance(item, dict) and item.get("values")
        }
        for parameter_name, parameter_value in {
            "device": device,
            "fit": fit,
        }.items():
            allowed_values = parameter_values.get(parameter_name) or []
            if parameter_value and allowed_values and parameter_value not in allowed_values:
                raise HTTPException(
                    400,
                    f"{parameter_name}={parameter_value} 不受所选引擎支持；可选: {', '.join(allowed_values)}",
                )
        cache_types = _engine_cache_types(engine_record)
        draft_cache_types = _engine_cache_types(engine_record, draft=True)
        for cache_name, cache_value, supported_cache_types in (
            ("k_cache_type", k_cache_type, cache_types),
            ("v_cache_type", v_cache_type, cache_types),
            ("draft_k_cache_type", draft_k_cache_type, draft_cache_types),
            ("draft_v_cache_type", draft_v_cache_type, draft_cache_types),
        ):
            if cache_value not in supported_cache_types:
                raise HTTPException(
                    400,
                    f"{cache_name} 不受所选 llama.cpp 引擎支持；可选: {', '.join(supported_cache_types)}",
                )
    draft_model_record = None
    draft_model_path = ""
    if draft_model_ref:
        if engine != "llama" or not bool(engine_record and engine_record.get("supports_draft_model")):
            raise HTTPException(409, "当前推理引擎不支持外置草稿模型")
        draft_model_record = await asyncio.to_thread(catalog_service.find, draft_model_ref)
        if not draft_model_record or draft_model_record.get("role") != "draft":
            raise HTTPException(400, "所选草稿模型不存在或不是草稿组件")
        draft_candidates = _list_draft_models_for_model(model_record)
        if draft_model_record.get("id") not in {item.get("id") for item in draft_candidates}:
            raise HTTPException(409, "草稿模型必须与主模型属于同一模型包或模型族")
        draft_model_path = str(
            await asyncio.to_thread(
                _safe_model_path,
                draft_model_record.get("relative_path", draft_model_record["id"]),
                True,
            )
        )
        if not Path(draft_model_path).is_file() or Path(draft_model_path).suffix.lower() != ".gguf":
            raise HTTPException(400, "草稿模型必须是模型目录内的 GGUF 文件")
    if reasoning is True or str(reasoning).lower() in {"1", "true", "on"}:
        reasoning = "on"
    elif reasoning is False or str(reasoning).lower() in {"0", "false", "off"}:
        reasoning = "off"
    else:
        reasoning = str(reasoning).lower().strip()
    if reasoning not in {"on", "off", "auto"}:
        raise HTTPException(400, "reasoning 必须是 on、off 或 auto")
    kv_offload = "on" if kv_offload is True or str(kv_offload).lower() in {"1", "true", "on"} else "off"
    if engine == "llama" and (not filepath.is_file() or filepath.suffix.lower() != ".gguf"):
        raise HTTPException(400, "llama 引擎只允许部署 GGUF 文件")
    if mmproj_file:
        mmproj_path = await asyncio.to_thread(_safe_model_path, mmproj_file, True)
        if not mmproj_path.is_file() or mmproj_path.suffix.lower() != ".gguf":
            raise HTTPException(400, "mmproj_file 必须是模型目录内的 GGUF 文件")

    # === vLLM 分支 ===
    if engine == "vllm":
        pass  # os already imported globally
        os.makedirs("/data/vllm-config", exist_ok=True)
        tp_size = _bounded_int(data.get("tp_size", 1), "tp_size", 1, 32)
        pp_size = _bounded_int(data.get("pp_size", 1), "pp_size", 1, 32)
        layer_partition = data.get("layer_partition", "")
        kv_cache_dtype = data.get("kv_cache_dtype", "fp8")
        kv_cache_memory_bytes = data.get("kv_cache_memory_bytes", None)
        gpu_memory_utilization = _bounded_float(data.get("gpu_memory_utilization", 0.90), "gpu_memory_utilization", 0.1, 1.0)
        if kv_cache_dtype not in {"auto", "fp8", "fp8_e4m3", "fp16"}:
            raise HTTPException(400, "kv_cache_dtype 不受支持")
        if layer_partition and not re.fullmatch(r"\d+(?:,\d+)*", str(layer_partition)):
            raise HTTPException(400, "layer_partition 格式无效")
        if kv_cache_memory_bytes is not None:
            kv_cache_memory_bytes = _bounded_int(kv_cache_memory_bytes, "kv_cache_memory_bytes", 1, 1099511627776)
        model_path = str(filepath)

        # GPU selection for vLLM (via env config file read by start-vllm.sh)
        gpu_map = {g["short_name"]: str(g["index"]) for g in detected_gpus}
        cuda_val = gpu_map.get(gpu, "")
        with open("/data/vllm-config/cuda_devices", "w") as _f:
            _f.write(cuda_val)
        os.chmod("/data/vllm-config/cuda_devices", 0o600)

        args_lines = [
            "--model", model_path,
            "--host", host, "--port", str(port),
            "--api-key", str(api_key),
            "--tensor-parallel-size", str(tp_size),
            "--kv-cache-dtype", kv_cache_dtype,
            "--gpu-memory-utilization", str(gpu_memory_utilization),
            "--max-model-len", str(ctx_size) if ctx_size else "131072",
                        "--enforce-eager",
            "--disable-custom-all-reduce",
            "--quantization", "compressed-tensors",
        ]
        if kv_cache_memory_bytes:
            args_lines.extend(["--kv-cache-memory-bytes", str(kv_cache_memory_bytes)])

        # PP configuration
        if pp_size > 1:
            args_lines.extend(["--pipeline-parallel-size", str(pp_size)])
            # Update existing --tensor-parallel-size to 1 instead of adding duplicate
            new_args = []
            skip_next = False
            for arg in args_lines:
                if skip_next:
                    skip_next = False
                    continue
                if arg == "--tensor-parallel-size":
                    skip_next = True
                    new_args.extend(["--tensor-parallel-size", "1"])
                    continue
                new_args.append(arg)
            args_lines = new_args

            if layer_partition:
                with open("/data/vllm-config/pp_layer_partition", "w") as _pf:
                    _pf.write(layer_partition)
                os.chmod("/data/vllm-config/pp_layer_partition", 0o600)
                print(f"[PP] Layer partition set to {layer_partition}")

        with open("/data/vllm-config/vllm.args", "w") as f:
            f.write(chr(10).join(args_lines) + chr(10))
        os.chmod("/data/vllm-config/vllm.args", 0o600)

        _switch_and_restart(engine)

        return {
            "engine": "vllm",
            "model": model_path,
            "tp_size": tp_size,
            "pp_size": pp_size,
            "layer_partition": layer_partition if pp_size > 1 else None,
            "kv_cache_dtype": kv_cache_dtype,
            "kv_cache_memory_bytes": kv_cache_memory_bytes,
            "gpu_memory_utilization": gpu_memory_utilization,
            "state": "restarting",
        }

    # 构建新的 start-llama-server.sh 内容（保持正确的多行格式）
    llama_bin = _resolve_binary_path(llama_version)
    if not llama_bin or not Path(llama_bin).is_file():
        raise HTTPException(409, f"引擎二进制不可用: {llama_version}")
    mmproj_path = ""
    if mmproj:
        if mmproj_file:
            projector = await asyncio.to_thread(catalog_service.find, mmproj_file)
            if not projector or projector.get("role") != "projection":
                raise HTTPException(400, "所选文件不是有效的视觉投影组件")
            if projector.get("relative_dir") != model_record.get("relative_dir"):
                raise HTTPException(409, "视觉投影组件与主模型不在同一模型包中")
            mmproj_path = str(await asyncio.to_thread(_safe_model_path, projector.get("relative_path", projector["id"]), True))
        else:
            matched = _match_mmproj_for_model(model_record.get("relative_path", model_record["id"]))
            if matched:
                mmproj_path = str(await asyncio.to_thread(_safe_model_path, matched, True))
        if not mmproj_path:
            raise HTTPException(409, "该模型没有可确认匹配的视觉投影组件")
    # ui_flag is handled below as a conditional line item

    flash_attn_str = str(flash_attn)
    cb_str = " -cb" if chunked_batch else ""

    lines = [
        "#!/bin/bash",
        "set -euo pipefail",
        f"source {shlex.quote(ENV_FILE)}",
        ': "${API_KEY:?API_KEY is required}"',
    ]
    if gpu != "all":
        selected = next(
            (
                item
                for item in detected_gpus
                if str(item.get("index")) == str(gpu) or item.get("short_name") == gpu
            ),
            None,
        )
        if selected is None:
            raise HTTPException(400, "无法解析所选 GPU")
        selected_index = int(selected["index"])
        if "amd" in str(selected.get("name", "")).lower():
            lines.append(f"export HIP_VISIBLE_DEVICES={selected_index}")
            lines.append(f"export ROCR_VISIBLE_DEVICES={selected_index}")
        else:
            lines.append(f"export CUDA_VISIBLE_DEVICES={selected_index}")
    # LD_LIBRARY_PATH: 引擎需要额外共享库时设置
    eng = _get_engine_by_key(llama_version)
    if eng and eng.get("lib_path"):
        lines.append(f"export LD_LIBRARY_PATH={shlex.quote(str(eng['lib_path']))}:\"${{LD_LIBRARY_PATH:-}}\"")
    generic_args, generic_exports = _generic_parameter_flags(eng, canonical_parameters)
    lines.extend(_engine_environment_exports(eng))
    lines.extend(generic_exports)
    tensor_split_line = (f"  --tensor-split {tensor_split} --main-gpu 0 \\") if tensor_split else ""
    spec_params = ""
    is_mtp_model = bool(model_requirements(model_record).get("mtp"))
    # The drawer submits "none" when both checkboxes are clear.  Keep the
    # backend default equally conservative: only a detected MTP model gets
    # automatic draft-mtp; n-gram is opt-in for all models.
    engine_spec_types = set(_engine_spec_types(engine_record))
    requested_types = [
        item.strip()
        for item in str(spec_type or "").split(",")
        if item.strip() and item.strip() != "none"
    ]
    draft_types = {"draft-simple", "draft-eagle3", "draft-mtp", "draft-dflash", "draft-dspark"}
    if draft_model_record:
        inferred = _draft_spec_type(draft_model_record)
        if "draft-mtp" in requested_types:
            raise HTTPException(409, "外置草稿模型不能与 MTP 内置草稿层同时启用，请先关闭 MTP")
        if not any(item in draft_types for item in requested_types):
            requested_types.insert(0, inferred)
    if not requested_types and not str(spec_type or "").strip() and is_mtp_model and _supports_mtp(llama_version):
        requested_types = ["draft-mtp"]
    invalid_types = [item for item in requested_types if item not in engine_spec_types]
    if invalid_types:
        raise HTTPException(400, f"所选引擎不支持的推测解码类型: {invalid_types}")
    if "draft-mtp" in requested_types and (not is_mtp_model or not _supports_mtp(llama_version)):
        raise HTTPException(409, "模型或所选引擎不支持 MTP 推测解码")
    if any(item in draft_types - {"draft-mtp"} for item in requested_types) and not draft_model_record:
        raise HTTPException(400, "外置草稿推测解码必须先选择草稿模型")
    if requested_types:
        spec_params = f" --spec-type {','.join(requested_types)}"
        if any(item in draft_types for item in requested_types):
            spec_params += f" --spec-draft-n-max {spec_draft_n_max}"
            if spec_draft_p_min is not None and _engine_supports_parameter(engine_record, "spec_draft_p_min"):
                spec_params += f" --spec-draft-p-min {spec_draft_p_min:g}"
        if any(item.startswith("ngram") for item in requested_types):
            spec_params += f" --spec-ngram-mod-n-min {ngram_mod_n_min} --spec-ngram-mod-n-max {ngram_mod_n_max} --spec-ngram-mod-n-match {ngram_mod_n_match}"
    else:
        spec_draft_n_max = 0
    uses_draft_spec = any(item in draft_types for item in requested_types)
    # cache_ram and sleep_idle_seconds
    extra_params = ""
    if cache_ram:
        extra_params += f" --cache-ram {cache_ram}"
    if sleep_idle_seconds:
        extra_params += f" --sleep-idle-seconds {sleep_idle_seconds}"
    if no_mmap:
        extra_params += " --no-mmap"
    if use_mlock:
        extra_params += " --mlock"
    if numa:
        extra_params += f" --numa {numa}"
    if poll_batch:
        extra_params += " --poll-batch 1"
    if n_cpu_moe:
        extra_params += f" --n-cpu-moe {n_cpu_moe}"
    if device and _engine_supports_parameter(engine_record, "device"):
        extra_params += f" --device {shlex.quote(device)}"
    if fit and _engine_supports_parameter(engine_record, "fit"):
        extra_params += f" --fit {fit}"
    if kv_unified and _engine_supports_parameter(engine_record, "kv_unified"):
        extra_params += " --kv-unified"
    if cache_reuse is not None and _engine_supports_parameter(engine_record, "cache_reuse"):
        extra_params += f" --cache-reuse {cache_reuse}"
    generic_params = " " + " ".join(generic_args) if generic_args else ""
    if llama_version in ("turboquant", "turbo"):
        lines.append("export TURBO_AUTO_ASYMMETRIC=0")
    lines.append(f"exec {shlex.quote(str(llama_bin))} \\")
    lines.append(f"  -m {shlex.quote(str(filepath))} \\")
    if draft_model_path:
        lines.append(f"  --model-draft {shlex.quote(draft_model_path)} \\")
    if alias:
        lines.append(f"  -a {shlex.quote(alias)} \\")
    lines += [
        f"  --host {shlex.quote(str(host))} --port {port} --api-key \"${{API_KEY}}\" \\",
        f"  -ngl {ngl} --ctx-size {ctx_size} \\",
        tensor_split_line or None,
        f"  --cache-type-k {k_cache_type} --cache-type-v {v_cache_type} \\",
        (f"  --cache-type-k-draft {draft_k_cache_type} --cache-type-v-draft {draft_v_cache_type} \\" if uses_draft_spec else None),
        f"  --flash-attn {flash_attn_str} -b {batch} -ub {ubatch}{cb_str} -np {concurrency} -t {threads} \\",
        "  --ui \\" if ui else None,
        f"  --threads-http {threads_http} --temp {temp} --reasoning {reasoning} \\",
        f"  --log-file /tmp/llama-server.log --log-verbosity 3 --log-colors off --metrics"
        + (f" --mmproj {shlex.quote(mmproj_path)}" if mmproj_path else "")
        + spec_params + extra_params + generic_params,
    ]
    lines = [l for l in lines if l is not None]
    script_content = "\n".join(lines) + chr(10)

    previous_script, previous_runtime = await asyncio.gather(
        asyncio.to_thread(read_start_script),
        asyncio.to_thread(_get_running_cmdline_config, True),
    )
    previous_active = Path(_ACTIVE_ENGINE_STATE).read_text(encoding="utf-8") if Path(_ACTIVE_ENGINE_STATE).exists() else ""
    persist_path = Path("/data/inference-hub/state/persist_config.json")
    previous_persist = persist_path.read_text(encoding="utf-8") if persist_path.exists() else ""
    try:
        await asyncio.to_thread(validate_start_script)
        await asyncio.to_thread(write_start_script, script_content)
        await asyncio.to_thread(
            update_persist_state,
            engine=llama_version,
            binary=llama_bin,
            model=str(filepath),
            args=" ".join([l.strip("\\") for l in lines[1:] if l is not None]).replace("  ", " ").strip(),
            parameters=canonical_parameters,
            profile_id=profile_id,
        )
        await asyncio.to_thread(_switch_and_restart, llama_version, str(filepath))
    except Exception as exc:
        rollback_errors = []
        try:
            if previous_script:
                await asyncio.to_thread(write_start_script, previous_script)
            if previous_active:
                await asyncio.to_thread(_atomic_write_text, _ACTIVE_ENGINE_STATE, previous_active)
            if previous_persist:
                await asyncio.to_thread(_atomic_write_text, persist_path, previous_persist)
            if previous_runtime and previous_runtime.get("model_path"):
                await asyncio.to_thread(
                    restart_llama_server,
                    previous_runtime.get("model_path", ""),
                    previous_runtime.get("llama_version", ""),
                )
        except Exception as rollback_exc:
            rollback_errors.append(str(rollback_exc))
        detail = f"部署未就绪，已尝试回滚: {exc}"
        if rollback_errors:
            detail += f"；回滚异常: {'; '.join(rollback_errors)}"
        raise HTTPException(502, detail)

    _invalidate_cmdline_cache()  # 部署成功，进程已变
    return {
        "path": str(filepath),
        "alias": alias,
        "ctx_size": ctx_size,
        "ngl": ngl,
        "tensor_split": tensor_split,
        "concurrency": concurrency,
        "mmproj": mmproj,
        "mmproj_file": mmproj_file,
        "draft_model_id": draft_model_record.get("id") if draft_model_record else None,
        "draft_model": Path(draft_model_path).name if draft_model_path else None,
        "k_cache_type": k_cache_type,
        "v_cache_type": v_cache_type,
        "draft_k_cache_type": draft_k_cache_type,
        "draft_v_cache_type": draft_v_cache_type,
        "host": host,
        "port": port,
        "flash_attn": flash_attn,
        "batch": batch,
        "ubatch": ubatch,
        "chunked_batch": chunked_batch,
        "threads": threads,
        "threads_http": threads_http,
        "temp": temp,
        "reasoning": reasoning,
        "ui": ui,
        "llama_version": llama_version,
        "spec_type": spec_type,
        "spec_draft_n_max": spec_draft_n_max,
        "kv_offload": kv_offload,
        "n_cpu_moe": n_cpu_moe,
        "gpu": gpu,
        "cache_ram": cache_ram,
        "sleep_idle_seconds": sleep_idle_seconds,
        "no_mmap": no_mmap,
        "use_mlock": use_mlock,
        "numa": numa,
        "poll_batch": poll_batch,
        "device": device,
        "fit": fit,
        "kv_unified": kv_unified,
        "cache_reuse": cache_reuse,
        "spec_draft_p_min": spec_draft_p_min,
    }

@app.post("/api/models/stop")
async def stop_model():
    if await asyncio.to_thread(stop_llama_server):
        _invalidate_cmdline_cache()  # 已停止，进程消失
        return {"stopped": True}
    else:
        raise HTTPException(502, "推理服务未能停止；请检查受限控制权限和 systemd 状态")

@app.post("/api/models/delete")
async def delete_model(request: Request):
    import shutil
    data = await request.json()
    filename = data.get("filename", "")
    model_record = await asyncio.to_thread(catalog_service.find, filename)
    if not model_record:
        raise HTTPException(404, "模型不存在")
    filename = model_record.get("relative_path", model_record["id"])
    filepath = await asyncio.to_thread(_safe_model_path, filename, True)
    runtime = await asyncio.to_thread(_get_running_cmdline_config)
    if runtime and runtime.get("model_path"):
        try:
            running_path = Path(runtime["model_path"]).resolve()
            target_path = filepath.resolve()
            if running_path == target_path or target_path in running_path.parents:
                raise HTTPException(409, "当前运行模型不能删除，请先停止服务或部署其他模型")
        except OSError:
            pass
    try:
        if filepath.is_dir():
            size = int((await asyncio.to_thread(
                subprocess.check_output, ["du", "-sb", str(filepath)], stderr=subprocess.DEVNULL
            )).split()[0])
        else:
            size = (await asyncio.to_thread(filepath.stat)).st_size
        trash_dir = Path(DATA_DIR) / ".trash"
        await asyncio.to_thread(trash_dir.mkdir, mode=0o700, parents=True, exist_ok=True)
        trash_name = f"{int(time.time())}-{uuid.uuid4().hex[:12]}-{filepath.name}"
        trash_path = trash_dir / trash_name
        await asyncio.to_thread(shutil.move, str(filepath), str(trash_path))
        await asyncio.to_thread(catalog_service.invalidate)
        return {
            "deleted": filename,
            "size": format_size(size),
            "recoverable": True,
            "trash_id": trash_name,
        }
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(500, f"删除失败: {str(e)}")


@app.post("/api/models/rename")
async def rename_model(request: Request):
    """重命名模型文件或目录"""
    import shutil
    data = await request.json()
    filename = data.get("filename", "")
    new_name = data.get("new_name", "").strip()
    if not filename or not new_name:
        raise HTTPException(400, "filename 和 new_name 不能为空")
    # 校验新文件名只包含安全字符
    import re
    if not re.match(r"^[\w.-]+$", new_name):
        raise HTTPException(400, "新文件名包含非法字符，只允许字母、数字、下划线、点和横线")
    # 防止路径穿越
    if "/" in new_name or ".." in new_name:
        raise HTTPException(400, "新文件名不能包含路径分隔符")
    model_record = await asyncio.to_thread(catalog_service.find, filename)
    if not model_record:
        raise HTTPException(404, "模型不存在")
    stable_uid = model_record["id"]
    filename = model_record.get("relative_path", stable_uid)
    filepath = await asyncio.to_thread(_safe_model_path, filename, True)
    runtime = await asyncio.to_thread(_get_running_cmdline_config)
    if runtime and runtime.get("model_path"):
        try:
            running_path = Path(runtime["model_path"]).resolve()
            target_path = filepath.resolve()
            if running_path == target_path or target_path in running_path.parents:
                raise HTTPException(409, "当前运行模型不能重命名，请先停止服务或部署其他模型")
        except OSError:
            pass
    relative_parent = filepath.parent.resolve().relative_to(Path(DATA_DIR).resolve())
    new_filepath = await asyncio.to_thread(_safe_model_path, str(relative_parent / new_name))
    if await asyncio.to_thread(new_filepath.exists):
        raise HTTPException(409, "目标文件名已存在")
    try:
        await asyncio.to_thread(filepath.rename, new_filepath)
    except OSError as e:
        raise HTTPException(500, f"重命名失败: {str(e)}")
    await asyncio.to_thread(catalog_service.invalidate)
    await asyncio.to_thread(catalog_service.list_models, force=True)
    return {
        "renamed": filename,
        "to": new_name,
        "id": stable_uid,
        "relative_path": str(new_filepath.resolve().relative_to(Path(DATA_DIR).resolve())),
    }

@app.get("/api/hub/search")
@app.get("/api/hub/search/{q}")
async def search_hugging_face(q: str, limit: int = 20):
    query = q.strip()
    if len(query) < 2 or len(query) > 100:
        raise HTTPException(400, "搜索词长度需在 2-100 个字符之间")
    limit = max(1, min(limit, 30))
    params = urllib.parse.urlencode({"search": query, "filter": "gguf", "sort": "downloads", "direction": -1, "limit": limit})
    rows = await asyncio.to_thread(_hf_json, f"{HF_API}/models?{params}")
    models = []
    for row in rows if isinstance(rows, list) else []:
        repo_id = str(row.get("id") or row.get("modelId") or "")
        if HF_REPO_RE.fullmatch(repo_id):
            models.append({"id": repo_id, "downloads": int(row.get("downloads") or 0),
                           "likes": int(row.get("likes") or 0), "last_modified": row.get("lastModified") or "",
                           "gated": bool(row.get("gated")), "tags": list(row.get("tags") or [])[:12]})
    return {"source": "huggingface", "models": models}


@app.get("/api/hub/files")
@app.get("/api/hub/files/{repo_id:path}")
async def hugging_face_files(repo_id: str):
    if not HF_REPO_RE.fullmatch(repo_id or ""):
        raise HTTPException(400, "无效的 Hugging Face 模型 ID")
    data = await asyncio.to_thread(
        _hf_json,
        f"{HF_API}/models/{urllib.parse.quote(repo_id, safe='/')}/tree/main?recursive=true&expand=false",
    )
    files = []
    for item in data if isinstance(data, list) else []:
        name = str(item.get("path") or "")
        if name.lower().endswith(".gguf"):
            size = int(item.get("size") or 0)
            files.append({"name": name, "size": size, "size_human": format_size(size) if size else "未知"})
    files.sort(key=lambda item: (item["size"] == 0, item["size"], item["name"]))
    return {"source": "huggingface", "repo_id": repo_id, "files": files}


@app.post("/api/hub/downloads", status_code=202)
async def create_hugging_face_download(request: Request):
    data = await request.json()
    repo_id, filename = _validate_hf_file(str(data.get("repo_id") or ""), str(data.get("filename") or ""))
    destination = _safe_model_path(Path(filename).name)
    if destination.exists():
        raise HTTPException(409, f"文件已存在: {destination.name}")
    task = await asyncio.to_thread(download_tasks.create, repo_id, filename, str(destination))
    background_executor.submit(_run_hf_download, task["task_id"], repo_id, filename, destination)
    return task


@app.get("/api/hub/downloads/{task_id}")
async def get_hugging_face_download(task_id: str):
    task = await asyncio.to_thread(download_tasks.get, task_id)
    if not task:
        raise HTTPException(404, "下载任务不存在")
    return task


@app.post("/api/upload")
async def upload_model(file: UploadFile = File(...)):
    original_name = file.filename or ""
    safe_name = Path(original_name).name
    if safe_name != original_name or not re.fullmatch(r"[\w.-]+\.gguf", safe_name, re.IGNORECASE):
        raise HTTPException(400, "只支持 .gguf 文件")
    dest = _safe_model_path(safe_name)
    if dest.exists():
        raise HTTPException(409, f"文件已存在: {safe_name}")
    temp_dest = dest.with_name(f".{safe_name}.{os.getpid()}.uploading")
    chunk_size = 4 * 1024 * 1024
    fd = None
    uploaded = 0
    try:
        fd = await asyncio.to_thread(os.open, str(temp_dest), os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        while True:
            chunk = await file.read(chunk_size)
            if not chunk:
                break
            uploaded += len(chunk)
            if uploaded > MAX_UPLOAD_BYTES:
                raise HTTPException(413, "上传文件超过节点限制")
            await asyncio.to_thread(_write_upload_chunk, fd, chunk)
        await asyncio.to_thread(os.fsync, fd)
        await asyncio.to_thread(os.close, fd)
        fd = None
        await asyncio.to_thread(os.replace, temp_dest, dest)
        await asyncio.to_thread(catalog_service.invalidate)
    except Exception:
        if fd is not None:
            with suppress(OSError):
                await asyncio.to_thread(os.close, fd)
        temp_dest.unlink(missing_ok=True)
        raise
    finally:
        await file.close()
    stat = await asyncio.to_thread(dest.stat)
    return {"path": str(dest), "name": safe_name, "size": format_size(stat.st_size)}

@app.post("/api/models/toggle-mmproj")
async def toggle_mmproj(request: Request):
    """Toggle mmproj by editing parsed argv instead of interpolating shell text."""
    data = await request.json()
    content = await asyncio.to_thread(read_start_script)
    if not content:
        raise HTTPException(500, "无法读取启动脚本")

    prefix = []
    command_parts = []
    in_command = False
    for raw_line in content.splitlines():
        stripped = raw_line.strip()
        if stripped.startswith("exec "):
            in_command = True
        if in_command:
            command_parts.append(stripped[:-1].rstrip() if stripped.endswith("\\") else stripped)
        else:
            prefix.append(raw_line)
    try:
        argv = shlex.split(" ".join(command_parts))
    except ValueError as exc:
        raise HTTPException(400, f"启动脚本无法安全解析: {exc}")
    if len(argv) < 2 or argv[0] != "exec":
        raise HTTPException(400, "启动脚本缺少 exec 命令")

    current_mmproj = None
    if "--mmproj" in argv:
        idx = argv.index("--mmproj")
        if idx + 1 < len(argv):
            current_mmproj = argv[idx + 1]
            del argv[idx:idx + 2]
        else:
            del argv[idx]
    enabled = current_mmproj is None
    mmproj_path = None
    if enabled:
        requested = str(data.get("mmproj_file") or "").strip()
        if not requested:
            model_arg = argv[argv.index("-m") + 1] if "-m" in argv and argv.index("-m") + 1 < len(argv) else ""
            alias_arg = argv[argv.index("-a") + 1] if "-a" in argv and argv.index("-a") + 1 < len(argv) else ""
            requested = _match_mmproj_for_model(alias_arg or model_arg) or ""
        if not requested:
            raise HTTPException(400, "未找到可用的 mmproj 文件")
        mmproj_path = str(await asyncio.to_thread(_safe_model_path, requested, True))
        if Path(mmproj_path).suffix.lower() != ".gguf":
            raise HTTPException(400, "mmproj 文件类型无效")
        argv.extend(["--mmproj", mmproj_path])

    rendered = list(prefix or ["#!/bin/bash"])
    rendered.append(f"exec {shlex.quote(argv[1])} \\")
    remaining = argv[2:]
    for index, arg in enumerate(remaining):
        suffix = " \\" if index < len(remaining) - 1 else ""
        rendered.append(f"  {shlex.quote(arg)}{suffix}")
    await asyncio.to_thread(validate_start_script)
    await asyncio.to_thread(write_start_script, "\n".join(rendered) + "\n")
    model_arg = argv[argv.index("-m") + 1] if "-m" in argv and argv.index("-m") + 1 < len(argv) else ""
    active_engine = await asyncio.to_thread(_get_active_engine)
    await asyncio.to_thread(restart_llama_server, model_arg, active_engine)
    return {"enabled": enabled, "mmproj_file": os.path.basename(mmproj_path) if enabled else None}


# === 服务管理 ===
LLAMA_SERVICE = "/etc/systemd/system/inference-server.service"
SET_LLAMA_RESTART = "/usr/local/bin/set-llama-restart.sh"
SETTINGS_FILE = "/data/dashboard/settings.json"

def _load_settings():
    import json
    try:
        with open(SETTINGS_FILE, 'r') as f:
            return json.load(f)
    except:
        return {}

def _save_settings(settings):
    import json
    with open(SETTINGS_FILE, 'w') as f:
        json.dump(settings, f, indent=2)

@app.get("/api/service/persist")
async def get_persist_mode():
    """获取推理服务持久化模式"""
    try:
        result = await asyncio.to_thread(
            subprocess.run,
            ["systemctl", "show", "inference-server", "-p", "Restart"],
            capture_output=True, text=True, timeout=10,
        )
        if result.returncode == 0:
            mode = result.stdout.strip().split('=')[1] if '=' in result.stdout else 'always'
            persist_mode = 'auto' if mode == 'always' else 'manual'
        else:
            persist_mode = 'auto'
    except:
        persist_mode = 'auto'
    return {"mode": persist_mode, "label": "auto" if persist_mode == "auto" else "manual"}

@app.post("/api/service/persist")
async def set_persist_mode(request: Request):
    """设置推理服务持久化模式"""
    data = await request.json()
    mode = data.get("mode", "auto")
    if mode not in ('auto', 'manual'):
        raise HTTPException(400, "无效模式")
    restart_val = 'always' if mode == 'auto' else 'no'
    try:
        r = await asyncio.to_thread(sudo_run, [SET_LLAMA_RESTART, restart_val], 10)
        if r.returncode != 0:
            raise HTTPException(500, f"修改失败: {r.stderr.strip()}")
        await asyncio.to_thread(sudo_run, ["systemctl", "daemon-reload"], 10)
        settings = await asyncio.to_thread(_load_settings)
        settings['persist_mode'] = mode
        await asyncio.to_thread(_save_settings, settings)
        return {"status": "ok", "message": "ok", "mode": mode}
    except HTTPException:
        raise
    except Exception as e:
        raise HTTPException(500, str(e))

@app.post("/api/service/restart")
async def restart_service():
    """重启推理服务"""
    try:
        runtime = await asyncio.to_thread(_get_running_cmdline_config)
        await asyncio.to_thread(
            restart_llama_server,
            runtime.get("model_path", "") if runtime else "",
            runtime.get("llama_version", "") if runtime else "",
        )
        _invalidate_cmdline_cache()  # 重启完成，pid 可能已变
        return {"status": "ok", "message": "已重启并通过就绪检查"}
    except Exception as e:
        raise HTTPException(500, str(e))

@app.post("/api/service/switch_version")
async def switch_version(request: Request):
    """切换 llama.cpp 版本并重启（不改变其他参数）"""
    data = await request.json()
    llama_version = data.get("llama_version") or await asyncio.to_thread(_get_active_engine) or ""

    # Engine switching now uses the same canonical deployment planner as a
    # normal deployment.  This keeps paths, profiles, environment variables,
    # speculative decoding and rollback behavior identical across both entry
    # points.  The legacy regex implementation below remains unreachable for
    # one compatibility release and can be removed after old clients migrate.
    target_engine = await asyncio.to_thread(_get_engine_by_key, llama_version)
    if not target_engine or not Path(str(target_engine.get("binary_path") or "")).is_file():
        raise HTTPException(400, "所选 llama.cpp 引擎不存在或二进制不可用")
    runtime_before_switch = await asyncio.to_thread(_get_running_cmdline_config, True)
    if not runtime_before_switch:
        runtime_before_switch = parse_script_config(await asyncio.to_thread(read_start_script))
    model_path = str((runtime_before_switch or {}).get("model_path") or "")
    if not model_path:
        raise HTTPException(400, "当前没有可切换的运行模型，请先部署模型")
    try:
        relative_model = str(Path(model_path).resolve().relative_to(Path(DATA_DIR).resolve()))
    except (ValueError, OSError):
        raise HTTPException(400, "当前模型路径不在模型目录内，已阻止切换")
    model_record = await asyncio.to_thread(catalog_service.find, relative_model)
    if not model_record:
        raise HTTPException(404, "当前运行模型未登记，无法安全切换引擎")

    def _artifact_id(path_value: str, role: str) -> str:
        if not path_value:
            return ""
        candidate = catalog_service.find(str(path_value))
        if candidate and candidate.get("role") == role:
            return str(candidate.get("id") or "")
        try:
            relative = str(Path(path_value).resolve().relative_to(Path(DATA_DIR).resolve()))
        except (ValueError, OSError):
            relative = str(path_value)
        candidate = catalog_service.find(relative)
        return str(candidate.get("id") or "") if candidate and candidate.get("role") == role else ""

    runtime = runtime_before_switch or {}
    persisted_parameters: dict[str, Any] = {}
    persisted_profile = "default"
    try:
        persisted = json.loads(Path("/data/inference-hub/state/persist_config.json").read_text(encoding="utf-8"))
        if isinstance(persisted, dict):
            persisted_parameters = persisted.get("parameters") if isinstance(persisted.get("parameters"), dict) else {}
            persisted_profile = str(persisted.get("profile_id") or "default")
    except (OSError, json.JSONDecodeError):
        pass
    target_keys = set(_engine_parameter_map(target_engine))
    canonical_parameters = {key: value for key, value in persisted_parameters.items() if key in target_keys}
    mmproj_id = _artifact_id(str(runtime.get("mmproj_file") or ""), "projection") if runtime.get("mmproj") else ""
    draft_id = _artifact_id(str(runtime.get("draft_model_path") or ""), "draft")
    switch_payload: dict[str, Any] = {
        "filename": model_record.get("id"),
        "engine": "llama",
        "llama_version": llama_version,
        "profile_id": data.get("profile_id") or persisted_profile,
        "ctx_size": runtime.get("ctx_size", 131072),
        "ngl": runtime.get("ngl", 99),
        "gpu": runtime.get("gpu", "all"),
        "concurrency": runtime.get("concurrency", 2),
        "k_cache_type": runtime.get("k_cache_type", "q8_0"),
        "v_cache_type": runtime.get("v_cache_type", "q8_0"),
        "batch": runtime.get("batch", 1024),
        "ubatch": runtime.get("ubatch", 512),
        "flash_attn": runtime.get("flash_attn", True),
        "chunked_batch": runtime.get("chunked_batch", True),
        "threads": runtime.get("threads", 8),
        "threads_http": runtime.get("threads_http", 4),
        "temp": runtime.get("temp", 0.7),
        "reasoning": runtime.get("reasoning", "off"),
        "ui": runtime.get("ui", False),
        "mmproj": bool(mmproj_id),
        "mmproj_file": mmproj_id,
        "draft_model_id": draft_id,
        "spec_type": runtime.get("spec_type") or "none",
        "spec_draft_n_max": runtime.get("spec_draft_n_max", 0),
        "draft_k_cache_type": runtime.get("draft_k_cache_type", "q8_0"),
        "draft_v_cache_type": runtime.get("draft_v_cache_type", "q8_0"),
        "ngram_mod_n_min": runtime.get("ngram_mod_n_min") or 48,
        "ngram_mod_n_max": runtime.get("ngram_mod_n_max") or 64,
        "ngram_mod_n_match": runtime.get("ngram_mod_n_match") or 24,
        "cache_ram": runtime.get("cache_ram", 8192),
        "sleep_idle_seconds": runtime.get("sleep_idle_seconds", 300),
        "device": runtime.get("device", ""),
        "fit": runtime.get("fit", ""),
        "kv_unified": runtime.get("kv_unified", False),
        "cache_reuse": runtime.get("cache_reuse"),
        "spec_draft_p_min": runtime.get("spec_draft_p_min"),
        "parameters": canonical_parameters,
    }
    switched = await deploy_model(_JsonBodyRequest(switch_payload))
    return {
        "status": "ok",
        "message": f"已切换到 {target_engine.get('name') or llama_version} 版本并重启",
        "deployment": switched,
    }

    # Legacy script mutation path retained below for rollback reference.
    spec_draft_n_max = data.get("spec_draft_n_max", 3)
    engine_record = await asyncio.to_thread(_get_engine_by_key, llama_version)
    if not engine_record or not Path(str(engine_record.get("binary_path") or "")).is_file():
        raise HTTPException(400, "所选 llama.cpp 引擎不存在或二进制不可用")

    # 读取当前脚本
    if not os.path.exists(START_SCRIPT):
        raise HTTPException(400, "启动脚本不存在，请先部署一个模型")

    content_script = await asyncio.to_thread(Path(START_SCRIPT).read_text)

    # 构建新二进制路径
    new_bin = _resolve_binary_path(llama_version)

    # 替换二进制路径
    # Replace old engine paths
    # 动态替换：遍历所有已注册引擎 binary 路径 + 已知历史路径，全部替换为新二进制
    for _e in await asyncio.to_thread(_scan_engines):
        _bp = _e.get("binary_path")
        if _bp:
            content_script = content_script.replace(_bp, new_bin)
    for _old in ("/data/llama-cpp-turboquant/build/bin/llama-server",
                 "/data/engines/turboquant/build/bin/llama-server",
                 "/data/llama.cpp/build/bin/llama-server",
                 "/data/llama-cpp-mtp/build/bin/llama-server",
                 "/data/engines/mtp/build/bin/llama-server",
                 "/data/llama-cpp-mtp-clean-new/build/bin/llama-server",
                 "/data/engines/mtp-clean/build/bin/llama-server"):
        content_script = content_script.replace(_old, new_bin)

    # 处理版本特有参数
    # LD_LIBRARY_PATH: 引擎需要额外共享库时设置
    eng = _get_engine_by_key(llama_version)
    if eng and eng.get("lib_path"):
        lib_line = f"export LD_LIBRARY_PATH={eng['lib_path']}:$LD_LIBRARY_PATH"
        if lib_line not in content_script:
            content_script = content_script.replace("#!/bin/bash\n", "#!/bin/bash\n" + lib_line + "\n", 1)
    else:
        # 移除旧的 LD_LIBRARY_PATH（如果不需要）
        lines = content_script.split("\n")
        lines = [l for l in lines if not l.startswith("export LD_LIBRARY_PATH=/data/engines/")]
        content_script = "\n".join(lines)

    # MTP: 添加/移除 speculative decoding 参数
    runtime_before_switch = await asyncio.to_thread(_get_running_cmdline_config, True)
    runtime_model = await asyncio.to_thread(
        catalog_service.find,
        str(Path(runtime_before_switch.get("model_path", "")).resolve().relative_to(Path(DATA_DIR).resolve())),
    ) if runtime_before_switch and runtime_before_switch.get("model_path") else None
    model_supports_mtp = bool(runtime_model and "MTP" in (runtime_model.get("classification", {}).get("capabilities") or []))
    if _supports_mtp(llama_version) and model_supports_mtp:
        if "--spec-type draft-mtp" not in content_script:
            # 提取现有值或默认 3
            m = re.search(r"--spec-draft-n-max\s+(\d+)", content_script)
            dmax = str(_bounded_int(spec_draft_n_max, "spec_draft_n_max", 0, 32)) if spec_draft_n_max is not None else (m.group(1) if m else "2")
            content_script = content_script.rstrip() + " --spec-type draft-mtp --spec-draft-n-max " + dmax + "\n"
    else:
        # 切换到非 MTP：移除 spec 参数 + draft cache 参数
        content_script = re.sub(r" --spec-type draft-mtp --spec-draft-n-max \d+", "", content_script)
        content_script = re.sub(r"--spec-type draft-mtp --spec-draft-n-max \d+\n", "", content_script)
        content_script = re.sub(r" --cache-type-k-draft \S+ --cache-type-v-draft \S+", "", content_script)
        content_script = re.sub(r"--cache-type-k-draft \S+ --cache-type-v-draft \S+\\\n", "", content_script)

    # TURBO: 添加/移除环境变量
    if llama_version in ("turboquant", "turbo"):
        if "TURBO_AUTO_ASYMMETRIC" not in content_script:
            content_script = content_script.replace("#!/bin/bash\n", "#!/bin/bash\nexport TURBO_AUTO_ASYMMETRIC=0\n", 1)
        # 更新 TURBO_AUTO_ASYMMETRIC 值
        asym_val = data.get("turbo_auto_asymmetric")
        if asym_val is not None:
            content_script = re.sub(r"export TURBO_AUTO_ASYMMETRIC=\d+", f"export TURBO_AUTO_ASYMMETRIC={asym_val}", content_script)
        # 更新 cache type
        if data.get("k_cache_type"):
            content_script = re.sub(r"--cache-type-k \S+", f"--cache-type-k {data['k_cache_type']}", content_script)
        if data.get("v_cache_type"):
            content_script = re.sub(r"--cache-type-v \S+", f"--cache-type-v {data['v_cache_type']}", content_script)
    else:
        # 切换到主线/MTP：移除 TURBO_AUTO_ASYMMETRIC
        lines = content_script.split("\n")
        lines = [l for l in lines if "TURBO_AUTO_ASYMMETRIC" not in l]
        content_script = "\n".join(lines)
        # 将 turbo 缓存类型替换为 q4_0（主线/MTP 不支持 turbo 缓存）
        content_script = content_script.replace("--cache-type-k turbo2", "--cache-type-k q4_0")
        content_script = content_script.replace("--cache-type-k turbo3", "--cache-type-k q4_0")
        content_script = content_script.replace("--cache-type-k turbo4", "--cache-type-k q4_0")
        content_script = content_script.replace("--cache-type-v turbo2", "--cache-type-v q4_0")
        content_script = content_script.replace("--cache-type-v turbo3", "--cache-type-v q4_0")
        content_script = content_script.replace("--cache-type-v turbo4", "--cache-type-v q4_0")
        # Draft cache: turbo 替换为 iq4_nl
        content_script = content_script.replace("--cache-type-k-draft turbo2", "--cache-type-k-draft iq4_nl")
        content_script = content_script.replace("--cache-type-k-draft turbo3", "--cache-type-k-draft iq4_nl")
        content_script = content_script.replace("--cache-type-k-draft turbo4", "--cache-type-k-draft iq4_nl")
        content_script = content_script.replace("--cache-type-v-draft turbo2", "--cache-type-v-draft iq4_nl")
        content_script = content_script.replace("--cache-type-v-draft turbo3", "--cache-type-v-draft iq4_nl")
        content_script = content_script.replace("--cache-type-v-draft turbo4", "--cache-type-v-draft iq4_nl")

    await asyncio.to_thread(write_start_script, content_script)

    # 同步更新 persist_config.json 的 args（防止引擎切换后残留 MTP 参数）
    if os.path.isfile(START_SCRIPT):
        with open(START_SCRIPT, 'r') as _sf:
            _script = _sf.read()
        _exec_match = re.search(r'exec\s+(\S+llama-server\s+.*)', _script, re.DOTALL)
        if _exec_match:
            _new_args = _exec_match.group(1).strip()
            _new_args = _new_args.replace(chr(92) + chr(10), ' ').replace(chr(10), ' ')
            _new_args = re.sub(r'\s+', ' ', _new_args)
            if os.path.isfile(PERSIST_CONFIG):
                try:
                    with open(PERSIST_CONFIG, 'r') as _pf:
                        _pc = json.load(_pf)
                    _pc['args'] = _new_args
                    with open(PERSIST_CONFIG, 'w') as _pf:
                        json.dump(_pc, _pf, indent=2)
                    print(f'[Persist] Synced args after switch: {llama_version}')
                except Exception as _pe:
                    print(f'[Persist] Warn: sync failed: {_pe}')

    # 重启
    await asyncio.to_thread(
        _switch_and_restart,
        llama_version,
        runtime_before_switch.get("model_path", "") if runtime_before_switch else "",
    )
    _invalidate_cmdline_cache()  # 引擎切换成功，进程已变
    return {"status": "ok", "message": f"已切换到 {_get_engine_by_key(llama_version)['name'] if _get_engine_by_key(llama_version) else llama_version} 版本并重启"}




@app.get("/api/engines")
async def list_engines():
    """返回所有可用引擎列表"""
    import glob, json
    engines = await asyncio.to_thread(_scan_engines)
    result = []
    for e in engines:
        result.append({
            "key": e["key"],
            "name": e["name"],
            "binary_path": e["binary_path"],
            "version": e.get("version", ""),
            "features": e.get("features", []),
            "supports_mtp": bool(e.get("supports_mtp")),
            "supports_draft_model": bool(e.get("supports_draft_model")),
            "supports_ngram": bool(e.get("supports_ngram", True)),
            "cache_types": _engine_cache_types(e),
            "draft_cache_types": _engine_cache_types(e, draft=True),
            "spec_types": _engine_spec_types(e),
            "version_params": e.get("version_params", {}),
            "backend": e.get("backend", ""),
            "branch": e.get("branch", ""),
            "commit": e.get("commit", ""),
            "source": e.get("source", ""),
            "github_url": e.get("github_url", ""),
            "gpu_targets": e.get("gpu_targets", []),
            "driver": e.get("driver", ""),
            "parameter_schema": e.get("parameter_schema", []),
            "deployment_parameters": e.get("deployment_parameters", e.get("parameter_schema", [])),
            "parameter_notes": e.get("parameter_notes", []),
            "recommended_params": e.get("recommended_params", {}),
            "exclusive_parameters": e.get("exclusive_parameters", []),
            "parameter_differences": e.get("parameter_differences", {}),
            "parameter_file": e.get("parameter_file", ""),
            "parameter_config_version": e.get("parameter_config_version"),
            "profiles": e.get("profiles", {}),
            "load_strategy": e.get("load_strategy", {}),
            "engine_environment": e.get("engine_environment", {}),
            "type": e.get("type", "llama"),
        })
    return {"engines": result}

@app.get("/api/service/status")
async def service_status():
    """获取推理服务状态"""
    running, content = await asyncio.gather(
        asyncio.to_thread(is_llama_running),
        asyncio.to_thread(read_start_script),
    )
    mode_result = await get_persist_mode()
    config = parse_script_config(content) if content else {}
    return {
        "running": running,
        "persist_mode": mode_result["mode"],
        "config": config,
    }


# ============================================================
# 快速切换列表管理 API (服务端存储，跨端口/跨浏览器共享)
# ============================================================
PERSIST_CONFIG = "/data/inference-hub/state/persist_config.json"

FAV_RECENT_FILE = "/data/inference-hub/state/quick_switch.json"

def _load_quick_switch():
    import json as _json, os as _os
    try:
        with open(FAV_RECENT_FILE, "r") as f:
            return _json.load(f)
    except Exception:
        return {"favorites": [], "recent": []}

def _save_quick_switch(data):
    import json as _json, os as _os
    _os.makedirs(_os.path.dirname(FAV_RECENT_FILE), exist_ok=True)
    with open(FAV_RECENT_FILE, "w") as f:
        _json.dump(data, f, indent=2)

@app.get("/api/quick-switch")
async def get_quick_switch():
    return await asyncio.to_thread(_load_quick_switch)

@app.post("/api/quick-switch")
async def update_quick_switch(request: Request):
    data = await request.json()
    current = await asyncio.to_thread(_load_quick_switch)
    if "favorites" in data:
        current["favorites"] = data["favorites"]
    if "recent" in data:
        current["recent"] = data["recent"]
    await asyncio.to_thread(_save_quick_switch, current)
    return current

@app.post("/api/quick-switch/add-recent")
async def add_recent_model(request: Request):
    data = await request.json()
    model_name = data.get("name", "")
    if not model_name:
        return {"error": "missing name"}
    current = await asyncio.to_thread(_load_quick_switch)
    recent = current.get("recent", [])
    recent = [x for x in recent if x != model_name]
    recent.insert(0, model_name)
    if len(recent) > 10:
        recent = recent[:10]
    current["recent"] = recent
    await asyncio.to_thread(_save_quick_switch, current)
    return current

@app.post("/api/quick-switch/toggle-fav")
async def toggle_favorite(request: Request):
    data = await request.json()
    model_name = data.get("name", "")
    if not model_name:
        return {"error": "missing name"}
    current = await asyncio.to_thread(_load_quick_switch)
    favs = current.get("favorites", [])
    if model_name in favs:
        favs.remove(model_name)
    else:
        favs.append(model_name)
    current["favorites"] = favs
    await asyncio.to_thread(_save_quick_switch, current)
    return current


@app.get("/api/models/pid")
async def get_llama_pid():
    """返回 llama-server 的进程 PID"""
    try:
        import subprocess
        result = await asyncio.to_thread(
            subprocess.run, ["fuser", "8080/tcp"], capture_output=True, text=True, timeout=5
        )
        if result.returncode == 0 and result.stdout.strip():
            pids = result.stdout.strip().split()
            if pids:
                return {"pid": int(pids[0]), "port": 8080}
        return {"pid": None, "port": 8080, "error": "not_found"}
    except Exception as e:
        return {"pid": None, "error": str(e)}
