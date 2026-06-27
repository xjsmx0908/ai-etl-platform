from datetime import datetime
from typing import Optional

from pydantic import BaseModel, Field


class HealthResponse(BaseModel):
    status: str
    model: str
    backend: str
    version: str = "1.0.0"
    timestamp: datetime = Field(default_factory=datetime.utcnow)


class RerankRequest(BaseModel):
    query: str = Field(..., min_length=1)
    documents: list[str]
    top_n: Optional[int] = None
    model: Optional[str] = None


class RerankResult(BaseModel):
    index: int
    relevance_score: float


class RerankResponse(BaseModel):
    model: str
    results: list[RerankResult]
    duration_ms: float = 0

