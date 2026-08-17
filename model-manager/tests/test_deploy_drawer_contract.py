import re
import unittest
from pathlib import Path


class DeployDrawerContractTests(unittest.TestCase):
    """Keep visible labels and the submitted form fields name-keyed."""

    source = (
        Path(__file__).resolve().parents[1]
        / "frontend"
        / "src"
        / "components"
        / "DeployDrawer.vue"
    ).read_text(encoding="utf-8")
    styles = (
        Path(__file__).resolve().parents[1]
        / "frontend"
        / "src"
        / "styles"
        / "main.css"
    ).read_text(encoding="utf-8")

    def test_visible_fields_bind_to_their_own_canonical_keys(self):
        for key in (
            "ctx_size",
            "concurrency",
            "spec_draft_n_max",
            "spec_draft_p_min",
            "ngram_mod_n_match",
            "fit",
            "reasoning",
        ):
            pattern = rf'data-field="{key}"[\s\S]*?v-model(?:\.number)?="form\.{key}"'
            self.assertRegex(self.source, pattern, msg=f"字段 {key} 绑定错位")

    def test_drawer_keeps_large_context_options_and_invalidates_old_state(self):
        self.assertIn("1048576", self.source)
        self.assertIn("524288", self.source)
        self.assertIn("model-manager:deploy-config:v5", self.source)
        self.assertNotIn("1. 引擎选择", self.source)

    def test_form_grid_controls_do_not_stretch_into_neighboring_rows(self):
        self.assertIn("align-content: start", self.styles)
        self.assertIn("align-items: start", self.styles)
        self.assertIn("height: 38px", self.styles)
        self.assertIn("width: 100%", self.styles)
        self.assertIn(".form-grid label > select", self.styles)

    def test_reasoning_mode_is_visible_in_basic_configuration(self):
        self.assertIn('data-field="reasoning"', self.source)
        self.assertIn('v-model="form.reasoning"', self.source)
        self.assertIn('<option value="off">关闭思考模式</option>', self.source)

    def test_advanced_sections_use_compact_titles(self):
        self.assertIn("<summary>更多通用参数 <ChevronDown", self.source)
        self.assertIn("<summary>更多模型参数 <ChevronDown", self.source)
        self.assertIn("<summary>拓展配置 <ChevronDown", self.source)
        self.assertNotIn("高级参数（共性）", self.source)
        self.assertNotIn("拓展配置（引擎/模型）", self.source)

    def test_core_parameters_are_split_by_model_and_engine_scope(self):
        self.assertIn('<h3>模型参数 <span class="section-note">模型能力与推理行为</span></h3>', self.source)
        self.assertIn('<h3>引擎参数 <span class="section-note">通用性能与资源</span></h3>', self.source)
        for key in ("ngl", "device", "batch", "ubatch", "threads", "k_cache_type", "v_cache_type"):
            self.assertIn(f'data-field="{key}"', self.source)
        self.assertIn("实际生效的引擎设备", self.source)
        self.assertNotIn('<span>GPU</span><input v-model="form.gpu"', self.source)

    def test_profile_label_covers_visible_core_overrides(self):
        for key in ("ngl", "threads", "spec_type", "reasoning", "mmproj", "temp"):
            self.assertIn(f"['{key}'", self.source)
        self.assertIn("修改上述任一核心参数后会标记为自定义调度", self.source)


if __name__ == "__main__":
    unittest.main()
