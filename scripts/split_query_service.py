"""把 internal/query/service.go 按职责机械拆到同 package 的多个文件（一次性迁移工具）。

判据：只搬位置，不改一个字节的函数体。脚本按「顶层声明边界」切分，不做任何重新
格式化（除了文件之间的空行与 import 块的筛选）。声明到目标文件的映射按**拆分前的
原始行号**写在 ASSIGNMENT 里，所以它只对拆分前的那一版有效。

复现方式（拆分前后各跑一次，第二次由 verify_go_file_split.py 证明正文一致）：

    git show <拆分前的提交>:services/etl-worker/internal/query/service.go > /tmp/orig.go
    python scripts/split_query_service.py /tmp/orig.go /tmp/split-out
    python scripts/verify_go_file_split.py /tmp/orig.go /tmp/split-out

用法：python split_query_service.py <输入 service.go> <输出目录>
"""

import re
import sys
from pathlib import Path

# 每个顶层声明的「原始行号 -> 目标文件」。行号来自拆分前的 service.go。
ASSIGNMENT = {
    # service.go：类型、构造、HTTP 入口、编排
    38: "service.go", 72: "service.go", 76: "service.go", 82: "service.go",
    90: "service.go", 97: "service.go", 104: "service.go", 111: "service.go",
    118: "service.go", 125: "service.go", 133: "service.go", 140: "service.go",
    144: "service.go", 156: "service.go", 176: "service.go", 184: "service.go",
    191: "service.go", 215: "service.go", 279: "service.go", 287: "service.go",
    294: "service.go", 307: "service.go", 315: "service.go", 317: "service.go",
    327: "service.go", 336: "service.go", 345: "service.go", 347: "service.go",
    357: "service.go", 367: "service.go", 374: "service.go", 409: "service.go",
    416: "service.go", 421: "service.go", 492: "service.go", 498: "service.go",
    505: "service.go", 568: "service.go", 572: "service.go", 927: "service.go",
    1096: "service.go",
    # sse.go：SSE 协议与请求体限制
    388: "sse.go", 395: "sse.go", 400: "sse.go", 1054: "sse.go",
    # llm_transport.go：LLM HTTP 传输与成本
    1068: "llm_transport.go", 1105: "llm_transport.go", 1109: "llm_transport.go",
    1116: "llm_transport.go", 1125: "llm_transport.go", 1320: "llm_transport.go",
    1329: "llm_transport.go", 1411: "llm_transport.go", 1477: "llm_transport.go",
    1484: "llm_transport.go", 1489: "llm_transport.go", 1493: "llm_transport.go",
    1495: "llm_transport.go", 1497: "llm_transport.go", 1508: "llm_transport.go",
    1510: "llm_transport.go", 1511: "llm_transport.go", 1536: "llm_transport.go",
    # prompt.go：提示词加载与组装
    453: "prompt.go", 473: "prompt.go", 1231: "prompt.go", 1277: "prompt.go",
    # retrieval_postprocess.go：检索后处理
    1522: "retrieval_postprocess.go", 1544: "retrieval_postprocess.go",
    1573: "retrieval_postprocess.go", 1595: "retrieval_postprocess.go",
    1604: "retrieval_postprocess.go",
    # grounding.go / citations.go：与既有 *_test.go 同名
    1616: "grounding.go", 1625: "grounding.go", 1631: "grounding.go",
    1652: "grounding.go", 1696: "grounding.go",
    1706: "citations.go", 1727: "citations.go",
}

DECL_RE = re.compile(r"^(func|type|const|var)\b")
IMPORT_RE = re.compile(r'^\s*(?:(\w+)\s+)?"([^"]+)"\s*$')


def parse_imports(header_lines):
    """Return [(group_index, rendered_line, name)] preserving the original grouping."""
    entries = []
    group = -1
    inside = False
    for line in header_lines:
        stripped = line.strip()
        if stripped == "import (":
            inside = True
            group += 1
            continue
        if inside and stripped == ")":
            inside = False
            continue
        if not inside:
            continue
        if not stripped:
            group += 1
            continue
        match = IMPORT_RE.match(line)
        if not match:
            raise SystemExit(f"无法解析 import 行: {line!r}")
        alias, path = match.group(1), match.group(2)
        name = alias or path.rsplit("/", 1)[-1]
        entries.append((group, line.rstrip(), name))
    return entries


def split_declarations(lines):
    """Return [(start_line_no, decl_lines)]. Doc comments attach to the following decl."""
    boundaries = []
    in_raw = False
    for index, line in enumerate(lines):
        # 必须先判声明、再切原始字符串状态：`const x = ` 这类行本身就是原始字符串的
        # 开头（行内只有一个反引号），先 toggle 会把这一行自己跳掉。
        if not in_raw and DECL_RE.match(line):
            boundaries.append(index)
        if line.count("`") % 2 == 1:
            in_raw = not in_raw

    chunks = []
    for position, start in enumerate(boundaries):
        end = boundaries[position + 1] if position + 1 < len(boundaries) else len(lines)
        chunks.append([start + 1, lines[start:end]])

    # 一个 chunk 末尾的注释行其实属于下一个声明（doc comment）。必须先剥掉尾随空行
    # 再收注释 —— 顺序反了的话 current[-1] 是空行，注释一行都收不到，全部丢掉。
    for position in range(len(chunks) - 1):
        current = chunks[position][1]
        next_lines = chunks[position + 1][1]
        while current and not current[-1].strip():
            current.pop()
        moved = []
        while len(current) > 1 and current[-1].lstrip().startswith("//"):
            moved.insert(0, current.pop())
        while current and not current[-1].strip():
            current.pop()
        if moved:
            while next_lines and not next_lines[0].strip():
                next_lines.pop(0)
            chunks[position + 1][1] = moved + next_lines

    # 第一个声明的 doc 注释落在 header 里（package 子句与 import 之后），同样要收进来。
    header = lines[: boundaries[0]]
    lead = []
    for line in reversed(header):
        if not line.strip():
            continue
        if not line.lstrip().startswith("//"):
            break
        lead.insert(0, line)
    if lead:
        body = chunks[0][1]
        while body and not body[0].strip():
            body.pop(0)
        chunks[0][1] = lead + body

    # 每个声明前面原本有几个空行。相邻的一行方法（中间没有空行）在 gofmt 里属于同一个
    # 对齐组，拆文件时若强行插空行会把对齐重置 —— 那就不再是「逐字节相同」了。
    results = []
    for index, (number, body) in enumerate(chunks):
        while body and not body[-1].strip():
            body.pop()
        gap = 0
        cursor = boundaries[index] - 1
        while cursor >= 0 and lines[cursor].lstrip().startswith("//"):
            cursor -= 1
        while cursor >= 0 and not lines[cursor].strip():
            gap += 1
            cursor -= 1
        results.append({"number": number, "body": body, "gap": gap})
    return results


def needed_imports(body_text, entries):
    used = set(re.findall(r"\b([a-zA-Z_][a-zA-Z0-9_]*)\.", body_text))
    return {name for _, _, name in entries if name in used}


def render(path, package_doc, entries, items):
    used = set()
    for item in items:
        used |= needed_imports("\n".join(item["body"]), entries)

    out = []
    if package_doc:
        # 包注释必须紧贴 `package query`，中间不能有空行，否则 Go 不把它当包注释
        # （go doc 第一行会消失）。
        out.extend(package_doc)
    out.append("package query")
    if used:
        out.append("")
        out.append("import (")
        last_group = None
        for group, line, name in entries:
            if name not in used:
                continue
            if last_group is not None and group != last_group:
                out.append("")
            out.append(line)
            last_group = group
        out.append(")")
    for index, item in enumerate(items):
        # 第一个声明与 import 块之间至少留一个空行；其余按原文的空行数照搬，
        # 原文相邻（gap=0）的就必须仍然相邻，否则 gofmt 的对齐组会被重置。
        out.extend([""] * (item["gap"] if index else max(item["gap"], 1)))
        out.extend(item["body"])
    # newline="\n" 必须显式给：Windows 上文本模式会把 \n 翻成 \r\n，本地 gofmt 会把
    # 每个文件都判成未格式化（远端 checkout 是 LF，两台机器结论会相反）。
    with open(path, "w", encoding="utf-8", newline="\n") as handle:
        handle.write("\n".join(out) + "\n")


def main():
    source = Path(sys.argv[1])
    outdir = Path(sys.argv[2])
    text = source.read_text(encoding="utf-8").replace("\r\n", "\n")
    lines = text.split("\n")

    chunks = split_declarations(lines)
    if len(chunks) != len(ASSIGNMENT):
        raise SystemExit(f"顶层声明数 {len(chunks)} != 映射表 {len(ASSIGNMENT)}，先核对行号")

    header = lines[: chunks[0]["number"] - 1]
    entries = parse_imports(header)
    # 包注释只是 `package query` 之前的那一段；header 里其余注释属于第一个声明。
    package_doc = []
    for line in header:
        if line.startswith("package "):
            break
        package_doc.append(line)
    while package_doc and not package_doc[-1].strip():
        package_doc.pop()

    buckets = {}
    for chunk in chunks:
        target = ASSIGNMENT.get(chunk["number"])
        if target is None:
            raise SystemExit(f"第 {chunk['number']} 行没有目标文件：{chunk['body'][0][:60]!r}")
        buckets.setdefault(target, []).append(chunk)

    outdir.mkdir(parents=True, exist_ok=True)
    for target, items in sorted(buckets.items()):
        doc = package_doc if target == "service.go" else []
        render(outdir / target, doc, entries, items)
        print(f"{target}: {len(items)} 个声明")

    total = sum(len(chunk["body"]) for chunk in chunks)
    print(f"合计声明 {len(chunks)} 个，正文 {total} 行")


if __name__ == "__main__":
    main()
