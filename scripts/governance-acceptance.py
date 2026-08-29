#!/usr/bin/env python3
"""Drive P2.4 governance acceptance through public HTTP interfaces."""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from dataclasses import dataclass, field
from datetime import datetime, timezone
from pathlib import Path
from typing import Any, Callable


SENSITIVE_KEYS = {
    "authorization",
    "cookie",
    "password",
    "token",
    "access_token",
    "refresh_token",
    "jwt_secret",
    "api_key",
}
BEARER_RE = re.compile(r"(?i)bearer\s+[A-Za-z0-9._~+/=-]+")


def decode_http_payload(raw: bytes) -> Any:
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError:
        return raw.decode("utf-8", errors="replace")


def encode_multipart(
    boundary: str,
    fields: dict[str, str],
    filename: str,
    content: str,
) -> bytes:
    parts: list[bytes] = []
    for name, value in fields.items():
        parts.extend(
            [
                f"--{boundary}\r\n".encode(),
                f'Content-Disposition: form-data; name="{name}"\r\n\r\n'.encode(),
                value.encode(),
                b"\r\n",
            ]
        )
    parts.extend(
        [
            f"--{boundary}\r\n".encode(),
            (
                'Content-Disposition: form-data; name="file"; '
                f'filename="{filename}"\r\n'
            ).encode(),
            b"Content-Type: text/plain\r\n\r\n",
            content.encode("utf-8"),
            b"\r\n",
            f"--{boundary}--\r\n".encode(),
        ]
    )
    return b"".join(parts)


def metric_value(metrics: str, series: str) -> float:
    for line in metrics.splitlines():
        if line.startswith(series + " "):
            try:
                return float(line.rsplit(" ", 1)[1])
            except ValueError:
                return 0.0
    return 0.0


def sanitize(value: Any) -> Any:
    """Return bounded report-safe data without credentials or HTTP secrets."""
    if isinstance(value, dict):
        return {
            str(key): sanitize(item)
            for key, item in value.items()
            if str(key).lower() not in SENSITIVE_KEYS
        }
    if isinstance(value, list):
        return [sanitize(item) for item in value[:50]]
    if isinstance(value, str):
        return BEARER_RE.sub("Bearer [REDACTED]", value)[:2000]
    return value


@dataclass
class Scenario:
    name: str
    started: float = field(default_factory=time.monotonic)
    status: str = "running"
    observations: list[dict[str, Any]] = field(default_factory=list)
    error: str = ""

    def observe(self, kind: str, **values: Any) -> None:
        self.observations.append(sanitize({"kind": kind, **values}))

    def finish(self) -> dict[str, Any]:
        return {
            "name": self.name,
            "status": self.status,
            "duration_ms": round((time.monotonic() - self.started) * 1000),
            "observations": self.observations,
            "error": self.error,
        }


class AcceptanceError(RuntimeError):
    pass


class Client:
    def __init__(self, base_url: str):
        self.base_url = base_url.rstrip("/")

    def request(
        self,
        method: str,
        path: str,
        *,
        token: str = "",
        body: Any = None,
        expected: tuple[int, ...] = (200,),
        headers: dict[str, str] | None = None,
    ) -> tuple[int, Any]:
        data = None
        request_headers = dict(headers or {})
        if token:
            request_headers["Authorization"] = f"Bearer {token}"
        if body is not None:
            data = json.dumps(body).encode("utf-8")
            request_headers["Content-Type"] = "application/json"
        request = urllib.request.Request(
            self.base_url + path,
            data=data,
            headers=request_headers,
            method=method,
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                status = response.status
                raw = response.read()
        except urllib.error.HTTPError as error:
            status = error.code
            raw = error.read()
        except OSError as error:
            raise AcceptanceError(f"{method} {path} unavailable: {error}") from error
        payload = decode_http_payload(raw)
        if status not in expected:
            raise AcceptanceError(
                f"{method} {path}: expected {expected}, got {status}: {sanitize(payload)}"
            )
        return status, payload

    def multipart_upload(
        self,
        token: str,
        *,
        filename: str,
        content: str,
        document_id: str,
        knowledge_space_id: str,
        idempotency_key: str,
    ) -> dict[str, Any]:
        boundary = "----governance-" + uuid.uuid4().hex
        fields = {
            "permission": "internal",
            "knowledge_space_id": knowledge_space_id,
            "doc_id": document_id,
            "doc_status": "active",
            "effective_date": "2026-08-29",
            "owner": "Enterprise Knowledge Office",
        }
        request = urllib.request.Request(
            self.base_url + "/v1/upload",
            data=encode_multipart(boundary, fields, filename, content),
            headers={
                "Authorization": f"Bearer {token}",
                "Content-Type": f"multipart/form-data; boundary={boundary}",
                "Idempotency-Key": idempotency_key,
            },
            method="POST",
        )
        try:
            with urllib.request.urlopen(request, timeout=30) as response:
                raw = response.read()
                status = response.status
        except urllib.error.HTTPError as error:
            status = error.code
            raw = error.read()
        payload = decode_http_payload(raw)
        if status != 202:
            raise AcceptanceError(f"upload expected 202, got {status}: {sanitize(payload)}")
        if not isinstance(payload, dict):
            raise AcceptanceError(f"upload returned invalid JSON: {sanitize(payload)}")
        return payload


class Runner:
    def __init__(self, api_url: str, worker_metrics_url: str, report_path: Path, timeout: int):
        self.client = Client(api_url)
        self.worker_metrics_url = worker_metrics_url
        self.report_path = report_path
        self.timeout = timeout
        self.scenarios: list[dict[str, Any]] = []
        self.started_at = datetime.now(timezone.utc)
        self.admin_token = ""
        self.reviewer_token = ""
        self.doc_id = "governance-policy"
        self.space_id = "governance-controlled"
        self.deletion_job_id = ""

    def compose(self, *args: str) -> None:
        command = ["docker", "compose", *args]
        result = subprocess.run(command, text=True, capture_output=True, check=False)
        if result.returncode:
            raise AcceptanceError(
                f"compose {' '.join(args)} failed: {sanitize(result.stderr or result.stdout)}"
            )

    def fetch_worker_metrics(self, required_series: str) -> str | bool:
        try:
            with urllib.request.urlopen(self.worker_metrics_url, timeout=10) as response:
                metrics = response.read().decode("utf-8", errors="replace")
        except OSError:
            return False
        return metrics if required_series in metrics else False

    def restore_deletion_dependencies(self) -> None:
        self.compose("start", "qdrant", "elasticsearch", "minio")
        # The acceptance-only MinIO tmpfs is intentionally reset when its
        # container restarts. Restarting the API exercises its normal startup
        # path, which waits for MinIO and recreates the configured bucket.
        self.compose("restart", "query-api")
        self.wait_until(
            "query API after deletion dependency recovery",
            lambda: self.client.request("GET", "/healthz")[0] == 200,
        )

    def wait_until(self, description: str, probe: Callable[[], Any]) -> Any:
        deadline = time.monotonic() + self.timeout
        last_error = ""
        while time.monotonic() < deadline:
            try:
                result = probe()
                if result:
                    return result
            except (AcceptanceError, OSError, KeyError, ValueError) as error:
                last_error = str(error)
            time.sleep(2)
        raise AcceptanceError(f"timed out waiting for {description}: {sanitize(last_error)}")

    def scenario(self, name: str, action: Callable[[Scenario], None]) -> None:
        scenario = Scenario(name)
        print(f"[governance] {name}", flush=True)
        try:
            action(scenario)
            scenario.status = "passed"
        except Exception as error:
            scenario.status = "failed"
            scenario.error = str(sanitize(str(error)))
            self.scenarios.append(scenario.finish())
            self.write_report("failed")
            raise
        self.scenarios.append(scenario.finish())
        self.write_report("running")

    def login(self, username: str, password: str) -> tuple[str, dict[str, Any]]:
        _, payload = self.client.request(
            "POST",
            "/v1/auth/login",
            body={"username": username, "password": password},
        )
        return payload["token"], sanitize(payload)

    def task_completed(self, token: str) -> dict[str, Any] | bool:
        _, task = self.client.request("GET", f"/v1/tasks/{self.doc_id}", token=token)
        if task.get("status") == "failed":
            raise AcceptanceError(f"ingestion failed: {sanitize(task)}")
        return task if task.get("status") == "completed" else False

    def query_sources(self, token: str) -> list[dict[str, Any]]:
        _, response = self.client.request(
            "POST",
            "/v1/query",
            token=token,
            body={
                "question": "What marker identifies the approved continuity policy?",
                "top_k": 5,
                "knowledge_space_id": self.space_id,
                "diagnostic_required_doc_ids": [self.doc_id],
                "retrieval_only": True,
            },
        )
        return response.get("retrieved_sources") or response.get("sources") or []

    @staticmethod
    def marker_summary(sources: list[dict[str, Any]]) -> dict[str, Any]:
        content = "\n".join(str(item.get("content", "")) for item in sources)
        return {
            "source_count": len(sources),
            "old_marker": "OLD_RELEASE_2026" in content,
            "new_marker": "NEW_RELEASE_2026" in content,
            "document_ids": sorted({str(item.get("doc_id", "")) for item in sources}),
        }

    def approve_current(self, requester: str, reviewer: str, scenario: Scenario) -> None:
        _, run = self.client.request(
            "POST",
            "/v1/agent/runs",
            token=requester,
            body={"workflow": "document_publication", "document_id": self.doc_id},
            expected=(201,),
        )
        if run.get("state") != "pending_approval":
            raise AcceptanceError(f"expected pending approval, got {sanitize(run)}")
        run_id = run["id"]
        _, approvals = self.client.request(
            "GET", f"/v1/agent/runs/{run_id}/approvals", token=requester
        )
        if len(approvals) != 1 or approvals[0].get("status") != "pending":
            raise AcceptanceError(f"expected one pending approval, got {sanitize(approvals)}")
        approval = approvals[0]
        if approval.get("requested_by") == "" or approval.get("requested_by") == approval.get("decided_by"):
            raise AcceptanceError("approval requester identity is invalid")
        _, decided = self.client.request(
            "POST",
            f"/v1/agent/runs/{run_id}/approve",
            token=reviewer,
            body={"approval_id": approval["id"], "reason": "P2.4 governance acceptance"},
        )
        if decided.get("state") != "completed":
            raise AcceptanceError(f"approval did not complete: {sanitize(decided)}")
        scenario.observe(
            "approval",
            run_id=run_id,
            approval_id=approval["id"],
            requested_by=approval.get("requested_by"),
            final_state=decided.get("state"),
        )

    def setup_and_recover_ingestion(self, scenario: Scenario) -> None:
        admin_user = os.environ["BOOTSTRAP_ADMIN_USERNAME"]
        admin_password = os.environ["BOOTSTRAP_ADMIN_PASSWORD"]
        reviewer_user = os.environ["GOVERNANCE_REVIEWER_USERNAME"]
        reviewer_password = os.environ["GOVERNANCE_REVIEWER_PASSWORD"]
        self.admin_token, login_view = self.login(admin_user, admin_password)
        scenario.observe("admin_login", status=200, subject=login_view.get("username", admin_user))
        _, reviewer = self.client.request(
            "POST",
            "/v1/users",
            token=self.admin_token,
            body={"username": reviewer_user, "password": reviewer_password, "role": "admin"},
            expected=(201,),
        )
        self.reviewer_token, _ = self.login(reviewer_user, reviewer_password)
        _, space = self.client.request(
            "POST",
            "/v1/knowledge-spaces",
            token=self.admin_token,
            body={"id": self.space_id, "name": "Governance Acceptance", "kind": "production"},
            expected=(201,),
        )
        self.compose("stop", "etl-worker", "kafka")
        upload = self.client.multipart_upload(
            self.admin_token,
            filename="governance-policy.txt",
            content=(
                "Governance continuity policy OLD_RELEASE_2026. "
                "The approved continuity marker is BLUE and remains authoritative until cutover."
            ),
            document_id=self.doc_id,
            knowledge_space_id=self.space_id,
            idempotency_key="governance-initial-v1",
        )
        scenario.observe("durable_upload", status=202, doc_id=upload.get("doc_id"), job_id=upload.get("job_id"))
        self.compose("stop", "query-api")
        self.compose("start", "kafka", "query-api", "etl-worker")
        self.wait_until("query API restart", lambda: self.client.request("GET", "/healthz")[0] == 200)
        task = self.wait_until("initial ingestion completion", lambda: self.task_completed(self.admin_token))
        scenario.observe("recovered_ingestion", status=task.get("status"), stage=task.get("stage"))
        scenario.observe("reviewer_created", user_id=reviewer.get("id"), role=reviewer.get("role"), space_id=space.get("id"))

    def independent_approval(self, scenario: Scenario) -> None:
        before = self.marker_summary(self.query_sources(self.admin_token))
        if before["source_count"] != 0:
            raise AcceptanceError(f"draft leaked before approval: {before}")
        scenario.observe("draft_query", **before)
        self.approve_current(self.admin_token, self.reviewer_token, scenario)
        after = self.wait_until(
            "approved old release visibility",
            lambda: (
                summary
                if (summary := self.marker_summary(self.query_sources(self.admin_token)))["old_marker"]
                else False
            ),
        )
        if after["new_marker"]:
            raise AcceptanceError(f"unexpected replacement marker: {after}")
        scenario.observe("published_query", **after)

    def replacement_continuity(self, scenario: Scenario) -> None:
        upload = self.client.multipart_upload(
            self.admin_token,
            filename="governance-policy.txt",
            content=(
                "Governance continuity policy NEW_RELEASE_2026. "
                "The approved continuity marker is GREEN after independent cutover."
            ),
            document_id=self.doc_id,
            knowledge_space_id=self.space_id,
            idempotency_key="governance-replacement-v2",
        )
        scenario.observe("replacement_upload", status=202, doc_id=upload.get("doc_id"), job_id=upload.get("job_id"))
        task = self.wait_until("replacement ingestion completion", lambda: self.task_completed(self.admin_token))
        summary = self.marker_summary(self.query_sources(self.admin_token))
        if not summary["old_marker"] or summary["new_marker"]:
            raise AcceptanceError(f"old release continuity failed: {summary}")
        scenario.observe("replacement_pending_query", task_status=task.get("status"), **summary)

    def approved_cutover(self, scenario: Scenario) -> None:
        self.approve_current(self.admin_token, self.reviewer_token, scenario)
        summary = self.wait_until(
            "approved replacement visibility",
            lambda: (
                current
                if (current := self.marker_summary(self.query_sources(self.admin_token)))["new_marker"]
                else False
            ),
        )
        if summary["old_marker"]:
            raise AcceptanceError(f"old release remained visible after cutover: {summary}")
        scenario.observe("cutover_query", **summary)

    def recoverable_deletion(self, scenario: Scenario) -> None:
        self.wait_until(
            "healthy deletion collector startup",
            lambda: self.fetch_worker_metrics("ai_etl_deletion_jobs"),
        )
        self.compose("pause", "etl-worker")
        _, accepted = self.client.request(
            "DELETE",
            f"/v1/documents/{self.doc_id}",
            token=self.admin_token,
            expected=(202,),
        )
        self.deletion_job_id = accepted["job_id"]
        summary = self.marker_summary(self.query_sources(self.admin_token))
        if summary["source_count"] != 0:
            raise AcceptanceError(f"deletion acceptance did not revoke query authority: {summary}")
        scenario.observe("delete_accepted", status=202, job_id=self.deletion_job_id, **summary)

        self.compose("stop", "qdrant", "elasticsearch", "minio")
        self.compose("unpause", "etl-worker")

        def deletion_failure_visible() -> dict[str, Any] | bool:
            text = self.fetch_worker_metrics("ai_etl_deletion_outcomes_total")
            failed = metric_value(text, 'ai_etl_deletion_outcomes_total{outcome="failed"}')
            return {"dependency_failure_outcomes": failed} if failed > 0 else False

        failure = self.wait_until("deletion dependency failure metric", deletion_failure_visible)
        status, pending = self.client.request(
            "GET", f"/v1/documents/{self.doc_id}", token=self.admin_token
        )
        if pending.get("deletion_status") != "pending":
            raise AcceptanceError(f"deletion job did not remain pending: {sanitize(pending)}")
        scenario.observe("dependencies_unavailable", **failure)
        scenario.observe("durable_partial_state", status=status, deletion_status="pending")
        self.restore_deletion_dependencies()

        def document_removed() -> dict[str, Any] | bool:
            status, payload = self.client.request(
                "GET",
                f"/v1/documents/{self.doc_id}",
                token=self.admin_token,
                expected=(200, 404),
            )
            return {"status": status, "body": payload} if status == 404 else False

        removed = self.wait_until("authoritative deletion completion", document_removed)
        scenario.observe("cleanup_completed", status=removed["status"])

    def audit_correlation(self, scenario: Scenario) -> None:
        _, audits = self.client.request(
            "GET", "/v1/audit?limit=100", token=self.admin_token
        )
        items = audits.get("items") or []
        relevant = [
            item
            for item in items
            if item.get("resource_id") == self.doc_id
            and item.get("action")
            in {"document.publication.update", "delete_accepted", "delete_completed"}
        ]
        actions = {item.get("action") for item in relevant}
        required = {"document.publication.update", "delete_accepted", "delete_completed"}
        if not required.issubset(actions):
            raise AcceptanceError(f"missing audit actions: {sorted(required - actions)}")
        correlated = [
            item
            for item in relevant
            if item.get("action") in {"delete_accepted", "delete_completed"}
            and (item.get("detail") or {}).get("deletion_job_id") == self.deletion_job_id
        ]
        if len(correlated) != 2:
            raise AcceptanceError("deletion audit correlation is incomplete")
        scenario.observe(
            "audit_entries",
            actions=sorted(actions),
            deletion_job_id=self.deletion_job_id,
            correlated_entries=len(correlated),
        )

    def write_report(self, status: str) -> None:
        report = {
            "schema_version": "1.0",
            "suite": "p2.4-governance-acceptance",
            "status": status,
            "started_at": self.started_at.isoformat(),
            "finished_at": datetime.now(timezone.utc).isoformat() if status != "running" else None,
            "scenarios": self.scenarios,
            "environment": {
                "model_backend": "deterministic",
                "generation_retention_enabled": False,
                "observation_seam": "public HTTP",
            },
        }
        self.report_path.parent.mkdir(parents=True, exist_ok=True)
        self.report_path.write_text(
            json.dumps(sanitize(report), indent=2, ensure_ascii=False) + "\n",
            encoding="utf-8",
        )

    def run(self) -> None:
        self.scenario("Dependency interruption", self.setup_and_recover_ingestion)
        self.scenario("Independent approval", self.independent_approval)
        self.scenario("Replacement continuity", self.replacement_continuity)
        self.scenario("Approved cutover", self.approved_cutover)
        self.scenario("Recoverable deletion", self.recoverable_deletion)
        self.scenario("Audit correlation", self.audit_correlation)
        self.write_report("passed")


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--api-url", required=True)
    parser.add_argument("--worker-metrics-url", required=True)
    parser.add_argument("--report", type=Path, required=True)
    parser.add_argument("--timeout", type=int, default=240)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    runner = Runner(args.api_url, args.worker_metrics_url, args.report, args.timeout)
    try:
        runner.run()
    except Exception as error:
        print(f"[governance] FAIL: {sanitize(str(error))}", file=sys.stderr)
        return 1
    print(f"[governance] PASS: report={args.report}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
