"""Pydantic Data Models"""
from pydantic import BaseModel, Field
from typing import List, Optional
from datetime import datetime

class ChunkResponse(BaseModel):
    """Single chunk response"""
    chunk_id: str
    doc_id: str
    tenant_id: str
    content: str
    index: int
    token_count: Optional[int] = None
    permission: Optional[str] = None
    file_hash: Optional[str] = None


class ParseResponse(BaseModel):
    """Parse response model"""
    doc_id: str
    tenant_id: str
    chunks: List[ChunkResponse]
    total_chunks: int
    parse_time_ms: float
    file_size_bytes: int
    status: str = "success"


class HealthResponse(BaseModel):
    """Health check response"""
    status: str
    version: str = "1.0.0"
    timestamp: datetime = Field(default_factory=datetime.utcnow)
