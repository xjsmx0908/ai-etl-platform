import sys
from contextlib import asynccontextmanager
from time import perf_counter

from fastapi import FastAPI, HTTPException
from loguru import logger

from app.config import get_settings
from app.models import HealthResponse, RerankRequest, RerankResponse
from app.reranker import build_reranker

settings = get_settings()
logger.remove()
logger.add(
    sys.stderr,
    level=settings.LOG_LEVEL,
    format="<green>{time:YYYY-MM-DD HH:mm:ss}</green> | <level>{level: <8}</level> | <cyan>{name}</cyan>:<cyan>{function}</cyan> - <level>{message}</level>",
)

reranker = build_reranker(settings)


@asynccontextmanager
async def lifespan(_: FastAPI):
    if settings.RERANKER_LOAD_ON_STARTUP:
        logger.info("loading reranker backend={} model={}", settings.RERANKER_BACKEND, settings.RERANKER_MODEL)
        reranker.load()
    yield


app = FastAPI(
    title="Reranker Service",
    description="HTTP Cross-Encoder reranker for hybrid retrieval",
    version="1.0.0",
    lifespan=lifespan,
)


@app.get("/healthz", response_model=HealthResponse)
async def health_check():
    return HealthResponse(status="healthy", model=settings.RERANKER_MODEL, backend=settings.RERANKER_BACKEND)


@app.get("/readyz", response_model=HealthResponse)
async def readiness_check():
    if not reranker.ready:
        raise HTTPException(status_code=503, detail="reranker model is not loaded")
    return HealthResponse(status="ready", model=settings.RERANKER_MODEL, backend=settings.RERANKER_BACKEND)


@app.post("/rerank", response_model=RerankResponse)
async def rerank(request: RerankRequest):
    if not request.documents:
        return RerankResponse(model=settings.RERANKER_MODEL, results=[])
    if len(request.documents) > settings.RERANKER_MAX_DOCUMENTS:
        raise HTTPException(status_code=400, detail=f"documents exceeds max {settings.RERANKER_MAX_DOCUMENTS}")

    top_n = request.top_n if request.top_n and request.top_n > 0 else len(request.documents)
    top_n = min(top_n, len(request.documents))
    documents = [doc[: settings.RERANKER_MAX_DOCUMENT_CHARS] for doc in request.documents]

    start = perf_counter()
    results = reranker.rerank(request.query, documents, top_n)
    duration_ms = (perf_counter() - start) * 1000
    return RerankResponse(
        model=settings.RERANKER_MODEL,
        results=results,
        duration_ms=duration_ms,
    )
