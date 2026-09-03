"""Parse document endpoint"""
from fastapi import APIRouter, Depends, HTTPException, UploadFile, File, Form
from fastapi.concurrency import run_in_threadpool
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
import fitz

router = APIRouter(prefix="/api/v1", tags=["parser"], dependencies=[Depends(require_internal_token)])

ALLOWED_EXTENSIONS = {
    ".pdf", ".docx", ".doc", ".txt", ".md", ".markdown", ".csv", ".log", ".rtf", ".odt",
    ".png", ".jpg", ".jpeg", ".webp", ".bmp", ".xls", ".xlsx", ".pptx",
}

STREAM_CHUNK_SIZE = 1024 * 1024


@router.post("/parse", response_model=ParseResponse)
async def parse_document_endpoint(
    doc_id: str = Form(...),
    tenant_id: str = Form(...),
    file: UploadFile = File(...),
    permission: str = Form(None),
    file_hash: str = Form(None),
    metadata: str = Form(None),
    page_start: int = Form(0),
    page_end: int = Form(None),
    chunk_index_offset: int = Form(0),
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
        # FastAPI replaces Form defaults with concrete values during HTTP
        # requests. Direct callers/tests may invoke the function without
        # dependency resolution, leaving Form objects in these parameters;
        # normalize those defaults before deciding whether this is a paged PDF
        # parse.
        if not isinstance(page_start, int):
            page_start = 0
        if not isinstance(page_end, int):
            page_end = None
        if not isinstance(chunk_index_offset, int):
            chunk_index_offset = 0
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
        pdf_page_count = 0
        if suffix == ".pdf":
            with fitz.open(tmp_path) as pdf_doc:
                pdf_page_count = len(pdf_doc)
        
        # Parse document
        if page_start or page_end is not None:
            extracted_text, file_size, parser_name = await run_in_threadpool(parse_document, tmp_path, page_start, page_end)
        else:
            extracted_text, file_size, parser_name = await run_in_threadpool(parse_document, tmp_path)
        
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
        if chunk_index_offset:
            for index, chunk in enumerate(chunks):
                chunk["index"] = chunk_index_offset + index
                chunk["chunk_id"] = f"{doc_id}_{chunk_index_offset + index:04d}"
        
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
            status="success",
            page_count=pdf_page_count or (page_end or 0),
            page_start=page_start,
            page_end=page_end if page_end is not None else (pdf_page_count or page_start),
            ocr_pages=sum(1 for line in extracted_text.splitlines() if line.startswith("--- Page ")),
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
