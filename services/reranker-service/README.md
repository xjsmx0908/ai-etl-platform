# Reranker Service

HTTP Cross-Encoder reranker for the retrieval gateway.

Default model:

```text
cross-encoder/ms-marco-MiniLM-L6-v2
```

Run with Docker Compose:

```bash
RETRIEVAL_ENABLE_RERANK=true \
RETRIEVAL_RERANK_POLICY=auto \
RERANK_ENDPOINT=http://reranker-service:8091/rerank \
docker compose --profile rerank up -d --build reranker-service query-api
```

The Query API uses `RETRIEVAL_RERANK_POLICY=auto` by default: exact identifier and keyword queries keep the fused retrieval order, while semantic and hybrid queries call this service.

API:

```bash
curl -X POST http://localhost:8091/rerank \
  -H 'Content-Type: application/json' \
  -d '{
    "query": "How many people live in Berlin?",
    "documents": [
      "Berlin had a population of 3.5 million.",
      "Berlin has many museums."
    ],
    "top_n": 1
  }'
```

Response shape is compatible with the Go retrieval client:

```json
{
  "results": [
    {"index": 0, "relevance_score": 8.6}
  ]
}
```

For local tests without downloading a model, set:

```bash
RERANKER_BACKEND=lexical
```
