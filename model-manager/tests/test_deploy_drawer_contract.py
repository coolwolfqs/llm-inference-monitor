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
        self.assertIn("<summary>高级参数 <ChevronDown", self.source)
        self.assertIn("<summary>拓展配置 <ChevronDown", self.source)
        self.assertNotIn("高级参数（共性）", self.source)
        self.assertNotIn("拓展配置（引擎/模型）", self.source)


if __name__ == "__main__":
    unittest.main()
