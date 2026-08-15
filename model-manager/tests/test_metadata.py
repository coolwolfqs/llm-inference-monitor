from __future__ import annotations

import json
import struct
import tempfile
import unittest
from pathlib import Path

from mm_core.catalog import CatalogService
from mm_core.metadata import classify_model, normalize_quantization, read_gguf_metadata


def _gguf_string(value: str) -> bytes:
    encoded = value.encode("utf-8")
    return struct.pack("<Q", len(encoded)) + encoded


def _write_minimal_gguf(path: Path, values: dict[str, object], *, pad_mb: int = 0) -> None:
    body = bytearray(b"GGUF")
    body += struct.pack("<IQQ", 3, 0, len(values))
    for key, value in values.items():
        body += _gguf_string(key)
        if isinstance(value, str):
            body += struct.pack("<I", 8) + _gguf_string(value)
        elif isinstance(value, int):
            body += struct.pack("<II", 4, value)
        elif isinstance(value, list) and all(isinstance(item, str) for item in value):
            body += struct.pack("<IIQ", 9, 8, len(value))
            for item in value:
                body += _gguf_string(item)
        else:
            raise TypeError(value)
    with path.open("wb") as stream:
        stream.write(body)
        if pad_mb:
            stream.truncate(pad_mb * 1024 * 1024)


class MetadataTests(unittest.TestCase):
    def test_gguf_metadata_beats_opaque_filename(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "unsloth-iq2xs.gguf"
            _write_minimal_gguf(
                path,
                {
                    "general.architecture": "muse-glimmer",
                    "general.type": "model",
                    "general.name": "Muse-Glimmer-30B",
                    "general.file_type": 15,
                    "muse-glimmer.context_length": 262144,
                },
            )
            metadata = read_gguf_metadata(path)
            result = classify_model(
                model_id=path.name,
                filename=path.name,
                model_format="GGUF",
                metadata=metadata,
            )
        self.assertEqual(result["family"], "Muse-Glimmer-30B")
        self.assertEqual(result["architecture_type"], "Dense")
        self.assertEqual(result["context_length"], 262144)
        self.assertIn("Vision", result["capabilities"])
        self.assertEqual(normalize_quantization(path.name), "IQ2_XS")
        self.assertEqual(result["quantization"], "Q4_K_M")
        self.assertEqual(result["warnings"][0]["code"], "filename_metadata_conflict")

    def test_qwen36_ff6core_variant_is_canonical_27b(self):
        result = classify_model(
            model_id="model.gguf",
            filename="Qwen3.6-40B-FF6core-MTP-Q6_K.gguf",
            model_format="GGUF",
            metadata={
                "general.architecture": "qwen35",
                "general.name": "Qwen3.6 40b Alpha3b Alpha6b v2",
            },
        )
        self.assertEqual(result["family"], "Qwen3.6-27B")
        self.assertEqual(result["parameters"], "27B")
        self.assertIn("MTP", result["tags"])

    def test_qwen38_nextn_metadata_does_not_imply_mtp(self):
        result = classify_model(
            model_id="model.gguf",
            filename="Qwen3.8-27B-UD-Q4_K_XL.gguf",
            model_format="GGUF",
            metadata={
                "general.architecture": "qwen35",
                "general.name": "Qwen3.8-27B",
                "qwen35.nextn_predict_layers": 1,
            },
        )
        self.assertEqual(result["family"], "Qwen3.8-27B")
        self.assertEqual(result["parameters"], "27B")
        self.assertNotIn("MTP", result["capabilities"])
        self.assertNotIn("MTP", result["tags"])

    def test_relevant_metadata_after_tokenizer_arrays_is_not_dropped(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "opaque-iq2xs.gguf"
            _write_minimal_gguf(
                path,
                {
                    "general.architecture": "qwen35",
                    "tokenizer.ggml.tokens": ["one", "two", "three"],
                    "general.file_type": 15,
                    "qwen35.context_length": 262144,
                },
            )
            metadata = read_gguf_metadata(path)
        self.assertEqual(metadata["general.file_type"], 15)
        self.assertEqual(metadata["qwen35.context_length"], 262144)

    def test_projection_is_not_deployable(self):
        result = classify_model(
            model_id="bundle/mmproj.gguf",
            filename="mmproj-kquant.gguf",
            model_format="GGUF",
            metadata={"general.type": "clip"},
        )
        self.assertEqual(result["role"], "projection")
        self.assertFalse(result["deployable"])
        self.assertEqual(result["supported_engines"], [])

    def test_clip_architecture_vision_bundle_is_projection(self):
        """Qwen vision-f16 bundles may omit general.type=clip and mmproj in the name."""
        result = classify_model(
            model_id="Qwen3.8-27B-Uncensored/Qwen3.8-27B-Uncensored-vision-f16.gguf",
            filename="Qwen3.8-27B-Uncensored-vision-f16.gguf",
            model_format="GGUF",
            metadata={
                "general.architecture": "clip",
                "general.type": "model",
                "general.name": "Qwen3.8-27B vision projector",
            },
        )
        self.assertEqual(result["role"], "projection")
        self.assertFalse(result["deployable"])
        self.assertIn("Vision", result["capabilities"])

    def test_extensor_variant_is_not_deployable_by_llama(self):
        result = classify_model(
            model_id="Qwen3.6-35B-A3B-MTP/Qwen3.6-35B-A3B-EXTENSOR-ROCmFP4-v1.extensor.gguf",
            filename="Qwen3.6-35B-A3B-EXTENSOR-ROCmFP4-v1.extensor.gguf",
            model_format="EXTENSOR",
            metadata={
                "general.architecture": "qwen35moe",
                "general.name": "Qwen3.6-35B-A3B EXTENSOR ROCmFP4 v1",
            },
        )
        self.assertEqual(result["supported_engines"], [])
        self.assertFalse(result["deployable"])
        self.assertEqual(result["warnings"][0]["code"], "unsupported_model_format")

    def test_dflash_artifact_is_a_non_deployable_draft_component(self):
        result = classify_model(
            model_id="Muse-Glimmer-30B-GGUF/dflash-kquant.gguf",
            filename="dflash-kquant.gguf",
            model_format="GGUF",
            metadata={"general.architecture": "muse-glimmer"},
        )
        self.assertEqual(result["role"], "draft")
        self.assertFalse(result["deployable"])
        self.assertEqual(result["supported_engines"], [])

    def test_current_llama_ftype_mapping(self):
        self.assertEqual(
            classify_model(
                model_id="x.gguf",
                filename="x-IQ4_NL.gguf",
                model_format="GGUF",
                metadata={"general.file_type": 25},
            )["quantization"],
            "IQ4_NL",
        )

    def test_alpha_variant_does_not_imply_moe(self):
        result = classify_model(
            model_id="qwen.gguf",
            filename="Qwen3.6-40B.gguf",
            model_format="GGUF",
            metadata={
                "general.architecture": "qwen35",
                "general.name": "Qwen3.6 40b Alpha3b Alpha6b v2",
            },
        )
        self.assertEqual(result["architecture_type"], "Dense")
        self.assertEqual(result["active_parameters"], [])

    def test_hf_directory_with_dot_keeps_full_family_name(self):
        result = classify_model(
            model_id="Qwen3.6-35B-A3B-AWQ-4bit",
            filename="Qwen3.6-35B-A3B-AWQ-4bit",
            model_format="HF",
            config={"model_type": "qwen3_5_moe"},
        )
        self.assertEqual(result["family"], "Qwen3.6-35B-A3B")


class CatalogTests(unittest.TestCase):
    def test_extensor_hyphen_suffix_is_not_deployable(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            path = root / "Qwen3.6-35B-A3B-EXTENSOR-ROCmFP4-v1-extensor.gguf"
            _write_minimal_gguf(
                path,
                {
                    "general.architecture": "qwen35moe",
                    "general.type": "model",
                    "general.name": "Qwen3.6-35B-A3B EXTENSOR ROCmFP4 v1",
                },
                pad_mb=101,
            )
            model = CatalogService(root, ttl_seconds=60).list_models()[0]
        self.assertEqual(model["format"], "EXTENSOR")
        self.assertFalse(model["deployable"])
        self.assertEqual(model["supported_engines"], [])

    def test_nested_files_receive_stable_relative_ids(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            bundle = root / "Muse-Glimmer-30B-GGUF"
            bundle.mkdir()
            main = bundle / "unsloth-iq2xs.gguf"
            projection = bundle / "mmproj-kquant.gguf"
            common = {
                "general.architecture": "muse-glimmer",
                "general.type": "model",
                "general.name": "Muse-Glimmer-30B",
            }
            _write_minimal_gguf(main, common, pad_mb=101)
            _write_minimal_gguf(
                projection,
                {"general.type": "clip", "general.name": "Muse projector"},
                pad_mb=101,
            )
            service = CatalogService(root, ttl_seconds=60)
            models = service.list_models()
        by_name = {item["name"]: item for item in models}
        self.assertEqual(
            by_name["unsloth-iq2xs.gguf"]["id"],
            "Muse-Glimmer-30B-GGUF/unsloth-iq2xs.gguf",
        )
        self.assertTrue(by_name["unsloth-iq2xs.gguf"]["deployable"])
        self.assertFalse(by_name["mmproj-kquant.gguf"]["deployable"])


if __name__ == "__main__":
    unittest.main()
