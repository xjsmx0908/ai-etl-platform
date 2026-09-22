"""契约：产品体验的状态只能有一个出处。

2026-09-22 之前，四份文档各自断言同一件事，而且互相打架：

  | 文档 | 日期 | 说法 |
  | --- | --- | --- |
  | `issues/findings-register.md` | 2026-09-11 | UAT-017～020 **开放** |
  | `docs/backlog.md` | 2026-09-12 | 「登记册开放项已清空」 |
  | `CONTINUATION.md` | 2026-09-09 | 「UAT-001～UAT-009 仍待真实页面复验」 |
  | `docs/product-experience-acceptance.md` | 2026-09-09 | 「2026-09-09 本轮判定：通过」 |

四句话各自都「没错」，但四句话不能同时为真。症状不是崩溃，是**读的人得出相反
的结论**：看 backlog 以为体验验收清完了，看登记册以为还有四条开放，看
CONTINUATION 以为连旧九条都没复验。

根因是**派生状态被复制到了多个地方**：条目状态只有登记册有列、有证据，其余三处
抄的是快照，任何一次新增建单都会让它们过期。修法是每个事实只留一个出处：

  - `issues/findings-register.md` —— UAT 逐条状态的唯一出处（含状态列与复验证据）。
  - `docs/backlog.md` —— 项目级事项（`P-*`）状态的唯一出处；涉及 UAT 只链接。
  - `docs/product-experience-acceptance.md` —— 章程，只写要求与判据，不写条目状态。
  - `CONTINUATION.md` —— 续接点与运行环境，不写任何会过期的状态断言。

只扫这四份，不是全仓：`docs/optimization-plan.md` 是缺陷台账，必须能引用被修掉的错误
措辞当证据；`LEARNINGS.codex.md` 是逐条带日期的日志，「当时待复验」是历史记录。被扫的
四份是**会断言当前状态**的那些。

注意这与 `docs/optimization-plan.md` §3 原本写的「以 backlog 为唯一状态源」不同：
逐条状态只能放在有状态列的那一份里，硬挪到 backlog 会变成双写，反而制造新的
漂移源。

断言分两组。第一组盯四份文档（共 7 条）；第二组是**判据本身的自检**（共 9 条），
因为这里的判据写错时的症状全都是「永远绿」：

  - 禁用词表被删空 / 改窄 —— 第 6、7 条用一份**独立的**期望样本（`MUST_BE_BANNED`）
    兜住；如果自检复用被检查的那张表，删表时自检会跟着变空。
  - 解析器把登记册末尾的模板算成真实条目 —— 第 9 条钉住「必须跳过代码块」。
  - 「续接文档不写 UAT 状态」按短语匹配会漏：原来写的是「仍待真实页面复验」，
    里面并没有连续的「待复验」。第 12 条改用「UAT 编号与状态词同现」判定，
    并用两种措辞各验一次。
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

REGISTER = ROOT / "issues" / "findings-register.md"
BACKLOG = ROOT / "docs" / "backlog.md"
CHARTER = ROOT / "docs" / "product-experience-acceptance.md"
CONTINUATION = ROOT / "CONTINUATION.md"

# 除登记册以外，不允许出现这些「当前状态快照」说法。它们全部是派生结论：
# 一旦新增或关闭一条 UAT，它们立刻过期，而且不会报错。
STALE_STATUS_PHRASES = (
    "开放项已清空",
    "仍待真实页面复验",
    "无开放 UAT",
)

# 判据自检的独立样本。故意不复用 STALE_STATUS_PHRASES —— 复用的话，把上面那张表
# 删空之后自检也跟着变空，永远绿。这里是「禁用词表本身」的期望值。
MUST_BE_BANNED = ("开放项已清空", "仍待真实页面复验", "无开放 UAT")

# 登记册状态列允许的取值，与文件开头的「状态」图例一致。
KNOWN_STATUSES = frozenset({"待复验", "开放", "待产品确认", "已修复", "不修复"})

FENCE_RE = re.compile(r"^```.*?^```", re.S | re.M)
INDEX_ROW_RE = re.compile(r"^\|\s*(UAT-\d{3})\s*\|")
DETAIL_HEADING_RE = re.compile(r"^###\s+(UAT-\d{3})\b")
DETAIL_STATUS_RE = re.compile(r"^-\s*状态：(.+?)\s*$")
DETAIL_INLINE_STATUS_RE = re.compile(r"^-\s*类型：.*状态：(.+?)\s*$")
CHARTER_ROW_RE = re.compile(r"^\|\s*\d+\s*\|\s*(UAT-\d{3})\s*\|")

# CONTINUATION.md 是续接点，不承载 UAT 状态。判据不是「匹配某个短语」，而是
# 「UAT 编号和任何状态词出现在同一行」—— 2026-09-22 之前写的是
# 「UAT-001～UAT-009 仍待真实页面复验」，而「仍待…复验」里并不含连续的「待复验」，
# 按短语匹配会漏。这里按「同现」判，不猜措辞。
EXPIRING_UAT_RE = re.compile(r"UAT-\d{3}.*(待|开放|已修复|已关闭|复验|通过)")

# backlog 里仍会逐条写「UAT-xxx 已复验」这类结案声明。它们不是计数快照，所以
# 上面那张禁用短语表抓不到；但它们同样是会被打回的状态断言，必须和登记册对齐。
POSITIVE_CLAIM_RE = re.compile(r"(UAT-\d{3})[^。\n]{0,40}?(已修复|已复验|已关闭|复验通过|关闭)")


def strip_fences(text):
    """Drop fenced code blocks.

    The register ends with a copy-paste template that itself contains
    `### UAT-0xx` and `- 状态：开放 | 待产品确认`. Without this the template's
    status would be attributed to the last real entry.
    """
    return FENCE_RE.sub("", text)


def read(path):
    return path.read_text(encoding="utf-8")


def find_stale_status(text):
    """Return the stale-status phrases present in `text`."""
    return [phrase for phrase in STALE_STATUS_PHRASES if phrase in text]


def parse_register(text):
    """Return (index_statuses, detail_statuses) keyed by UAT id."""
    body = strip_fences(text)
    index = {}
    detail = {}
    current = None
    for line in body.splitlines():
        heading = DETAIL_HEADING_RE.match(line)
        if heading:
            current = heading.group(1)
            continue
        row = INDEX_ROW_RE.match(line)
        if row:
            cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
            if len(cells) >= 6:
                index[row.group(1)] = cells[5]
            continue
        if current:
            match = DETAIL_STATUS_RE.match(line) or DETAIL_INLINE_STATUS_RE.match(line)
            if match:
                detail[current] = match.group(1).strip()
    return index, detail


def register_conflicts(text):
    """UAT ids whose index row and detail section disagree."""
    index, detail = parse_register(text)
    return sorted(
        item_id
        for item_id in set(index) & set(detail)
        if index[item_id] != detail[item_id]
    )


def parse_charter_matrix(text):
    """Return {UAT id: status} from the charter's old-issue re-check table."""
    matrix = {}
    for line in strip_fences(text).splitlines():
        row = CHARTER_ROW_RE.match(line)
        if not row:
            continue
        cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
        if len(cells) >= 5:
            matrix[row.group(1)] = cells[-1]
    return matrix


class RegisterIsTheOnlyStatusSourceTest(unittest.TestCase):
    def setUp(self):
        self.register_text = read(REGISTER)

    def test_register_holds_status_and_says_so(self):
        index, detail = parse_register(self.register_text)
        # A parse that silently returns nothing would make every other check vacuous.
        self.assertGreaterEqual(len(index), 20, "登记册索引表解析为空或过短")
        self.assertGreaterEqual(len(detail), 20, "登记册明细段解析为空或过短")
        self.assertIn("状态只在本文件维护", self.register_text)

    def test_register_statuses_are_known_values(self):
        index, detail = parse_register(self.register_text)
        for source in (index, detail):
            for item_id, status in sorted(source.items()):
                self.assertIn(status, KNOWN_STATUSES, f"{item_id} 的状态 {status!r} 不在图例内")

    def test_register_index_and_detail_agree(self):
        conflicts = register_conflicts(self.register_text)
        self.assertEqual(conflicts, [], f"索引表与明细段状态不一致：{conflicts}")

    def test_no_other_document_keeps_a_status_snapshot(self):
        for path in (BACKLOG, CHARTER, CONTINUATION):
            found = find_stale_status(read(path))
            self.assertEqual(found, [], f"{path.name} 里还有会过期的状态快照：{found}")

    def test_continuation_has_no_expiring_uat_claim(self):
        offenders = [
            line.strip()
            for line in read(CONTINUATION).splitlines()
            if EXPIRING_UAT_RE.search(line)
        ]
        self.assertEqual(offenders, [], f"CONTINUATION.md 里还有会过期的 UAT 状态断言：{offenders}")

    def test_charter_matrix_agrees_with_register(self):
        index, _ = parse_register(self.register_text)
        matrix = parse_charter_matrix(read(CHARTER))
        self.assertGreaterEqual(len(matrix), 9, "章程旧问题复验清单解析为空或过短")
        for item_id, status in sorted(matrix.items()):
            self.assertIn(item_id, index, f"章程引用了登记册里不存在的 {item_id}")
            self.assertEqual(
                status,
                index[item_id],
                f"{item_id}：章程写 {status!r}，登记册写 {index[item_id]!r}",
            )

    def test_backlog_closure_claims_agree_with_register(self):
        index, _ = parse_register(self.register_text)
        claims = POSITIVE_CLAIM_RE.findall(read(BACKLOG))
        # 解析不到声明说明判据失效了，比「恰好都一致」更值得报出来。
        self.assertGreaterEqual(len(claims), 4, "backlog 里解析不到任何 UAT 结案声明，判据可能失效")
        for item_id, word in claims:
            self.assertIn(item_id, index, f"backlog 引用了登记册里不存在的 {item_id}")
            self.assertEqual(
                index[item_id],
                "已修复",
                f"backlog 说 {item_id} {word}，登记册写 {index[item_id]!r}",
            )


class PredicateSelfCheckTest(unittest.TestCase):
    """判据自检。判据写错时症状是「永远绿」，只跑正例看不出来。"""

    def test_stale_status_detector_fires_on_every_phrase(self):
        for phrase in MUST_BE_BANNED:
            synthetic = f"# 某文档\n\n最后核验：2026-09-12。（{phrase}）\n"
            self.assertIn(
                phrase,
                find_stale_status(synthetic),
                f"禁用短语表里的 {phrase!r} 没有被判据抓到",
            )

    def test_every_known_stale_phrase_is_still_banned(self):
        for phrase in MUST_BE_BANNED:
            self.assertIn(phrase, STALE_STATUS_PHRASES, f"{phrase!r} 从禁用词表里掉了")

    def test_expiring_uat_detector_fires_on_both_wordings(self):
        for line in (
            "- UAT-001～UAT-009 仍待真实页面复验。",
            "- UAT-013 开放。",
            "- UAT-013 已修复。",
        ):
            self.assertTrue(EXPIRING_UAT_RE.search(line), f"漏判：{line}")

    def test_expiring_uat_detector_passes_a_pointer(self):
        self.assertIsNone(EXPIRING_UAT_RE.search("- 产品体验逐条状态见 issues/findings-register.md。"))

    def test_stale_status_detector_passes_clean_text(self):
        self.assertEqual(find_stale_status("# 干净文档\n\n逐条状态见登记册。\n"), [])

    def test_register_parser_ignores_fenced_template(self):
        synthetic = (
            "### UAT-020 标题\n\n- 状态：已修复\n\n"
            "```md\n### UAT-0xx 标题\n\n- 状态：开放 | 待产品确认\n```\n"
        )
        index, detail = parse_register(synthetic)
        self.assertEqual(detail, {"UAT-020": "已修复"})
        self.assertEqual(index, {})

    def test_register_conflict_detector_fires(self):
        synthetic = (
            "| UAT-001 | 美观 UX | S3 | `/a` | 全部 | 开放 | 摘要 | 反馈 |\n"
            "| --- | --- | --- | --- | --- | --- | --- | --- |\n\n"
            "### UAT-001 标题\n\n- 类型：美观 UX · 级别：S3 · 状态：已修复\n"
        )
        self.assertEqual(register_conflicts(synthetic), ["UAT-001"])
        self.assertEqual(parse_register(synthetic)[1], {"UAT-001": "已修复"})

    def test_charter_row_parser_fires(self):
        synthetic = (
            "| 旧编号 | 登记 ID | 摘要 | 复验方法 | 状态 |\n"
            "| --- | --- | --- | --- | --- |\n"
            "| 1 | UAT-001 | 摘要 | 方法 | 已修复 |\n"
        )
        self.assertEqual(parse_charter_matrix(synthetic), {"UAT-001": "已修复"})

    def test_backlog_claim_parser_fires(self):
        synthetic = "- P-X | 某事项 | done | UAT-013 已复验：错密登录显示中文 | 依据 |\n"
        self.assertEqual(POSITIVE_CLAIM_RE.findall(synthetic), [("UAT-013", "已复验")])


if __name__ == "__main__":
    unittest.main(verbosity=2)
