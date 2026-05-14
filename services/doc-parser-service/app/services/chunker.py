"""Semantic chunking with heading-aware splitting and paragraph merging"""
import re
from typing import List, Dict, Any
from loguru import logger
from app.config import get_settings


def chunk_text(
    text: str,
    doc_id: str,
    tenant_id: str,
    permission: str = None,
    file_hash: str = None
) -> List[Dict[str, Any]]:
    """
    Split text into semantic chunks
    
    Strategy:
    - Markdown headings (H1-H6) always start a new chunk
    - Empty lines act as paragraph boundaries
    - Short paragraphs (< MIN_CHUNK_SIZE) are merged with next one
    - Oversized paragraphs are force-split with overlap
    
    Args:
        text: Extracted text content
        doc_id: Document ID
        tenant_id: Tenant ID
        permission: Permission level
        file_hash: File hash
        
    Returns:
        List of chunk dictionaries
    """
    settings = get_settings()
    min_size = settings.MIN_CHUNK_SIZE
    max_size = settings.MAX_CHUNK_SIZE
    overlap = settings.CHUNK_OVERLAP
    
    lines = text.split('\n')
    chunks = []
    buffer = []
    chunk_idx = 0
    
    for line in lines:
        # Check if line is a markdown heading
        if is_heading(line) and buffer:
            # Emit current buffer as chunk
            chunk_text = '\n'.join(buffer).strip()
            if chunk_text:
                chunks.extend(
                    split_oversized_chunk(chunk_text, doc_id, tenant_id, chunk_idx, 
                                         permission, file_hash, max_size, overlap)
                )
                chunk_idx = len(chunks)
            buffer = [line]
            continue
        
        # Empty line = paragraph boundary
        if line.strip() == '' and buffer:
            buffer_text = '\n'.join(buffer).strip()
            
            # Only emit if buffer exceeds minimum size (merge short paragraphs)
            if len(buffer_text) >= min_size:
                chunks.extend(
                    split_oversized_chunk(buffer_text, doc_id, tenant_id, chunk_idx,
                                         permission, file_hash, max_size, overlap)
                )
                chunk_idx = len(chunks)
                
                # Keep overlap for next chunk
                last_chunk = chunks[-1]['content'] if chunks else ''
                buffer = [get_overlap(last_chunk, overlap)] if overlap > 0 else []
            # If too short, keep accumulating (paragraph merge)
            else:
                buffer.append(line)
            continue
        
        buffer.append(line)
        
        # Force split if buffer exceeds max size
        buffer_text = '\n'.join(buffer)
        if len(buffer_text) >= max_size:
            chunks.extend(
                split_oversized_chunk(buffer_text, doc_id, tenant_id, chunk_idx,
                                     permission, file_hash, max_size, overlap)
            )
            chunk_idx = len(chunks)
            
            # Keep overlap
            last_chunk = chunks[-1]['content'] if chunks else ''
            buffer = [get_overlap(last_chunk, overlap)] if overlap > 0 else []
    
    # Flush remaining content
    if buffer:
        chunk_text = '\n'.join(buffer).strip()
        if chunk_text:
            chunks.extend(
                split_oversized_chunk(chunk_text, doc_id, tenant_id, chunk_idx,
                                     permission, file_hash, max_size, overlap)
            )
    
    logger.info(f"Chunked document {doc_id}: {len(chunks)} chunks created")
    return chunks


def is_heading(line: str) -> bool:
    """Check if line is a markdown heading (H1-H6)"""
    stripped = line.strip()
    if len(stripped) < 2:
        return False
    
    level = 0
    for ch in stripped:
        if ch == '#':
            level += 1
        else:
            break
    
    return 1 <= level <= 6 and len(stripped) > level and stripped[level] == ' '


def split_oversized_chunk(
    text: str,
    doc_id: str,
    tenant_id: str,
    start_idx: int,
    permission: str,
    file_hash: str,
    max_size: int,
    overlap: int
) -> List[Dict[str, Any]]:
    """Split oversized chunk into smaller pieces with overlap"""
    chunks = []
    
    if len(text) <= max_size:
        # No split needed
        chunks.append({
            'chunk_id': f"{doc_id}_{start_idx:04d}",
            'doc_id': doc_id,
            'tenant_id': tenant_id,
            'content': text,
            'index': start_idx,
            'token_count': estimate_tokens(text),
            'permission': permission,
            'file_hash': file_hash
        })
        return chunks
    
    # Force split with overlap
    pos = 0
    local_idx = 0
    while pos < len(text):
        # Take chunk-sized slice
        end = min(pos + max_size, len(text))
        chunk_text = text[pos:end].strip()
        
        if chunk_text:
            chunks.append({
                'chunk_id': f"{doc_id}_{start_idx + local_idx:04d}",
                'doc_id': doc_id,
                'tenant_id': tenant_id,
                'content': chunk_text,
                'index': start_idx + local_idx,
                'token_count': estimate_tokens(chunk_text),
                'permission': permission,
                'file_hash': file_hash
            })
            local_idx += 1
        
        # Move position with overlap
        if end >= len(text):
            break
        pos = end - overlap if overlap < end else 0
    
    return chunks


def get_overlap(text: str, overlap_size: int) -> str:
    """Get overlap suffix from text"""
    if len(text) <= overlap_size:
        return text
    return text[-overlap_size:]


def estimate_tokens(text: str) -> int:
    """
    Rough token count estimation
    Rule of thumb: 1 token ≈ 4 characters for English
    """
    # Simple heuristic: 1 token ≈ 4 chars
    return len(text) // 4
