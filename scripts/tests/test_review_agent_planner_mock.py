import importlib.util
import json
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path


ROOT = Path(__file__).resolve().parents[2]


def load_mock():
    path = ROOT / "scripts" / "review-agent-planner-mock.py"
    spec = importlib.util.spec_from_file_location("review_agent_planner_mock", path)
    module = importlib.util.module_from_spec(spec)
    assert spec.loader is not None
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


class ReviewAgentPlannerMockTests(unittest.TestCase):
    def test_decides_required_tools_then_final_report(self):
        mock = load_mock()
        prompt = "Plan the next action for this durable run JSON: {\"steps\":[]}"
        first = json.loads(mock.next_review_decision(prompt))
        self.assertEqual(first["type"], "tool_call")
        self.assertEqual(first["tool_name"], "get_review_context")

        second_prompt = '{"steps":[{"tool_name":"get_review_context","state":"completed"}]}'
        second = json.loads(mock.next_review_decision(second_prompt))
        self.assertEqual(second["tool_name"], "get_exact_candidate_chunks")
        self.assertEqual(second["arguments"], {"offset": 0, "limit": 20})

        all_tools = {
            "steps": [
                {"tool_name": name, "state": "completed"}
                for name in mock.REVIEW_TOOLS
            ]
        }
        final = json.loads(mock.next_review_decision(json.dumps(all_tools)))
        self.assertEqual(final["type"], "final")
        report = json.loads(final["final"])
        self.assertEqual(report["status"], "completed")
        self.assertEqual(report["recommendation"], "publish")
        self.assertEqual(report["findings"], [])

    def test_hold_release_unblocks_waiter(self):
        mock = load_mock()
        with tempfile.NamedTemporaryFile(delete=False) as handle:
            hold = Path(handle.name)
        released = []

        def waiter():
            mock.wait_for_hold_release(hold, timeout=5)
            released.append(True)

        thread = threading.Thread(target=waiter)
        thread.start()
        time.sleep(0.3)
        self.assertTrue(thread.is_alive())
        hold.unlink()
        thread.join(timeout=5)
        self.assertFalse(thread.is_alive())
        self.assertEqual(released, [True])


if __name__ == "__main__":
    unittest.main()
