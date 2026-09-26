"""契约：宿主机回收脚本只能在「本项目自己的残留」范围内动，且默认不动。

这台宿主机是共享的：`docker ps -a` 里有别的项目的容器（openclaw、deeptutor、
umami、kafka-ui、一个 bot 栈、一个 blog 栈）。所以这个脚本的危险不是「回收得少」，
而是「回收得多」—— 一次全局 prune 就能把别人的数据删掉，而且不可逆、不会报错。

2026-09-26 实测留下了两条会被想当然写错的判据，这个文件把它们钉住：

  1. **构建缓存的旋钮是 `--min-free-space`，不是 `--max-used-space`。**
     当时缓存报 55.21GB，其中 51.29GB 是 `Shared`（与镜像共用的层），私有只有
     3.93GB。`--max-used-space 20GB` 回收 **0B** —— 上限比可回收量大，无事可做。
     换成 `--min-free-space 40GB` 才回收 4.04GB。只写「设上限」看着更稳，实际是
     一个永远不动的手。

  2. **镜像删掉的 tag 数不等于释放的字节数。**
     32 个 `ai-etl-*` 隔离栈镜像加起来 8.8GB，`df` 只涨了 2GB：它们与线上栈同源，
     层是同一批 blob。所以脚本里任何「预计回收 X GB」的说法都必须能被实测推翻，
     不能写死成结论。

断言都作用在「去掉注释后的可执行行」上：脚本头部刻意写了被禁用的命令名（说明
为什么禁用），拿全文去 grep 会把说明当成违规。
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "reclaim-host-disk.sh"


def executable_lines(text: str) -> str:
    """去掉整行注释与行尾注释，只留下真正会被 shell 执行的内容。"""
    kept = []
    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        # A trailing comment after code: cut it at the first unquoted '#'.
        kept.append(re.sub(r"\s+#\s.*$", "", line))
    return "\n".join(kept)


class ReclaimHostDiskContractTests(unittest.TestCase):
    def setUp(self):
        self.text = SCRIPT.read_text(encoding="utf-8")
        self.code = executable_lines(self.text)

    def test_script_exists_and_is_executable_bash(self):
        self.assertTrue(SCRIPT.exists(), "scripts/reclaim-host-disk.sh is missing")
        self.assertTrue(self.text.startswith("#!/usr/bin/env bash"))
        self.assertIn("set -euo pipefail", self.code)

    def test_dry_run_is_the_default(self):
        # The dangerous default is "run it and see"; the whole point is that a
        # bare invocation cannot change the host.
        self.assertRegex(self.code, r"DRY_RUN=1")
        self.assertRegex(self.code, r"--apply\)\s*DRY_RUN=0")
        self.assertIn("DRY RUN", self.text)

    def test_never_touches_volumes(self):
        # 99 of 118 volumes are unreferenced and 19.80GB of 24.60GB is in them,
        # but a read-only probe found another project's bot stack and a pnpm
        # store. There is no volume this script is allowed to remove.
        for banned in ("docker volume rm", "docker volume prune", "volume prune"):
            self.assertNotIn(banned, self.code, f"{banned} must not be executed")

    def test_never_runs_a_global_sweep(self):
        # On a shared host "unused" only means "unused by anyone" by accident.
        for banned in ("docker system prune", "docker image prune", "docker container prune"):
            self.assertNotIn(banned, self.code, f"{banned} must not be executed")

    def test_build_cache_uses_the_knob_that_actually_reclaims(self):
        # Measured 2026-09-26: a cap reclaimed 0B because the private cache was
        # already under it, while a free-space target reclaimed 4.04GB.
        self.assertIn("--min-free-space", self.code)
        self.assertIn("BUILD_CACHE_MIN_FREE", self.code)
        # A cap is still offered, but as an addition rather than the mechanism.
        self.assertIn("--max-used-space", self.code)
        self.assertIn("BUILD_CACHE_MAX_USED", self.code)

    def test_each_image_is_guarded_immediately_before_removal(self):
        # A stack that is still up must not be cut out from under itself, and the
        # check has to happen at removal time rather than once at the top of the
        # run, because the run takes long enough for that to matter.
        self.assertIn("--filter", self.code)
        self.assertIn("ancestor=", self.code)
        self.assertRegex(self.code, r"image_in_use")

    def test_only_this_projects_throwaway_images_are_considered(self):
        # The allowlist is a pattern, not "everything unreferenced", so a project
        # appearing on this host later cannot be swept up by a stale list.
        self.assertIn("OWNED_REPO_PATTERN='^ai-etl-'", self.code)
        self.assertIn("PROTECTED_REPO_PATTERN='^ai-etl-platform-'", self.code)

    def test_reports_before_and_after(self):
        # A reclaim run whose effect nobody can see is a run nobody can audit.
        self.assertIn('print_host_state "before"', self.code)
        self.assertIn('print_host_state "after"', self.code)
        self.assertIn("df -h /", self.code)

    def test_states_what_it_deliberately_leaves_alone(self):
        # The residual is the part an operator has to decide about; a script that
        # only reports what it removed implies there was nothing else.
        self.assertIn("not touched by design", self.code)
        self.assertIn("base images", self.text)
        self.assertIn("other projects", self.text)


if __name__ == "__main__":
    unittest.main()
