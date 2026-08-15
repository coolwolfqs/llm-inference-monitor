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


if __name__ == "__main__":
    unittest.main()
