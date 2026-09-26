import os
import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HARNESS = ROOT / "scripts" / "tests" / "query_sse_client_harness.cjs"

# The harness transpiles `web/lib/querySSE.ts` with the TypeScript compiler out
# of `web/node_modules`. The `eval` job installs Python dependencies only, so on
# a fresh runner that directory does not exist and the harness dies with
# `Cannot find module '.../web/node_modules/typescript'`. Because it sat in the
# same file as a text assertion that needs nothing, it made the whole `eval` job
# red on every push -- and since a failing step skips the ones after it, the
# deterministic eval below it never ran either.
#
# Gated the same way as the reauthentication contract (RUN_WEB_HTTP_CONTRACT)
# rather than skipped on a missing directory: the `web` job installs
# node_modules and runs this through `npm run test:sse`, so the coverage moves
# to where the dependency exists instead of disappearing. A directory-existence
# check would let a runner that forgot to install node_modules skip the
# contract and still report green.
HARNESS_READY = os.environ.get("RUN_QUERY_SSE_HARNESS") == "1"


class QuerySSEClientTests(unittest.TestCase):
    @unittest.skipUnless(
        HARNESS_READY,
        "set RUN_QUERY_SSE_HARNESS=1 where web/node_modules exists (`npm run test:sse`)",
    )
    def test_query_sse_returns_after_done_or_error_without_waiting_for_body_eof(self):
        api_client = (ROOT / "web" / "lib" / "apiClient.ts").read_text(encoding="utf-8")
        self.assertIn('from "./querySSE"', api_client)
        self.assertIn("consumeQuerySSEStream(resp.body, handlers)", api_client)

        proc = subprocess.run(
            ["node", str(HARNESS)],
            cwd=ROOT,
            capture_output=True,
            text=True,
            timeout=10,
            check=False,
        )
        self.assertEqual(proc.returncode, 0, proc.stdout + proc.stderr)
        self.assertIn("ok", proc.stdout)

    def test_qa_page_renders_completed_marker_after_sse_done(self):
        page = (ROOT / "web" / "app" / "(app)" / "qa" / "page.tsx").read_text(encoding="utf-8")
        self.assertIn('onDone: (m) => {', page)
        self.assertIn("typeof m.answer === \"string\"", page)
        self.assertIn("onReplace: (text) => setAnswer(text)", page)
        self.assertIn('setStatus("done")', page)
        self.assertIn('setPhase("回答已完成")', page)
        self.assertIn('{status === "done" && <span', page)
        self.assertIn("回答已完成</span>", page)


if __name__ == "__main__":
    unittest.main()
