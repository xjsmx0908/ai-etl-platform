import importlib.util
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


class ReindexTest(unittest.TestCase):
    def _load(self):
        module_path = Path(__file__).resolve().parents[1] / "reindex.py"
        spec = importlib.util.spec_from_file_location("reindex_mod", module_path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module

    def test_parse_object_key(self):
        module = self._load()
        tenant, doc_id = module.parse_object_key("tenant-a/doc-123.pdf")
        self.assertEqual(tenant, "tenant-a")
        self.assertEqual(doc_id, "doc-123")

    def test_parse_object_key_no_tenant(self):
        module = self._load()
        tenant, doc_id = module.parse_object_key("doc-1.txt")
        self.assertEqual(tenant, "")
        self.assertEqual(doc_id, "doc-1")


if __name__ == "__main__":
    unittest.main()
