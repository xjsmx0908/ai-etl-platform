import re
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


class WebUploadAcceptTests(unittest.TestCase):
    def test_all_upload_inputs_share_the_supported_office_extensions(self):
        file_types = (ROOT / "web" / "lib" / "fileTypes.ts").read_text(encoding="utf-8")
        match = re.search(r'UPLOAD_ACCEPT\s*=\s*"([^"]+)"', file_types)
        self.assertIsNotNone(match)
        accepted = set(match.group(1).split(","))
        self.assertTrue({".doc", ".docx", ".xls", ".xlsx", ".pptx"} <= accepted)

        for relative_path in (
            "web/app/(app)/data/page.tsx",
            "web/app/(app)/documents/[id]/page.tsx",
        ):
            source = (ROOT / relative_path).read_text(encoding="utf-8")
            self.assertIn("accept={UPLOAD_ACCEPT}", source)


if __name__ == "__main__":
    unittest.main()
