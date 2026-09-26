#!/usr/bin/env python3
"""从台账里机械抽出「欠账」行，并校验 `docs/open-items.md` 给每一行都做了分类。

为什么要有这个脚本
------------------
2026-09-26，用户问「还有哪些没有做的工作」，我凭回忆列了一份，把**我该做而没做完**的东西
（对账 CI 从没跑过、切块改动没跑评测轨）归进了「可做可不做」那一堆。用户反问
「你为什么会分类错呢」。

根因有两条，第二条才是可修的：
  1. 我用「要不要用户参与」这个维度分堆，却给其中一堆起了带价值判断的名字 —— 维度是
     「谁来做」，名字承诺的是「重不重要」。
  2. **清单是凭回忆列的，而回忆会漏。** 台账里早就写着「未端到端验证」「没跑过」「未做」——
     它们是**已经写下来的欠账**，机械扫描就能拿到全集。

所以本脚本只做第 2 条的机械部分：**扫出台账里每一处欠账词，强制 `docs/open-items.md`
把它分类**（登记成 OPEN 条目并给出 owner，或明确判为「不是欠账」并给理由）。
分类从此是数据，不是判断。

分类写在 `open-items.md` 里，**不写进台账** —— 台账保持可读，分类集中一处。

用法
----
    python3 scripts/extract-open-items.py --list    # 列出全部欠账行及其分类
    python3 scripts/extract-open-items.py --check   # 校验（CI 用；失败退出码 1）

`docs/open-items.md` 的格式
--------------------------
    ### OPEN-03 <标题>
    - owner: agent
    - 判据: <怎么算做完>
    - 出处: <台账章节 / 文件:行>
    - 锚: <台账里那一行的原文片段，用来把条目钉到具体行上>

    ## 含欠账词但已确认不是欠账

    | 锚（台账原文片段） | 为什么不是欠账 |
    | --- | --- |
    | 凡未实测的均明确标注为「未验证」 | 编制方式说明，不是在说有什么没做 |

「锚」用**原文片段**而不是行号 —— 行号会随编辑漂移，片段不会。锚必须在台账里
**恰好命中一行**，命中 0 行或 2 行以上都算错。
"""

from __future__ import annotations

import argparse
import io
import os
import re
import sys
from collections import Counter

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LEDGER = os.path.join(ROOT, "docs", "optimization-plan.md")
OPEN_ITEMS = os.path.join(ROOT, "docs", "open-items.md")

# 强制口径。**故意用正则而不是字面量**：实测「仍未被验证」不含字面量「未验证」
# （中间夹了「被」），字面量匹配会漏。方向取**宁可多抓**：多抓的代价是多写一条
# 「不是欠账」的理由，漏抓的代价是欠账不进清单 —— 后者正是这个脚本要防的病。
DEBT_PATTERNS = [
    r"未.{0,2}(验证|端到端|跑过|复验)",
    r"未(做|动|修|接|覆盖)",
    r"没.{0,4}(跑|动|验|接|做|改)",
    r"待决策|待定|仍待|暂缓|尚未",
    r"blocked|pending decision",
]
DEBT_RE = re.compile("|".join("(?:%s)" % p for p in DEBT_PATTERNS))

# 更宽的口径，**只用于报告**。它抓到的多半是历史叙述（缺陷表里描述"当初没做"的那一行），
# 所以不强制；但每次运行都会把它们印出来，让人看一眼「有没有欠账刚好躲过强制词表」。
WIDE_PATTERNS = [
    r"未.{0,6}(验证|端到端|跑过|做|动|修|复验|覆盖|接|改)",
    r"没.{0,6}(跑|动|验|接|改|做)",
    r"没有.{0,6}(跑|做|改|接|验|动)",
]
WIDE_RE = re.compile("|".join("(?:%s)" % p for p in WIDE_PATTERNS))

ITEM_RE = re.compile(r"^###\s+(OPEN-\d+)\s*(.*)$")
KV_RE = re.compile(r"^-\s*([^\s:：]+)\s*[:：]\s*(.*)$")

# owner 的四个取值 —— 这就是「分类」，写成数据。
VALID_OWNERS = {
    "agent": "我该做、没做完（不许再叫它「可做可不做」）",
    "user": "需要用户点头或定口径",
    "external": "外部依赖（业务签字 / IdP 选型 / 预算）",
    "declined": "已明确决定不做（必须带理由）",
}

# 锚的最短长度（归一化后按字符数）。太短的锚会「一片盖多行」，把没分类的行蒙过去。
MIN_ANCHOR = 12


def norm(text: str) -> str:
    """归一化到可比较的形式：去 markdown 标记与所有空白。"""
    text = re.sub(r"[`*_>\[\]]", "", text)
    return re.sub(r"\s+", "", text)


class DebtLine:
    __slots__ = ("no", "text", "nkey")

    def __init__(self, no: int, text: str):
        self.no = no
        self.text = text
        self.nkey = norm(text)

    @property
    def excerpt(self) -> str:
        return re.sub(r"\s+", " ", self.text.strip())[:100]


def read_debt_lines() -> list[DebtLine]:
    out = []
    for no, raw in enumerate(io.open(LEDGER, encoding="utf-8").read().splitlines(), 1):
        if DEBT_RE.search(raw):
            out.append(DebtLine(no, raw))
    return out


def read_wide_only_lines() -> list[DebtLine]:
    out = []
    for no, raw in enumerate(io.open(LEDGER, encoding="utf-8").read().splitlines(), 1):
        if WIDE_RE.search(raw) and not DEBT_RE.search(raw):
            out.append(DebtLine(no, raw))
    return out


class OpenItem:
    __slots__ = ("id", "title", "fields", "anchors")

    def __init__(self, oid: str, title: str):
        self.id = oid
        self.title = title
        self.fields: dict[str, str] = {}
        self.anchors: list[str] = []


def read_open_items(path: str | None = None) -> tuple[dict[str, OpenItem], list[tuple[str, str]]]:
    path = path or OPEN_ITEMS
    """返回 (OPEN 条目, 「不是欠账」的 (锚, 理由) 列表)。"""
    items: dict[str, OpenItem] = {}
    not_debt: list[tuple[str, str]] = []
    if not os.path.exists(path):
        return items, not_debt

    text = io.open(path, encoding="utf-8").read()
    section = "items"
    current: OpenItem | None = None
    for raw in text.splitlines():
        line = raw.strip()
        if line.startswith("## ") and "不是欠账" in line:
            section = "not_debt"
            current = None
            continue
        if section == "items":
            m = ITEM_RE.match(line)
            if m:
                current = OpenItem(m.group(1), m.group(2).strip())
                items[m.group(1)] = current
                continue
            if current is None:
                continue
            m = KV_RE.match(line)
            if m:
                key, value = m.group(1), m.group(2).strip()
                if key == "锚":
                    current.anchors.append(value)
                else:
                    current.fields[key] = value
        elif section == "not_debt":
            if not line.startswith("|") or set(line) <= set("|-: "):
                continue
            cells = [c.strip() for c in line.strip("|").split("|")]
            if len(cells) < 2 or cells[0] in ("锚（台账原文片段）", "锚"):
                continue
            not_debt.append((cells[0], cells[1]))
    return items, not_debt


def read_all_lines() -> list[DebtLine]:
    """台账全部行。锚在**全文**里找 —— 一条欠账的出处不一定含欠账词
    （实测：「`refusalMarkers` 仍是宽子串匹配」整句没有任何欠账词，但它是一条真欠账）。"""
    return [
        DebtLine(no, raw)
        for no, raw in enumerate(io.open(LEDGER, encoding="utf-8").read().splitlines(), 1)
    ]


def match_anchors(lines: list[DebtLine]) -> dict[str, list[int]]:
    """锚 → 命中台账的行号列表。"""
    hits: dict[str, list[int]] = {}
    for anchor in ANCHOR_CACHE:
        key = norm(anchor)
        hits[anchor] = [ln.no for ln in lines if key and key in ln.nkey]
    return hits


ANCHOR_CACHE: list[str] = []


def check() -> int:
    problems: list[str] = []
    debt = read_debt_lines()
    all_lines = read_all_lines()
    items, not_debt = read_open_items()

    ANCHOR_CACHE.clear()
    for item in items.values():
        ANCHOR_CACHE.extend(item.anchors)
    ANCHOR_CACHE.extend(a for a, _ in not_debt)

    hits = match_anchors(all_lines)

    # 锚必须够长（防「拿一个短词去覆盖一片行」），且必须在台账里命中至少一行
    for item in items.values():
        if not item.anchors and not item.fields.get("台账未提及"):
            problems.append(
                f"{item.id} 既没有「锚」也没有「台账未提及」—— 无法判断它在台账里有没有出处"
            )
        for anchor in item.anchors:
            if len(norm(anchor)) < MIN_ANCHOR:
                problems.append(
                    f"{item.id} 的锚太短（{len(norm(anchor))} < {MIN_ANCHOR} 字）：{anchor[:50]!r}"
                )
            if not hits.get(anchor):
                problems.append(f"{item.id} 的锚在台账里找不到：{anchor[:50]!r}")
    for anchor, _reason in not_debt:
        if len(norm(anchor)) < MIN_ANCHOR:
            problems.append(f"「不是欠账」的锚太短（{len(norm(anchor))} < {MIN_ANCHOR} 字）：{anchor[:50]!r}")
        if not hits.get(anchor):
            problems.append(f"「不是欠账」的锚在台账里找不到：{anchor[:50]!r}")

    # 「不是欠账」的锚必须落在**强制欠账行**上 —— 否则它没有在分类任何东西
    debt_nos = {ln.no for ln in debt}
    for anchor, _reason in not_debt:
        if hits.get(anchor) and not (set(hits[anchor]) & debt_nos):
            problems.append(
                f"「不是欠账」的锚没有落在任何欠账行上（它没有在分类任何东西）：{anchor[:50]!r}"
            )

    # 每一处欠账都必须被分类（至少被一条锚命中；同一行可以被多条锚命中 —— 一行确实可能
    # 同时描述两件事，比如 §0 那行「已知未修」）
    covered: dict[int, list[str]] = {}
    for anchor, nos in hits.items():
        for no in nos:
            covered.setdefault(no, []).append(anchor)

    unclassified = [ln for ln in debt if ln.no not in covered]
    for ln in unclassified:
        problems.append(
            f"docs/optimization-plan.md:{ln.no} 含欠账词但没有被分类"
            f"（在 open-items.md 里给它一条 OPEN 条目，或写进「含欠账词但已确认不是欠账」）：{ln.excerpt}"
        )

    # 条目自身的字段
    for item in items.values():
        owner = item.fields.get("owner", "")
        if owner not in VALID_OWNERS:
            problems.append(f"{item.id} 的 owner={owner!r} 不合法，只能是 {sorted(VALID_OWNERS)}")
        if not item.title:
            problems.append(f"{item.id} 缺标题")
        if not item.fields.get("判据"):
            problems.append(f"{item.id} 缺「判据」（怎么算做完）")
        if not item.fields.get("出处"):
            problems.append(f"{item.id} 缺「出处」")
        if owner == "declined" and not item.fields.get("理由"):
            problems.append(f"{item.id} 的 owner=declined 但没有写「理由」—— 「不做」必须留下为什么")

    counts = Counter(i.fields.get("owner", "?") for i in items.values())
    print("台账里的欠账行：{} 行".format(len(debt)))
    print("已登记 OPEN 条目：{} 条".format(len(items)))
    for owner in ("agent", "user", "external", "declined"):
        print("  {:<9} {:>3} 条   {}".format(owner, counts.get(owner, 0), VALID_OWNERS[owner]))
    print("判为「不是欠账」：{} 行".format(len(not_debt)))

    extra = read_wide_only_lines()
    if extra:
        print("\n宽口径额外命中（只报告，不强制 —— 人工看一眼有没有欠账躲过强制词表）：{} 行".format(len(extra)))
        for ln in extra:
            print("  {:>5}  {}".format(ln.no, ln.excerpt))

    if problems:
        print("\n发现问题 {} 处：".format(len(problems)), file=sys.stderr)
        for p in problems:
            print("  - " + p, file=sys.stderr)
        return 1
    print("\nOK：台账里每一处欠账都已分类，且每条 OPEN 条目都钉到了具体行。")
    return 0


def listing() -> int:
    debt = read_debt_lines()
    all_lines = read_all_lines()
    items, not_debt = read_open_items()

    ANCHOR_CACHE.clear()
    for item in items.values():
        ANCHOR_CACHE.extend(item.anchors)
    ANCHOR_CACHE.extend(a for a, _ in not_debt)
    hits = match_anchors(all_lines)

    by_line: dict[int, list[str]] = {}
    for anchor, nos in hits.items():
        for no in nos:
            by_line.setdefault(no, []).append(anchor)
    owner_of = {}
    for item in items.values():
        for anchor in item.anchors:
            for no in hits.get(anchor, []):
                owner_of[no] = item.fields.get("owner", "?") + " " + item.id

    for ln in debt:
        tag = owner_of.get(ln.no) or ("不是欠账" if ln.no in by_line else "*** 未分类 ***")
        print("{:>5}  {:<24} {}".format(ln.no, tag, ln.excerpt))
    extra = read_wide_only_lines()
    if extra:
        print("\n宽口径额外命中（只报告，不强制）：{} 行".format(len(extra)))
        for ln in extra:
            print("  {:>5}  {}".format(ln.no, ln.excerpt))
    return 0


def main() -> int:
    ap = argparse.ArgumentParser(description="抽出台账里的欠账行并校验 open-items 覆盖")
    g = ap.add_mutually_exclusive_group(required=True)
    g.add_argument("--list", action="store_true", help="列出全部欠账行及其分类")
    g.add_argument("--check", action="store_true", help="校验（失败退出码 1）")
    ap.add_argument("--ledger", default=None, help="台账路径（测试用；默认仓库里的那份）")
    ap.add_argument("--open-items", default=None, help="清单路径（测试用）")
    args = ap.parse_args()

    # 这两个路径是模块级常量；read_* 每次都从模块级读，所以这里覆盖即可。
    global LEDGER, OPEN_ITEMS
    if args.ledger:
        LEDGER = args.ledger
    if args.open_items:
        OPEN_ITEMS = args.open_items

    return listing() if args.list else check()


if __name__ == "__main__":
    raise SystemExit(main())
