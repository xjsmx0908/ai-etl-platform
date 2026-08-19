# Codex Learnings

This file is an append-only record of completed PRAR cycles.

## 2026-08-19 - Legacy Word DOC Parsing

- **Perceive:** A genuine OLE Microsoft Word `.doc` upload repeatedly failed in parser-service with `PackageNotFoundError` and entered the Kafka DLQ.
- **Reason:** The upload and worker layers accepted `.doc`, but parser-service routed it to `python-docx`, which only reads OOXML packages. The runtime image had no legacy Word converter.
- **Act:** Added a dedicated parser that runs headless LibreOffice without a shell, inside a per-request temporary profile, with a 60-second timeout. Kept DOCX parsing unchanged and preserved the original upload size.
- **Refine:** Added a public-boundary regression test, rebuilt the production image, ran all parser tests, and replayed the original object through the authenticated parser API. The request succeeded with 18 chunks.
- **Prevention:** Every advertised file extension needs a fixture or boundary test that exercises its real on-disk format; sharing a parser based only on similar extensions is insufficient.
