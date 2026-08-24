#!/usr/bin/env python3
"""Download an approved Hugging Face dataset snapshot as BEIR source files."""

from __future__ import annotations

import argparse
import hashlib
import json
import sys
from pathlib import Path
from typing import Any
from urllib.error import HTTPError, URLError
from urllib.parse import urlencode
from urllib.request import urlopen


DEFAULT_CATALOG = Path(__file__).resolve().parents[1] / "docs" / "evals" / "public-datasets.json"
DEFAULT_API_BASE_URL = "https://datasets-server.huggingface.co"
OUTPUT_FILES = {
    "corpus": "corpus.jsonl",
    "queries": "queries.jsonl",
    "qrels": "qrels.tsv",
}
REQUIRED_DATASET_FIELDS = ("name", "repository", "revision", "license", "source_url", "configs")


class DownloadError(RuntimeError):
    pass


def non_empty_string(value: Any) -> bool:
    return isinstance(value, str) and bool(value.strip())


def load_approved_dataset(catalog_path: Path, dataset_key: str) -> dict[str, Any]:
    try:
        catalog = json.loads(catalog_path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise DownloadError(f"cannot read dataset catalog: {exc.__class__.__name__}") from exc
    datasets = catalog.get("datasets") if isinstance(catalog, dict) else None
    if not isinstance(datasets, dict) or dataset_key not in datasets:
        raise DownloadError(f"dataset {dataset_key!r} is not in the approved catalog")
    dataset = datasets[dataset_key]
    if not isinstance(dataset, dict):
        raise DownloadError(f"catalog entry {dataset_key!r} must be an object")
    for field_name in REQUIRED_DATASET_FIELDS:
        value = dataset.get(field_name)
        if field_name == "configs":
            if not isinstance(value, dict):
                raise DownloadError(f"catalog entry {dataset_key!r} requires configs")
        elif not non_empty_string(value):
            raise DownloadError(f"catalog entry {dataset_key!r} requires {field_name}")
    configs = dataset["configs"]
    if set(configs) != set(OUTPUT_FILES):
        raise DownloadError("approved dataset requires corpus, queries, and qrels configs")
    return dataset


def fetch_config_rows(
    api_base_url: str,
    dataset: dict[str, Any],
    config_name: str,
    *,
    page_size: int,
) -> list[dict[str, Any]]:
    config = dataset["configs"][config_name]
    if not isinstance(config, dict):
        raise DownloadError(f"config {config_name} must be an object")
    remote_config = config.get("config")
    split = config.get("split")
    expected_rows = config.get("expected_rows")
    if not non_empty_string(remote_config) or not non_empty_string(split):
        raise DownloadError(f"config {config_name} requires config and split")
    if not isinstance(expected_rows, int) or expected_rows < 1:
        raise DownloadError(f"config {config_name} requires a positive expected_rows")

    rows: list[dict[str, Any]] = []
    total: int | None = None
    while total is None or len(rows) < total:
        params = urlencode(
            {
                "dataset": dataset["repository"],
                "config": remote_config,
                "split": split,
                "offset": len(rows),
                "length": page_size,
                "revision": dataset["revision"],
            }
        )
        url = f"{api_base_url.rstrip('/')}/rows?{params}"
        try:
            with urlopen(url, timeout=30) as response:
                payload = json.load(response)
        except (HTTPError, URLError, TimeoutError, json.JSONDecodeError) as exc:
            raise DownloadError(f"dataset server request failed for {config_name}: {exc.__class__.__name__}") from exc
        if not isinstance(payload, dict) or not isinstance(payload.get("rows"), list):
            raise DownloadError(f"dataset server returned invalid rows for {config_name}")
        response_total = payload.get("num_rows_total")
        if not isinstance(response_total, int) or response_total < 0:
            raise DownloadError(f"dataset server omitted row count for {config_name}")
        if total is not None and response_total != total:
            raise DownloadError(f"dataset server row count changed during {config_name} download")
        total = response_total
        page: list[dict[str, Any]] = []
        for item in payload["rows"]:
            if not isinstance(item, dict) or not isinstance(item.get("row"), dict):
                raise DownloadError(f"dataset server returned an invalid row for {config_name}")
            if item.get("truncated_cells"):
                raise DownloadError(f"dataset server truncated a cell for {config_name}")
            page.append(item["row"])
        if not page and len(rows) < total:
            raise DownloadError(f"dataset server pagination stopped early for {config_name}")
        rows.extend(page)
        if len(rows) > total:
            raise DownloadError(f"dataset server returned too many rows for {config_name}")

    if len(rows) != expected_rows:
        raise DownloadError(
            f"{config_name} row count {len(rows)} does not match approved snapshot {expected_rows}"
        )
    return rows


def render_jsonl(rows: list[dict[str, Any]]) -> str:
    return "".join(json.dumps(row, ensure_ascii=False, separators=(",", ":")) + "\n" for row in rows)


def render_qrels(rows: list[dict[str, Any]]) -> str:
    lines = ["query-id\tcorpus-id\tscore"]
    for row in rows:
        query_id = str(row.get("query-id", "")).strip()
        corpus_id = str(row.get("corpus-id", "")).strip()
        if not query_id or not corpus_id:
            raise DownloadError("qrels rows require query-id and corpus-id")
        score = row.get("score", 1)
        lines.append(f"{query_id}\t{corpus_id}\t{score}")
    return "\n".join(lines) + "\n"


def download(args: argparse.Namespace) -> None:
    dataset = load_approved_dataset(Path(args.catalog), args.dataset)
    downloaded = {
        config_name: fetch_config_rows(
            args.api_base_url,
            dataset,
            config_name,
            page_size=args.page_size,
        )
        for config_name in OUTPUT_FILES
    }
    rendered = {
        "corpus.jsonl": render_jsonl(downloaded["corpus"]),
        "queries.jsonl": render_jsonl(downloaded["queries"]),
        "qrels.tsv": render_qrels(downloaded["qrels"]),
    }
    checksums: dict[str, str] = {}
    for config_name, filename in OUTPUT_FILES.items():
        approved_checksum = dataset["configs"][config_name].get("sha256")
        if (
            not isinstance(approved_checksum, str)
            or len(approved_checksum) != 64
            or any(character not in "0123456789abcdef" for character in approved_checksum.lower())
        ):
            raise DownloadError(f"config {config_name} requires an approved SHA-256 checksum")
        actual_checksum = hashlib.sha256(rendered[filename].encode("utf-8")).hexdigest()
        if actual_checksum != approved_checksum.lower():
            raise DownloadError(f"{config_name} checksum does not match approved snapshot")
        checksums[filename] = actual_checksum
    manifest = {
        "dataset": args.dataset,
        "name": dataset["name"],
        "repository": dataset["repository"],
        "revision": dataset["revision"],
        "license": dataset["license"],
        "source_url": dataset["source_url"],
        "files": {
            filename: {
                "rows": len(downloaded[config_name]),
                "sha256": checksums[filename],
            }
            for config_name, filename in OUTPUT_FILES.items()
        },
    }
    output_dir = Path(args.output_dir)
    output_dir.mkdir(parents=True, exist_ok=True)
    for filename, content in rendered.items():
        (output_dir / filename).write_text(content, encoding="utf-8")
    (output_dir / "download-manifest.json").write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2) + "\n",
        encoding="utf-8",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description="Download an approved public RAG evaluation dataset")
    parser.add_argument("--dataset", required=True, help="approved catalog key")
    parser.add_argument("--catalog", default=str(DEFAULT_CATALOG))
    parser.add_argument("--output-dir", required=True)
    parser.add_argument("--api-base-url", default=DEFAULT_API_BASE_URL)
    parser.add_argument("--page-size", type=int, default=100)
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    if args.page_size < 1 or args.page_size > 100:
        print("ERROR: --page-size must be between 1 and 100", file=sys.stderr)
        return 2
    try:
        download(args)
    except DownloadError as exc:
        print(f"ERROR: {exc}", file=sys.stderr)
        return 2
    print(f"Downloaded approved dataset {args.dataset} to {args.output_dir}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
