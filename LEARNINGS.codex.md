# Codex Learnings

This file is an append-only record of completed PRAR cycles.

## 2026-08-19 - Legacy Word DOC Parsing

- **Perceive:** A genuine OLE Microsoft Word `.doc` upload repeatedly failed in parser-service with `PackageNotFoundError` and entered the Kafka DLQ.
- **Reason:** The upload and worker layers accepted `.doc`, but parser-service routed it to `python-docx`, which only reads OOXML packages. The runtime image had no legacy Word converter.
- **Act:** Added a dedicated parser that runs headless LibreOffice without a shell, inside a per-request temporary profile, with a 60-second timeout. Kept DOCX parsing unchanged and preserved the original upload size.
- **Refine:** Added a public-boundary regression test, rebuilt the production image, ran all parser tests, and replayed the original object through the authenticated parser API. The request succeeded with 18 chunks.
- **Prevention:** Every advertised file extension needs a fixture or boundary test that exercises its real on-disk format; sharing a parser based only on similar extensions is insufficient.

## 2026-08-19 - Document Content Search Web Proxy

- **Perceive:** Content searches from the Web document manager returned `q is required`, while the same `q` request sent directly to Query API succeeded.
- **Reason:** The client generated `/api/documents/search?q=...`, but no matching static Next.js route existed. The request fell through to `/api/documents/[id]`, where `search` became a document id and the query string was dropped.
- **Act:** Added a dedicated search proxy that forwards the complete query string and the HttpOnly session token. Extended the isolated smoke stack with a Web port and a session-proxy content-search assertion.
- **Refine:** Rebuilt and redeployed Web, verified the reported Chinese query returned HTTP 200, and passed the production build, TypeScript check, service tests, Trivy/configuration gates, and full pipeline smoke. `npm audit` retained the existing Next.js/PostCSS findings, and the deterministic eval runner remained blocked by an existing Compose compatibility issue where an unpublished port resolves to `0`.
- **Prevention:** Every client API path needs a matching proxy-route test at the browser-facing boundary; backend endpoint coverage alone cannot detect Next.js route fallthrough.
