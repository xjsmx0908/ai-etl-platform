"""Measure real full `/v1/query` latency on a running Query API.

This is the instrument behind the P-CAP-3 observation in
`docs/ingestion-capacity.md`. It is a separate track from
`scripts/ingestion-capacity.py` (accepted-to-ready ingest) and from
`scripts/load-test.py` (isolated mock stack, p95 gates). It is **not the L1
query gate** and writes no threshold anywhere.

What it does:

- sends 5-10 serial streaming queries, one at a time, never concurrent
- rotates the question each round, because the retrieval path has a semantic
  cache: asking the same thing twice measures the cache, not the system
- reports `total`, `ttft`, the platform's own `done.ttft`, the retrieval
  duration the platform logged, and the cache-hit flag per round

What the numbers include -- the wording here is the same wording the doc uses,
and a contract test pins both:

- `total` is request start to the SSE `done` event: retrieval + generation +
  grounding.
- `ttft` is request start to the first non-empty `delta`. The `sources` event
  is emitted before generation starts, so **`ttft` includes retrieval**.
  "Generation time" is therefore `total - ttft`, not `total`.
- With n rounds, a nearest-rank p95 is the maximum. Say so; do not present it
  as an interpolated tail.

Do not start Compose or a second stack for this. Do not feed these numbers into
L1 p95 thresholds.

Usage:
  python3 scripts/query-latency-observation.py http://127.0.0.1:8080 <token> --rounds 8
"""

import argparse
import json
import math
import statistics
import sys
import time
import urllib.error
import urllib.request

# Questions come from the `default` tenant corpus, which is all published. They
# are deliberately distinct: the retrieval path carries a semantic cache
# (SEMANTIC_CACHE_ENABLED), so a repeat would report the cache's latency.
DEFAULT_QUESTIONS = [
    "第51周例会各部门沟通了什么内容？",
    "第50周例会讨论了哪些事项？",
    "社保卡常见问题有哪些？",
    "尖山印象公租房启动分配了多少套房源？",
    "关于给予有关人员即时激励的通知内容是什么？",
    "2022年度个人述职工作的要求是什么？",
    "北京后勤管理钉钉流程怎么操作？",
    "内网通的部门设置是怎样的？",
    "财务制度培训的内容有哪些？",
    "在职人员年休假信息收集表怎么填写？",
]


def open_stream(base, token, question, timeout):
    request = urllib.request.Request(
        base + "/v1/query",
        data=json.dumps({"question": question, "top_k": 5}).encode("utf-8"),
        method="POST",
        headers={
            "Authorization": "Bearer " + token,
            "Content-Type": "application/json",
            "Accept": "text/event-stream",
        },
    )
    return urllib.request.urlopen(request, timeout=timeout)


def read_one_round(base, token, question, timeout):
    """Run one full streaming query and time it.

    A clean SSE `error` event is recorded as data rather than raised: one bad
    round must not discard the rest of the observation.
    """
    started = time.monotonic()
    row = {
        "question": question,
        "http_status": None,
        "total_s": None,
        "ttft_s": None,
        "sources_s": None,
        "first_status_s": None,
        "delta_events": 0,
        "cache_hit": None,
        "source_count": None,
        "answer_chars": None,
        "server_duration": None,
        # The platform times its own request-start-to-first-token and reports it
        # in the done event. Comparing it with ttft_s separates "the model was
        # slow" from "bytes were held up after the model answered".
        "platform_ttft": None,
        "retrieval_ms": None,
        "error": "",
    }
    try:
        with open_stream(base, token, question, timeout) as response:
            row["http_status"] = response.status
            event = None
            for raw in response:
                line = raw.decode("utf-8", "replace").rstrip("\r\n")
                if line.startswith("event: "):
                    event = line[len("event: "):].strip()
                    continue
                if not line.startswith("data: "):
                    continue
                payload = line[len("data: "):]
                if event == "status" and row["first_status_s"] is None:
                    row["first_status_s"] = time.monotonic() - started
                elif event == "sources" and row["sources_s"] is None:
                    row["sources_s"] = time.monotonic() - started
                    try:
                        row["source_count"] = len(json.loads(payload).get("sources") or [])
                    except json.JSONDecodeError:
                        pass
                elif event in ("delta", "replace"):
                    row["delta_events"] += 1
                    if row["ttft_s"] is None:
                        try:
                            text = json.loads(payload).get("text") or ""
                        except json.JSONDecodeError:
                            text = payload
                        if text.strip():
                            row["ttft_s"] = time.monotonic() - started
                elif event == "done":
                    row["total_s"] = time.monotonic() - started
                    try:
                        body = json.loads(payload)
                    except json.JSONDecodeError:
                        body = {}
                    row["server_duration"] = body.get("duration")
                    row["platform_ttft"] = body.get("ttft")
                    row["answer_chars"] = len(body.get("answer") or "")
                    retrieval = body.get("retrieval") or {}
                    row["cache_hit"] = retrieval.get("cache_hit")
                    row["retrieval_ms"] = retrieval.get("duration_ms")
                    if row["source_count"] is None:
                        row["source_count"] = len(body.get("sources") or [])
                elif event == "error":
                    try:
                        row["error"] = json.loads(payload).get("error") or payload
                    except json.JSONDecodeError:
                        row["error"] = payload
    except urllib.error.HTTPError as error:
        row["http_status"] = error.code
        row["error"] = error.read().decode("utf-8", "replace")[:200]
    except Exception as error:  # noqa: BLE001 - keep the remaining rounds
        row["error"] = f"{type(error).__name__}: {error}"
    if row["total_s"] is None:
        row["total_s"] = time.monotonic() - started
    return row


def percentile_nearest_rank(values, fraction):
    ordered = sorted(values)
    if not ordered:
        return None
    rank = max(1, min(len(ordered), math.ceil(fraction * len(ordered))))
    return ordered[rank - 1]


def parse_go_duration(text):
    """Go prints durations like "25.96s" or "1m2.5s". Anything else returns None
    rather than a wrong number."""
    if not text or not isinstance(text, str):
        return None
    total, number = 0.0, ""
    for char in text:
        if char.isdigit() or char == ".":
            number += char
            continue
        if not number:
            return None
        if char == "h":
            total += float(number) * 3600
        elif char == "m":
            total += float(number) * 60
        elif char == "s":
            total += float(number)
        else:
            return None
        number = ""
    return total or None


def summarize(rows, rounds):
    ok = [row for row in rows if not row["error"] and row["http_status"] == 200]
    if not ok:
        return None
    totals = [row["total_s"] for row in ok]
    ttfts = [row["ttft_s"] for row in ok if row["ttft_s"] is not None]
    platform = [
        value for value in (parse_go_duration(row["platform_ttft"]) for row in ok)
        if value is not None
    ]
    retrieval = [row["retrieval_ms"] for row in ok if row["retrieval_ms"] is not None]
    return {
        "rounds": rounds,
        "ok_rounds": len(ok),
        "cache_hits": sum(1 for row in ok if row["cache_hit"]),
        "total_p50_s": round(percentile_nearest_rank(totals, 0.50), 2),
        "total_p95_s": round(percentile_nearest_rank(totals, 0.95), 2),
        "total_min_s": round(min(totals), 2),
        "total_max_s": round(max(totals), 2),
        "ttft_p50_s": round(percentile_nearest_rank(ttfts, 0.50), 2) if ttfts else None,
        "ttft_p95_s": round(percentile_nearest_rank(ttfts, 0.95), 2) if ttfts else None,
        "ttft_min_s": round(min(ttfts), 2) if ttfts else None,
        "ttft_max_s": round(max(ttfts), 2) if ttfts else None,
        "platform_ttft_p50_s": round(percentile_nearest_rank(platform, 0.50), 2) if platform else None,
        "retrieval_p50_ms": percentile_nearest_rank(retrieval, 0.50) if retrieval else None,
        "retrieval_max_ms": max(retrieval) if retrieval else None,
        "mean_s": round(statistics.fmean(totals), 2),
        "rows": rows,
    }


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("base", help="Query API base URL, e.g. http://127.0.0.1:8080")
    parser.add_argument("token", help="platform JWT with the query scope")
    parser.add_argument("--rounds", type=int, default=8)
    parser.add_argument("--offset", type=int, default=0,
                        help="start index into the question pool")
    parser.add_argument("--timeout", type=float, default=120.0)
    parser.add_argument("--json-out", default="")
    args = parser.parse_args()

    rounds = max(1, args.rounds)
    pool = DEFAULT_QUESTIONS * ((args.offset + rounds) // len(DEFAULT_QUESTIONS) + 1)
    questions = pool[args.offset:args.offset + rounds]

    with urllib.request.urlopen(args.base + "/healthz", timeout=10) as response:
        print(f"healthz: {response.status}")

    rows = []
    for index, question in enumerate(questions, start=1):
        row = read_one_round(args.base, args.token, question, args.timeout)
        rows.append(row)
        ttft = "None" if row["ttft_s"] is None else f"{row['ttft_s']:.2f}"
        print(
            f"[{index}/{rounds}] total={row['total_s']:.2f}s ttft={ttft}s "
            f"platform_ttft={row['platform_ttft']} retrieval_ms={row['retrieval_ms']} "
            f"deltas={row['delta_events']} cache_hit={row['cache_hit']} "
            f"sources={row['source_count']} chars={row['answer_chars']} q={question}"
            + (f" ERROR={row['error']}" if row["error"] else "")
        )

    summary = summarize(rows, rounds)
    if summary is None:
        print("\nno round succeeded; no percentiles to report")
        return 1

    print("\n--- summary ---")
    print(json.dumps({k: v for k, v in summary.items() if k != "rows"},
                     ensure_ascii=False, indent=2))
    print(f"note: n={summary['ok_rounds']} -> nearest-rank p95 is the maximum, "
          f"not an interpolated tail")
    if summary["cache_hits"]:
        print(f"warning: {summary['cache_hits']} round(s) hit the semantic cache; "
              f"their retrieval time is the cache, not the system")
    if args.json_out:
        with open(args.json_out, "w", encoding="utf-8") as handle:
            json.dump(summary, handle, ensure_ascii=False, indent=2)
        print(f"wrote {args.json_out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
