import tempfile
import unittest
from pathlib import Path

from mm_core.registry import ArtifactRegistry


class ArtifactRegistryTests(unittest.TestCase):
    def test_uid_survives_rename(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            database = root / "state" / "catalog.sqlite3"
            artifact = root / "before.gguf"
            artifact.write_bytes(b"GGUF")
            registry = ArtifactRegistry(database)
            first = registry.identify(artifact, "before.gguf", "model")
            renamed = root / "after.gguf"
            artifact.rename(renamed)
            second = registry.identify(renamed, "after.gguf", "model")
            self.assertEqual(first, second)

    def test_different_inode_gets_different_uid(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            registry = ArtifactRegistry(root / "catalog.sqlite3")
            first = root / "one.gguf"
            second = root / "two.gguf"
            first.write_bytes(b"1")
            second.write_bytes(b"2")
            self.assertNotEqual(
                registry.identify(first, first.name, "model"),
                registry.identify(second, second.name, "model"),
            )


if __name__ == "__main__":
    unittest.main()
