import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PAGE = ROOT / "web" / "app" / "(app)" / "quality" / "page.tsx"


class QualityPageCopyTests(unittest.TestCase):
    def test_page_explains_offline_regression_and_not_enterprise_gold(self):
        text = PAGE.read_text(encoding="utf-8")
        self.assertIn("离线评测", text)
        self.assertIn("不是某一次问答的即时评分", text)
        self.assertIn("企业业务 Gold", text)
        self.assertIn("不能当作企业级 SLO", text)
        self.assertIn("{r.note", text)
        self.assertIn("目标文档出现在前 k 条来源", text)


if __name__ == "__main__":
    unittest.main()
