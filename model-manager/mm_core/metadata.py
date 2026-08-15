"""Deterministic model metadata and taxonomy extraction.

Evidence priority is: explicit local override > embedded GGUF/HF metadata >
filename heuristics.  Unknown values stay unknown instead of being guessed.
"""

from __future__ import annotations

import json
import re
import struct
from pathlib import Path
from typing import Any, BinaryIO


GGUF_SCALAR_FORMATS = {
    0: "<B",
    1: "<b",
    2: "<H",
    3: "<h",
    4: "<I",
    5: "<i",
    6: "<f",
    7: "<?",
    10: "<Q",
    11: "<q",
    12: "<d",
}

GGUF_FILE_TYPES = {
    0: "F32",
    1: "F16",
    2: "Q4_0",
    3: "Q4_1",
    7: "Q8_0",
    8: "Q5_0",
    9: "Q5_1",
    10: "Q2_K",
    11: "Q3_K_S",
    12: "Q3_K_M",
    13: "Q3_K_L",
    14: "Q4_K_S",
    15: "Q4_K_M",
    16: "Q5_K_S",
    17: "Q5_K_M",
    18: "Q6_K",
    19: "IQ2_XXS",
    20: "IQ2_XS",
    21: "Q2_K_S",
    22: "IQ3_XS",
    23: "IQ3_XXS",
    24: "IQ1_S",
    25: "IQ4_NL",
    26: "IQ3_S",
    27: "IQ3_M",
    28: "IQ2_S",
    29: "IQ2_M",
    30: "IQ4_XS",
    31: "IQ1_M",
    32: "BF16",
    36: "TQ1_0",
    37: "TQ2_0",
    38: "MXFP4_MOE",
    39: "NVFP4",
    40: "Q1_0",
    41: "Q2_0",
}

_QWEN36_40B_CANONICAL_27B = re.compile(
    r"Qwen\s*3[._-]?6.*?40B.*?(?:FF6core|Alpha3b|Alpha6b|Deck|Eleanor)",
    re.IGNORECASE,
)


def _read_exact(stream: BinaryIO, size: int) -> bytes:
    value = stream.read(size)
    if len(value) != size:
        raise ValueError("truncated GGUF metadata")
    return value


def _read_u64(stream: BinaryIO) -> int:
    return struct.unpack("<Q", _read_exact(stream, 8))[0]


def _read_string(stream: BinaryIO, *, limit: int = 16 * 1024 * 1024) -> str:
    size = _read_u64(stream)
    if size > limit:
        raise ValueError(f"GGUF string exceeds {limit} bytes")
    return _read_exact(stream, size).decode("utf-8", errors="replace")


def _skip_value(stream: BinaryIO, value_type: int) -> None:
    """Skip a GGUF value without allocating tokenizer-sized arrays."""

    if value_type == 8:
        size = _read_u64(stream)
        if size > 16 * 1024 * 1024:
            raise ValueError("GGUF string is unreasonably large")
        stream.seek(size, 1)
        return
    if value_type == 9:
        item_type = struct.unpack("<I", _read_exact(stream, 4))[0]
        length = _read_u64(stream)
        if length > 100_000_000:
            raise ValueError("invalid GGUF array length")
        fmt = GGUF_SCALAR_FORMATS.get(item_type)
        if fmt is not None:
            stream.seek(struct.calcsize(fmt) * length, 1)
            return
        for _ in range(length):
            _skip_value(stream, item_type)
        return
    fmt = GGUF_SCALAR_FORMATS.get(value_type)
    if fmt is None:
        raise ValueError(f"unsupported GGUF value type {value_type}")
    stream.seek(struct.calcsize(fmt), 1)


def _read_value(stream: BinaryIO, value_type: int, *, keep: bool) -> Any:
    if value_type == 8:
        value = _read_string(stream)
        return value if keep else None
    if value_type == 9:
        item_type = struct.unpack("<I", _read_exact(stream, 4))[0]
        length = _read_u64(stream)
        if length > 100_000_000:
            raise ValueError("invalid GGUF array length")
        if not keep or length > 256:
            fmt = GGUF_SCALAR_FORMATS.get(item_type)
            if fmt is not None:
                stream.seek(struct.calcsize(fmt) * length, 1)
            else:
                for _ in range(length):
                    _skip_value(stream, item_type)
            return None
        return [_read_value(stream, item_type, keep=True) for _ in range(length)]
    fmt = GGUF_SCALAR_FORMATS.get(value_type)
    if fmt is None:
        raise ValueError(f"unsupported GGUF value type {value_type}")
    value = struct.unpack(fmt, _read_exact(stream, struct.calcsize(fmt)))[0]
    return value if keep else None


def _wanted_gguf_key(key: str) -> bool:
    if key in {
        "general.architecture",
        "general.type",
        "general.name",
        "general.basename",
        "general.size_label",
        "general.finetune",
        "general.file_type",
        "general.tags",
        "general.quantization_version",
    }:
        return True
    return key.endswith(
        (
            ".context_length",
            ".block_count",
            ".embedding_length",
            ".attention.head_count",
            ".attention.head_count_kv",
            ".expert_count",
            ".expert_used_count",
            ".nextn_predict_layers",
        )
    )


def read_gguf_metadata(path: Path) -> dict[str, Any]:
    """Read only small, classification-relevant GGUF metadata values."""

    result: dict[str, Any] = {}
    try:
        with path.open("rb") as stream:
            if _read_exact(stream, 4) != b"GGUF":
                return result
            version = struct.unpack("<I", _read_exact(stream, 4))[0]
            if version not in {2, 3}:
                return result
            _read_u64(stream)  # tensor count
            metadata_count = _read_u64(stream)
            if metadata_count > 1_000_000:
                return result
            for _ in range(metadata_count):
                key = _read_string(stream, limit=64 * 1024)
                value_type = struct.unpack("<I", _read_exact(stream, 4))[0]
                keep = _wanted_gguf_key(key)
                value = _read_value(stream, value_type, keep=keep)
                if keep:
                    result[key] = value
    except (OSError, ValueError, struct.error):
        return {}
    return result


def read_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
        return value if isinstance(value, dict) else {}
    except (OSError, ValueError, TypeError):
        return {}


def normalize_quantization(name: str, config: dict[str, Any] | None = None) -> str:
    config = config or {}
    quant_cfg = config.get("quantization_config") or {}
    method = str(quant_cfg.get("quant_method") or "").strip()
    if method:
        return method.upper()

    upper = name.upper().replace("-", "_")
    patterns = (
        r"MXFP4(?:_MOE)?",
        r"NVFP4",
        r"IQ[1-4]_?(?:XXS|XS|NL|S|M)",
        r"Q[2-8]_K(?:_[SML])?",
        r"Q[2-8]_[01]",
        r"TQ[1-4](?:_[0-9]+[SM]?)?",
        r"(?:BF16|FP16|F16|FP8)",
    )
    for pattern in patterns:
        match = re.search(pattern, upper)
        if match:
            value = match.group(0)
            value = re.sub(r"^(IQ[1-4])(?=(XXS|XS|NL|S|M)$)", r"\1_", value)
            return value
    if "KQUANT" in upper or "K_QUANT" in upper:
        return "K-QUANT"
    if config:
        dtype = str(config.get("torch_dtype") or "").upper()
        if dtype:
            return dtype
        return "BF16"
    return ""


def quantization_from_metadata(metadata: dict[str, Any]) -> str:
    value = metadata.get("general.file_type")
    if isinstance(value, (int, float)):
        return GGUF_FILE_TYPES.get(int(value), f"GGUF_FTYPE_{int(value)}")
    return ""


def _first_number(text: str) -> str:
    match = re.search(r"(?<![A-Z0-9])(\d+(?:\.\d+)?)\s*B\b", text, re.I)
    return f"{match.group(1)}B" if match else ""


def _active_parameters(text: str) -> list[str]:
    values = re.findall(r"(?:^|[-_\s])A\s*(\d+(?:\.\d+)?)\s*B\b", text, re.I)
    return list(dict.fromkeys(f"{value}B" for value in values))


def _family_from_evidence(filename: str, metadata: dict[str, Any], config: dict[str, Any]) -> str:
    stem = filename[:-5] if filename.lower().endswith(".gguf") else filename
    combined = " ".join(
        str(value or "")
        for value in (
            stem,
            metadata.get("general.name"),
            metadata.get("general.basename"),
            config.get("model_type"),
        )
    )
    compact = re.sub(r"[_\s]+", "-", combined)
    if _QWEN36_40B_CANONICAL_27B.search(compact):
        return "Qwen3.6-27B"

    patterns = (
        (r"Hermes\s*3[._-]?6.*?(\d+(?:\.\d+)?)B(?:[-_]?A(\d+(?:\.\d+)?)B)?", "Hermes3.6"),
        (r"Qwen\s*3[._-]?8.*?(\d+(?:\.\d+)?)B(?:[-_]?A(\d+(?:\.\d+)?)B)?", "Qwen3.8"),
        (r"Qwen\s*3[._-]?6.*?(\d+(?:\.\d+)?)B(?:[-_]?A(\d+(?:\.\d+)?)B)?", "Qwen3.6"),
        (r"Qwen\s*3[._-]?5.*?(\d+(?:\.\d+)?)B(?:[-_]?A(\d+(?:\.\d+)?)B)?", "Qwen3.5"),
        (r"Muse[-_\s]*Glimmer(?:.*?(\d+(?:\.\d+)?)B)?", "Muse-Glimmer"),
        (r"Gemma\s*4.*?(\d+(?:\.\d+)?)B(?:[-_]?A(\d+(?:\.\d+)?)B)?", "Gemma4"),
    )
    for pattern, prefix in patterns:
        match = re.search(pattern, compact, re.I)
        if match:
            total = match.group(1) if match.lastindex and match.group(1) else ""
            active = match.group(2) if match.lastindex and match.lastindex >= 2 else ""
            suffix = f"-{total}B" if total else ""
            if active:
                suffix += f"-A{active}B"
            return prefix + suffix

    architecture = str(metadata.get("general.architecture") or config.get("model_type") or "")
    if architecture:
        return architecture.replace("_", "-")
    return "未分类"


def classify_model(
    *,
    model_id: str,
    filename: str,
    model_format: str,
    metadata: dict[str, Any] | None = None,
    config: dict[str, Any] | None = None,
    override: dict[str, Any] | None = None,
) -> dict[str, Any]:
    metadata = metadata or {}
    config = config or {}
    override = override or {}
    evidence: list[str] = []
    lower = filename.lower()
    text_config = config.get("text_config") if isinstance(config.get("text_config"), dict) else {}
    architecture = str(
        metadata.get("general.architecture")
        or text_config.get("model_type")
        or config.get("model_type")
        or ""
    )
    general_type = str(metadata.get("general.type") or "model").lower()

    # Some bundled vision projectors (notably Qwen `*-vision-f16.gguf`) do
    # not set `general.type=clip` and do not include `mmproj` in the filename;
    # their GGUF architecture is still `clip`. Treat that authoritative
    # architecture as a projection component so the deployment drawer can
    # offer an explicit projector / no-projector choice.
    architecture_lower = architecture.lower()
    if (
        "mmproj" in lower
        or general_type in {"clip", "projector"}
        or architecture_lower in {"clip", "projector"}
    ):
        role = "projection"
    elif re.search(r"(?:^|[-_])(draft|dflash|eagle)(?:[-_.]|$)", lower):
        role = "draft"
    else:
        role = "model"
    role = str(override.get("role") or role)

    family = str(override.get("family") or _family_from_evidence(filename, metadata, config))
    source = "override" if override else ("metadata" if metadata or config else "filename")
    confidence = "high" if source in {"override", "metadata"} else "medium"

    evidence_text = " ".join(
        str(value or "")
        for value in (filename, metadata.get("general.name"), metadata.get("general.size_label"))
    )
    total_parameters = str(override.get("parameters") or _first_number(evidence_text))
    if _QWEN36_40B_CANONICAL_27B.search(evidence_text):
        total_parameters = "27B"
    active_parameters = list(override.get("active_parameters") or _active_parameters(evidence_text))

    config_arch = " ".join(
        str(v)
        for v in (
            list(config.get("architectures", []) or [])
            + list(text_config.get("architectures", []) or [])
        )
    )
    expert_count = next(
        (
            int(value)
            for key, value in metadata.items()
            if key.endswith(".expert_count") and isinstance(value, (int, float))
        ),
        0,
    )
    expert_count = expert_count or int(
        text_config.get("num_experts")
        or config.get("num_experts")
        or text_config.get("num_local_experts")
        or config.get("num_local_experts")
        or 0
    )
    if role != "model":
        architecture_type = "Component"
    elif "moe" in architecture.lower() or "moe" in config_arch.lower() or expert_count > 1 or active_parameters:
        architecture_type = "MoE"
    elif any(token in architecture.lower() or token in config_arch.lower() for token in (
        "qwen", "gemma", "llama", "mistral", "muse-glimmer", "phi", "deepseek"
    )):
        architecture_type = "Dense"
    else:
        architecture_type = "Unknown"
        confidence = "low" if source == "filename" else confidence

    embedded_quantization = quantization_from_metadata(metadata)
    filename_quantization = normalize_quantization(
        filename, config if model_format == "HF" else None
    )
    quantization = str(
        override.get("quantization")
        or embedded_quantization
        or filename_quantization
    )
    context_length = next(
        (
            int(value)
            for key, value in metadata.items()
            if key.endswith(".context_length") and isinstance(value, (int, float))
        ),
        int(
            text_config.get("max_position_embeddings")
            or config.get("max_position_embeddings")
            or 0
        ),
    )

    capabilities: list[str] = []
    declared_tags = [
        str(value).lower()
        for value in (metadata.get("general.tags") or [])
    ]
    if role == "projection" or config.get("vision_config") or "vision" in architecture.lower() or "muse-glimmer" in architecture.lower() or any("image" in tag for tag in declared_tags):
        capabilities.append("Vision")
    # nextn_predict_layers is present on ordinary qwen35/qwen35moe GGUFs as an
    # architectural field; it is not proof that the packaged model is an MTP
    # variant. Only explicit MTP metadata/tags or an MTP filename may create
    # the user-facing MTP capability tag.
    explicit_mtp_metadata = any(
        key.rsplit(".", 1)[-1].lower() in {
            "mtp_num_hidden_layers",
            "mtp_layers",
            "mtp_layer_count",
        }
        and bool(value)
        for key, value in metadata.items()
    )
    explicit_mtp_tags = any(
        re.fullmatch(r"(?:mtp|multi[-_ ]?token[-_ ]?prediction)", tag)
        for tag in declared_tags
    )
    if explicit_mtp_metadata or text_config.get("mtp_num_hidden_layers") or config.get("mtp_num_hidden_layers") or explicit_mtp_tags or re.search(r"(?:^|[-_])MTP(?:[-_.]|$)", filename, re.I):
        capabilities.append("MTP")
    if re.search(r"reasoning|reasoner|opus|r1(?:[-_.]|$)", lower) or any("reason" in tag for tag in declared_tags):
        capabilities.append("Reasoning")
    if re.search(r"uncensored|uncen|abliterated|unheretic|heretic", lower):
        capabilities.append("Uncensored")
    capabilities.extend(str(value) for value in override.get("capabilities", []) if value)
    capabilities = list(dict.fromkeys(capabilities))

    tags: list[str] = []
    if architecture_type in {"MoE", "Dense"}:
        tags.append(architecture_type)
    tags.extend(capabilities)
    tags.extend(str(value) for value in override.get("tags", []) if value)
    tags = list(dict.fromkeys(tags))

    if metadata:
        evidence.append("gguf_metadata")
    elif config:
        evidence.append("hf_config")
    else:
        evidence.append("filename")
    if override:
        evidence.insert(0, "manual_override")

    supported_engines = []
    if role == "model":
        if model_format == "GGUF":
            supported_engines = ["llama"]
        elif model_format == "HF":
            supported_engines = ["vllm"]

    warnings: list[dict[str, Any]] = []
    if model_format == "EXTENSOR":
        warnings.append(
            {
                "code": "unsupported_model_format",
                "field": "format",
                "value": "EXTENSOR",
                "message": "当前 llama.cpp 未注册 EXTENSOR 专用推理引擎",
            }
        )
    if embedded_quantization and filename_quantization and embedded_quantization != filename_quantization:
        warnings.append(
            {
                "code": "filename_metadata_conflict",
                "field": "quantization",
                "filename_value": filename_quantization,
                "metadata_value": embedded_quantization,
            }
        )

    return {
        "id": model_id,
        "role": role,
        "category": {
            "model": "模型",
            "projection": "视觉组件",
            "draft": "草稿组件",
        }.get(role, "其他组件"),
        "family": family,
        "architecture": architecture or None,
        "architecture_type": architecture_type,
        "parameters": total_parameters or None,
        "active_parameters": active_parameters,
        "quantization": quantization or None,
        "context_length": context_length or None,
        "capabilities": capabilities,
        "tags": tags,
        "source": source,
        "confidence": confidence,
        "evidence": evidence,
        "supported_engines": supported_engines,
        "deployable": bool(supported_engines),
        "general_name": metadata.get("general.name") or None,
        "base_model": metadata.get("general.basename") or None,
        "warnings": warnings,
    }
