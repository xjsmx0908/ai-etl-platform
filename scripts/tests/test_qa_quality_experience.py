import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]


class QaQualityExperienceTests(unittest.TestCase):
    def test_qa_uses_all_authorized_spaces_and_empty_state(self):
        helper = (ROOT / "web" / "lib" / "qaSpaces.ts").read_text(encoding="utf-8")
        page = (ROOT / "web" / "app" / "(app)" / "qa" / "page.tsx").read_text(encoding="utf-8")
        self.assertIn("queryableKnowledgeSpaces", helper)
        self.assertIn("preferredKnowledgeSpace", helper)
        self.assertIn("当前没有可检索的知识空间，无法问答", helper)
        self.assertIn("queryableKnowledgeSpaces", page)
        self.assertIn("EMPTY_QUERYABLE_SPACES_MESSAGE", page)
        self.assertIn('aria-label="知识空间"', page)
        self.assertNotIn("hasMultipleProductionSpaces", page)
        self.assertNotIn('items.filter((space) => space.kind === "production")', page)

    def test_quality_empty_state_explains_offline_eval(self):
        page = (ROOT / "web" / "app" / "(app)" / "quality" / "page.tsx").read_text(encoding="utf-8")
        self.assertIn("离线评测", page)
        self.assertIn("不是某一次问答的即时评分", page)
        self.assertIn("不会改变在线问答结果", page)
