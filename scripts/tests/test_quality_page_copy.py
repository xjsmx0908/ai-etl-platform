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
        self.assertIn("Recall@5 / hit_rate ≥ 90%", text)
        self.assertIn("第一名稳不稳", text)

    def test_recall_bars_stay_aligned_when_notes_differ(self):
        text = PAGE.read_text(encoding="utf-8")
        self.assertIn("flex items-start gap-6", text)
        self.assertIn("whitespace-nowrap", text)
        self.assertIn("tabular-nums", text)
        self.assertNotIn("flex items-end gap-6", text)
        self.assertNotIn("absolute inset-x-0 top-1", text)
        self.assertNotIn("max-w-[88px]", text)


if __name__ == "__main__":
    unittest.main()
