"""证明一次 Go 文件拆分是纯机械的：拆分前后「每个顶层声明的正文」作为多重集合完全相等。

配套 `split_query_service.py` 使用。它比 `git diff --stat` 强的地方在于：diff 只告诉你
「动了多少行」，这个脚本告诉你「有没有任何一个声明的内容变了」—— 前者在文件被搬空
重建时会给出满屏增删，读的人无法从里面看出结论。

用法：python verify_go_file_split.py <拆分前的文件> <拆分产物目录>
"""

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import split_query_service as m  # noqa: E402


def bodies_of(path):
    text = Path(path).read_text(encoding="utf-8").replace("\r\n", "\n")
    lines = text.split("\n")
    out = []
    for chunk in m.split_declarations(lines):
        body = list(chunk["body"])
        while body and not body[-1].strip():
            body.pop()
        while body and not body[0].strip():
            body.pop(0)
        out.append("\n".join(body))
    return out


def main():
    original = bodies_of(sys.argv[1])
    outdir = Path(sys.argv[2])
    pieces = []
    for path in sorted(outdir.glob("*.go")):
        pieces.extend(bodies_of(path))

    before = sorted(original)
    after = sorted(pieces)
    print(f"拆分前声明 {len(before)} 个，拆分后 {len(after)} 个")
    if before == after:
        print("正文多重集合完全一致：每个声明都逐字节相同")
        return 0

    only_before = [b for b in before if b not in after]
    only_after = [b for b in after if b not in before]
    print(f"不一致：仅在拆分前 {len(only_before)} 个，仅在拆分后 {len(only_after)} 个")
    for item in only_before[:5]:
        print("--- 仅拆分前 ---")
        print(item[:400])
    for item in only_after[:5]:
        print("--- 仅拆分后 ---")
        print(item[:400])
    return 1


if __name__ == "__main__":
    sys.exit(main())
