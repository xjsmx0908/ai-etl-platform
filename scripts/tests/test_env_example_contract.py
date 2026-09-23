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
     按「所有 compose 文件的插值键」与「服务代码读取的键」分别做差集，修前各有
     19 个和 11 个缺口（后者已排除 13 个单测专用键 —— 它们不该写进模板）。
     两份差集只有 4 个键重合，并起来是 26 个。其中 11 个当时已经写在
     services/etl-worker/.env.example 里 —— 更早的一次记录只跟根模板比对，
     把缺口算大了。

  3. 根模板顶层键没人接（§8 记的第三类缺口）。
     前两条断言只覆盖「compose 插值」与「代码读取」两个方向，各有一个方向能通过
     而这一类失败：键写在根模板顶层，但既不在任何 compose 的 ${} 里，也不被任何
     宿主脚本读 —— 改了永远到不了进程，上面两条契约一声不吭。§8 记了 18 个，
     其中 5 个（TASK_STATUS_STORE / TASK_STATUS_TTL / MULTIPART_MAX_MEMORY_MB /
     OUTBOX_RELAY_BATCH_SIZE / OUTBOX_RELAY_LEASE）是纯粹的接线缺口，代码侧默认值
     够用，已在 docker-compose.yml 里补上透传；剩下 13 个刻意不可达，进
     NOT_REACHABLE_KEYS 逐条给出理由，并在模板里贴着那把键注明「不会进容器」。

下面八条断言把「宿主 .env 里能调的键」和「实际会被读到的键」钉在一起，
其中 test_source_scan_skips_hidden_and_vendored_trees、test_every_config_env_helper_is_scanned
和 test_unreachable_keys_are_flagged_next_to_the_key 末尾那一段是判据本身的自检 ——
判据写错时症状是「同一个测试在两台机器上结论相反」或「永远绿」，光靠正例发现不了。
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

# 判据 ③（根模板顶层键 ⊆ compose 插值 ∪ 宿主脚本读取）的显式例外。
#
# §8 把这一类记成「已知覆盖缺口」：写在 .env 里不会生效，而契约测试不检查它。
# 这里把它变成一张必须逐条给出理由的白名单 —— 新增死键会让
# test_every_root_template_key_reaches_the_process 变红，而绕过它的唯一方式是
# 往这张表里加一行并写下理由（并且模板里也要给出对应的说明）。
#
# 两类，都不该靠改配置解决：
#   - 集群内地址：compose 直接写死成服务名。改成可配置等于「允许连集群外的
#     Redis/PG/MinIO/Qdrant/Kafka」，那是产品决定，不是配置修复。
#   - secret：容器读的是 <KEY>_FILE。EnvSecret 确实优先读明文键，正因为如此
#     才不能把明文透传进去 —— 那会盖掉 secret 文件里的那份。
NOT_REACHABLE_KEYS = {
    "KAFKA_BROKERS": "compose 写死为服务名 kafka:9092",
    "REDIS_CACHE_ADDR": "compose 写死为服务名 redis-cache:6379",
    "REDIS_STATE_ADDR": "compose 写死为服务名 redis-state:6379",
    "STORE_ENDPOINT": "compose 写死为 http://qdrant:6333",
    "S3_ENDPOINT": "compose 写死为 minio:9000",
    "REDIS_ADDR": "旧配置回退值；容器里已由 REDIS_CACHE_ADDR / REDIS_STATE_ADDR 覆盖",
    "REDIS_DB": "旧配置回退值；容器里已由 REDIS_CACHE_DB / REDIS_STATE_DB 覆盖",
    "PG_DSN": "容器读 PG_DSN_FILE",
    "MINIO_ROOT_USER": "容器读 MINIO_ROOT_USER_FILE",
    "MINIO_ROOT_PASSWORD": "容器读 MINIO_ROOT_PASSWORD_FILE",
    "REDIS_CACHE_PASSWORD": "容器读 REDIS_CACHE_PASSWORD_FILE",
    "REDIS_STATE_PASSWORD": "容器读 REDIS_STATE_PASSWORD_FILE",
    "NOTIFICATION_WEBHOOK_TOKEN": "容器读 NOTIFICATION_WEBHOOK_TOKEN_FILE",
}

# 模板里给这些键写的提示语。运维要能从模板本身看出来「这一行改了没用」，
# 而不是只能从这张表里知道。
NOT_REACHABLE_MARKER = "不会进容器"

# 提示语必须写在这把键的定义行上方多少行之内。一段注释说明一个键时通常 2-3 行，
# 说明一对键（如 MINIO_ROOT_USER / MINIO_ROOT_PASSWORD）时是 3-4 行；8 行留了
# 重排换行的余量，又不足以让「写在文件另一头」蒙混过关。
NOT_REACHABLE_WINDOW = 8

HOST_SCRIPT_DIRS = (ROOT / "scripts",)

# 宿主脚本里出现键名的几种写法。
SCRIPT_ASSIGNMENT = re.compile(r"(?m)^\s*([A-Z_][A-Z0-9_]*)\s*=")
SCRIPT_ENVIRON = re.compile(r'os\.environ(?:\.get)?[\(\[]\s*["\']([A-Z_][A-Z0-9_]*)["\']')
SCRIPT_SHELL_VAR = re.compile(r"\$\{?([A-Z_][A-Z0-9_]*)")


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


def iter_host_scripts():
    """根模板的键在 compose 之外还有一条生效路径：宿主脚本直接读它。

    测试文件必须排除。`scripts/tests/` 里会**引用**键名当断言目标
    （`test_security_hardening.py` 就断言 `MINIO_ROOT_USER=...` 不许出现在 compose 里），
    把它当「脚本读它」会把一个被明确禁止的键算成可达 —— 判据写错时的症状是
    「本该报红的不报红」，只跑正例发现不了。这条有独立的自检测试盯着。
    """
    for directory in HOST_SCRIPT_DIRS:
        for path in sorted(directory.rglob("*")):
            if not path.is_file() or path.suffix == ".pyc":
                continue
            if "tests" in path.relative_to(ROOT).parts:
                continue
            if path.name.startswith("test_") or path.name.endswith("_test.py"):
                continue
            yield path


def host_script_keys():
    keys = set()
    for path in iter_host_scripts():
        text = path.read_text(encoding="utf-8", errors="ignore")
        keys |= set(SCRIPT_ASSIGNMENT.findall(text))
        keys |= set(SCRIPT_ENVIRON.findall(text))
        keys |= set(SCRIPT_SHELL_VAR.findall(text))
    return keys


def keys_flagged_in_template(text: str, window: int = NOT_REACHABLE_WINDOW):
    """返回「定义行上方 window 行内出现过 NOT_REACHABLE_MARKER」的顶层键。

    白名单只约束了「哪些键允许不可达」，运维仍然需要从模板本身看出来这一点 ——
    否则一个在 .env 里改了没反应的键和一个正常键长得一模一样。所以模板里的提示语
    也是契约的一部分，而且是**必须贴着那把键**的那部分：写在文件另一头的提示语
    等于没有提示语。
    """
    lines = text.splitlines()
    flagged = set()
    for index, line in enumerate(lines):
        match = TOP_LEVEL_DEF.match(line)
        if not match:
            continue
        window_lines = lines[max(0, index - window) : index]
        if any(NOT_REACHABLE_MARKER in above for above in window_lines):
            flagged.add(match.group(1))
    return flagged


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

    def test_every_root_template_key_reaches_the_process(self):
        # 判据 ③：根模板顶层键 ⊆ compose 插值 ∪ 宿主脚本读取。
        #
        # 前两条断言只覆盖了「compose 插值」与「代码读取」两个方向，各自都能通过
        # 而这一条失败 —— §8 记的 18 个键就是这么漏掉的：它们既不在 compose 的
        # ${} 里，也不被任何宿主脚本读，于是写在 .env 里永远到不了进程，而
        # 上面两条契约一声不吭。其中 5 个（TASK_STATUS_STORE / TASK_STATUS_TTL /
        # MULTIPART_MAX_MEMORY_MB / OUTBOX_RELAY_BATCH_SIZE / OUTBOX_RELAY_LEASE）
        # 是纯粹的接线缺口，已在 docker-compose.yml 里补上透传；剩下 13 个是
        # 刻意不可达的，进 NOT_REACHABLE_KEYS 并逐条给出理由。
        top_level = set(TOP_LEVEL_DEF.findall((ROOT / ".env.example").read_text(encoding="utf-8")))
        reachable = interpolated_keys() | host_script_keys()

        self.assertTrue(top_level, "没能从根模板解析出任何顶层键，判据已失效")
        self.assertTrue(
            top_level & reachable,
            "根模板的顶层键一个都没被 compose 或脚本读到，判据已失效",
        )

        unreachable = sorted(top_level - reachable - set(NOT_REACHABLE_KEYS))
        self.assertEqual(
            unreachable,
            [],
            "以下键写在根 .env.example 顶层，但既不被 docker compose 插值、也不被任何宿主"
            "脚本读取 —— 改了不会有任何反应，也不会报错。要么在 docker-compose.yml 里"
            "接上它，要么写进 NOT_REACHABLE_KEYS 并说明理由，并在模板里注明"
            f"「{NOT_REACHABLE_MARKER}」：" + ", ".join(unreachable),
        )

        # 反向：白名单里的键必须还在模板顶层。键被删掉而白名单留着，等于给同一个
        # 名字永久豁免 —— 将来它被加回来时不会再报红。
        stale = sorted(set(NOT_REACHABLE_KEYS) - top_level)
        self.assertEqual(
            stale,
            [],
            "以下键已不在根模板顶层，请从 NOT_REACHABLE_KEYS 里删掉：" + ", ".join(stale),
        )

    def test_unreachable_keys_are_flagged_next_to_the_key(self):
        # 上面那条断言放行了 13 个不可达的键，条件是它们在模板里带提示语。这条
        # 检查提示语真的在 —— 而且贴着那把键。只断言「模板里出现过这几个字」
        # 是不够的：文件里任何一处出现都能满足它。
        text = (ROOT / ".env.example").read_text(encoding="utf-8")
        top_level = set(TOP_LEVEL_DEF.findall(text))
        expected = set(NOT_REACHABLE_KEYS) & top_level
        self.assertTrue(expected, "白名单与模板没有交集，判据已失效")

        flagged = keys_flagged_in_template(text)
        missing = sorted(expected - flagged)
        self.assertEqual(
            missing,
            [],
            f"以下键在 NOT_REACHABLE_KEYS 里，但定义行上方 {NOT_REACHABLE_WINDOW} 行内没有"
            f"「{NOT_REACHABLE_MARKER}」提示 —— 运维从模板里看不出这一行改了没用："
            + ", ".join(missing),
        )

        # 判据自检：窗口必须真的在起作用。提示语挪远了就必须不再算数，否则这条
        # 断言可以靠文件里任意一处出现那几个字通过。
        synthetic = "\n".join(
            ["# " + NOT_REACHABLE_MARKER] + ["FOO=1"] + ["# 无关注释"] * 10 + ["BAR=2"]
        )
        self.assertEqual(
            keys_flagged_in_template(synthetic),
            {"FOO"},
            "提示语窗口判定失效：远处的提示语不该算在 BAR 头上",
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
