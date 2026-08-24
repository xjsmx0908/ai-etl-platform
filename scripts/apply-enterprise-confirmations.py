#!/usr/bin/env python3
"""Resolve enterprise review confirmations by the user-approved latest-date rule."""

from __future__ import annotations

import argparse
import copy
import csv
from dataclasses import dataclass
from datetime import date, datetime
import json
from pathlib import Path
import re
import subprocess
import sys
import tempfile
from typing import Any
import zipfile


ROOT = Path(__file__).resolve().parent.parent
DEFAULT_INPUT = ROOT / "docs" / "evals" / "private" / "p1.4-enterprise"
CONFIRMATION_FIELDS = [
    "confirmation_type",
    "document_id",
    "related_document_id",
    "filename",
    "suggested_owner",
    "suggested_value",
    "reason_code",
    "confirmed_owner",
    "confirmed_effective_status",
    "confirmed_effective_document",
    "selected_date",
    "date_source",
    "review_decision",
    "review_notes",
]


class ConfirmationError(RuntimeError):
    pass


@dataclass(frozen=True)
class DateEvidence:
    document_id: str
    value: str
    source: str

    @property
    def parsed(self) -> date:
        return date.fromisoformat(self.value)


def read_csv(path: Path) -> list[dict[str, str]]:
    with path.open(encoding="utf-8-sig", newline="") as handle:
        return list(csv.DictReader(handle))


def write_csv(path: Path, rows: list[dict[str, Any]]) -> None:
    with path.open("w", encoding="utf-8-sig", newline="") as handle:
        writer = csv.DictWriter(handle, fieldnames=CONFIRMATION_FIELDS, extrasaction="ignore")
        writer.writeheader()
        writer.writerows(rows)


def write_json(path: Path, value: Any) -> None:
    path.write_text(json.dumps(value, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def extract_text_dates(value: str) -> list[date]:
    found: set[date] = set()
    patterns = (
        re.compile(r"(?<!\d)(20\d{2})[年./-](0?[1-9]|1[0-2])[月./-](0?[1-9]|[12]\d|3[01])日?(?!\d)"),
        re.compile(r"(?<!\d)(20\d{2})(0[1-9]|1[0-2])([0-3]\d)(?!\d)"),
        re.compile(r"(?<!\d)(20\d{2})[年./-](0?[1-9]|1[0-2])月?(?![./\d-])"),
    )
    for pattern in patterns:
        for match in pattern.finditer(value or ""):
            year = int(match.group(1))
            month = int(match.group(2))
            day = int(match.group(3)) if (match.lastindex or 0) >= 3 else 1
            try:
                found.add(date(year, month, day))
            except ValueError:
                continue
    return sorted(found)


def parse_pdf_metadata_dates(path: Path) -> list[date]:
    try:
        result = subprocess.run(
            ["pdfinfo", str(path)], capture_output=True, text=True, timeout=30, check=False
        )
    except (OSError, subprocess.TimeoutExpired):
        return []
    values: list[date] = []
    for line in result.stdout.splitlines():
        if not line.startswith(("CreationDate:", "ModDate:")):
            continue
        raw = line.split(":", 1)[1].strip()
        try:
            values.append(datetime.strptime(raw, "%a %b %d %H:%M:%S %Y %Z").date())
        except ValueError:
            continue
    return sorted(set(values))


def parse_office_metadata_dates(path: Path) -> list[date]:
    target_suffix = ".xlsx" if path.suffix.lower() in {".xls", ".xlsx"} else ".docx"
    source = path
    with tempfile.TemporaryDirectory(prefix="p15-office-meta-") as temporary:
        if path.suffix.lower() not in {".xlsx", ".docx", ".pptx"}:
            try:
                result = subprocess.run(
                    [
                        "libreoffice",
                        "--headless",
                        "--convert-to",
                        target_suffix.lstrip("."),
                        "--outdir",
                        temporary,
                        str(path),
                    ],
                    capture_output=True,
                    text=True,
                    timeout=120,
                    check=False,
                )
            except (OSError, subprocess.TimeoutExpired):
                return []
            if result.returncode != 0:
                return []
            candidates = list(Path(temporary).glob(f"*{target_suffix}"))
            if not candidates:
                return []
            source = candidates[0]
        try:
            with zipfile.ZipFile(source) as archive:
                core = archive.read("docProps/core.xml").decode("utf-8", errors="ignore")
        except (OSError, KeyError, zipfile.BadZipFile):
            return []
    values = []
    for raw in re.findall(r"<(?:dcterms:created|dcterms:modified)[^>]*>([^<]+)</", core):
        try:
            values.append(datetime.fromisoformat(raw.replace("Z", "+00:00")).date())
        except ValueError:
            continue
    return sorted(set(values))


def latest_date_evidence(document: dict[str, Any]) -> DateEvidence:
    document_id = str(document.get("id", ""))
    path = Path(str(document.get("source_path", "")))
    if not path.is_absolute():
        path = ROOT / path
    candidates: list[tuple[date, str]] = []
    for value in extract_text_dates(str(document.get("content", ""))):
        candidates.append((value, "content"))
    for value in extract_text_dates(str(document.get("filename", ""))):
        candidates.append((value, "filename"))
    suffix = path.suffix.lower()
    metadata_dates = parse_pdf_metadata_dates(path) if suffix == ".pdf" else parse_office_metadata_dates(path)
    for value in metadata_dates:
        candidates.append((value, "pdf_metadata" if suffix == ".pdf" else "office_modified"))
    if not candidates:
        raise ConfirmationError(f"no explicit date for {document_id}")
    selected_date, source = max(candidates, key=lambda item: (item[0], item[1]))
    return DateEvidence(document_id, selected_date.isoformat(), source)


def choose_latest_document(candidates: dict[str, DateEvidence]) -> DateEvidence:
    if not candidates:
        raise ConfirmationError("no version candidates")
    ordered = sorted(candidates.values(), key=lambda item: item.parsed, reverse=True)
    if len(ordered) > 1 and ordered[0].parsed == ordered[1].parsed:
        raise ConfirmationError("latest dates are tied")
    return ordered[0]


def resolve_confirmations(
    rows: list[dict[str, str]], documents: dict[str, dict[str, Any]]
) -> list[dict[str, str]]:
    resolved: list[dict[str, str]] = []
    for original in rows:
        row = dict(original)
        confirmation_type = row.get("confirmation_type", "")
        document_id = row.get("document_id", "")
        related_id = row.get("related_document_id", "")
        ids = [value for value in (document_id, related_id) if value]
        try:
            evidence = {value: latest_date_evidence(documents[value]) for value in ids}
            winner = choose_latest_document(evidence) if confirmation_type == "effective_version" else evidence[document_id]
        except (KeyError, ConfirmationError) as exc:
            row.update(
                {
                    "review_decision": "pending",
                    "review_notes": f"latest_date_rule_unresolved:{type(exc).__name__}",
                }
            )
            resolved.append(row)
            continue
        row.update(
            {
                "confirmed_owner": row.get("suggested_owner", "") or "unknown",
                "confirmed_effective_status": "historical",
                "confirmed_effective_document": winner.document_id if confirmation_type == "effective_version" else document_id,
                "selected_date": winner.value,
                "date_source": winner.source,
                "review_decision": "approved",
                "review_notes": "user_confirmed_latest_date_rule;not_policy_register_validation",
            }
        )
        resolved.append(row)
    return resolved


def confirmation_map(rows: list[dict[str, str]]) -> tuple[dict[str, dict[str, str]], set[str]]:
    approved: dict[str, dict[str, str]] = {}
    excluded: set[str] = set()
    for row in rows:
        if row.get("review_decision") != "approved":
            continue
        if row.get("confirmation_type") == "effective_version":
            winner = row.get("confirmed_effective_document", "")
            pair = {row.get("document_id", ""), row.get("related_document_id", "")}
            if winner not in pair:
                raise ConfirmationError("confirmed effective document is not in conflict pair")
            approved[winner] = row
            excluded.update(pair - {winner})
        else:
            approved[row.get("document_id", "")] = row
    return approved, excluded


def build_gold_candidate(
    source: dict[str, Any],
    document_reviews: dict[str, dict[str, str]],
    case_reviews: dict[str, dict[str, str]],
    confirmations: list[dict[str, str]],
) -> dict[str, Any]:
    if any(row.get("review_decision") != "approved" for row in confirmations):
        raise ConfirmationError("not all confirmation rows are approved")
    approved_confirmations, excluded_ids = confirmation_map(confirmations)
    eligible_ids: set[str] = set()
    output_documents: list[dict[str, Any]] = []
    for document in source.get("documents", []):
        document_id = str(document.get("id", ""))
        review = document_reviews.get(document_id, {})
        if document_id in excluded_ids:
            continue
        auto_eligible = review.get("review_decision") == "auto_approved_technical"
        confirmation = approved_confirmations.get(document_id)
        if not auto_eligible and confirmation is None:
            continue
        value = copy.deepcopy(document)
        value["permission"] = review.get("proposed_permission") or value.get("permission", "internal")
        metadata = value.setdefault("metadata", {})
        metadata["eval_dataset"] = "p1.5-enterprise-gold-candidate"
        metadata["review_status"] = "user_date_rule_confirmed" if confirmation else "auto_approved_technical"
        if confirmation:
            metadata["business_owner"] = confirmation.get("confirmed_owner", "")
            metadata["effective_status"] = confirmation.get("confirmed_effective_status", "historical")
            metadata["confirmation_basis"] = "user_confirmed_latest_date_rule"
            metadata["selected_date"] = confirmation.get("selected_date", "")
        eligible_ids.add(document_id)
        output_documents.append(value)

    output_cases: list[dict[str, Any]] = []
    for case in source.get("cases", []):
        case_id = str(case.get("id", ""))
        document_id = str(case.get("document_id", ""))
        review = case_reviews.get(case_id, {})
        if document_id not in eligible_ids or review.get("technical_decision") != "approved":
            continue
        value = copy.deepcopy(case)
        metadata = value.setdefault("metadata", {})
        metadata["review_status"] = "gold_candidate"
        metadata["retrieval_style"] = review.get("retrieval_style", "unknown")
        output_cases.append(value)

    provenance = copy.deepcopy(source.get("provenance", {}))
    provenance.update(
        {
            "label_status": "user_date_rule_gold_candidate",
            "confirmation_rule": "latest_explicit_or_embedded_date",
            "user_date_rule_confirmation_complete": True,
            "business_approval_complete": False,
            "external_data_transfer": False,
        }
    )
    return {
        "version": "2.0",
        "name": "p1.5-enterprise-user-date-rule-gold-candidate",
        "dataset_type": "enterprise_private_gold_candidate",
        "evaluation_scope": "answer_and_retrieval",
        "provenance": provenance,
        "documents": output_documents,
        "cases": output_cases,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Apply enterprise review confirmations")
    parser.add_argument("--input-dir", default=str(DEFAULT_INPUT))
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    input_dir = Path(args.input_dir)
    source = json.loads((input_dir / "silver-candidates.json").read_text(encoding="utf-8"))
    documents = {str(value.get("id", "")): value for value in source.get("documents", [])}
    rows = read_csv(input_dir / "human-confirmation.csv")
    resolved = resolve_confirmations(rows, documents)
    write_csv(input_dir / "human-confirmation.csv", resolved)
    pending = sum(row.get("review_decision") != "approved" for row in resolved)
    if pending:
        print(f"ERROR: {pending} confirmation rows remain pending", file=sys.stderr)
        return 3
    document_reviews = {
        row["document_id"]: row for row in read_csv(input_dir / "automated-document-review.csv")
    }
    case_reviews = {
        row["case_id"]: row for row in read_csv(input_dir / "automated-case-review.csv")
    }
    candidate = build_gold_candidate(source, document_reviews, case_reviews, resolved)
    write_json(input_dir / "gold-candidate.json", candidate)
    summary = {
        "confirmation_rows": len(resolved),
        "pending_confirmations": pending,
        "gold_candidate_documents": len(candidate["documents"]),
        "gold_candidate_cases": len(candidate["cases"]),
        "business_approval_complete": False,
        "confirmation_rule": "latest_explicit_or_embedded_date",
    }
    write_json(input_dir / "confirmation-summary.json", summary)
    print(json.dumps(summary, ensure_ascii=False, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
