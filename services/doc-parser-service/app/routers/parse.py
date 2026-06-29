"""Parse document endpoint"""
from fastapi import APIRouter, Depends, HTTPException, UploadFile, File, Form
from loguru import logger
import time
import os
import tempfile
import hashlib
import json

from app.config import get_settings
from app.models import ParseResponse, ChunkResponse
from app.security import require_internal_token
from app.services.parser import parse_document
from app.services.chunker import chunk_text

router = APIRouter(prefix="/api/v1", tags=["parser"], dependencies=[Depends(require_internal_token)])

ALLOWED_EXTENSIONS = {
    ".pdf", ".docx", ".doc", ".txt", ".md", ".markdown", ".csv", ".log", ".rtf", ".odt"
}

STREAM_CHUNK_SIZE = 1024 * 1024


@router.post("/parse", response_model=ParseResponse)
async def parse_document_endpoint(
    doc_id: str = Form(...),
    tenant_id: str = Form(...),
    file: UploadFile = File(...),
    permission: str = Form(None),
    file_hash: str = Form(None),
    metadata: str = Form(None)
):
    """
    Parse uploaded document and return semantic chunks
    
    - **doc_id**: Document identifier
    - **tenant_id**: Tenant identifier  
    - **file**: Document file (PDF, DOCX, TXT, MD, etc.)
    - **permission**: Optional permission level
    - **file_hash**: Optional file hash
    - **metadata**: Optional JSON object with business exact-match fields
    """
    start_time = time.time()
    tmp_path = None
    file_size = 0
    content_hash = None
    
    try:
        settings = get_settings()
        max_file_size_bytes = settings.MAX_FILE_SIZE_MB * 1024 * 1024
        metadata_values = parse_metadata(metadata)

        # Save uploaded file to temp in chunks to avoid loading the entire file into memory.
        suffix = os.path.splitext(file.filename or "")[1].lower()
        if suffix not in ALLOWED_EXTENSIONS:
            raise HTTPException(status_code=415, detail=f"unsupported file type: {suffix or 'unknown'}")
        with tempfile.NamedTemporaryFile(delete=False, suffix=suffix) as tmp:
            hasher = hashlib.sha256()
            while True:
                chunk = await file.read(STREAM_CHUNK_SIZE)
                if not chunk:
                    break
                file_size += len(chunk)
                if file_size > max_file_size_bytes:
                    raise HTTPException(
                        status_code=413,
                        detail=f"file too large (max {settings.MAX_FILE_SIZE_MB}MB)"
                    )
                tmp.write(chunk)
                hasher.update(chunk)
            tmp_path = tmp.name
            content_hash = hasher.hexdigest()
        
        logger.info(f"Processing file: {file.filename} -> {tmp_path}")
        
        # Parse document
        extracted_text, file_size, parser_name = parse_document(tmp_path)
        
        # Generate file hash if not provided
        if not file_hash:
            file_hash = content_hash
        
        # Chunk text
        chunks = chunk_text(
            text=extracted_text,
            doc_id=doc_id,
            tenant_id=tenant_id,
            permission=permission,
            file_hash=file_hash,
            metadata=metadata_values
        )
        
        parse_time_ms = (time.time() - start_time) * 1000
        
        # Build response
        chunk_responses = [
            ChunkResponse(**chunk) for chunk in chunks
        ]
        
        return ParseResponse(
            doc_id=doc_id,
            tenant_id=tenant_id,
            chunks=chunk_responses,
            total_chunks=len(chunks),
            parse_time_ms=round(parse_time_ms, 2),
            file_size_bytes=file_size,
            status="success"
        )
        
    except FileNotFoundError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except HTTPException:
        raise
    except Exception as e:
        logger.error(f"Parse failed: {e}")
        raise HTTPException(status_code=500, detail=f"Parse failed: {str(e)}")
    finally:
        if tmp_path and os.path.exists(tmp_path):
            os.unlink(tmp_path)


def parse_metadata(raw: str | None) -> dict[str, str] | None:
    if raw is None or raw.strip() == "":
        return None
    try:
        values = json.loads(raw)
    except json.JSONDecodeError as exc:
        raise HTTPException(status_code=400, detail="metadata must be a JSON object with string values") from exc
    if not isinstance(values, dict):
        raise HTTPException(status_code=400, detail="metadata must be a JSON object with string values")

    clean = {}
    for key, value in values.items():
        if not isinstance(key, str) or not isinstance(value, str):
            raise HTTPException(status_code=400, detail="metadata must be a JSON object with string values")
        key = key.strip()
        value = value.strip()
        if not key or not value:
            continue
        if len(key) > 64 or len(value) > 512:
            raise HTTPException(status_code=400, detail="metadata key/value too long")
        clean[key] = value
    return clean or None
