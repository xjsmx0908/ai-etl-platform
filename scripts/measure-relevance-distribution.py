#!/usr/bin/env python3
"""Measure query<->document cosine distributions for the relevance-gate decision.

Re-measures ADR 0006 on the current embedding. ADR 0006 was measured with
nomic-embed-text 768d (overlap width 0.2784, gate not enabled); it explicitly
requires re-running this measurement after any embedding change. This script
is purely offline: it embeds the golden-set documents and queries with the
live embedding endpoint and compares vectors directly, so it does NOT need
the query-api, Qdrant, or Kafka.

Outputs:
  - percentiled distributions for positives (query->target) and negatives
    (query->unrelated document), plus each query's "unrelated ceiling"
    (score of its closest unrelated document);
  - a threshold scan in the ADR 0006 table format so a future
    RETRIEVAL_MIN_RELEVANCE gate can be justified (or rejected) with data.

CLI args override env; env mirrors the Go services' precedence (KEY > KEY_FILE).
"""
from __future__ import annotations

import argparse
import json
import math
import os
import sys
import urllib.request
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DEFAULT_SET = ROOT / "docs" / "evals" / "semantic-golden-set.json"
DEFAULT_ENDPOINT = os.getenv("EMBED_ENDPOINT", "http://localhost:11434/api/embeddings")
DEFAULT_MODEL = os.getenv("EMBED_MODEL", "bge-m3")


def embed(endpoint: str, model: str, text: str, timeout: int = 30) -> list[float]:
    body = json.dumps({"model": model, "prompt": text}).encode("utf-8")
    req = urllib.request.Request(
        endpoint, data=body, headers={"Content-Type": "application/json"}
    )
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        payload = json.loads(resp.read())
    vec = payload.get("embedding")
    if not vec:
        raise RuntimeError(f"embedding endpoint returned no embedding: {payload}")
    return vec


def dot(a, b):
    return sum(x * y for x, y in zip(a, b))


def cosine(a, b):
    na = math.sqrt(dot(a, a))
    nb = math.sqrt(dot(b, b))
    if na == 0 or nb == 0:
        return 0.0
    return dot(a, b) / (na * nb)


def pctile(vals: list[float], p: float) -> float:
    if not vals:
        return 0.0
    s = sorted(vals)
    return s[min(len(s) - 1, max(0, int(p * len(s))))]


def dist(vals: list[float]) -> dict:
    if not vals:
        return {"count": 0}
    return {
        "count": len(vals),
        "min": round(min(vals), 4),
        "p25": round(pctile(vals, 0.25), 4),
        "median": round(pctile(vals, 0.5), 4),
        "p75": round(pctile(vals, 0.75), 4),
        "max": round(max(vals), 4),
        "mean": round(sum(vals) / len(vals), 4),
    }


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument("--golden-set", default=str(DEFAULT_SET))
    ap.add_argument("--embed-endpoint", default=DEFAULT_ENDPOINT)
    ap.add_argument("--embed-model", default=DEFAULT_MODEL)
    ap.add_argument("--min-threshold", type=float, default=0.45)
    ap.add_argument("--max-threshold", type=float, default=0.95)
    ap.add_argument("--threshold-step", type=float, default=0.02)
    args = ap.parse_args()

    with open(args.golden_set, encoding="utf-8") as fh:
        data = json.load(fh)
    cases = data["cases"]

    print(
        f"[measure] embedding {len(cases)} documents + {len(cases)} queries "
        f"({args.embed_model}) from {args.golden_set}",
        file=sys.stderr,
    )
    doc_vecs = [
        embed(args.embed_endpoint, args.embed_model, c["content"])
        for c in cases
    ]
    q_vecs = [
        embed(args.embed_endpoint, args.embed_model, c["query"])
        for c in cases
    ]

    positives: list[float] = []      # query -> its own target doc
    negatives: list[float] = []      # every query -> every unrelated doc
    ceilings: list[float] = []       # every query -> its closest unrelated doc
    for qi, case in enumerate(cases):
        qv = q_vecs[qi]
        unrelated = []
        for di, dv in enumerate(doc_vecs):
            if di == qi:
                continue
            score = cosine(qv, dv)
            unrelated.append(score)
            negatives.append(score)
        ceiling = max(unrelated) if unrelated else 0.0
        ceilings.append(ceiling)
        if case.get("expect_hit", True):
            positives.append(cosine(qv, doc_vecs[qi]))

    pos, neg, ceil = dist(positives), dist(negatives), dist(ceilings)
    print("[measure] distribution (query<->target = positive; query<->unrelated = negative):")
    print(f"  positives (n={pos['count']}): "
          f"min={pos['min']} p25={pos['p25']} med={pos['median']} "
          f"p75={pos['p75']} max={pos['max']} mean={pos['mean']}")
    print(f"  negatives (n={neg['count']}): "
          f"min={neg['min']} p25={neg['p25']} med={neg['median']} "
          f"p75={neg['p75']} max={neg['max']} mean={neg['mean']}")
    print(f"  unrelated ceiling per query (n={ceil['count']}): "
          f"min={ceil['min']} p25={ceil['p25']} med={ceil['median']} "
          f"p75={ceil['p75']} max={ceil['max']} mean={ceil['mean']}")

    # Overlap width of the positive/negative distributions, ADR 0006 style:
    # the size of [max(pos.min, neg.min), min(pos.max, neg.max)], 0 when disjoint.
    overlap = max(0.0, min(pos["max"], neg["max"]) - max(pos["min"], neg["min"]))
    print(f"  overlap width (positive vs negative): {overlap:.4f}")

    print("\n[measure] threshold scan (real hit dropped = target< t; negative kept = unrelated>= t):")
    print("  threshold | real_hits_dropped | neg_pairs_kept | neg_ceiling_kept")
    best = None
    for t in [
        round(x, 2)
        for x in [
            args.min_threshold + i * args.threshold_step
            for i in range(
                int((args.max_threshold - args.min_threshold) / args.threshold_step) + 1
            )
        ]
    ]:
        dropped = sum(1 for v in positives if v < t)
        neg_kept = sum(1 for v in negatives if v >= t)
        ceil_kept = sum(1 for v in ceilings if v >= t)
        dropped_ratio = dropped / len(positives) if positives else 1.0
        ceil_kept_ratio = ceil_kept / len(ceilings) if ceilings else 1.0
        if dropped == 0 and ceil_kept == 0 and best is None:
            best = t
        print(
            f"  {t:9.2f} | {dropped:>3}/{len(positives):<3} ({dropped_ratio:.1%}) "
            f"| {neg_kept:>5}/{len(negatives):<5} | {ceil_kept:>3}/{len(ceilings):<3} "
            f"({ceil_kept_ratio:.1%})"
        )

    if best is not None:
        print(f"\n[measure] clean threshold found: t={best:.2f} (0 real hits dropped, "
              f"0 query ceilings kept -> every negative's closest unrelated doc is gated)")
    else:
        print("\n[measure] NO clean threshold: any gate drops real hits or lets "
              "unrelated ceilings through -> RETRIEVAL_MIN_RELEVANCE not justified; "
              "pursue fallback mechanisms (empty-candidate / source-count / relative floor).")
    return 0


if __name__ == "__main__":
    sys.exit(main())
