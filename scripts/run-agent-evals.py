#!/usr/bin/env python3
"""Agent task evaluation: run a small task set through the Agent API and report
success rate, average step count, and tool usage.

Requires a running Query API with Agent enabled (AGENT_PLANNER_TYPE=llm) and a
working LLM. Skipped (exit 0 with a notice) when --api-base is empty and no
REAL_API_BASE env is set.

Metrics produced:
  - task success rate
  - average steps per run
  - tool distribution (which tools were called)
  - runs that hit max steps / failed / timed out
"""

from __future__ import annotations

import argparse
import json
import sys
import time
from typing import Any, Dict, List
from urllib import error as urllib_error
from urllib import request

DEFAULT_TASKS: List[Dict[str, Any]] = [
    {"id": "agent-001", "task": "查询知识库：上传文件的大小上限是多少？"},
    {"id": "agent-002", "task": "知识库支持哪些文档格式？"},
    {"id": "agent-003", "task": "文档权限分哪几级？"},
    {"id": "agent-004", "task": "检索失败的文档会怎样处理？"},
    {"id": "agent-005", "task": "知识库缓存是怎么工作的？"},
]

TERMINAL_STATES = {"completed", "failed", "cancelled"}
POLL_SECONDS = 2.0
MAX_WAIT_SECONDS = 90


def http_json(method: str, url: str, token: str, body: bytes | None = None, timeout: float = 30.0) -> Dict[str, Any]:
    req = request.Request(url=url, data=body, method=method)
    req.add_header("Authorization", f"Bearer {token}")
    if body:
        req.add_header("Content-Type", "application/json")
    try:
        with request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read().decode("utf-8")
            return json.loads(raw) if raw else {}
    except urllib_error.HTTPError as e:
        raw = e.read().decode("utf-8", errors="ignore")
        try:
            return {"error": raw, "status": e.code}
        except Exception:
            return {"error": raw, "status": e.code}


def run_agent_task(api_base: str, token: str, task: str, task_id: str) -> Dict[str, Any]:
    create = http_json("POST", f"{api_base}/v1/agent/runs", token,
                       json.dumps({"task": task}).encode("utf-8"))
    run_id = create.get("run_id") or create.get("id")
    if not run_id:
        return {"task_id": task_id, "state": "create_failed", "error": str(create)[:200]}

    deadline = time.time() + MAX_WAIT_SECONDS
    last: Dict[str, Any] = {}
    while time.time() < deadline:
        last = http_json("GET", f"{api_base}/v1/agent/runs/{run_id}", token)
        state = last.get("state", "")
        if state in TERMINAL_STATES:
            break
        time.sleep(POLL_SECONDS)
    else:
        return {"task_id": task_id, "state": "timeout", "run_id": run_id, "steps": len(last.get("steps", []))}

    steps = last.get("steps", [])
    tool_calls = [s.get("tool_name") for s in steps if s.get("type") == "tool_call"]
    return {
        "task_id": task_id,
        "state": state,
        "run_id": run_id,
        "steps": len(steps),
        "tool_calls": tool_calls,
        "error": last.get("error", ""),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Run Agent task evaluation")
    parser.add_argument("--api-base", default="", help="Query API base URL")
    parser.add_argument("--token", default="", help="JWT with agent+query scope")
    parser.add_argument("--task-set", default="default", help="task set id (only 'default' built in)")
    args = parser.parse_args()

    api_base = (args.api_base or __import__("os").getenv("REAL_API_BASE", "")).strip().rstrip("/")
    if not api_base:
        print("[agent-eval] no --api-base; skipping live agent eval (exit 0)")
        return 0
    if not args.token:
        print("[agent-eval] --token required for live agent eval")
        return 2

    results = [run_agent_task(api_base, args.token, t["task"], t["id"]) for t in DEFAULT_TASKS]
    succeeded = [r for r in results if r["state"] == "completed"]
    failed = [r for r in results if r["state"] != "completed"]
    steps = [r["steps"] for r in results]

    tool_counts: Dict[str, int] = {}
    for r in results:
        for tool in r.get("tool_calls", []):
            tool_counts[tool] = tool_counts.get(tool, 0) + 1

    print("\n=== Agent Eval Report ===")
    print(f"Tasks: {len(results)}")
    print(f"Success: {len(succeeded)}/{len(results)} ({len(succeeded)/len(results):.0%})")
    if steps:
        print(f"Avg steps: {sum(steps)/len(steps):.2f} (completed: {[r['steps'] for r in succeeded]})")
    print(f"Tool usage: {json.dumps(tool_counts, ensure_ascii=False)}")
    if failed:
        print("Failed:")
        for r in failed:
            print(f"  {r['task_id']}: {r['state']} {r.get('error', '')[:100]}")

    summary = {
        "total": len(results),
        "success": len(succeeded),
        "avg_steps": (sum(steps) / len(steps)) if steps else 0,
        "tool_usage": tool_counts,
        "results": results,
    }
    with open("docs/evals/reports/agent-eval.json", "w", encoding="utf-8") as f:
        json.dump(summary, f, ensure_ascii=False, indent=2)
    print("\nSaved: docs/evals/reports/agent-eval.json")
    return 0


if __name__ == "__main__":
    sys.exit(main())
