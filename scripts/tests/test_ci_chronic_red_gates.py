"""The CI gates that were red on every push, for reasons unrelated to the code.

Two of the jobs in `.github/workflows/ci.yml` had never passed, and neither
failure was visible from the code under test -- both were properties of the job
that ran it, which is why every local run looked fine:

  - `eval` runs `scripts/tests` on a runner that installs Python dependencies
    only. One contract in that directory drives a node harness that needs
    `web/node_modules/typescript`, so it died with MODULE_NOT_FOUND. A failing
    step skips the steps after it, so `Run deterministic eval` -- the gate the
    job is named after -- had never run either.
  - `observability` runs `promtool check config`, which reads every file the
    config references. `prometheus.yml` points at two runtime secrets under
    `/run/secrets` that a runner does not have.

These tests pin the two properties that were missing rather than the two
symptoms: a gated contract must have something that opens its gate, and the
check that reads runtime secrets must be handed placeholders derived from the
config it is checking.
"""
import json
import re
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
TESTS_DIR = ROOT / "scripts" / "tests"
WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"
WEB_PACKAGE = ROOT / "web" / "package.json"
PROMETHEUS = ROOT / "infrastructure" / "prometheus.yml"

SSE_TEST = TESTS_DIR / "test_query_sse_client.py"
SSE_HARNESS = TESTS_DIR / "query_sse_client_harness.cjs"
SSE_GATE = "RUN_QUERY_SSE_HARNESS"

PROMTOOL_STEP_NAME = "- name: Validate Prometheus config and alert rules"

# Deliberately more permissive than the pattern the CI step greps with. If the
# step's pattern is narrower than this one, the two sets differ and the coverage
# test below fails; a reference regex identical to the step's own pattern would
# be comparing it with itself and would pass on any input.
REFERENCED_SECRET_RE = r"/run/secrets/[^\s\"'#]+"

# Gates that are opened by hand on purpose: the contract needs something a runner
# does not have, and its skip message says how to open it. Anything gated and
# *not* listed here is the defect this file exists to catch -- it skips on every
# run and nothing reports it as missing. Listing it here is a claim, and the
# claim is checked in both directions below: an entry that CI starts opening
# fails, so this cannot quietly become a place to park a forgotten gate.
MANUAL_ONLY_GATES = {
    "BACKUP_STACK_LIVE_TEST": (
        "test_backup_restore_contract.py runs backup-stack.sh against a deployed "
        "stack; there is none in CI"
    ),
}


def job_block(job: str) -> str:
    """The text of a job, up to the next job at the same level."""
    text = WORKFLOW.read_text(encoding="utf-8")
    start = text.index(f"\n  {job}:\n")
    rest = text[start + 1:]
    match = re.search(r"\n  [a-z][a-z0-9-]*:\n", rest[1:])
    return rest if match is None else rest[: match.start() + 1]


def step_block(job: str, name: str) -> str:
    """The text of one step, up to the next step in the same job."""
    body = job_block(job)
    start = body.index(name)
    rest = body[start:]
    match = re.search(r"\n {6}- name: ", rest)
    return rest if match is None else rest[: match.start()]


def gate_variables() -> dict:
    """Every env var a `skipUnless` in scripts/tests reads, and the files reading it.

    Parsed by matching parentheses rather than by regex over the whole call, so a
    gate whose message contains a bracket still parses.
    """
    gates: dict = {}
    for path in sorted(TESTS_DIR.glob("test_*.py")):
        source = path.read_text(encoding="utf-8")
        for match in re.finditer(r"skipUnless\(", source):
            depth = 1
            index = match.end()
            while index < len(source) and depth:
                if source[index] == "(":
                    depth += 1
                elif source[index] == ")":
                    depth -= 1
                index += 1
            call = source[match.end():index]
            for var in re.findall(r'os\.environ\.get\(\s*"([A-Z0-9_]+)"', call):
                gates.setdefault(var, set()).add(path.name)
    return gates


class AGatedContractMustHaveSomethingThatOpensTheGate(unittest.TestCase):
    """A contract that skips itself and is never enabled is coverage that is not there.

    `test_query_sse_client.py` was the other failure mode -- it ran and failed
    instead of skipping -- but the same mistake one step further along produces
    this one: gate the contract, forget to enable the gate anywhere, and the
    suite reports green while the contract never executes.
    """

    def test_every_gate_variable_is_enabled_somewhere_in_ci(self):
        enabled = WORKFLOW.read_text(encoding="utf-8") + WEB_PACKAGE.read_text(
            encoding="utf-8"
        )
        gates = gate_variables()
        self.assertTrue(
            gates,
            "no `skipUnless` gate was found in scripts/tests; this assertion would "
            "otherwise pass on an empty set and guard nothing",
        )
        for var, files in sorted(gates.items()):
            if var in MANUAL_ONLY_GATES:
                continue
            with self.subTest(variable=var):
                if not re.search(rf"{var}[=:] ?[\"']?1[\"']?", enabled):
                    self.fail(
                        f"{var} is read by {sorted(files)} but nothing in ci.yml or "
                        f"web/package.json sets it to 1, so that contract is skipped "
                        f"on every run and nothing reports it as missing. Either "
                        f"enable it where its dependency exists, or add it to "
                        f"MANUAL_ONLY_GATES with the reason it cannot run in CI."
                    )

    def test_a_gate_listed_as_manual_is_not_actually_opened_by_ci(self):
        enabled = WORKFLOW.read_text(encoding="utf-8") + WEB_PACKAGE.read_text(
            encoding="utf-8"
        )
        for var, reason in sorted(MANUAL_ONLY_GATES.items()):
            with self.subTest(variable=var):
                if re.search(rf"{var}[=:] ?[\"']?1[\"']?", enabled):
                    self.fail(
                        f"{var} is listed as manual-only ({reason}) but CI now sets "
                        f"it; remove the entry so the list keeps meaning something"
                    )


class TheNodeHarnessContractRunsWhereNodeModulesExist(unittest.TestCase):
    """The contract moved jobs; it did not get skipped into silence."""

    def test_the_harness_is_what_needs_web_node_modules(self):
        # The reason the contract cannot live in the `eval` job.
        self.assertIn("web/node_modules", SSE_HARNESS.read_text(encoding="utf-8"))

    def test_the_eval_job_neither_installs_node_dependencies_nor_opens_the_gate(self):
        eval_job = job_block("eval")
        self.assertNotIn("npm ci", eval_job)
        self.assertNotIn(SSE_GATE, eval_job)

    def test_the_web_job_installs_node_dependencies_and_runs_the_contract(self):
        web_job = job_block("web")
        self.assertIn("npm ci", web_job)
        self.assertIn("npm run test:sse", web_job)

    def test_the_npm_script_opens_the_same_gate_the_contract_reads(self):
        scripts = json.loads(WEB_PACKAGE.read_text(encoding="utf-8"))["scripts"]
        set_vars = set(re.findall(r"([A-Z0-9_]+)=1", scripts["test:sse"]))
        self.assertEqual(
            set_vars,
            {SSE_GATE},
            "`npm run test:sse` must set exactly the variable the contract reads; "
            "if the two names drift the contract skips in the one job that could "
            "have run it",
        )
        self.assertIn(f'os.environ.get("{SSE_GATE}")', SSE_TEST.read_text(encoding="utf-8"))


class PromtoolIsHandedPlaceholdersDerivedFromTheConfig(unittest.TestCase):
    """`check config` validates referenced files, so it needs those files to exist."""

    def test_the_config_still_references_runtime_secrets(self):
        referenced = re.findall(REFERENCED_SECRET_RE, PROMETHEUS.read_text(encoding="utf-8"))
        self.assertTrue(
            referenced,
            "prometheus.yml references no /run/secrets path, so the placeholder "
            "step is now vacuous and this class guards nothing",
        )

    def test_the_step_derives_placeholders_from_the_config(self):
        step = step_block("observability", PROMTOOL_STEP_NAME)
        self.assertIn(":/run/secrets:ro", step)
        pattern = re.search(r"grep -oE '([^']+)'", step)
        self.assertIsNotNone(
            pattern,
            "the step must derive the placeholder paths from prometheus.yml; a "
            "hardcoded list silently stops covering a secret added later",
        )

    def test_the_derivation_pattern_covers_every_referenced_secret(self):
        step = step_block("observability", PROMTOOL_STEP_NAME)
        pattern = re.search(r"grep -oE '([^']+)'", step).group(1)
        text = PROMETHEUS.read_text(encoding="utf-8")
        self.assertEqual(
            set(re.findall(pattern, text)),
            set(re.findall(REFERENCED_SECRET_RE, text)),
            f"the pattern {pattern!r} used by the CI step does not match every "
            f"/run/secrets path prometheus.yml references, so a placeholder for "
            f"one of them is never created and `check config` fails on a file the "
            f"step was supposed to supply",
        )

    def test_the_step_does_not_silence_the_check_with_syntax_only(self):
        # If --syntax-only is ever the right call, delete this assertion and say
        # so in the step's comment: it stops validating the rule files, TLS files
        # and credential files the config references, which is most of what this
        # step is for.
        step = step_block("observability", PROMTOOL_STEP_NAME)
        self.assertNotIn("--syntax-only", step)


if __name__ == "__main__":
    unittest.main()
