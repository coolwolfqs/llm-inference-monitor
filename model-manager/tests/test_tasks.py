import tempfile
import unittest
from pathlib import Path

from mm_core.tasks import DeploymentTaskStore


class DeploymentTaskStoreTests(unittest.TestCase):
    def test_task_lifecycle(self):
        with tempfile.TemporaryDirectory() as tmp:
            store = DeploymentTaskStore(Path(tmp) / "catalog.sqlite3")
            task = store.create("mdl_test", "llama")
            self.assertEqual(task["state"], "queued")
            self.assertEqual(store.active()["task_id"], task["task_id"])
            store.update(task["task_id"], state="running", phase="deploying", progress=35)
            store.update(
                task["task_id"], state="succeeded", phase="ready", progress=100,
                status_code=200, result={"ctx_size": 32768},
            )
            finished = store.get(task["task_id"])
            self.assertEqual(finished["result"]["ctx_size"], 32768)
            self.assertIsNone(store.active())

    def test_restart_marks_active_task_interrupted(self):
        with tempfile.TemporaryDirectory() as tmp:
            database = Path(tmp) / "catalog.sqlite3"
            store = DeploymentTaskStore(database)
            task = store.create("mdl_test", "llama")
            restarted = DeploymentTaskStore(database)
            item = restarted.get(task["task_id"])
            self.assertEqual(item["state"], "failed")
            self.assertEqual(item["phase"], "interrupted")


if __name__ == "__main__":
    unittest.main()
