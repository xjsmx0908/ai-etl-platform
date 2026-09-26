# -*- coding: utf-8 -*-
"""台账全文一致性审计的双向断言。

每条断言都是**成对**的：新文本必须在，**且**旧文本必须已清。
只断言「文本变了」是不够的 —— 上一轮就踩过 re.sub 把行首 | 一起吃掉的破坏性改动
照样通过「mutated != original」。所以这里逐条钉住两侧。

另加结构不变量：§1.3 表格行号必须连续 1..20（防止新行把表结构弄坏）、
§7 的步骤块里不许再有「（待做）」。
"""
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
PLAN = (ROOT / "docs" / "optimization-plan.md").read_text(encoding="utf-8")
CONT = (ROOT / "CONTINUATION.md").read_text(encoding="utf-8")

PAIRS = [
    # (说明, 文档, 必须出现的文本, 必须消失的文本)
    ("§7 第 25 步改判为已完成", PLAN,
     "第 25 步（已完成） UAT 其余 7 页真实页面复验", "第 25 步（待做）"),
    ("§7 第 25 步注明日期与证据", PLAN,
     "执行记录 `2026-09-23-uat-rest`", "第 25 步留到最后"),
    ("§7 顺序说明改为「排在最后」并说明它已完成", PLAN,
     "**已于 2026-09-23 完成**（7 页各走一轮", "与第 25 步互斥"),
    ("§8 缺陷 13 的失败 summary 改判为「已被缺陷 15 做掉」", PLAN,
     "**这个「未做」后来被缺陷 15 做掉了**", "**记录在案，未做。**"),
    ("§8 旧措辞「缺陷 13 没有动…」改为「当时没有动」", PLAN,
     "**缺陷 13 当时没有动预审失败路径的 `summary`**", "**缺陷 13 没有动预审失败路径的 `summary`**"),
    ("§8 不可达键「两类」改「三类」（表是三行）", PLAN,
     "这 13 个分三类", "这 13 个分两类"),
    ("§8「代码改动限于四处」改为「集中在这些章节」", PLAN,
     "**代码改动集中在这些章节**", "**代码改动限于四处**"),
    ("§8 代码改动清单补上 §1.2 / §4.4 / §5.1 / UAT 四处", PLAN,
     "§5.1 的 `query/service.go` 机械拆分", "`c7b4003`），以及"),
    ("§8 台账审计改判为已完成并列出 7 处漂移", PLAN,
     "全文一致性审计已于 2026-09-23 完成", "本文仍没有做全文一致性审计"),
    ("§8 旧结论「这件事仍未完成」已清", PLAN,
     "纯措辞差异与带日期的历史快照", "**这件事仍未完成**"),
    ("§2.3 契约测试条数标注为「当时 6 条、现 8 条」", PLAN,
     "当时 6 条断言", "（6 条断言）"),
    ("§5.2 拆成「首量 / 复量」两段", PLAN,
     "**复量（2026-09-23）**：前四个已涨到", "\n\n这些没有 5.1 那么突出的职责混杂"),
    ("§5.2 首量标注日期、复量数字落进正文", PLAN,
     "**首量（2026-09-21）**：`agentapi/service.go` 1195 行", "（**2026-09-23 复量**"),
    ("§6 的 282 tests 标为快照并给出当前值", PLAN,
     "现为 307", "已验：`scripts/tests` 282 tests OK（18 skipped）；"),
    ("§0 修订说明写明本轮做了审计", PLAN,
     "同日完成全文一致性审计并修掉 7 处漂移", "（2026-09-22，2026-09-23 补第十七至二十处）"),
    ("CONTINUATION 更新时间跟上", CONT,
     "更新时间：2026-09-23（Asia/Shanghai）", "更新时间：2026-09-22（Asia/Shanghai）"),
    ("CONTINUATION 的 Go 工具链说明与 §2.1 对齐", CONT,
     "2026-09-22 复核后远端已可直接跑", "- Go 宿主机可能没有 `go/gofmt`，使用 Docker 执行测试和格式化："),
]

STRUCT = [
    ("§1.3 表格行号连续 1..20", None, None),
    ("§7 步骤块内不再有「（待做）」", None, None),
]


def plan_section(start_marker, end_marker):
    start = PLAN.index(start_marker)
    end = PLAN.index(end_marker, start)
    return PLAN[start:end]


def main():
    failed = []

    for name, doc, must_have, must_be_gone in PAIRS:
        if must_have not in doc:
            failed.append(f"{name}: 新文本缺失 -> {must_have[:60]!r}")
        if must_be_gone in doc:
            failed.append(f"{name}: 旧文本仍在 -> {must_be_gone[:60]!r}")

    # 结构不变量 1：§1.3 表格行号 1..20 连续
    rows = re.findall(r"^\| (\d+) \|", PLAN, flags=re.M)
    if rows != [str(i) for i in range(1, 21)]:
        failed.append(f"§1.3 表格行号不是连续的 1..20：{rows}")

    # 结构不变量 2：§7 的代码块里没有「（待做）」
    steps = plan_section("## 7. 建议执行顺序", "**为什么是这个顺序**")
    if "（待做）" in steps:
        failed.append("§7 步骤块里仍有「（待做）」")
    if "第 26 步（已完成）" not in steps:
        failed.append("§7 第 26 步不见了")

    total = len(PAIRS) + 2
    if failed:
        print(f"FAIL {len(failed)}/{total}")
        for f in failed:
            print("  -", f)
        return 1
    print(f"OK {total}/{total}（{len(PAIRS)} 条双向断言 + 2 条结构不变量）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
