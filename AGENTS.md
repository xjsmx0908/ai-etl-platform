# Repository Guidelines

## Project Structure & Module Organization
This repository contains an AI ETL/RAG platform with Go and Python services. `services/etl-worker/` is the Go module for the Kafka ETL worker and Query API; entry points live in `cmd/worker` and `cmd/api`, while reusable packages live under `internal/`. `services/doc-parser-service/` is the Python FastAPI parser service, with application code in `app/` and tests in `tests/`. `services/reranker-service/` is an optional FastAPI Cross-Encoder reranker enabled through the `rerank` Docker Compose profile. Shared orchestration and operational assets are at the root: `docker-compose.yml`, `infrastructure/`, `deploy/`, `scripts/`, `docs/`, and `secrets/examples/`.

## Build, Test, and Development Commands
- `docker compose up -d`: starts the full local stack.
- `docker compose ps` / `docker compose logs -f`: inspect running services.
- `bash scripts/e2e-smoke.sh`: runs the end-to-end upload, parse, embed, store, and query smoke flow.
- `python3 scripts/run-evals.py`: runs deterministic RAG regression checks.
- `uv run --with-requirements scripts/requirements-test.txt python -m unittest discover -s scripts/tests -p 'test_*.py' -q`: runs evaluation, governance, and evidence-schema contracts in an isolated Python environment.
- `cd services/etl-worker && make build`: builds both Go binaries.
- `cd services/etl-worker && make test`: runs Go race tests with coverage for `internal/...`.
- `cd services/doc-parser-service && pip install -r requirements.txt && pytest -q`: installs parser dependencies and runs Python tests.
- `cd services/reranker-service && pip install -r requirements-test.txt && RERANKER_BACKEND=lexical RERANKER_LOAD_ON_STARTUP=false pytest -q`: runs lightweight reranker tests without downloading a model.

## Coding Style & Naming Conventions
Use `gofmt` for Go and keep package names short, lowercase, and domain-focused. Go tests should sit beside the package under test and use `_test.go`. Python code follows standard PEP 8 conventions: four-space indentation, `snake_case` functions/modules, and typed Pydantic models where request or response shapes are involved. Keep configuration environment-driven and mirror new settings in `.env.example` files.

## Testing Guidelines
Add or update tests with behavioral changes. Go coverage is collected by `make test`; CI also runs `go vet` and `go test ./... -count=1`. Python tests use `pytest` from `services/doc-parser-service` and `services/reranker-service`. Prefer deterministic fixtures and mocks for external systems; use `scripts/e2e-smoke.sh` only for full-stack validation.

## Commit & Pull Request Guidelines
The Git history uses Conventional Commit-style prefixes such as `feat:`, `fix:`, and `chore:`. Keep commits focused and describe the user-visible or operational impact. Pull requests should include a concise summary, linked issue when applicable, verification commands run, and screenshots or logs for API/operational behavior changes. Branch protection expects one approval and the `Required Checks` CI gate to pass before merge.

## Security & Configuration Tips
Never commit real credentials. Use `.env.example` and `secrets/examples/` as templates, and keep local secrets in ignored files. Parser endpoints require `X-Internal-Token` outside development; preserve tenant, permission, JWT, and CORS checks when changing request flows.
