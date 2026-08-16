import unittest

from mm_core.deployment import (
    DeploymentPlanError,
    match_model_engine,
    resolve_deployment_plan,
)


def _engine(**overrides):
    engine = {
        "key": "rocm",
        "type": "llama",
        "version": "test",
        "supports_mtp": True,
        "supports_draft_model": True,
        "supports_ngram": True,
        "spec_types": ["none", "draft-mtp", "draft-simple", "ngram-mod"],
        "deployment_parameters": [
            {"key": "ctx_size", "type": "integer", "default": 131072},
            {"key": "concurrency", "type": "integer", "default": 1},
            {"key": "k_cache_type", "type": "select", "default": "q8_0"},
            {"key": "v_cache_type", "type": "select", "default": "q8_0"},
            {"key": "draft_k_cache_type", "type": "select", "default": "q8_0"},
            {"key": "draft_v_cache_type", "type": "select", "default": "q8_0"},
            {"key": "spec_type", "type": "select", "default": "none"},
            {"key": "mmproj", "type": "boolean", "default": False},
            {"key": "draft_model", "type": "model", "default": ""},
        ],
        "profiles": {"default": {"label": "默认", "parameters": {"concurrency": 2}}},
    }
    engine.update(overrides)
    return engine


def _model(**overrides):
    model = {
        "id": "qwen.gguf",
        "format": "GGUF",
        "role": "model",
        "deployable": True,
        "supported_engines": ["llama"],
        "classification": {"context_length": 524288, "capabilities": ["MTP"]},
    }
    model.update(overrides)
    return model


class DeploymentResolverTests(unittest.TestCase):
    def test_server_plan_combines_model_and_engine_defaults(self):
        plan = resolve_deployment_plan(
            _model(),
            _engine(),
            projectors=[],
            draft_models=[],
        )
        self.assertEqual(plan["profile_id"], "default")
        self.assertEqual(plan["parameters"]["concurrency"], 2)
        self.assertEqual(plan["parameters"]["k_cache_type"], "q8_0")
        self.assertEqual(plan["parameters"]["v_cache_type"], "q8_0")
        self.assertEqual(plan["parameters"]["spec_type"], "draft-mtp")
        self.assertEqual(plan["limits"]["ctx_size_max"], 524288)

    def test_capability_matching_is_case_insensitive(self):
        plan = resolve_deployment_plan(
            _model(classification={"context_length": 131072, "capabilities": ["mtp"]}),
            _engine(),
        )
        self.assertEqual(plan["parameters"]["spec_type"], "draft-mtp")

    def test_vision_projector_is_selected_only_from_same_bundle(self):
        model = _model(classification={"context_length": 262144, "capabilities": ["Vision"]}, relative_dir="bundle")
        plan = resolve_deployment_plan(
            model,
            _engine(),
            projectors=[{"id": "mmproj.gguf", "relative_dir": "bundle"}],
        )
        self.assertEqual(plan["parameters"]["mmproj_file"], "mmproj.gguf")
        self.assertTrue(plan["parameters"]["mmproj"])

    def test_missing_projector_blocks_vision_model(self):
        result = match_model_engine(
            _model(classification={"capabilities": ["Vision"]}),
            _engine(),
            projectors=[],
        )
        self.assertFalse(result.compatible)
        self.assertIn("视觉投影", "；".join(result.reasons))

    def test_model_without_mtp_is_not_auto_enabled(self):
        plan = resolve_deployment_plan(_model(classification={"capabilities": []}), _engine())
        self.assertEqual(plan["parameters"]["spec_type"], "none")

    def test_text_model_does_not_auto_load_a_bundle_projector(self):
        plan = resolve_deployment_plan(
            _model(classification={"capabilities": []}, relative_dir="bundle"),
            _engine(),
            projectors=[{"id": "mmproj.gguf", "relative_dir": "bundle"}],
        )
        self.assertFalse(plan["parameters"].get("mmproj", False))
        self.assertNotIn("mmproj_file", plan["parameters"])

    def test_explicit_context_above_model_limit_is_rejected(self):
        with self.assertRaises(DeploymentPlanError) as raised:
            resolve_deployment_plan(_model(), _engine(), overrides={"ctx_size": 1048576})
        self.assertEqual(raised.exception.code, "context_exceeds_limit")

    def test_explicit_unsupported_spec_type_is_rejected(self):
        with self.assertRaises(DeploymentPlanError) as raised:
            resolve_deployment_plan(
                _model(),
                _engine(spec_types=["none"]),
                overrides={"spec_type": "draft-mtp"},
            )
        self.assertEqual(raised.exception.code, "spec_type_not_supported")

    def test_explicit_engine_parameter_must_be_supported(self):
        with self.assertRaises(DeploymentPlanError) as raised:
            resolve_deployment_plan(
                _model(),
                _engine(deployment_parameters=[{"key": "experimental", "supported": False}]),
                overrides={"experimental": True},
            )
        self.assertEqual(raised.exception.code, "unsupported_parameter")

    def test_external_draft_must_be_a_catalog_artifact(self):
        with self.assertRaises(DeploymentPlanError) as raised:
            resolve_deployment_plan(
                _model(),
                _engine(),
                overrides={"draft_model_id": "other.gguf"},
                draft_models=[{"id": "draft.gguf", "role": "draft"}],
            )
        self.assertEqual(raised.exception.code, "invalid_draft_model")


if __name__ == "__main__":
    unittest.main()
