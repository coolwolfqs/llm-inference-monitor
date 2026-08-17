"""Model/engine matching and scheduler-plan resolution.

The catalog describes what a model is, the engine registry describes what a
binary can do, and this module is the single boundary that turns both into a
deployable scheduler plan.  The UI may present and override the plan, but it
must not reimplement these compatibility rules.
"""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path
from typing import Any, Iterable


@dataclass(frozen=True)
class MatchResult:
    compatible: bool
    reasons: tuple[str, ...] = ()
    warnings: tuple[str, ...] = ()
    capabilities: tuple[str, ...] = ()

    def as_dict(self) -> dict[str, Any]:
        return {
            "compatible": self.compatible,
            "reasons": list(self.reasons),
            "warnings": list(self.warnings),
            "capabilities": list(self.capabilities),
        }


class DeploymentPlanError(ValueError):
    """A deterministic, user-actionable deployment-plan validation failure."""

    def __init__(self, message: str, *, code: str = "invalid_plan") -> None:
        super().__init__(message)
        self.code = code


def _text_set(values: Any) -> set[str]:
    if isinstance(values, str):
        values = [values]
    if not isinstance(values, (list, tuple, set)):
        return set()
    return {str(value).strip().lower() for value in values if str(value).strip()}


def _canonical_artifact_id(reference: Any, artifacts: Iterable[dict[str, Any]]) -> str:
    """Resolve a UI/legacy artifact reference to the current bundle's ID.

    The API historically accepted a mixture of catalog IDs, file names and
    absolute paths. Deployment planning must normalize those representations
    before validating bundle membership; otherwise a stale path is reported as
    a cross-model projector even when it points to the selected model's own
    artifact.
    """
    wanted = str(reference or "").strip()
    if not wanted:
        return ""
    matches: list[dict[str, Any]] = []
    wanted_name = Path(wanted).name
    for artifact in artifacts:
        if not isinstance(artifact, dict):
            continue
        references = {
            str(artifact.get(key) or "").strip()
            for key in ("id", "relative_path", "name", "path")
            if str(artifact.get(key) or "").strip()
        }
        artifact_name = Path(str(artifact.get("name") or artifact.get("path") or "")).name
        if wanted in references or (wanted_name and wanted_name == artifact_name):
            matches.append(artifact)
    if len(matches) != 1:
        return ""
    artifact = matches[0]
    return str(artifact.get("id") or artifact.get("relative_path") or artifact.get("name") or "").strip()


def _capability_set(model: dict[str, Any]) -> set[str]:
    classification = model.get("classification") if isinstance(model.get("classification"), dict) else {}
    values = classification.get("capabilities") or model.get("capabilities") or []
    return {str(value).strip().lower() for value in values if str(value).strip()}


def model_requirements(
    model: dict[str, Any],
    *,
    projectors: Iterable[dict[str, Any]] = (),
    draft_models: Iterable[dict[str, Any]] = (),
) -> dict[str, Any]:
    """Normalize catalog evidence into stable matching requirements."""

    classification = model.get("classification") if isinstance(model.get("classification"), dict) else {}
    capabilities = _capability_set(model)
    architecture = str(classification.get("architecture") or model.get("architecture") or "").strip().lower()
    architecture_type = str(classification.get("architecture_type") or model.get("architecture_type") or "").strip().lower()
    context_length = classification.get("context_length") or model.get("ctx_default")
    try:
        context_length = int(context_length) if context_length else None
    except (TypeError, ValueError):
        context_length = None
    projectors = list(projectors)
    draft_models = list(draft_models)
    return {
        "model_id": str(model.get("id") or model.get("relative_path") or ""),
        "format": str(model.get("format") or "").strip().lower(),
        "role": str(model.get("role") or classification.get("role") or "model").strip().lower(),
        "architecture": architecture,
        "architecture_type": architecture_type,
        "quantization": str(classification.get("quantization") or model.get("quant_type") or "").strip().lower(),
        "context_length": context_length,
        "capabilities": sorted(capabilities),
        "vision": "vision" in capabilities,
        "mtp": "mtp" in capabilities,
        "reasoning": "reasoning" in capabilities,
        "projector_count": len(projectors),
        "draft_model_count": len(draft_models),
        "supported_engines": sorted(_text_set(model.get("supported_engines"))),
    }


def engine_capabilities(engine: dict[str, Any]) -> dict[str, Any]:
    """Normalize registry + binary probe facts for matching and UI use."""

    match = engine.get("match") if isinstance(engine.get("match"), dict) else {}
    capabilities = engine.get("capabilities") if isinstance(engine.get("capabilities"), dict) else {}
    schema = engine.get("deployment_parameters") or engine.get("parameter_schema") or []
    parameter_keys = {
        str(item.get("key"))
        for item in schema
        if isinstance(item, dict) and str(item.get("key") or "").strip() and item.get("supported", True) is not False
    }
    spec_types = _text_set(engine.get("spec_types") or capabilities.get("spec_types"))
    return {
        "engine_key": str(engine.get("key") or ""),
        "type": str(engine.get("type") or engine.get("runtime_type") or "llama").strip().lower(),
        "formats": _text_set(match.get("formats") or capabilities.get("formats") or ["gguf"]),
        "architectures": _text_set(match.get("architectures") or capabilities.get("architectures")),
        "architecture_types": _text_set(match.get("architecture_types") or capabilities.get("architecture_types")),
        "quantizations": _text_set(match.get("quantizations") or capabilities.get("quantizations")),
        "required_model_capabilities": _text_set(match.get("required_capabilities")),
        "excluded_model_capabilities": _text_set(match.get("excluded_capabilities")),
        "max_context": _positive_int(match.get("max_context") or capabilities.get("max_context")),
        "vision": bool(engine.get("supports_mmproj", capabilities.get("vision", "mmproj" in parameter_keys)) or "mmproj" in parameter_keys),
        "mtp": bool(engine.get("supports_mtp", capabilities.get("mtp", "draft-mtp" in spec_types))),
        "draft_model": bool(engine.get("supports_draft_model", capabilities.get("draft_model", "draft_model" in parameter_keys))),
        "ngram": bool(engine.get("supports_ngram", capabilities.get("ngram", any(value.startswith("ngram-") for value in spec_types)))),
        "spec_types": sorted(spec_types),
        "parameter_keys": parameter_keys,
    }


def _positive_int(value: Any) -> int | None:
    try:
        parsed = int(value)
    except (TypeError, ValueError):
        return None
    return parsed if parsed > 0 else None


def match_model_engine(
    model: dict[str, Any],
    engine: dict[str, Any],
    *,
    projectors: Iterable[dict[str, Any]] = (),
    draft_models: Iterable[dict[str, Any]] = (),
) -> MatchResult:
    """Return compatibility without making assumptions in the frontend."""

    requirements = model_requirements(model, projectors=projectors, draft_models=draft_models)
    capabilities = engine_capabilities(engine)
    reasons: list[str] = []
    warnings: list[str] = []
    model_caps = set(requirements["capabilities"])

    if requirements["role"] != "model" or not model.get("deployable", True):
        reasons.append("当前文件不是可独立部署的主模型")
    if requirements["format"] not in capabilities["formats"]:
        reasons.append(f"模型格式 {requirements['format'] or '未知'} 不在引擎支持范围")
    supported_engines = set(requirements["supported_engines"])
    if supported_engines and capabilities["type"] not in supported_engines and capabilities["engine_key"] not in supported_engines:
        reasons.append(f"模型登记的引擎类型不包含 {capabilities['type']}")
    if capabilities["architectures"] and requirements["architecture"] not in capabilities["architectures"]:
        reasons.append(f"模型架构 {requirements['architecture'] or '未知'} 不匹配该引擎")
    if capabilities["architecture_types"] and requirements["architecture_type"] not in capabilities["architecture_types"]:
        reasons.append(f"模型类型 {requirements['architecture_type'] or '未知'} 不匹配该引擎")
    if capabilities["quantizations"] and requirements["quantization"] not in capabilities["quantizations"]:
        reasons.append(f"量化类型 {requirements['quantization'] or '未知'} 不匹配该引擎")
    missing = capabilities["required_model_capabilities"] - model_caps
    if missing:
        reasons.append(f"模型缺少引擎要求能力: {', '.join(sorted(missing))}")
    excluded = capabilities["excluded_model_capabilities"] & model_caps
    if excluded:
        reasons.append(f"引擎不支持模型能力: {', '.join(sorted(excluded))}")
    if requirements["context_length"] and capabilities["max_context"] and requirements["context_length"] > capabilities["max_context"]:
        reasons.append(f"模型上下文 {requirements['context_length']} 超过引擎上限 {capabilities['max_context']}")

    if requirements["vision"] and not capabilities["vision"]:
        warnings.append("该引擎不能加载视觉投影组件，部署后仅保留文本能力")
    if requirements["vision"] and requirements["projector_count"] == 0:
        reasons.append("模型声明视觉能力，但模型目录没有匹配的视觉投影组件")
    if requirements["mtp"] and not capabilities["mtp"]:
        warnings.append("模型包含 MTP 能力，但该引擎只能关闭 MTP 后部署")
    if requirements["draft_model_count"] and not capabilities["draft_model"]:
        warnings.append("模型目录存在草稿模型，但该引擎不支持外置草稿模型")

    return MatchResult(
        compatible=not reasons,
        reasons=tuple(dict.fromkeys(reasons)),
        warnings=tuple(dict.fromkeys(warnings)),
        capabilities=tuple(sorted({
            "vision" if capabilities["vision"] else "",
            "mtp" if capabilities["mtp"] and requirements["mtp"] else "",
            "draft_model" if capabilities["draft_model"] and requirements["draft_model_count"] else "",
            "ngram" if capabilities["ngram"] else "",
        } - {""})),
    )


def _profile_values(profile: dict[str, Any] | None) -> dict[str, Any]:
    if not isinstance(profile, dict):
        return {}
    values = profile.get("parameters") or profile.get("values")
    return dict(values) if isinstance(values, dict) else {}


def _parameter_defaults(engine: dict[str, Any], model: dict[str, Any]) -> dict[str, Any]:
    classification = model.get("classification") if isinstance(model.get("classification"), dict) else {}
    model_context = classification.get("context_length") or model.get("ctx_default")
    defaults: dict[str, Any] = {}
    if model_context:
        defaults["ctx_size"] = model_context
    recommended = engine.get("recommended_params") if isinstance(engine.get("recommended_params"), dict) else {}
    defaults.update(recommended)
    for item in engine.get("deployment_parameters") or engine.get("parameter_schema") or []:
        if not isinstance(item, dict):
            continue
        if item.get("supported") is False:
            continue
        key = str(item.get("key") or "").strip()
        if key and key not in defaults and item.get("default") is not None:
            defaults[key] = item.get("default")
    return defaults


def resolve_deployment_plan(
    model: dict[str, Any],
    engine: dict[str, Any],
    *,
    profile_id: str | None = None,
    overrides: dict[str, Any] | None = None,
    projectors: Iterable[dict[str, Any]] = (),
    draft_models: Iterable[dict[str, Any]] = (),
) -> dict[str, Any]:
    """Resolve profile/defaults/overrides and return the canonical plan."""

    projectors = list(projectors)
    draft_models = list(draft_models)
    match = match_model_engine(model, engine, projectors=projectors, draft_models=draft_models)
    if not match.compatible:
        raise DeploymentPlanError("；".join(match.reasons), code="engine_model_mismatch")
    profiles = engine.get("profiles") if isinstance(engine.get("profiles"), dict) else {}
    selected_profile = str(profile_id or "default").strip() or "default"
    if profiles and selected_profile not in profiles:
        raise DeploymentPlanError(f"引擎 profile 不存在: {selected_profile}", code="unknown_profile")
    profile = profiles.get(selected_profile) if isinstance(profiles, dict) else {}
    profile_values = _profile_values(profile)
    values = _parameter_defaults(engine, model)
    values.update(profile_values)
    overrides = dict(overrides or {})
    values.update({str(key): value for key, value in overrides.items() if str(key).strip()})

    requirements = model_requirements(model, projectors=projectors, draft_models=draft_models)
    capabilities = engine_capabilities(engine)
    if requirements["mtp"] and capabilities["mtp"] and "spec_type" not in overrides:
        values["spec_type"] = "draft-mtp"
    elif "spec_type" not in overrides:
        values.setdefault("spec_type", "none")
    explicit_projector = str(overrides.get("mmproj_file") or "").strip()
    explicit_visual_disable = "mmproj" in overrides and not bool(overrides.get("mmproj"))
    if explicit_projector:
        canonical_projector = _canonical_artifact_id(explicit_projector, projectors)
        if canonical_projector:
            values["mmproj_file"] = canonical_projector
    if (
        projectors
        and capabilities["vision"]
        and not explicit_projector
        and not explicit_visual_disable
    ):
        values["mmproj_file"] = str(projectors[0].get("id") or projectors[0].get("relative_path") or "")
        values["mmproj"] = bool(values["mmproj_file"])

    context_limit = capabilities["max_context"] or 1048576
    if requirements["context_length"]:
        # llama-server's ctx-size is the total scheduler budget.  A model
        # advertised as 256K can therefore be tried at 512K with two slots;
        # each sequence still stays within the model's native 256K window.
        try:
            requested_concurrency = max(1, int(values.get("concurrency") or 1))
        except (TypeError, ValueError):
            raise DeploymentPlanError("并发数必须是整数", code="invalid_concurrency")
        context_limit = min(context_limit, requirements["context_length"] * requested_concurrency)
        # A profile that did not explicitly choose a context should follow
        # the scheduler capacity of its concurrency instead of remaining at
        # the single-slot model limit.
        if "ctx_size" not in overrides and "ctx_size" not in profile_values:
            values["ctx_size"] = context_limit
    try:
        requested_context = int(values.get("ctx_size") or context_limit)
    except (TypeError, ValueError):
        raise DeploymentPlanError("上下文大小必须是整数", code="invalid_context")
    if "ctx_size" in overrides and requested_context > context_limit:
        raise DeploymentPlanError(
            f"上下文大小 {requested_context} 超过模型/引擎上限 {context_limit}",
            code="context_exceeds_limit",
        )
    values["ctx_size"] = min(requested_context, context_limit)
    if values.get("spec_type") == "draft-mtp" and (not requirements["mtp"] or not capabilities["mtp"]):
        raise DeploymentPlanError("当前模型或引擎不支持 MTP 推测解码", code="mtp_not_supported")
    if values.get("mmproj") and not capabilities["vision"]:
        raise DeploymentPlanError("当前引擎不支持视觉投影组件", code="vision_not_supported")
    if values.get("mmproj_file"):
        projector_ids = {
            str(item.get("id") or item.get("relative_path") or item.get("name") or "")
            for item in projectors
            if isinstance(item, dict)
        }
        if str(values["mmproj_file"]) not in projector_ids:
            raise DeploymentPlanError("视觉投影组件必须来自当前模型目录", code="invalid_projector")
    if overrides.get("draft_model_id"):
        draft_ids = {
            str(item.get("id") or item.get("relative_path") or item.get("name") or "")
            for item in draft_models
            if isinstance(item, dict)
        }
        if not capabilities["draft_model"]:
            raise DeploymentPlanError("当前引擎不支持外置草稿模型", code="draft_not_supported")
        if str(overrides["draft_model_id"]) not in draft_ids:
            raise DeploymentPlanError("草稿模型必须来自当前模型包或模型族", code="invalid_draft_model")

    requested_spec_types = {
        part.strip().lower()
        for part in str(values.get("spec_type") or "none").split(",")
        if part.strip()
    }
    unsupported_spec_types = requested_spec_types - set(capabilities["spec_types"])
    if unsupported_spec_types:
        raise DeploymentPlanError(
            f"所选引擎不支持推测解码类型: {', '.join(sorted(unsupported_spec_types))}",
            code="spec_type_not_supported",
        )
    if "draft-mtp" in requested_spec_types and (not requirements["mtp"] or not capabilities["mtp"]):
        raise DeploymentPlanError("当前模型或引擎不支持 MTP 推测解码", code="mtp_not_supported")
    if any(item.startswith("ngram-") for item in requested_spec_types) and not capabilities["ngram"]:
        raise DeploymentPlanError("当前引擎不支持 n-gram 推测解码", code="ngram_not_supported")

    parameter_map = {
        str(item.get("key")): item
        for item in (engine.get("deployment_parameters") or engine.get("parameter_schema") or [])
        if isinstance(item, dict) and str(item.get("key") or "").strip()
    }
    unsupported = sorted(
        key for key in overrides
        if key in parameter_map and parameter_map[key].get("supported") is False
    )
    if unsupported:
        raise DeploymentPlanError(
            f"所选引擎不支持参数: {', '.join(unsupported)}",
            code="unsupported_parameter",
        )
    unknown = sorted(key for key in overrides if key not in parameter_map and key not in {"mmproj_file", "draft_model_id"})
    if unknown:
        raise DeploymentPlanError(f"所选引擎参数未注册: {', '.join(unknown)}", code="unknown_parameter")
    return {
        "schema_version": 1,
        "model": requirements,
        "engine": {
            "key": capabilities["engine_key"],
            "type": capabilities["type"],
            "version": engine.get("version"),
            "parameter_file": engine.get("parameter_file"),
        },
        "match": match.as_dict(),
        "profile_id": selected_profile,
        "profile": profile or {},
        "parameters": values,
        "parameter_schema": engine.get("deployment_parameters") or engine.get("parameter_schema") or [],
        "limits": {"ctx_size_max": context_limit},
        "artifacts": {
            "projectors": projectors,
            "draft_models": draft_models,
        },
    }
