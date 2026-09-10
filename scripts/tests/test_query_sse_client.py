import subprocess
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
HARNESS = ROOT / "scripts" / "tests" / "query_sse_client_harness.cjs"


class QuerySSEClientTests(unittest.TestCase):
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
