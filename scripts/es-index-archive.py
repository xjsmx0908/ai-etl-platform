#!/usr/bin/env python3
"""Archive and restore one Elasticsearch index without a snapshot repository.

`_snapshot` needs `path.repo` in the cluster's elasticsearch.yml, which the
deployment image does not set and cannot set at runtime. A logical archive keeps
the recovery plan workable against any target cluster, and — unlike a rebuild
from the source objects — it does not depend on the host-side embedding model,
which is not part of the stack and is not backed up.

Archive format (JSONL, first line is the header):
  {"type":"header","index":..., "settings":{...},"mappings":{...},"count":N}
  {"type":"doc","_id":..., "_source":{...}}

Usage:
  python3 scripts/es-index-archive.py dump \
      --address http://127.0.0.1:9200 --index documents_text --out es.jsonl
  python3 scripts/es-index-archive.py restore \
      --address http://127.0.0.1:9200 --index documents_text --in es.jsonl
"""

from __future__ import annotations

import argparse
import json
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

SCROLL_KEEPALIVE = "5m"
PAGE_SIZE = 1000
BULK_BATCH = 500
# Index settings that are read-only or node-scoped. Replaying them into a fresh
# cluster either fails outright (uuid, creation_date, provided_name) or pins the
# index to the shape of the cluster it came from (routing, store, version).
SETTINGS_KEEP = ("number_of_shards", "number_of_replicas", "analysis")


def _request(
    address: str,
    path: str,
    method: str = "GET",
    body: bytes | None = None,
    content_type: str = "application/json",
    timeout: float = 60.0,
) -> bytes:
    request = urllib.request.Request(f"{address.rstrip('/')}{path}", data=body, method=method)
    if body is not None:
        request.add_header("Content-Type", content_type)
    with urllib.request.urlopen(request, timeout=timeout) as response:
        return response.read()


def _json_request(address: str, path: str, method: str, payload: dict, timeout: float = 60.0) -> dict:
    raw = _request(address, path, method, json.dumps(payload).encode("utf-8"), timeout=timeout)
    return json.loads(raw) if raw else {}


def _index_snapshot(address: str, index: str) -> dict:
    raw = _request(address, f"/{index}")
    parsed = json.loads(raw)
    key = next(iter(parsed))
    return parsed[key]


def dump(address: str, index: str, out: Path) -> int:
    meta = _index_snapshot(address, index)
    settings = {
        k: v for k, v in (meta.get("settings", {}).get("index") or {}).items()
        if k in SETTINGS_KEEP
    }
    header = {
        "type": "header",
        "index": index,
        "settings": settings,
        "mappings": meta.get("mappings") or {},
        "count": None,
    }

    written = 0
    scroll_id: str | None = None
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("w", encoding="utf-8") as handle:
        handle.write(json.dumps(header, sort_keys=True) + "\n")
        try:
            page = _json_request(
                address,
                f"/{index}/_search?scroll={SCROLL_KEEPALIVE}",
                "POST",
                {"size": PAGE_SIZE, "sort": ["_doc"], "query": {"match_all": {}}},
            )
            while True:
                scroll_id = page.get("_scroll_id") or scroll_id
                hits = page.get("hits", {}).get("hits", [])
                if not hits:
                    break
                for hit in hits:
                    handle.write(
                        json.dumps(
                            {"type": "doc", "_id": hit["_id"], "_source": hit["_source"]},
                            sort_keys=True,
                        )
                        + "\n"
                    )
                    written += 1
                page = _json_request(
                    address,
                    "/_search/scroll",
                    "POST",
                    {"scroll": SCROLL_KEEPALIVE, "scroll_id": scroll_id},
                )
        finally:
            if scroll_id:
                try:
                    _request(address, "/_search/scroll", "DELETE", json.dumps({"scroll_id": [scroll_id]}).encode())
                except Exception:  # noqa: BLE001 - a stale scroll expires on its own
                    pass

    # Rewrite the header with the real count so a restore can assert on it.
    with out.open("r+", encoding="utf-8") as handle:
        header["count"] = written
        handle.write(json.dumps(header, sort_keys=True) + "\n")
    print(f"[es-archive] dumped {written} documents from {index} to {out}", file=sys.stderr)
    return written


def restore(address: str, index: str, source: Path) -> int:
    with source.open("r", encoding="utf-8") as handle:
        header = json.loads(handle.readline())
        if header.get("type") != "header":
            raise SystemExit(f"{source} does not start with an archive header")

        body: dict = {"mappings": header.get("mappings") or {}}
        if header.get("settings"):
            body["settings"] = header["settings"]
        # A restore always targets an empty index: recreating is the only way to
        # guarantee the archived mappings (not whatever a previous run left) win.
        try:
            _request(address, f"/{index}", "DELETE")
        except urllib.error.HTTPError as exc:
            if exc.code != 404:
                raise
        _json_request(address, f"/{index}", "PUT", body)

        actions: list[str] = []
        restored = 0
        for line in handle:
            record = json.loads(line)
            if record.get("type") != "doc":
                continue
            actions.append(json.dumps({"index": {"_index": index, "_id": record["_id"]}}))
            actions.append(json.dumps(record["_source"]))
            if len(actions) >= BULK_BATCH * 2:
                restored += _flush(address, actions)
                actions = []
        if actions:
            restored += _flush(address, actions)

    _request(address, f"/{index}/_refresh", "POST", b"")
    deadline = time.time() + 30
    while time.time() < deadline:
        count = json.loads(_request(address, f"/{index}/_count"))["count"]
        if count >= restored:
            break
        time.sleep(1)
    print(
        f"[es-archive] restored {restored} documents into {index} (expected {header.get('count')})",
        file=sys.stderr,
    )
    if header.get("count") is not None and restored != header["count"]:
        print(
            f"[es-archive] archive held {header['count']} documents but {restored} were replayed",
            file=sys.stderr,
        )
        return 1
    return 0


def _flush(address: str, actions: list[str]) -> int:
    payload = ("\n".join(actions) + "\n").encode("utf-8")
    raw = _request(
        address,
        "/_bulk?refresh=false",
        "POST",
        payload,
        content_type="application/x-ndjson",
        timeout=180.0,
    )
    parsed = json.loads(raw)
    if parsed.get("errors"):
        first = next(
            (item for item in parsed.get("items", []) if item.get("index", {}).get("error")),
            None,
        )
        raise SystemExit(f"bulk restore rejected documents: {json.dumps(first)[:400]}")
    return len(actions) // 2


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("dump", "restore"))
    parser.add_argument("--address", default="http://127.0.0.1:9200")
    parser.add_argument("--index", default="documents_text")
    parser.add_argument("--out", type=Path)
    parser.add_argument("--in", dest="source", type=Path)
    args = parser.parse_args()

    if args.action == "dump":
        if not args.out:
            parser.error("--out is required for dump")
        dump(args.address, args.index, args.out)
        return 0
    if not args.source:
        parser.error("--in is required for restore")
    return restore(args.address, args.index, args.source)


if __name__ == "__main__":
    sys.exit(main())
