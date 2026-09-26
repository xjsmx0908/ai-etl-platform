"""契约：台账里的每一处欠账都必须被 `docs/open-items.md` 分类，且分类必须带 owner。

为什么会有这个文件
------------------
2026-09-26，用户问「还有哪些没有做的工作」，我凭回忆列了一份，把**我该做而没做完**的东西
（对账 CI 从没跑过、切块改动没跑评测轨）归进了「可做可不做」那一堆。用户反问
「你为什么会分类错呢？真的很奇怪。希望你希望不要再发生这种事情。」

根因的第二条是可修的，也是这个测试盯的东西：

    **清单是凭回忆列的，而回忆会漏。** 台账里早就写着「未端到端验证」「没跑过」「未做」——
    它们是已经写下来的欠账，机械扫描就能拿到，我却没去扫。

所以这里不测「我记不记得」，测的是「台账和清单对不对得上」：

  1. 台账里每一处欠账词，都必须在 `open-items.md` 里有归宿 ——
     要么是一条带 `owner` 的 OPEN 条目，要么被明确判为「不是欠账」并给出理由；
  2. 每条 OPEN 条目必须**钉到台账的具体一行**（锚），且锚必须够长、必须真实存在；
  3. `owner` 只能是四个取值之一，`declined` 必须带理由。

**反向验证也在这个文件里**（`test_*_is_rejected` 那一批）：往台账里塞一句新的欠账、
把 owner 改成不存在的值、把锚改短 —— 三种篡改都必须让校验失败。
没有这批断言，前三条只是「现在恰好过」，不是「以后会拦住」。
"""

import re
import shutil
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "extract-open-items.py"
LEDGER = ROOT / "docs" / "optimization-plan.md"
OPEN_ITEMS = ROOT / "docs" / "open-items.md"


def run_check(ledger: Path | None = None, open_items: Path | None = None) -> subprocess.CompletedProcess:
    cmd = [sys.executable, str(SCRIPT), "--check"]
    if ledger is not None:
        cmd += ["--ledger", str(ledger)]
    if open_items is not None:
        cmd += ["--open-items", str(open_items)]
    return subprocess.run(cmd, capture_output=True, text=True, cwd=str(ROOT))


class TestTheLedgerAndTheListAgree(unittest.TestCase):
    def test_the_real_ledger_and_the_real_list_are_in_sync(self):
        r = run_check()
        self.assertEqual(r.returncode, 0, "校验没通过：\n" + r.stderr)

    def test_the_check_reports_a_count_for_each_owner(self):
        """四个计数必须打出来 —— `agent` 那个数就是被「可做可不做」掩盖掉的量。"""
        r = run_check()
        self.assertEqual(r.returncode, 0, r.stderr)
        for owner in ("agent", "user", "external", "declined"):
            self.assertRegex(r.stdout, r"(?m)^\s+%s\s+\d+ 条" % owner)

    def test_the_ledger_does_not_carry_the_classification(self):
        """分类集中在 open-items.md 里，不写进台账 —— 台账要能当文档读。"""
        self.assertNotIn("<!-- open:", LEDGER.read_text(encoding="utf-8"))

    def test_every_open_item_has_an_owner_and_a_criterion(self):
        text = OPEN_ITEMS.read_text(encoding="utf-8")
        blocks = re.split(r"(?m)^###\s+", text)[1:]
        self.assertGreater(len(blocks), 0, "open-items.md 里一条 OPEN 条目都没有")
        for block in blocks:
            if not block.startswith("OPEN-"):
                continue
            oid = block.split()[0]
            self.assertIn("- owner:", block, f"{oid} 没有 owner")
            self.assertIn("- 判据:", block, f"{oid} 没有判据")


class TestTamperingIsRejected(unittest.TestCase):
    """反向验证：三种篡改都必须让校验失败。

    每一条都对应一种「我以为登记过了」的真实失败方式：
    写了欠账不登记 / owner 写成别的词 / 锚被编辑后悄悄失效。
    """

    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp(prefix="open-items-"))
        self.addCleanup(shutil.rmtree, self.tmp, ignore_errors=True)

    def copy(self) -> tuple[Path, Path]:
        ledger = self.tmp / "optimization-plan.md"
        items = self.tmp / "open-items.md"
        shutil.copyfile(LEDGER, ledger)
        shutil.copyfile(OPEN_ITEMS, items)
        return ledger, items

    def test_a_new_debt_line_without_a_classification_is_rejected(self):
        ledger, items = self.copy()
        with ledger.open("a", encoding="utf-8") as fh:
            fh.write("\n- 这条改动尚未验证，也还没有人复核。\n")
        r = run_check(ledger, items)
        self.assertEqual(r.returncode, 1, "塞了一句新欠账却没有报错")
        self.assertIn("没有被分类", r.stderr)

    def test_an_owner_outside_the_four_values_is_rejected(self):
        ledger, items = self.copy()
        text = items.read_text(encoding="utf-8").replace("- owner: agent", "- owner: nobody", 1)
        items.write_text(text, encoding="utf-8")
        r = run_check(ledger, items)
        self.assertEqual(r.returncode, 1, "非法的 owner 没有被拦住")
        self.assertIn("owner=", r.stderr)

    def test_a_declined_item_without_a_reason_is_rejected(self):
        """「不做」必须留下为什么 —— 否则它和「忘了做」长得一样。"""
        ledger, items = self.copy()
        text = items.read_text(encoding="utf-8")
        # 去掉第一条 declined 条目的理由行
        m = re.search(r"(?ms)^### OPEN-\d+ .*?\n- owner: declined\n(.*?)(?=\n###|\n##)", text)
        self.assertIsNotNone(m, "open-items.md 里没有 declined 条目")
        block = m.group(0)
        stripped = "\n".join(l for l in block.splitlines() if not l.startswith("- 理由:"))
        items.write_text(text.replace(block, stripped), encoding="utf-8")
        r = run_check(ledger, items)
        self.assertEqual(r.returncode, 1, "declined 缺理由没有被拦住")
        self.assertIn("理由", r.stderr)

    def test_an_anchor_that_no_longer_matches_is_rejected(self):
        ledger, items = self.copy()
        text = items.read_text(encoding="utf-8")
        m = re.search(r"(?m)^- 锚: (.+)$", text)
        self.assertIsNotNone(m)
        edited = text.replace(m.group(1), "这段文字在台账里已经不存在了", 1)
        items.write_text(edited, encoding="utf-8")
        r = run_check(ledger, items)
        self.assertEqual(r.returncode, 1, "锚失效没有被拦住")
        self.assertIn("锚在台账里找不到", r.stderr)

    def test_a_too_short_anchor_is_rejected(self):
        """短锚会「一片盖多行」，把没分类的行蒙过去。"""
        ledger, items = self.copy()
        text = items.read_text(encoding="utf-8")
        m = re.search(r"(?m)^- 锚: (.+)$", text)
        self.assertIsNotNone(m)
        items.write_text(text.replace(m.group(1), "未验证", 1), encoding="utf-8")
        r = run_check(ledger, items)
        self.assertEqual(r.returncode, 1, "过短的锚没有被拦住")
        self.assertIn("锚太短", r.stderr)


if __name__ == "__main__":
    unittest.main()
