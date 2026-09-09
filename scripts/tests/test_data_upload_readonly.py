import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class DataUploadReadonlyTests(unittest.TestCase):
    def test_readonly_role_cannot_start_data_ingest_upload(self):
        helper = (ROOT / "web" / "lib" / "permissions.ts").read_text(encoding="utf-8")
        page = (ROOT / "web" / "app" / "(app)" / "data" / "page.tsx").read_text(encoding="utf-8")
        self.assertIn("canUploadDocuments", helper)
        self.assertIn('return role === "admin" || role === "user"', helper)
        self.assertIn("当前账号为只读用户，没有数据接入权限", helper)
        self.assertIn("canUploadDocuments", page)
        self.assertIn("READONLY_UPLOAD_DENIED_MESSAGE", page)
        self.assertIn("disabled={!canUpload", page)
        self.assertIn("if (!canUpload", page)
        self.assertNotIn(
            "disabled={!file || !knowledgeSpace || !governanceComplete || status === \"uploading\" || status === \"polling\"}",
            page,
        )


if __name__ == "__main__":
    unittest.main()
