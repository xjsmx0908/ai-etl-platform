#!/usr/bin/env python3
"""Convert a licensed BEIR dataset into the repository eval protocol v2."""

from __future__ import annotations

import argparse
import csv
import hashlib
import json
import re
import sys
from pathlib import Path
from typing import Any


class ImportError(RuntimeError):
    pass


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    try:
        with path.open(encoding="utf-8") as handle:
            for line_number, line in enumerate(handle, start=1):
                if not line.strip():
                    continue
                value = json.loads(line)
                if not isinstance(value, dict):
                    raise ImportError(f"{path.name}:{line_number}: row must be an object")
                rows.append(value)
    except OSError as exc:
        raise ImportError(f"cannot read {path}: {exc}") from exc
    except json.JSONDecodeError as exc:
        raise ImportError(f"{path.name}:{exc.lineno}: invalid JSON") from exc
    return rows


def row_id(row: dict[str, Any], path: Path) -> str:
    value = str(row.get("_id", row.get("id", ""))).strip()
    if not value:
        raise ImportError(f"{path.name}: every row requires _id or id")
    return value


def read_qrels(path: Path) -> dict[str, list[tuple[str, float]]]:
    try:
        with path.open(encoding="utf-8", newline="") as handle:
            rows = list(csv.reader(handle, delimiter="\t"))
    except OSError as exc:
        raise ImportError(f"cannot read {path}: {exc}") from exc
    if not rows:
        raise ImportError(f"{path.name}: qrels are empty")

    header = [value.strip().lower() for value in rows[0]]
    has_header = "query-id" in header and "corpus-id" in header
    query_index = header.index("query-id") if has_header else 0
    corpus_index = header.index("corpus-id") if has_header else 1
    score_index = header.index("score") if has_header and "score" in header else 2
    qrels: dict[str, list[tuple[str, float]]] = {}
    for line_number, row in enumerate(rows[1:] if has_header else rows, start=2 if has_header else 1):
        if not row or all(not cell.strip() for cell in row):
            continue
        if len(row) <= max(query_index, corpus_index):
            raise ImportError(f"{path.name}:{line_number}: invalid qrel row")
        query_id = row[query_index].strip()
        corpus_id = row[corpus_index].strip()
        try:
            score = float(row[score_index]) if len(row) > score_index and row[score_index].strip() else 1.0
        except ValueError as exc:
            raise ImportError(f"{path.name}:{line_number}: score must be numeric") from exc
        if query_id and corpus_id and score > 0:
            qrels.setdefault(query_id, []).append((corpus_id, score))
    if not qrels:
        raise ImportError(f"{path.name}: no positive qrels found")
    return qrels


def stable_sample(ids: list[str], limit: int, seed: int) -> list[str]:
    if limit <= 0 or limit >= len(ids):
        return list(ids)
    ranked = sorted(
        ids,
        key=lambda value: hashlib.sha256(f"{seed}:{value}".encode("utf-8")).hexdigest(),
    )
    selected = set(ranked[:limit])
    return [value for value in ids if value in selected]


def safe_filename(document_id: str) -> str:
    safe = re.sub(r"[^A-Za-z0-9._-]+", "-", document_id).strip("-.")
    if not safe:
        safe = hashlib.sha256(document_id.encode("utf-8")).hexdigest()[:16]
    return f"public-{safe[:100]}.txt"


def convert(args: argparse.Namespace) -> dict[str, Any]:
    source_dir = Path(args.beir_dir)
    corpus_path = source_dir / args.corpus_file
    queries_path = source_dir / args.queries_file
    qrels_path = source_dir / args.qrels_file

    corpus_rows = read_jsonl(corpus_path)
    query_rows = read_jsonl(queries_path)
    corpus_by_id = {row_id(row, corpus_path): row for row in corpus_rows}
    query_by_id = {row_id(row, queries_path): row for row in query_rows}
    if len(corpus_by_id) != len(corpus_rows):
        raise ImportError("corpus document ids must be unique")
    if len(query_by_id) != len(query_rows):
        raise ImportError("query ids must be unique")

    qrels = read_qrels(qrels_path)
    eligible_queries = [query_id for query_id in query_by_id if query_id in qrels]
    selected_queries = stable_sample(eligible_queries, args.max_queries, args.seed)
    if not selected_queries:
        raise ImportError("no queries have positive qrels")

    relevant_ids = {
        document_id
        for query_id in selected_queries
        for document_id, _ in qrels[query_id]
    }
    missing_documents = sorted(relevant_ids.difference(corpus_by_id))
    if missing_documents:
        raise ImportError(f"qrels reference {len(missing_documents)} missing corpus document(s)")

    corpus_ids = list(corpus_by_id)
    if args.max_documents > 0:
        if args.max_documents < len(relevant_ids):
            raise ImportError(
                f"--max-documents={args.max_documents} is below the {len(relevant_ids)} relevant documents required"
            )
        distractors = [document_id for document_id in corpus_ids if document_id not in relevant_ids]
        selected_distractors = stable_sample(
            distractors,
            args.max_documents - len(relevant_ids),
            args.seed,
        )
        selected_document_set = relevant_ids.union(selected_distractors)
        selected_document_ids = [document_id for document_id in corpus_ids if document_id in selected_document_set]
    else:
        selected_document_ids = corpus_ids

    documents: list[dict[str, Any]] = []
    for document_id in selected_document_ids:
        row = corpus_by_id[document_id]
        title = str(row.get("title", "")).strip()
        text = str(row.get("text", row.get("content", ""))).strip()
        if not text:
            raise ImportError(f"corpus document {document_id} has no text")
        content = f"{title}\n\n{text}" if title else text
        documents.append(
            {
                "id": document_id,
                "filename": safe_filename(document_id),
                "permission": "internal",
                "content": content,
                "metadata": {
                    "eval_dataset": args.name,
                    "source_document_id": document_id,
                },
            }
        )

    cases: list[dict[str, Any]] = []
    for query_id in selected_queries:
        ranked_relevant = sorted(qrels[query_id], key=lambda item: (-item[1], item[0]))
        relevant_document_ids = [document_id for document_id, _ in ranked_relevant]
        query_text = str(query_by_id[query_id].get("text", query_by_id[query_id].get("query", ""))).strip()
        if not query_text:
            raise ImportError(f"query {query_id} has no text")
        cases.append(
            {
                "id": f"query-{query_id}",
                "document_id": relevant_document_ids[0],
                "query": query_text,
                "acceptable_doc_ids": relevant_document_ids,
                "expect_hit": True,
                "require_source_citation": False,
                "metadata": {
                    "category": "public_retrieval",
                    "source_query_id": query_id,
                },
            }
        )

    sampled = args.max_queries > 0 or args.max_documents > 0
    return {
        "version": "2.0",
        "name": args.name,
        "dataset_type": "public_benchmark",
        "evaluation_scope": "retrieval",
        "provenance": {
            "source_format": "beir",
            "source_url": args.source_url,
            "license": args.license,
            "dataset_version": args.dataset_version,
            "split": args.split,
            "benchmark_comparable": not sampled,
            "sampling": {
                "max_documents": args.max_documents,
                "max_queries": args.max_queries,
                "seed": args.seed,
            },
        },
        "documents": documents,
        "cases": cases,
    }


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Import a licensed public BEIR retrieval dataset")
    parser.add_argument("--beir-dir", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--name", required=True)
    parser.add_argument("--dataset-version", required=True)
    parser.add_argument("--source-url", required=True)
    parser.add_argument("--license", required=True, help="SPDX-style license identifier")
    parser.add_argument("--split", required=True)
    parser.add_argument("--corpus-file", default="corpus.jsonl")
    parser.add_argument("--queries-file", default="queries.jsonl")
    parser.add_argument("--qrels-file", default="qrels.tsv")
    parser.add_argument("--max-queries", type=int, default=0)
    parser.add_argument("--max-documents", type=int, default=0)
    parser.add_argument("--seed", type=int, default=42)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.max_queries < 0 or args.max_documents < 0:
        print("ERROR: sampling limits must be >= 0", file=sys.stderr)
        return 2
    try:
        dataset = convert(args)
        output = Path(args.output)
        output.parent.mkdir(parents=True, exist_ok=True)
        output.write_text(json.dumps(dataset, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    except ImportError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    print(
        f"Imported {len(dataset['documents'])} documents and {len(dataset['cases'])} queries to {output}"
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
