"""Cached model catalog with stable relative identifiers."""

from __future__ import annotations

import copy
import json
import os
import threading
import time
from pathlib import Path
from typing import Any

from .metadata import classify_model, normalize_quantization, read_gguf_metadata, read_json
from .registry import ArtifactRegistry


def format_size(size: int) -> str:
    value = float(max(0, size))
    for unit in ("B", "KB", "MB", "GB", "TB"):
        if value < 1024 or unit == "TB":
            return f"{value:.1f} {unit}"
        value /= 1024
    return f"{value:.1f} TB"


class CatalogService:
    """Build and cache a node-local inventory.

    The service is intentionally single-node and in-process. Mutating API
    routes invalidate it immediately; external file changes are discovered on
    the next short TTL refresh.
    """

    schema_version = "3.0"

    def __init__(
        self,
        models_dir: Path,
        *,
        overrides_file: Path | None = None,
        database: Path | None = None,
        ttl_seconds: float = 8.0,
    ) -> None:
        self.models_dir = models_dir.resolve()
        self.overrides_file = overrides_file
        self.registry = ArtifactRegistry(database) if database else None
        self.ttl_seconds = max(1.0, float(ttl_seconds))
        self._lock = threading.RLock()
        self._models: list[dict[str, Any]] = []
        self._by_id: dict[str, dict[str, Any]] = {}
        self._scanned_at = 0.0
        self._generation = 0
        self._scan_duration_ms = 0.0
        self._gguf_metadata_cache: dict[str, tuple[tuple[int, int, int, int], dict[str, Any]]] = {}

    def invalidate(self) -> None:
        with self._lock:
            self._scanned_at = 0.0

    def _load_overrides(self) -> dict[str, dict[str, Any]]:
        if not self.overrides_file:
            return {}
        try:
            value = json.loads(self.overrides_file.read_text(encoding="utf-8"))
            if isinstance(value, dict) and isinstance(value.get("models"), dict):
                value = value["models"]
            return {
                str(key): item
                for key, item in value.items()
                if isinstance(item, dict)
            } if isinstance(value, dict) else {}
        except (OSError, ValueError, TypeError):
            return {}

    def _relative_id(self, path: Path) -> str:
        return path.resolve().relative_to(self.models_dir).as_posix()

    def _directory_size(self, directory: Path) -> int:
        total = 0
        for root, dirs, files in os.walk(directory, followlinks=False):
            dirs[:] = [name for name in dirs if not (Path(root) / name).is_symlink()]
            for name in files:
                path = Path(root) / name
                try:
                    if not path.is_symlink():
                        total += path.stat().st_size
                except OSError:
                    continue
        return total

    def _override_for(
        self,
        overrides: dict[str, dict[str, Any]],
        model_id: str,
        filename: str,
    ) -> dict[str, Any]:
        return overrides.get(model_id) or overrides.get(filename) or {}

    def _gguf_record(
        self,
        path: Path,
        overrides: dict[str, dict[str, Any]],
    ) -> dict[str, Any] | None:
        try:
            if path.is_symlink():
                return None
            resolved = path.resolve()
            relative_parts = resolved.relative_to(self.models_dir).parts
            if any(part.startswith(".") for part in relative_parts):
                return None
            stat = path.stat()
        except (OSError, ValueError):
            return None
        if stat.st_size < 100 * 1024 * 1024:
            return None
        model_id = self._relative_id(path)
        cache_key = str(path.resolve())
        fingerprint = (stat.st_dev, stat.st_ino, stat.st_size, stat.st_mtime_ns)
        cached = self._gguf_metadata_cache.get(cache_key)
        if cached and cached[0] == fingerprint:
            embedded = cached[1]
        else:
            embedded = read_gguf_metadata(path)
            self._gguf_metadata_cache[cache_key] = (fingerprint, embedded)
        classification = classify_model(
            model_id=model_id,
            filename=path.name,
            model_format="GGUF",
            metadata=embedded,
            override=self._override_for(overrides, model_id, path.name),
        )
        uid = self.registry.identify(path, model_id, classification["role"]) if self.registry else model_id
        return {
            "id": uid,
            "relative_path": model_id,
            "name": path.name,
            "display_name": path.name,
            "alias": classification["family"],
            "path": str(path),
            "relative_dir": "" if Path(model_id).parent == Path(".") else Path(model_id).parent.as_posix(),
            "size": stat.st_size,
            "size_human": format_size(stat.st_size),
            "modified": stat.st_mtime,
            "ctx_default": classification["context_length"],
            "format": "GGUF",
            "quant_type": classification["quantization"] or "",
            "tags": classification["tags"],
            "role": classification["role"],
            "category": classification["category"],
            "family": classification["family"],
            "classification": classification,
            "deployable": classification["deployable"],
            "supported_engines": classification["supported_engines"],
        }

    def _find_model_dir(self, directory: Path) -> Path | None:
        if (directory / "config.json").is_file():
            return directory
        try:
            children = [item for item in directory.iterdir() if item.is_dir()]
        except OSError:
            return None
        if len(children) == 1 and (children[0] / "config.json").is_file():
            return children[0]
        return None

    def _hf_records(
        self,
        overrides: dict[str, dict[str, Any]],
        gguf_parents: set[Path],
    ) -> list[dict[str, Any]]:
        records: list[dict[str, Any]] = []
        try:
            directories = [item for item in self.models_dir.iterdir() if item.is_dir()]
        except OSError:
            return records
        for directory in sorted(directories, key=lambda value: value.name.lower()):
            if directory.name.startswith(".") or directory.is_symlink():
                continue
            model_dir = self._find_model_dir(directory)
            if model_dir is None:
                continue
            # A GGUF bundle can contain a tokenizer config; do not count it as a
            # second Hugging Face model unless it also contains model weights.
            weight_patterns = ("*.safetensors", "*.bin")
            if directory.resolve() in gguf_parents and not any(
                list(model_dir.glob(pattern)) for pattern in weight_patterns
            ):
                continue
            config = read_json(model_dir / "config.json")
            if not config:
                continue
            size = self._directory_size(directory)
            if size < 100 * 1024 * 1024:
                continue
            model_id = self._relative_id(directory)
            classification = classify_model(
                model_id=model_id,
                filename=directory.name,
                model_format="HF",
                config=config,
                override=self._override_for(overrides, model_id, directory.name),
            )
            uid = self.registry.identify(directory, model_id, classification["role"]) if self.registry else model_id
            try:
                modified = directory.stat().st_mtime
            except OSError:
                modified = 0.0
            records.append(
                {
                    "id": uid,
                    "relative_path": model_id,
                    "name": directory.name,
                    "display_name": directory.name,
                    "alias": classification["family"],
                    "path": str(directory),
                    "relative_dir": "" if Path(model_id).parent == Path(".") else Path(model_id).parent.as_posix(),
                    "size": size,
                    "size_human": format_size(size),
                    "modified": modified,
                    "ctx_default": classification["context_length"],
                    "format": "HF",
                    "quant_type": normalize_quantization(directory.name, config),
                    "tags": classification["tags"],
                    "role": classification["role"],
                    "category": classification["category"],
                    "family": classification["family"],
                    "classification": classification,
                    "deployable": classification["deployable"],
                    "supported_engines": classification["supported_engines"],
                }
            )
        return records

    def _scan(self) -> list[dict[str, Any]]:
        overrides = self._load_overrides()
        gguf_records: list[dict[str, Any]] = []
        gguf_parents: set[Path] = set()
        if self.models_dir.exists():
            for path in self.models_dir.rglob("*.gguf"):
                record = self._gguf_record(path, overrides)
                if record:
                    gguf_records.append(record)
                    gguf_parents.add(path.parent.resolve())
        records = gguf_records + self._hf_records(overrides, gguf_parents)
        if self.registry:
            self.registry.mark_missing({str(item["id"]) for item in records})
        active_paths = {str(Path(item["path"]).resolve()) for item in gguf_records}
        self._gguf_metadata_cache = {
            key: value
            for key, value in self._gguf_metadata_cache.items()
            if key in active_paths
        }
        records.sort(key=lambda item: (-float(item["modified"]), item["id"].lower()))
        return records

    def list_models(self, *, force: bool = False) -> list[dict[str, Any]]:
        now = time.monotonic()
        with self._lock:
            if (
                not force
                and self._models
                and now - self._scanned_at < self.ttl_seconds
            ):
                return copy.deepcopy(self._models)
            started = time.perf_counter()
            models = self._scan()
            self._models = models
            self._by_id = {item["id"]: item for item in models}
            self._scanned_at = time.monotonic()
            self._scan_duration_ms = round((time.perf_counter() - started) * 1000, 2)
            self._generation += 1
            return copy.deepcopy(models)

    def find(self, model_id: str, *, force: bool = False) -> dict[str, Any] | None:
        models = self.list_models(force=force)
        exact = next((item for item in models if item["id"] == model_id), None)
        if exact:
            return exact
        path_match = next((item for item in models if item.get("relative_path") == model_id), None)
        if path_match:
            return path_match
        basename_matches = [item for item in models if item["name"] == model_id]
        return basename_matches[0] if len(basename_matches) == 1 else None

    def resolve_id(self, model_id: str) -> str | None:
        item = self.find(model_id)
        return str(item["id"]) if item else None

    def uid_for_path(self, relative_path: str) -> str | None:
        item = self.find(relative_path)
        return str(item["id"]) if item else None

    def metadata(self) -> dict[str, Any]:
        with self._lock:
            age = max(0.0, time.monotonic() - self._scanned_at) if self._scanned_at else None
            return {
                "schema_version": self.schema_version,
                "generation": self._generation,
                "scanned_at": time.time() - age if age is not None else None,
                "age_seconds": round(age, 3) if age is not None else None,
                "scan_duration_ms": self._scan_duration_ms,
                "ttl_seconds": self.ttl_seconds,
            }

    def summary(self, models: list[dict[str, Any]]) -> dict[str, Any]:
        roles: dict[str, int] = {}
        families: dict[str, int] = {}
        deployable = 0
        for item in models:
            roles[item["role"]] = roles.get(item["role"], 0) + 1
            families[item["family"]] = families.get(item["family"], 0) + 1
            deployable += int(bool(item["deployable"]))
        return {
            "total": len(models),
            "deployable": deployable,
            "components": len(models) - deployable,
            "roles": roles,
            "families": families,
            "size_bytes": sum(int(item["size"]) for item in models),
        }
