import json
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import app


class EngineRegistryTests(unittest.TestCase):
    def setUp(self):
        app._ENGINES_CACHE["engines"] = []
        app._ENGINES_CACHE["ts"] = 0

    def tearDown(self):
        app._ENGINES_CACHE["engines"] = []
        app._ENGINES_CACHE["ts"] = 0

    def test_backend_only_version_file_is_normalized(self):
        with tempfile.TemporaryDirectory() as tmp:
            engines_dir = Path(tmp)
            version_dir = engines_dir / "rocm"
            version_dir.mkdir()
            (version_dir / "VERSION.json").write_text(
                json.dumps({
                    "backend": "rocm",
                    "version": "b10333-65-g8e7f22b67",
                    "features": ["muse-glimmer"],
                })
            )
            binary = engines_dir / "llama" / "build-rocm" / "bin" / "llama-server"
            binary.parent.mkdir(parents=True)
            binary.touch()

            with patch.object(app, "_ENGINES_DIR", str(engines_dir)):
                engines = app._scan_engines()

            self.assertEqual(len(engines), 1)
            self.assertEqual(engines[0]["key"], "rocm")
            self.assertEqual(engines[0]["name"], "rocm")
            self.assertEqual(engines[0]["type"], "llama")
            self.assertEqual(engines[0]["binary_path"], str(binary))
            self.assertEqual(engines[0]["version_params"], {})
            self.assertFalse(engines[0]["supports_mtp"])

    def test_explicit_legacy_fields_remain_authoritative(self):
        record = app._normalize_engine_record(
            {
                "key": "legacy",
                "name": "Legacy engine",
                "binary_path": "/opt/legacy/llama-server",
                "type": "llama",
            },
            "/data/engines/legacy/VERSION.json",
        )
        self.assertEqual(record["key"], "legacy")
        self.assertEqual(record["name"], "Legacy engine")
        self.assertEqual(record["binary_path"], "/opt/legacy/llama-server")

    def test_binary_help_advertises_mtp_when_registry_omits_it(self):
        with tempfile.TemporaryDirectory() as tmp:
            engines_dir = Path(tmp)
            version_dir = engines_dir / "vulkan"
            version_dir.mkdir()
            (version_dir / "VERSION.json").write_text(
                json.dumps({"backend": "vulkan", "features": []})
            )
            binary = engines_dir / "llama" / "build-vulkan" / "bin" / "llama-server"
            binary.parent.mkdir(parents=True)
            binary.write_text("#!/bin/sh\nprintf '%s\\n' '--spec-type none,draft-mtp'\n")
            binary.chmod(0o755)

            with patch.object(app, "_ENGINES_DIR", str(engines_dir)):
                engines = app._scan_engines()

            self.assertTrue(engines[0]["supports_mtp"])
            self.assertIn("MTP", engines[0]["features"])
            self.assertEqual(engines[0]["version_params"]["spec_draft_n_max"], 3)

    def test_binary_help_is_authoritative_for_cache_and_draft_capabilities(self):
        with tempfile.TemporaryDirectory() as tmp:
            engines_dir = Path(tmp)
            version_dir = engines_dir / "vulkan"
            version_dir.mkdir()
            (version_dir / "VERSION.json").write_text(
                json.dumps({"backend": "vulkan", "features": []})
            )
            binary = engines_dir / "llama" / "build-vulkan" / "bin" / "llama-server"
            binary.parent.mkdir(parents=True)
            binary.write_text(
                "#!/bin/sh\n"
                "printf '%s\\n' '--cache-type-k TYPE allowed values: f32, q8_0, q5_1'\n"
                "printf '%s\\n' '--cache-type-k-draft TYPE allowed values: f16, q8_0'\n"
                "printf '%s\\n' '--model-draft FNAME'\n"
                "printf '%s\\n' '--spec-type none,draft-mtp,ngram-mod'\n"
            )
            binary.chmod(0o755)

            with patch.object(app, "_ENGINES_DIR", str(engines_dir)):
                engines = app._scan_engines()

            self.assertEqual(engines[0]["cache_types"], ["f32", "q8_0", "q5_1"])
            self.assertEqual(engines[0]["draft_cache_types"], ["f16", "q8_0"])
            self.assertTrue(engines[0]["supports_draft_model"])
            self.assertEqual(engines[0]["spec_types"], ["none", "draft-mtp", "ngram-mod"])

    def test_branch_parameter_metadata_is_preserved_and_allowlisted(self):
        record = app._normalize_engine_record(
            {
                "key": "strix-halo-vulkan",
                "name": "Strix Halo Vulkan",
                "binary_path": "/data/engines/strix-halo-vulkan/bin/llama-server",
                "parameter_notes": ["no exclusive flags"],
                "recommended_params": {"device": "Vulkan0", "batch": 512},
                "parameter_schema": [
                    {"key": "device", "type": "select", "values": ["Vulkan0"]},
                    {"key": "spec_draft_p_min", "type": "number", "min": 0, "max": 1},
                ],
            },
            "/data/engines/strix-halo-vulkan/VERSION.json",
        )
        self.assertEqual(record["parameter_notes"], ["no exclusive flags"])
        self.assertEqual(record["recommended_params"]["device"], "Vulkan0")
        self.assertTrue(app._engine_supports_parameter(record, "device"))
        self.assertTrue(app._engine_supports_parameter(record, "spec_draft_p_min"))
        self.assertFalse(app._engine_supports_parameter(record, "kv_unified"))

    def test_common_deployment_schema_uses_help_probe_and_recommendations(self):
        with tempfile.TemporaryDirectory() as tmp:
            engines_dir = Path(tmp)
            version_dir = engines_dir / "vulkan"
            version_dir.mkdir()
            (version_dir / "VERSION.json").write_text(json.dumps({
                "backend": "vulkan",
                "recommended_params": {"batch": 512},
            }))
            binary = engines_dir / "llama" / "build-vulkan" / "bin" / "llama-server"
            binary.parent.mkdir(parents=True)
            binary.write_text(
                "#!/bin/sh\n"
                "if [ \"$1\" = \"--list-devices\" ]; then printf '%s\\n' '  Vulkan0: AMD'; exit 0; fi\n"
                "printf '%s\\n' '--ctx-size --batch-size --ubatch-size -np -t --flash-attn --device --fit --cache-type-k --cache-type-v --cache-type-k-draft --cache-type-v-draft --kv-unified --cache-reuse --spec-type none,draft-mtp --spec-draft-n-max --spec-draft-p-min --model-draft'\n"
            )
            binary.chmod(0o755)
            with patch.object(app, "_ENGINES_DIR", str(engines_dir)):
                engines = app._scan_engines()
            record = engines[0]
            schema = record["deployment_parameters"]
            by_key = {item["key"]: item for item in schema}
            self.assertEqual(len(schema), len(app._COMMON_ENGINE_PARAMETER_DEFINITIONS))
            self.assertTrue(by_key["kv_unified"]["supported"])
            self.assertEqual(by_key["device"]["values"], ["Vulkan0"])
            self.assertEqual(by_key["batch"]["recommended"], 512)
            self.assertEqual(record["recommended_params"]["device"], "Vulkan0")

    def test_engine_parameter_file_extends_common_catalog_and_profile(self):
        with tempfile.TemporaryDirectory() as tmp:
            engines_dir = Path(tmp)
            common_dir = engines_dir / "common"
            engine_dir = engines_dir / "strix"
            common_dir.mkdir()
            engine_dir.mkdir()
            (common_dir / "deployment-parameters.json").write_text(json.dumps({
                "schema_version": 2,
                "parameters": [
                    {"key": "load_mode", "label": "加载策略", "type": "select", "flag": "--load-mode", "values": ["mmap", "mlock"], "default": "mmap", "group": "加载"},
                ],
            }))
            (engine_dir / "deployment-parameters.json").write_text(json.dumps({
                "schema_version": 2,
                "extends": "../common/deployment-parameters.json",
                "profiles": {"default": {"label": "Strix 默认", "parameters": {"load_mode": "mlock"}}},
                "parameters": [
                    {"key": "experimental", "label": "实验开关", "type": "boolean", "env": "GGML_TEST_EXPERIMENT", "supported": True, "common": False, "group": "引擎专属"},
                ],
            }))
            (engine_dir / "VERSION.json").write_text(json.dumps({
                "key": "strix",
                "backend": "vulkan",
                "binary_path": str(engines_dir / "bin" / "llama-server"),
            }))
            binary = engines_dir / "bin" / "llama-server"
            binary.parent.mkdir()
            binary.write_text("#!/bin/sh\nprintf '%s\\n' '--help --load-mode --device Vulkan0'\n")
            binary.chmod(0o755)

            with patch.object(app, "_ENGINES_DIR", str(engines_dir)):
                engines = app._scan_engines()

            record = engines[0]
            by_key = {item["key"]: item for item in record["deployment_parameters"]}
            self.assertEqual(record["parameter_config_version"], 2)
            self.assertEqual(record["profiles"]["default"]["parameters"]["load_mode"], "mlock")
            self.assertEqual(record["recommended_params"]["load_mode"], "mlock")
            self.assertEqual(by_key["load_mode"]["values"], ["mmap", "mlock"])
            self.assertTrue(by_key["experimental"]["supported"])
            self.assertIn("experimental", record["exclusive_parameters"])

    def test_generic_parameter_flags_render_argv_and_engine_environment(self):
        engine = {"deployment_parameters": [
            {"key": "load_mode", "type": "select", "flag": "--load-mode", "managed": False, "supported": True},
            {"key": "experimental", "type": "boolean", "env": "GGML_TEST_EXPERIMENT", "supported": True, "managed": False},
        ]}
        args, exports = app._generic_parameter_flags(engine, {"load_mode": "mlock", "experimental": True})
        self.assertEqual(args, ["--load-mode", "mlock"])
        self.assertEqual(exports, ["export GGML_TEST_EXPERIMENT=1"])


if __name__ == "__main__":
    unittest.main()
