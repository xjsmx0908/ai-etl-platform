import os

os.environ["RERANKER_BACKEND"] = "lexical"
os.environ["RERANKER_LOAD_ON_STARTUP"] = "false"

from fastapi.testclient import TestClient

from app.main import app, reranker


def test_rerank_orders_documents_by_relevance():
    client = TestClient(app)
    reranker.load()

    resp = client.post(
        "/rerank",
        json={
            "query": "refund order alpha040",
            "documents": [
                "general shipping policy",
                "refund status for order alpha040 is complete",
            ],
            "top_n": 1,
        },
    )

    assert resp.status_code == 200
    body = resp.json()
    assert body["results"][0]["index"] == 1
    assert body["results"][0]["relevance_score"] > 0
