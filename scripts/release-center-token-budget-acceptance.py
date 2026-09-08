#!/usr/bin/env python3
"""Verify real-model Review Agent token budget termination through public HTTP."""

from __future__ import annotations

import argparse
import importlib.util
import json
import os
import sys
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


def require_real_model() -> dict[str, str]:
    endpoint = os.environ.get("LLM_ENDPOINT", "").strip()
    model = os.environ.get("LLM_MODEL", "").strip()
    planner = os.environ.get("AGENT_PLANNER_TYPE", "").strip().lower()
    budget = os.environ.get("AGENT_REVIEW_MAX_TOKEN_BUDGET", "").strip()
    if not endpoint or not model:
        raise AcceptanceError("LLM_ENDPOINT and LLM_MODEL are required for real-model acceptance")
    if planner != "auto":
        raise AcceptanceError("AGENT_PLANNER_TYPE must be auto for real-model acceptance")
    lowered = f"{endpoint} {model}".lower()
    if "mock" in lowered or "localhost:18080" in lowered or "127.0.0.1:18080" in lowered:
        raise AcceptanceError("mock model endpoints are not accepted by this gate")
    try:
        parsed_budget = int(budget)
    except ValueError as exc:
        raise AcceptanceError("AGENT_REVIEW_MAX_TOKEN_BUDGET must be an integer") from exc
    if parsed_budget < 1:
        raise AcceptanceError("AGENT_REVIEW_MAX_TOKEN_BUDGET must be positive")
    return {"model": model, "budget": str(parsed_budget)}


def run(api_url: str, report_path: Path, timeout: int) -> None:
    model_config = require_real_model()
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
    space = "release-center-token-budget"
    client.request("POST", "/v1/knowledge-spaces", admin, {"id": space, "name": "Token Budget", "kind": "production"}, (201, 409))
    document_id = "rc-token-budget-" + uuid.uuid4().hex[:10]
    client.upload(admin, document_id, space, "Token budget termination acceptance document", owner="Token Budget Acceptance")

    def task_done() -> bool:
        task = client.request("GET", f"/v1/tasks/{document_id}", admin)
        if task.get("status") == "failed":
            raise AcceptanceError(f"ingestion failed: {sanitize(task)}")
        return task.get("status") == "completed"

    functional_runner = functional.Runner(api_url, report_path, timeout)
    functional_runner.wait(task_done, "token budget ingestion")

    def review_detail() -> Any:
        items = client.request("GET", "/v1/release-center/requests", admin).get("items", [])
        request = next((item for item in items if item.get("document_id") == document_id), None)
        if not request:
            return False
        detail = client.request("GET", f"/v1/release-center/requests/{request['request_id']}", admin)
        if detail.get("review", {}).get("status") != "failed":
            return False
        return request, detail

    request, detail = functional_runner.wait(review_detail, "failed token budget review")
    review = detail.get("review", {})
    if request.get("state") != "manual_exception":
        raise AcceptanceError(f"budget exhaustion did not create manual exception: {sanitize(request)}")
    if review.get("recommendation") != "manual_review" or review.get("risk_level") != "high":
        raise AcceptanceError(f"budget exhaustion was not failed closed: {sanitize(review)}")
    run_id = str(review.get("agent_run_id") or "").strip()
    if not run_id:
        raise AcceptanceError("failed review did not retain agent_run_id")
    standalone_review = client.request(
        "GET", f"/v1/release-center/review-reports/{review.get('review_id')}", admin
    ).get("review", {})
    if standalone_review != review:
        raise AcceptanceError("standalone review report does not match request detail")
    run_snapshot = client.request("GET", f"/v1/agent/runs/{run_id}", admin)
    if run_snapshot.get("state") != "failed" or run_snapshot.get("error") != "token_budget_exceeded":
        raise AcceptanceError(f"agent run did not fail on token budget: {sanitize(run_snapshot)}")
    used = int(run_snapshot.get("tokens_used") or 0)
    maximum = int(run_snapshot.get("max_token_budget") or 0)
    if maximum != int(model_config["budget"]) or used <= maximum:
        raise AcceptanceError(f"cumulative token accounting is invalid: {sanitize(run_snapshot)}")
    usage_steps = [
        step.get("planner_usage", {})
        for step in run_snapshot.get("steps", [])
        if isinstance(step, dict) and step.get("planner_usage")
    ]
    persisted_usage = sum(int(item.get("prompt_tokens") or 0) + int(item.get("completion_tokens") or 0) for item in usage_steps)
    if not usage_steps or persisted_usage != used:
        raise AcceptanceError(f"planner usage was not persisted per step: {sanitize(run_snapshot)}")

    payload = sanitize(
        {
            "schema_version": "1.0",
            "suite": "release-center-token-budget-acceptance",
            "status": "passed",
            "started_at": started_at.isoformat(),
            "finished_at": datetime.now(timezone.utc).isoformat(),
            "environment": {
                "observation_seam": "public HTTP",
                "model_backend": "real-model",
                "model": model_config["model"],
                "budget": maximum,
            },
            "scenario": {
                "document_id": document_id,
                "request_id": request.get("request_id"),
                "review_id": review.get("review_id"),
                "agent_run_id": run_id,
                "request_state": request.get("state"),
                "review_status": review.get("status"),
                "review_recommendation": review.get("recommendation"),
                "review_risk": review.get("risk_level"),
                "run_state": run_snapshot.get("state"),
                "run_error": run_snapshot.get("error"),
                "tokens_used": used,
                "max_token_budget": maximum,
                "planner_usage_steps": len(usage_steps),
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
        print(f"[release-center-token-budget] FAIL: {sanitize(str(exc))}", file=sys.stderr)
        return 1
    print(f"[release-center-token-budget] PASS: report={args.report}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
