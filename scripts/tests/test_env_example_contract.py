"""契约：.env.example 必须和 compose 插值、代码读取保持一致。

这个文件是为 2026-09-22 修掉的两处漂移写的，两类漂移都会静默失败 —— 界面和
启动日志都不会报错，只有「改了配置没反应」这一种症状：

  1. 死键（宿主键与容器变量名不一致）。
     根 .env.example 的切块段写着 MIN_CHUNK_SIZE / MAX_CHUNK_SIZE / CHUNK_OVERLAP，
     但 compose 的 parser-service 段实际是：
         MIN_CHUNK_SIZE=128                             ← 硬编码字面量
         MAX_CHUNK_SIZE=${PARSER_MAX_CHUNK_SIZE:-600}   ← 从 PARSER_* 取值
         CHUNK_OVERLAP=${PARSER_CHUNK_OVERLAP:-50}      ← 从 PARSER_* 取值
     三个宿主键全都是死键：改了没有任何反应，也不会报错。切块参数是入库质量的
     核心旋钮，最难排查的就是这一类配置缺陷。
     修复方式是把 MIN_CHUNK_SIZE 也改成插值，三个键统一带 PARSER_ 前缀 ——
     原计划「删掉 3 个死键只留 PARSER_*」会连 MIN 的可调性一起删掉。

  2. 未文档化的键。
     按「所有 compose 文件的插值键」与「服务代码读取的键」分别做差集，
     修前各有 29 个和 25 个缺口（后者含 13 个单测专用键，不该写进模板）。
     其中 11 个当时已经写在 services/etl-worker/.env.example 里 —— 早先的记录
     只跟根模板比对，把缺口算大了。

下面六条断言把「宿主 .env 里能调的键」和「实际会被读到的键」钉在一起，
其中 test_source_scan_skips_hidden_and_vendored_trees 和
test_every_config_env_helper_is_scanned 是判据本身的自检 —— 判据写错时
症状是「同一个测试在两台机器上结论相反」，光靠正例发现不了。
"""

import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]

# 三份模板。.env.example 里被注释掉的示例同样算「已文档化」—— 注释行在这里
# 表达的是「有 compose 默认值的可选覆盖项」，不是「不要用」。
TEMPLATES = (
    ROOT / ".env.example",
    ROOT / "services" / "etl-worker" / ".env.example",
    ROOT / "services" / "doc-parser-service" / ".env.example",
)

# 全部 compose 文件，含 restore / identity-demo / smoke / governance / eval 叠加层。
# 不排除叠加层：叠加层的键同样是「宿主 .env 里能调的旋钮」，而一个没人知道存在
# 的旋钮和死键的危害一样 —— 它只是不会报错。这些键写在根 .env.example 的
# 「场景叠加层专用键」一节里。
COMPOSE_FILES = tuple(sorted(ROOT.glob("docker-compose*.yml")))

GO_SERVICE = ROOT / "services" / "etl-worker"
CONFIG_GO = GO_SERVICE / "internal" / "config" / "config.go"
PYTHON_SETTINGS_DIRS = (
    ROOT / "services" / "doc-parser-service" / "app",
    ROOT / "services" / "alert-webhook-service",
)

# 遍历源码时必须跳过的目录名（第三方依赖 / 构建产物）。
SKIPPED_DIR_NAMES = frozenset({"vendor", "node_modules", "testdata", "__pycache__"})

# internal/config 里所有读 env 的助手。新增助手必须同时加进这里，
# 否则契约会漏检 —— test_every_config_env_helper_is_scanned 负责发现这种漂移。
ENV_HELPERS = r"(?:Env(?:Str|Secret|Int|Float|Bool|Duration|CSV)|secretCSV|os\.Getenv)"

KEY_DEF = re.compile(r"^\s*#?\s*([A-Z_][A-Z0-9_]*)\s*=", re.M)
TOP_LEVEL_DEF = re.compile(r"^([A-Z_][A-Z0-9_]*)\s*=", re.M)
INTERPOLATION = re.compile(r"\$\{([A-Z_][A-Z0-9_]*)")
GO_ENV_READ = re.compile(ENV_HELPERS + r'\(\s*"([A-Z_][A-Z0-9_]*)"')
PY_SETTING = re.compile(r"^\s{4}([A-Z][A-Z0-9_]*)\s*:\s*\w+\s*=", re.M)
PY_GETENV = re.compile(r"""os\.getenv\(\s*["']([A-Z_][A-Z0-9_]*)["']""")

CHUNK_VARS = ("MIN_CHUNK_SIZE", "MAX_CHUNK_SIZE", "CHUNK_OVERLAP")


def is_first_party(relative_parts):
    """判断一组路径分量是否属于第一方源码（相对服务目录）。

    隐藏目录必须排除：远端 services/etl-worker 下有 .gomod(350MB) / .gocache(523MB)，
    里面是 6000+ 个第三方 .go 文件 —— AWS SDK 读 AWS_ACCESS_KEY、minio-go 读
    MINIO_ALIAS、grpc 读 GRPC_GO_LOG_*，它们和本项目的配置毫无关系。
    不排除就会把这些键当成「代码读取但未文档化」，于是同一个测试在本机绿、
    在服务器红。这条判据有独立的自检测试盯着。
    """
    return all(
        not part.startswith(".") and part not in SKIPPED_DIR_NAMES
        for part in relative_parts
    )


def iter_sources(root: Path, suffix: str):
    for path in sorted(root.rglob("*" + suffix)):
        if is_first_party(path.relative_to(root).parts[:-1]):
            yield path


def documented_keys():
    keys = set()
    for path in TEMPLATES:
        keys |= set(KEY_DEF.findall(path.read_text(encoding="utf-8")))
    return keys


def interpolated_keys():
    keys = set()
    for path in COMPOSE_FILES:
        keys |= set(INTERPOLATION.findall(path.read_text(encoding="utf-8")))
    return keys


def code_referenced_keys():
    keys = set()
    for path in iter_sources(GO_SERVICE, ".go"):
        # 单测专用键（*_TEST_DSN、OIDC_KEYCLOAK_TEST_* 等）不是部署配置。
        if path.name.endswith("_test.go"):
            continue
        keys |= set(GO_ENV_READ.findall(path.read_text(encoding="utf-8", errors="ignore")))
    for directory in PYTHON_SETTINGS_DIRS:
        for path in iter_sources(directory, ".py"):
            text = path.read_text(encoding="utf-8", errors="ignore")
            keys |= set(PY_SETTING.findall(text))
            keys |= set(PY_GETENV.findall(text))
    return keys


class EnvExampleContractTests(unittest.TestCase):
    def test_every_compose_interpolated_key_is_documented(self):
        # 宿主 .env 的值只有被 compose 插值才进得了容器。没写进模板的键等于
        # 一个没人知道的旋钮，调了没反应也查不到原因。
        missing = sorted(interpolated_keys() - documented_keys())
        self.assertEqual(
            missing,
            [],
            "以下键被 docker compose 插值，却没写进任何 .env.example：" + ", ".join(missing),
        )

    def test_every_code_referenced_key_is_documented(self):
        # 反过来：代码会读的键必须能在模板里找到，否则运维只能靠读源码发现它。
        missing = sorted(code_referenced_keys() - documented_keys())
        self.assertEqual(
            missing,
            [],
            "以下键被服务代码读取，却没写进任何 .env.example：" + ", ".join(missing),
        )

    def test_chunking_host_keys_are_prefixed_and_wired(self):
        # 死键回归防线。宿主键必须是 PARSER_ 前缀的那三个，且 compose 必须
        # 真的把它们接进 parser 容器的同名变量。裸名字一旦回到根模板顶层，
        # 就说明有人又加了一个改了没反应的键。
        root_text = (ROOT / ".env.example").read_text(encoding="utf-8")
        top_level = set(TOP_LEVEL_DEF.findall(root_text))
        compose_text = (ROOT / "docker-compose.yml").read_text(encoding="utf-8")

        for bare in CHUNK_VARS:
            self.assertNotIn(
                bare,
                top_level,
                f"根 .env.example 顶层又出现死键 {bare}：compose 会用容器变量名覆盖它，"
                f"宿主 .env 里这一行不会生效。要调就写 PARSER_{bare}。",
            )
            host_key = "PARSER_" + bare
            self.assertIn(
                host_key,
                top_level,
                f"{host_key} 必须写在根 .env.example 顶层，否则这个值无处可调。",
            )
            self.assertIn(
                "- " + bare + "=${" + host_key + ":-",
                compose_text,
                f"docker-compose.yml 没有把 {host_key} 接进 parser 容器的 {bare}，"
                f"{host_key} 现在是死键。",
            )

    def test_parser_template_defaults_match_service_settings(self):
        # doc-parser-service/.env.example 记录的是「直接跑本服务」时的默认值，
        # 必须与 pydantic Settings 的字段默认值一致，否则本地起服务的行为
        # 和文档写的不是一回事。
        template = {}
        for line in (
            ROOT / "services" / "doc-parser-service" / ".env.example"
        ).read_text(encoding="utf-8").splitlines():
            match = re.match(r"^([A-Z_][A-Z0-9_]*)=(.*)$", line)
            if match:
                template[match.group(1)] = match.group(2).strip()

        settings = {}
        config_text = (
            ROOT / "services" / "doc-parser-service" / "app" / "config.py"
        ).read_text(encoding="utf-8")
        for name, value in re.findall(
            r"^\s{4}([A-Z][A-Z0-9_]*)\s*:\s*\w+\s*=\s*(\S+)$", config_text, re.M
        ):
            settings[name] = value

        self.assertTrue(settings, "没能从 app/config.py 解析出任何 Settings 字段，判据已失效")
        for name in CHUNK_VARS:
            self.assertIn(name, settings, f"app/config.py 里没有 {name} 字段了")
            self.assertEqual(
                template.get(name),
                settings[name],
                f"{name} 在 doc-parser-service/.env.example 里是 {template.get(name)!r}，"
                f"而 app/config.py 的默认值是 {settings[name]!r}",
            )

    def test_source_scan_skips_hidden_and_vendored_trees(self):
        # 这条判据的自检。写这个测试的时候，第一版用 rglob("*.go") 直接扫
        # services/etl-worker，本机（262 个 .go）全绿、远端（6365 个 .go，
        # 多出来的是 .gomod/.gocache 里的第三方源码）报 52 个假缺口。
        # 判据本身出错时症状是「同一个测试两台机器结论相反」，所以单独立一条。
        cases = [
            ((".gomod", "github.com", "aws", "aws-sdk-go", "aws", "config.go"), False),
            ((".gocache", "00", "abc-d"), False),
            (("vendor", "github.com", "x", "y.go"), False),
            (("node_modules", "pkg", "index.js"), False),
            (("testdata", "fixture.go"), False),
            (("__pycache__", "config.cpython-313.pyc"), False),
            (("internal", "config", "config.go"), True),
            (("cmd", "worker", "main.go"), True),
        ]
        for parts, expected in cases:
            self.assertEqual(
                is_first_party(parts),
                expected,
                f"{'/'.join(parts)} 的第一方判定错了：期望 {expected}",
            )

        # 正例侧必须真的扫到东西，否则上面那条断言会因为集合为空而变成空转。
        scanned = list(iter_sources(GO_SERVICE, ".go"))
        self.assertTrue(scanned, "没有扫到任何 Go 源码，判据已失效")
        self.assertTrue(
            any(path.name == "config.go" for path in scanned),
            "没扫到 internal/config/config.go，判据已失效",
        )

    def test_every_config_env_helper_is_scanned(self):
        # 判据自检：config.go 里每新增一个读 env 的助手，都必须被 ENV_HELPERS
        # 覆盖，否则上面那条断言会静默漏掉用新助手读的键。
        helpers = set(re.findall(r"^func (\w+)\(key[ ,]", CONFIG_GO.read_text(encoding="utf-8"), re.M))
        self.assertTrue(helpers, "没能从 config.go 解析出任何 env 助手，判据已失效")
        for name in sorted(helpers):
            self.assertRegex(
                name,
                ENV_HELPERS,
                f"config.go 里的 {name} 是一个读 env 的助手，但没有被 ENV_HELPERS 覆盖，"
                f"用它读的键不会被契约检查到。",
            )


if __name__ == "__main__":
    unittest.main()
