#!/usr/bin/env python3
"""Verify Review Agent resume after query-api and redis-state Compose faults."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import subprocess
import sys
import time
import uuid
from datetime import datetime, timezone
from pathlib import Path
from typing import Any


ROOT = Path(__file__).resolve().parents[1]
FUNCTIONAL_DRIVER = ROOT / "scripts" / "release-center-functional-acceptance.py"
spec = importlib.util.spec_from_file_location("release_center_functional_acceptance", FUNCTIONAL_DRIVER)
if spec is None or spec.loader is None:
    raise RuntimeError("unable to load release-center HTTP client")
functional = importlib.util.module_from_spec(spec)
sys.modules[spec.name] = functional
spec.loader.exec_module(functional)

Client = functional.Client
AcceptanceError = functional.AcceptanceError
sanitize = functional.sanitize


class FatalAcceptanceError(AcceptanceError):
    pass


REQUIRED_TOOLS = (
    "get_review_context",
    "get_exact_candidate_chunks",
    "scan_sensitive_data",
    "scan_prompt_injection",
)


def public_run(run: dict[str, Any]) -> dict[str, Any]:
    steps = []
    for step in run.get("steps") or []:
        if not isinstance(step, dict):
            continue
        steps.append(
            {
                "index": step.get("index"),
                "type": step.get("type"),
                "state": step.get("state"),
                "tool_name": step.get("tool_name"),
                "idempotency_key": step.get("idempotency_key"),
            }
        )
    return {
        "id": run.get("id"),
        "state": run.get("state"),
        "error": run.get("error"),
        "step_count": len(run.get("steps") or []),
        "steps": steps,
    }


def completed_tool_steps(run: dict[str, Any]) -> list[dict[str, Any]]:
    steps = []
    for step in public_run(run).get("steps") or []:
        if step.get("type") == "tool_call" and step.get("state") == "completed" and step.get("tool_name"):
            steps.append(step)
    return steps


def compose_cmd(*args: str) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["docker", "compose", *args],
        check=False,
        cwd=ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
        text=True,
        timeout=60,
    )


def compose_service(action: str, service: str) -> None:
    result = compose_cmd(action, service)
    if result.returncode != 0:
        raise AcceptanceError(f"docker compose {action} {service} failed: {sanitize(result.stderr)}")


def redis_cli(*args: str) -> str:
    result = compose_cmd("exec", "-T", "redis-state", "redis-cli", *args)
    if result.returncode != 0:
        raise AcceptanceError(f"redis-cli {' '.join(args)} failed: {sanitize(result.stderr or result.stdout)}")
    return result.stdout


def wait_redis_state(timeout: int = 60) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = compose_cmd("exec", "-T", "redis-state", "redis-cli", "ping")
        if result.returncode == 0 and result.stdout.strip() == "PONG":
            return
        time.sleep(1)
    raise AcceptanceError("redis-state did not recover")


def load_review_runs() -> list[dict[str, Any]]:
    listing = redis_cli("KEYS", "agent:run:review-run-*")
    runs: list[dict[str, Any]] = []
    for line in listing.splitlines():
        key = line.strip()
        if not key.startswith("agent:run:review-run-"):
            continue
        raw = redis_cli("--raw", "GET", key).strip()
        if not raw:
            continue
        try:
            payload = json.loads(raw)
        except json.JSONDecodeError as exc:
            raise AcceptanceError(f"review run {key} is not JSON") from exc
        if isinstance(payload, dict):
            runs.append(payload)
    return runs


def release_planner_hold() -> None:
    hold = Path(os.environ.get("REVIEW_PLANNER_HOLD_PATH", "").strip())
    if str(hold):
        hold.unlink(missing_ok=True)


def lock_wait_seconds() -> float:
    raw = os.environ.get("AGENT_LOCK_TTL", "10s").strip().lower()
    if raw.endswith("s"):
        raw = raw[:-1]
    try:
        return max(1.0, float(raw)) + 2.0
    except ValueError:
        return 12.0


def wait_for(fn, description: str, timeout: int):
    deadline = time.monotonic() + timeout
    last_error = ""
    while time.monotonic() < deadline:
        try:
            result = fn()
            if result:
                return result
        except FatalAcceptanceError:
            raise
        except (AcceptanceError, KeyError, TypeError, ValueError, json.JSONDecodeError) as exc:
            last_error = str(exc)
        time.sleep(1)
    suffix = f": {sanitize(last_error)}" if last_error else ""
    raise AcceptanceError(f"timed out waiting for {description}{suffix}")


def run(api_url: str, report_path: Path, timeout: int) -> None:
    started_at = datetime.now(timezone.utc)
    client = Client(api_url)
    admin = client.request(
        "POST",
        "/v1/auth/login",
        body={
            "username": os.environ["BOOTSTRAP_ADMIN_USERNAME"],
            "password": os.environ["BOOTSTRAP_ADMIN_PASSWORD"],
        },
    )["token"]
    space = "release-center-review-recovery"
    client.request("POST", "/v1/knowledge-spaces", admin, {"id": space, "name": "Review Recovery", "kind": "production"}, (201, 409))
    document_id = "rc-review-recovery-" + uuid.uuid4().hex[:10]
    client.upload(admin, document_id, space, "Review recovery acceptance document", owner="Review Recovery Acceptance")

    def task_done() -> bool:
        task = client.request("GET", f"/v1/tasks/{document_id}", admin)
        if task.get("status") == "failed":
            raise AcceptanceError(f"ingestion failed: {sanitize(task)}")
        return task.get("status") == "completed"

    wait_for(task_done, "recovery ingestion", timeout)

    def persisted_partial_run() -> dict[str, Any] | bool:
        for item in load_review_runs():
            if item.get("state") in {"failed", "completed", "cancelled"}:
                raise FatalAcceptanceError(f"review run finished before fault injection: {sanitize(public_run(item))}")
            if completed_tool_steps(item):
                return item
        return False

    before = public_run(wait_for(persisted_partial_run, "persisted review tool step", timeout))
    first_step = completed_tool_steps(before)[0]
    compose_service("kill", "query-api")
    compose_service("stop", "redis-state")
    compose_service("start", "redis-state")
    wait_redis_state()
    after_redis = public_run(wait_for(lambda: next((item for item in load_review_runs() if item.get("id") == before["id"]), False), "redis restored review run", timeout))
    if after_redis["id"] != before["id"]:
        raise FatalAcceptanceError(f"redis restart changed review run id: {sanitize(after_redis)}")
    restored_steps = completed_tool_steps(after_redis)
    if not restored_steps or restored_steps[0].get("idempotency_key") != first_step.get("idempotency_key"):
        raise FatalAcceptanceError(f"redis restart lost the original review step: {sanitize({'before': before, 'after': after_redis})}")
    if after_redis.get("state") in {"failed", "completed", "cancelled"}:
        raise FatalAcceptanceError(f"redis restart finalized the review run too early: {sanitize(after_redis)}")
    time.sleep(lock_wait_seconds())
    release_planner_hold()
    compose_service("start", "query-api")

    def api_ready() -> bool:
        try:
            client.request("GET", "/healthz")
            return True
        except AcceptanceError:
            return False

    wait_for(api_ready, "query-api healthz", 90)

    def finished_review() -> Any:
        items = client.request("GET", "/v1/release-center/requests", admin).get("items", [])
        request = next((item for item in items if item.get("document_id") == document_id), None)
        if not request:
            return False
        detail = client.request("GET", f"/v1/release-center/requests/{request['request_id']}", admin)
        review = detail.get("review") or {}
        if not review.get("review_id"):
            return False
        return request, detail

    request, detail = wait_for(finished_review, "resumed review request", timeout)
    review = detail.get("review") or {}
    if review.get("agent_run_id") != before["id"]:
        raise AcceptanceError(f"resumed review used a different run: {sanitize({'before': before, 'review': review})}")
    if request.get("state") != "approval_pending" or review.get("status") != "completed" or review.get("recommendation") != "publish":
        raise AcceptanceError(f"resumed review was not a successful publish recommendation: {sanitize(detail)}")
    run_snapshot = public_run(client.request("GET", f"/v1/agent/runs/{before['id']}", admin))
    tool_names = [step.get("tool_name") for step in completed_tool_steps(run_snapshot)]
    if tool_names != list(REQUIRED_TOOLS):
        raise AcceptanceError(f"resumed review duplicated or skipped tools: {sanitize(run_snapshot)}")
    if completed_tool_steps(run_snapshot)[0].get("idempotency_key") != first_step.get("idempotency_key"):
        raise AcceptanceError(f"resumed review changed the original idempotency key: {sanitize(run_snapshot)}")
    if run_snapshot.get("state") != "completed":
        raise AcceptanceError(f"resumed review run did not complete: {sanitize(run_snapshot)}")

    payload = sanitize(
        {
            "schema_version": "1.0",
            "suite": "release-center-review-recovery-acceptance",
            "status": "passed",
            "started_at": started_at.isoformat(),
            "finished_at": datetime.now(timezone.utc).isoformat(),
            "environment": {
                "observation_seam": "public HTTP plus compose redis-state",
                "fault": ["kill query-api", "stop redis-state", "start redis-state", "start query-api"],
                "planner": "held review mock",
            },
            "scenario": {
                "document_id": document_id,
                "request_id": request.get("request_id"),
                "review_id": review.get("review_id"),
                "agent_run_id": before["id"],
                "request_state": request.get("state"),
                "review_status": review.get("status"),
                "review_recommendation": review.get("recommendation"),
                "before_fault": before,
                "after_redis_restore": after_redis,
                "after_resume": run_snapshot,
            },
        }
    )
    report_path.parent.mkdir(parents=True, exist_ok=True)
    report_path.write_text(json.dumps(payload, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--api-url", required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=300)
    args = parser.parse_args()
    try:
        run(args.api_url, args.report, args.timeout)
    except Exception as exc:
        print(f"[release-center-review-recovery] FAIL: {sanitize(str(exc))}", file=sys.stderr)
        return 1
    print(f"[release-center-review-recovery] PASS: report={args.report}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
