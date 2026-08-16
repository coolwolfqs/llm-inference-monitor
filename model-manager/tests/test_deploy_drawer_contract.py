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

    def test_visible_fields_bind_to_their_own_canonical_keys(self):
        for key in (
            "ctx_size",
            "concurrency",
            "spec_draft_n_max",
            "spec_draft_p_min",
            "ngram_mod_n_match",
            "fit",
        ):
            pattern = rf'data-field="{key}"[\s\S]*?v-model(?:\.number)?="form\.{key}"'
            self.assertRegex(self.source, pattern, msg=f"字段 {key} 绑定错位")

    def test_drawer_keeps_large_context_options_and_invalidates_old_state(self):
        self.assertIn("1048576", self.source)
        self.assertIn("524288", self.source)
        self.assertIn("model-manager:deploy-config:v5", self.source)
        self.assertNotIn("1. 引擎选择", self.source)


if __name__ == "__main__":
    unittest.main()
