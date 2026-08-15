#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${ROOT_DIR}"

echo "[day1] 1/4 environment"
docker compose up -d

echo "[day1] 2/4 smoke"
E2E_KEEP_SERVICES=1 EMBED_DIMENSION=768 bash scripts/e2e-smoke.sh

echo "[day1] 3/4 eval"
python3 scripts/run-evals.py --embed-dim 768 --keep-services

echo "[day1] 4/4 load test"
python3 scripts/load-test.py --requests 40 --concurrency 5 --embed-dim 768

echo "[day1] done"
