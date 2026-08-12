import importlib.util
import sys
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parents[1]))


class AgentEvalTest(unittest.TestCase):
    def _load(self):
        module_path = Path(__file__).resolve().parents[1] / "run-agent-evals.py"
        spec = importlib.util.spec_from_file_location("run_agent_evals", module_path)
        module = importlib.util.module_from_spec(spec)
        sys.modules[spec.name] = module
        spec.loader.exec_module(module)
        return module

    def test_skips_without_api_base(self):
        module = self._load()
        with mock.patch.object(sys, "argv", ["run-agent-evals.py"]):
            self.assertEqual(module.main(), 0)

    def test_run_agent_task_success(self):
        module = self._load()
        calls = {"n": 0}

        def fake_http(method, url, token, body=None, timeout=30.0):
            if method == "POST":
                return {"run_id": "run-1"}
            calls["n"] += 1
            if calls["n"] == 1:
                return {"state": "running", "steps": []}
            return {"state": "completed", "steps": [
                {"type": "tool_call", "tool_name": "rag_query"},
                {"type": "final", "tool_name": ""},
            ]}

        with mock.patch.object(module, "http_json", side_effect=fake_http):
            result = module.run_agent_task("http://x", "tok", "task", "agent-001")
        self.assertEqual(result["state"], "completed")
        self.assertEqual(result["steps"], 2)
        self.assertEqual(result["tool_calls"], ["rag_query"])

    def test_run_agent_task_create_failure(self):
        module = self._load()
        with mock.patch.object(module, "http_json", return_value={"error": "boom"}):
            result = module.run_agent_task("http://x", "tok", "task", "agent-x")
        self.assertEqual(result["state"], "create_failed")


if __name__ == "__main__":
    unittest.main()
