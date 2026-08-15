import tempfile
import unittest
from pathlib import Path

from mm_core.operations import OperationStore


class OperationStoreTests(unittest.TestCase):
    def test_operation_lifecycle(self):
        with tempfile.TemporaryDirectory() as tmp:
            store = OperationStore(Path(tmp) / "catalog.sqlite3")
            operation_id = store.start("POST", "/api/models/deploy", "127.0.0.1")
            self.assertEqual(store.list()[0]["state"], "running")
            store.finish(operation_id, 200)
            item = store.list()[0]
            self.assertEqual(item["operation_id"], operation_id)
            self.assertEqual(item["state"], "succeeded")
            self.assertGreaterEqual(item["duration_ms"], 0)

    def test_failure_text_is_bounded(self):
        with tempfile.TemporaryDirectory() as tmp:
            store = OperationStore(Path(tmp) / "catalog.sqlite3")
            operation_id = store.start("POST", "/api/models/deploy", "127.0.0.1")
            store.finish(operation_id, 500, "x" * 1000)
            item = store.list()[0]
            self.assertEqual(item["state"], "failed")
            self.assertEqual(len(item["error"]), 500)


if __name__ == "__main__":
    unittest.main()
