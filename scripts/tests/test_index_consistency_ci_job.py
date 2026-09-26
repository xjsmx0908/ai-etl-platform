"""The `index-consistency` job and the eval runner must agree on one stack identity.

The job was assembled from the same primitives the eval job uses and was never
observed to run. It guessed three values the runner decides, and each guess
produced a *wrong answer* rather than an error:

  - the bootstrap admin is `eval-admin` in a per-run tenant, not `admin`; the
    login answered 401, so the check never ran at all;
  - a freshly built stack's Qdrant collection is `documents` while the
    long-running demo tenant's is `documents-v2`; the check asked for the demo
    name, got a 404 on every document, and reported 47 `probe_failed` findings --
    a red that says nothing about cross-layer consistency;
  - `docker compose` without COMPOSE_FILE / COMPOSE_PROJECT_NAME resolves against
    the *default* project, which on a host that also runs the demo stack answers
    with the demo stack's port, and on a bare runner answers nothing.

The fix is that the runner writes `stack-descriptor.json` and the job reads it.
These tests pin that contract mechanically: the consumer's key names must exist in
the producer's output, and neither side may re-declare a value the other owns.
"""
import importlib.util
import json
import re
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
RUNNER = ROOT / "scripts" / "run-evals.py"
CHECK = ROOT / "scripts" / "check-index-consistency.py"
WORKFLOW = ROOT / ".github" / "workflows" / "ci.yml"

JOB_NAME = "index-consistency"

# `run-evals.py` imports its sibling `judge_eval`, so the scripts directory has to
# be importable before the module can be loaded.
sys.path.insert(0, str(ROOT / "scripts"))


def load_runner():
    spec = importlib.util.spec_from_file_location("run_evals_for_ci_job_test", RUNNER)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


def job_block() -> str:
    """The text of the `index-consistency` job, up to the next job at the same level."""
    text = WORKFLOW.read_text(encoding="utf-8")
    start = text.index(f"\n  {JOB_NAME}:\n")
    rest = text[start + 1:]
    # A sibling job starts at two-space indentation with a bare `name:` key.
    match = re.search(r"\n  [a-z][a-z0-9-]*:\n", rest[1:])
    return rest if match is None else rest[: match.start() + 1]


class TheJobReadsWhatTheRunnerWrites(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.runner = load_runner()
        cls.job = job_block()

    def test_the_job_exists_and_sets_the_compose_project_at_job_level(self):
        # Job-level `env:` is the point: every step below runs `docker compose`,
        # and a step that does not inherit these resolves against another project.
        header = self.job.split("steps:", 1)[0]
        self.assertIn("COMPOSE_FILE: docker-compose.yml:docker-compose.eval.yml", header)
        self.assertIn("COMPOSE_PROJECT_NAME: ai-etl-consistency", header)
        self.assertIn("STACK_DESCRIPTOR:", header)

    def test_every_key_the_job_reads_is_one_the_runner_writes(self):
        # Every `jq` call that reads the descriptor, in either of the two shapes
        # the job uses: `jq -r .key "$STACK_DESCRIPTOR"` and
        # `jq -c '{username: .admin_username, ...}' "$STACK_DESCRIPTOR"`.
        #
        # The key charset matters. A first version captured `[a-z_]+`, which does
        # not match a digit -- so `jq -r .store_collection_v2` was not seen as a
        # key at all and the test stayed green on a typo it existed to catch.
        referenced = set()
        for call in re.findall(r"jq\b[^\n]*\$STACK_DESCRIPTOR", self.job):
            referenced.update(re.findall(r"\.([A-Za-z0-9_]+)", call))
        self.assertTrue(referenced, "the job reads no key from the descriptor")
        # The four the descriptor exists to carry; reading fewer means the job
        # went back to knowing something it should have asked for.
        self.assertTrue(
            {"admin_username", "admin_password", "store_collection", "es_index"} <= referenced,
            f"the job stopped reading part of the descriptor: {sorted(referenced)}",
        )
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "stack-descriptor.json"
            self.runner.write_stack_descriptor(
                path,
                tenant_id="tenant-eval-test",
                admin_username=self.runner.EVAL_ADMIN_USERNAME,
                admin_password=self.runner.EVAL_ADMIN_PASSWORD,
                env={"STORE_COLLECTION": "documents", "ES_INDEX": "documents_text"},
            )
            written = json.loads(path.read_text(encoding="utf-8"))
        missing = sorted(referenced - set(written))
        self.assertEqual(missing, [], f"the job reads keys the runner never writes: {missing}")

    def test_the_descriptor_carries_the_names_the_check_is_pointed_at(self):
        # Without these two the check falls back to its demo-tenant defaults and
        # the run is red for a reason that is not a consistency problem.
        self.assertIn('--collection "${collection}"', self.job)
        self.assertIn('--es-index "${es_index}"', self.job)
        self.assertIn('collection="$(jq -r .store_collection "$STACK_DESCRIPTOR")"', self.job)
        self.assertIn('es_index="$(jq -r .es_index "$STACK_DESCRIPTOR")"', self.job)

    def test_the_check_still_defaults_to_the_demo_tenant(self):
        # The defaults are correct for the stack they were written for; the job
        # overriding them is what makes them safe to keep.
        source = CHECK.read_text(encoding="utf-8")
        self.assertIn('DEFAULT_COLLECTION = "documents-v2"', source)
        self.assertIn('DEFAULT_ES_INDEX = "documents_text_v2"', source)


class NothingIsDeclaredTwice(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.job = job_block()
        cls.runner_source = RUNNER.read_text(encoding="utf-8")

    def test_the_job_does_not_re_declare_the_bootstrap_admin(self):
        # The 401 came from this workflow guessing `admin`/`admin`.
        self.assertNotIn("BOOTSTRAP_ADMIN_USERNAME", self.job)
        self.assertNotIn("BOOTSTRAP_ADMIN_PASSWORD", self.job)
        self.assertNotIn('"username":"admin"', self.job)

    def test_the_bootstrap_password_has_exactly_one_definition(self):
        # Two copies inside the runner is how the workflow acquired a third.
        self.assertEqual(self.runner_source.count("eval-admin-password-2026"), 1)
        self.assertEqual(self.runner_source.count('"eval-admin"'), 1)

    def test_the_descriptor_is_written_after_the_login_it_describes(self):
        # A descriptor naming an admin the stack rejects is worse than none: the
        # next step then fails on credentials instead of on "the stack is not up".
        login = self.runner_source.index("admin_token = login_eval_user(api_base, admin_username, admin_password)")
        descriptor = self.runner_source.index("write_stack_descriptor(\n            descriptor_path,")
        self.assertLess(login, descriptor)


class TheProbeFailureSaysWhichLayerFailed(unittest.TestCase):
    def test_a_probe_failure_names_the_layer_and_the_target(self):
        # Measured: a wrong --collection produced 47 findings whose entire text
        # was `HTTP 404 Not Found`, which reads as a consistency problem.
        spec = importlib.util.spec_from_file_location("check_index_consistency_for_test", CHECK)
        module = importlib.util.module_from_spec(spec)
        assert spec.loader is not None
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)

        class FakeError(Exception):
            code = 404
            reason = "Not Found"

        finding = module.probe_failure("qdrant", "doc-1", "collection documents-v2", FakeError())
        self.assertEqual(finding["kind"], "probe_failed")
        self.assertEqual(finding["layer"], "qdrant")
        self.assertIn("documents-v2", finding["detail"])

    def test_the_check_probes_each_layer_separately(self):
        source = CHECK.read_text(encoding="utf-8")
        for layer in ("qdrant", "elasticsearch", "chunks endpoint"):
            self.assertIn(f'probe_failure("{layer}"', source)


class TheSeedFailureNamesTheRealProblem(unittest.TestCase):
    """The seed step's failure has to point at the stack, not at a parser.

    Measured on a real runner: the eval stack has never come up in CI, and what
    the job reported was `could not parse query-api host port: ''` -- because
    `docker compose up` was run without `check=True`, so its error was discarded
    and the only surviving symptom was an empty port string.
    """

    @classmethod
    def setUpClass(cls):
        cls.runner = load_runner()

    def test_a_missing_query_api_port_says_the_stack_did_not_come_up(self):
        with self.assertRaises(self.runner.EvalRunnerError) as caught:
            self.runner.api_base_from_compose_port("")
        self.assertIn("stack did not come up", str(caught.exception))

    def test_compose_up_is_checked_so_its_error_survives(self):
        source = RUNNER.read_text(encoding="utf-8")
        call = re.search(
            r'run_cmd\(\s*\["docker", "compose", "up", "-d", "--build"\][^)]*\)', source, re.S
        )
        self.assertIsNotNone(call, "the compose up call moved; update this test")
        self.assertIn("check=True", call.group(0))

    def test_the_job_publishes_the_seed_output_when_it_fails(self):
        job = job_block()
        # `tee` keeps the output around; the failure step turns it into an
        # annotation, which is the only one of the available channels that is
        # readable through the checks API without a token.
        self.assertIn("tee /tmp/seed.log", job)
        self.assertIn("if: failure()", job)
        self.assertIn("::error", job)


class TheJobIsAllowedToBlockAMerge(unittest.TestCase):
    """Green is worth nothing if a red is not allowed to stop anything.

    This job existed from the commit that introduced it until 2026-09-26 without
    ever passing, and nobody noticed, because `required-checks` did not list it.
    A job that runs, fails and is not required is a signal with no consumer.
    """

    def test_required_checks_waits_for_this_job(self):
        text = WORKFLOW.read_text(encoding="utf-8")
        block = text[text.index("\n  required-checks:\n"):]
        needs = block[block.index("\n    needs:\n"): block.index("\n    steps:")]
        self.assertIn(
            "\n      - index-consistency",
            needs,
            "the consistency job is not in `required-checks.needs`, so it can go red "
            "without stopping anything",
        )

    def test_required_checks_enforces_the_result(self):
        text = WORKFLOW.read_text(encoding="utf-8")
        enforcement = text[text.index("- name: Enforce all required jobs passed"):]
        self.assertIn(
            '[ "${{ needs.index-consistency.result }}" = "success" ] || exit 1',
            enforcement,
            "being in `needs` only makes the job wait for it; without this line a "
            "red consistency job still leaves Required Checks green",
        )


if __name__ == "__main__":
    unittest.main()
