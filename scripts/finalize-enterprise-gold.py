#!/usr/bin/env python3
"""Promote a private enterprise candidate only after bound business approval."""

from __future__ import annotations

import argparse
import copy
import hashlib
import json
import os
import sys
import tempfile
from datetime import datetime
from pathlib import Path
from typing import Any


class FinalizeError(RuntimeError):
    pass


REQUIRED_ATTESTATIONS = {
    "document_authority_confirmed",
    "permissions_confirmed",
    "effective_versions_confirmed",
    "questions_and_reference_answers_confirmed",
}


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--candidate", required=True)
    parser.add_argument("--approval", required=True)
    parser.add_argument("--signed-approval", required=True)
    parser.add_argument("--output", required=True)
    return parser.parse_args()


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def read_json_object(path: Path, label: str) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise FinalizeError(f"{label} must be a readable JSON object") from exc
    if not isinstance(value, dict):
        raise FinalizeError(f"{label} must be a JSON object")
    return value


def object_ids(items: Any, label: str) -> set[str]:
    if not isinstance(items, list) or not items:
        raise FinalizeError(f"candidate {label} must be a non-empty array")
    values = [str(item.get("id", "")).strip() for item in items if isinstance(item, dict)]
    if len(values) != len(items) or any(not value for value in values):
        raise FinalizeError(f"candidate {label} require unique non-empty ids")
    if len(set(values)) != len(values):
        raise FinalizeError(f"candidate {label} require unique non-empty ids")
    return set(values)


def approval_ids(value: Any, label: str) -> set[str]:
    if not isinstance(value, list) or any(not isinstance(item, str) or not item.strip() for item in value):
        raise FinalizeError(f"{label} approval scope must be a string array")
    normalized = [item.strip() for item in value]
    if len(set(normalized)) != len(normalized):
        raise FinalizeError(f"{label} approval scope contains duplicate ids")
    return set(normalized)


def required_text(value: Any, label: str) -> str:
    if not isinstance(value, str) or not value.strip():
        raise FinalizeError(f"{label} is required")
    return value.strip()


def validate_approval_metadata(approval: dict[str, Any]) -> None:
    if approval.get("schema_version") != "1.0":
        raise FinalizeError("approval schema_version must be 1.0")
    if approval.get("decision") != "approved":
        raise FinalizeError("approval decision must be approved")
    if approval.get("approval_scope") != "entire_dataset":
        raise FinalizeError("approval_scope must be entire_dataset")
    for field in ("approval_id", "approved_by", "approver_role"):
        required_text(approval.get(field), field)
    approved_at = required_text(approval.get("approved_at"), "approved_at")
    try:
        parsed = datetime.fromisoformat(approved_at.replace("Z", "+00:00"))
    except ValueError as exc:
        raise FinalizeError("approved_at must be an ISO-8601 timestamp") from exc
    if parsed.tzinfo is None:
        raise FinalizeError("approved_at must include a timezone")


def write_private_json(path: Path, value: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary_name = ""
    try:
        with tempfile.NamedTemporaryFile(
            "w", encoding="utf-8", dir=path.parent, prefix=f".{path.name}.", delete=False
        ) as handle:
            temporary_name = handle.name
            json.dump(value, handle, ensure_ascii=False, indent=2)
            handle.write("\n")
        os.chmod(temporary_name, 0o600)
        os.replace(temporary_name, path)
    finally:
        if temporary_name:
            Path(temporary_name).unlink(missing_ok=True)


def main() -> int:
    args = parse_args()
    candidate_path = Path(args.candidate)
    approval_path = Path(args.approval)
    signed_approval = Path(args.signed_approval)
    if not signed_approval.is_file() or not signed_approval.read_bytes():
        print("ERROR: signed approval artifact is required", file=sys.stderr)
        return 2
    try:
        approval = read_json_object(approval_path, "approval")
        candidate = read_json_object(candidate_path, "candidate")
        validate_approval_metadata(approval)
        provenance = candidate.get("provenance")
        if not isinstance(provenance, dict) or provenance.get("business_approval_complete") is not False:
            raise FinalizeError("candidate must explicitly be pending business approval")
        if approval.get("candidate_sha256") != sha256_file(candidate_path):
            raise FinalizeError("candidate SHA256 does not match approval")
        if approval.get("signed_approval_sha256") != sha256_file(signed_approval):
            raise FinalizeError("signed approval SHA256 does not match artifact")
        if approval_ids(approval.get("approved_document_ids"), "document") != object_ids(
            candidate.get("documents"), "documents"
        ):
            raise FinalizeError("document approval scope does not match candidate")
        if approval_ids(approval.get("approved_case_ids"), "case") != object_ids(
            candidate.get("cases"), "cases"
        ):
            raise FinalizeError("case approval scope does not match candidate")
        attestations = approval.get("attestations")
        if not isinstance(attestations, dict) or set(attestations) != REQUIRED_ATTESTATIONS:
            raise FinalizeError("business attestations are incomplete")
        if any(attestations[name] is not True for name in REQUIRED_ATTESTATIONS):
            raise FinalizeError("all business attestations must be approved")

        promoted = copy.deepcopy(candidate)
        promoted["dataset_type"] = "enterprise_private_gold"
        promoted_provenance = promoted["provenance"]
        promoted_provenance["label_status"] = "business_approved_gold"
        promoted_provenance["business_approval_complete"] = True
        promoted_provenance["business_approval"] = {
            "schema_version": approval["schema_version"],
            "approval_id": approval["approval_id"].strip(),
            "approved_by": approval["approved_by"].strip(),
            "approver_role": approval["approver_role"].strip(),
            "approved_at": approval["approved_at"].strip(),
            "approval_scope": approval["approval_scope"],
            "candidate_sha256": approval["candidate_sha256"],
            "signed_approval_sha256": approval["signed_approval_sha256"],
            "approval_record_sha256": sha256_file(approval_path),
            "approved_document_count": len(approval["approved_document_ids"]),
            "approved_case_count": len(approval["approved_case_ids"]),
        }
        write_private_json(Path(args.output), promoted)
    except (OSError, FinalizeError) as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    print(
        json.dumps(
            {
                "business_approval_complete": True,
                "document_count": len(promoted["documents"]),
                "case_count": len(promoted["cases"]),
                "candidate_sha256": approval["candidate_sha256"],
            },
            sort_keys=True,
        )
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
