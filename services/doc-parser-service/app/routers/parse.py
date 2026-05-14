"""Parse document endpoint"""
from fastapi import APIRouter, HTTPException, UploadFile, File, Form
from loguru import logger
import time
import os
import tempfile
import hashlib

from app.models import ParseRequest, ParseResponse, ChunkResponse
from app.services.parser import parse_document
from app.services.chunker import chunk_text

router = APIRouter(prefix="/api/v1", tags=["parser"])


@router.post("/parse", response_model=ParseResponse)
async def parse_document_endpoint(
    doc_id: str = Form(...),
    tenant_id: str = Form(...),
    file: UploadFile = File(...),
    permission: str = Form(None),
    file_hash: str = Form(None)
):
    """
    Parse uploaded document and return semantic chunks
    
    - **doc_id**: Document identifier
    - **tenant_id**: Tenant identifier  
    - **file**: Document file (PDF, DOCX, TXT, MD, etc.)
    - **permission**: Optional permission level
    - **file_hash**: Optional file hash
    """
    start_time = time.time()
    
    try:
        # Save uploaded file to temp
        suffix = os.path.splitext(file.filename)[1]
        with tempfile.NamedTemporaryFile(delete=False, suffix=suffix) as tmp:
            content = await file.read()
            tmp.write(content)
            tmp_path = tmp.name
        
        logger.info(f"Processing file: {file.filename} -> {tmp_path}")
        
        # Parse document
        extracted_text, file_size, parser_name = parse_document(tmp_path)
        
        # Generate file hash if not provided
        if not file_hash:
            file_hash = hashlib.sha256(content).hexdigest()
        
        # Chunk text
        chunks = chunk_text(
            text=extracted_text,
            doc_id=doc_id,
            tenant_id=tenant_id,
            permission=permission,
            file_hash=file_hash
        )
        
        # Clean up temp file
        os.unlink(tmp_path)
        
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
    except Exception as e:
        logger.error(f"Parse failed: {e}")
        raise HTTPException(status_code=500, detail=f"Parse failed: {str(e)}")


@router.post("/parse-file-path", response_model=ParseResponse)
async def parse_file_path_endpoint(request: ParseRequest):
    """
    Parse document from server-side file path
    
    - **doc_id**: Document identifier
    - **tenant_id**: Tenant identifier
    - **file_path**: Path to file on server
    - **permission**: Optional permission level
    """
    if not request.file_path:
        raise HTTPException(status_code=400, detail="file_path is required")
    
    start_time = time.time()
    
    try:
        # Parse document
        extracted_text, file_size, parser_name = parse_document(request.file_path)
        
        # Chunk text
        chunks = chunk_text(
            text=extracted_text,
            doc_id=request.doc_id,
            tenant_id=request.tenant_id,
            permission=request.permission,
            file_hash=None
        )
        
        parse_time_ms = (time.time() - start_time) * 1000
        
        # Build response
        chunk_responses = [
            ChunkResponse(**chunk) for chunk in chunks
        ]
        
        return ParseResponse(
            doc_id=request.doc_id,
            tenant_id=request.tenant_id,
            chunks=chunk_responses,
            total_chunks=len(chunks),
            parse_time_ms=round(parse_time_ms, 2),
            file_size_bytes=file_size,
            status="success"
        )
        
    except FileNotFoundError as e:
        raise HTTPException(status_code=404, detail=str(e))
    except Exception as e:
        logger.error(f"Parse failed: {e}")
        raise HTTPException(status_code=500, detail=f"Parse failed: {str(e)}")
