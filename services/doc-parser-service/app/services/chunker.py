"""Semantic chunking with heading-aware splitting and paragraph merging"""
import re
import unicodedata
from typing import List, Dict, Any
from loguru import logger
from app.config import get_settings


def chunk_text(
    text: str,
    doc_id: str,
    tenant_id: str,
    permission: str = None,
    file_hash: str = None,
    metadata: Dict[str, str] = None
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
        metadata: Business exact-match fields
        
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
        # PDF page markers and spreadsheet sheet markers are explicit semantic
        # boundaries. Without this, extracted pages/sheets can be repeatedly
        # accumulated through short blank-line buffers and produce a staircase
        # of near-duplicate chunks, or mix unrelated tables.
        if is_section_marker(line) and buffer:
            current = '\n'.join(buffer).strip()
            if current:
                chunks.extend(
                    split_oversized_chunk(current, doc_id, tenant_id, chunk_idx,
                                         permission, file_hash, metadata, max_size, overlap)
                )
                chunk_idx = len(chunks)
            buffer = [line]
            continue

        # Check if line is a markdown heading
        if is_heading(line) and buffer:
            # Emit current buffer as chunk
            chunk_text = '\n'.join(buffer).strip()
            if chunk_text:
                chunks.extend(
                    split_oversized_chunk(chunk_text, doc_id, tenant_id, chunk_idx, 
                                         permission, file_hash, metadata, max_size, overlap)
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
                                         permission, file_hash, metadata, max_size, overlap)
                )
                chunk_idx = len(chunks)
                
                # Paragraph boundaries are semantic boundaries. Overlap is only
                # useful when force-splitting one oversized body; copying a
                # complete short paragraph into the next chunk creates near-
                # duplicate chunks and lets repeated headers dominate retrieval.
                buffer = []
            # If too short, keep accumulating (paragraph merge)
            else:
                buffer.append(line)
            continue
        
        buffer.append(line)
        
        # Force split if buffer exceeds max size. Spreadsheet sheets stay intact
        # until a section boundary so later row chunks can keep the header.
        buffer_text = '\n'.join(buffer)
        if len(buffer_text) >= max_size and not is_tabular_content(buffer_text):
            chunks.extend(
                split_oversized_chunk(buffer_text, doc_id, tenant_id, chunk_idx,
                                     permission, file_hash, metadata, max_size, overlap)
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
                                     permission, file_hash, metadata, max_size, overlap)
            )

    # Drop low-value chunks (signature pages, tables of contents, near-empty
    # table fragments) that would otherwise pollute retrieval — a long document
    # can otherwise be dominated by these noise chunks instead of its real body.
    kept = [c for c in chunks if not is_noise_chunk(c["content"])]
    dropped = len(chunks) - len(kept)
    if dropped:
        logger.info(f"Chunked document {doc_id}: dropped {dropped}/{len(chunks)} noise chunks")

    # Parser output is the earliest reliable boundary for preventing one
    # document's repeated body from occupying multiple retrieval slots. Use a
    # normalized comparison key while preserving the first chunk's text.
    unique = []
    seen = set()
    for item in kept:
        key = normalize_content_key(item["content"])
        if key in seen:
            continue
        seen.add(key)
        item["index"] = len(unique)
        item["chunk_id"] = f"{doc_id}_{len(unique):04d}"
        unique.append(item)

    duplicates = len(kept) - len(unique)
    if duplicates:
        logger.info(
            f"Chunked document {doc_id}: dropped {duplicates}/{len(kept)} duplicate chunks"
        )
    return unique


SIGNATURE_KEYWORDS = ("拟制", "审核", "批准", "会签")
TOC_PATTERNS = ("目次", "目 次", "目 录", "目录")
TOC_MAX_NOISE_CHARS = 300


def normalize_content_key(content: str) -> str:
    """Return a stable key for exact-content deduplication."""
    normalized = unicodedata.normalize("NFKC", content)
    return " ".join(normalized.split())


def is_noise_chunk(content: str) -> bool:
    """True for chunks with little retrieval value. Conservative by design:
    real document body is never dropped; only signature pages, tables of
    contents, and near-empty table fragments."""
    text = content.strip()
    if not text:
        return True

    # Only short table-of-contents fragments are safe to discard. Long business
    # bodies can legitimately mention a product/catalog directory, and dropping
    # the whole chunk would make the document silently disappear from retrieval.
    if len(text) <= TOC_MAX_NOISE_CHARS and any(k in text for k in TOC_PATTERNS):
        return True

    # Signature / approval pages are short, table-like form text.
    if any(k in text for k in SIGNATURE_KEYWORDS):
        meaningful = re.sub(r"[\s|\|+\-—＿_.,:：;；/()（）]", "", text)
        if len(meaningful) < 60:
            return True

    # Extremely low information density: mostly table lines / whitespace.
    # Tabular employee rosters are sparse by design (IDs, tabs, short names)
    # and must not be dropped as empty table fragments.
    if not is_tabular_content(text):
        meaningful = re.sub(r"[\s|\|+\-—＿_.,:：;；/()（）0-9]", "", text)
        if len(meaningful) / max(len(text), 1) < 0.3:
            return True

    return False


def is_section_marker(line: str) -> bool:
    stripped = line.strip()
    return bool(re.match(r"^--- Page \d+ ---$", stripped) or re.match(r"^--- Sheet: .+ ---$", stripped))


def is_tabular_content(text: str) -> bool:
    stripped = text.strip()
    if stripped.startswith("--- Sheet:"):
        return True
    lines = [line for line in stripped.splitlines() if line.strip()]
    if not lines:
        return False
    tabbed = sum(1 for line in lines if "\t" in line)
    if len(lines) == 1:
        return tabbed == 1
    return tabbed >= max(2, (len(lines) + 1) // 2)


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
    metadata: Dict[str, str],
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
            'file_hash': file_hash,
            'metadata': dict(metadata) if metadata else None
        })
        return chunks

    if is_tabular_content(text):
        tabular = split_tabular_text(text, max_size)
        chunks = []
        for local_idx, piece in enumerate(tabular):
            chunks.append({
                'chunk_id': f"{doc_id}_{start_idx + local_idx:04d}",
                'doc_id': doc_id,
                'tenant_id': tenant_id,
                'content': piece,
                'index': start_idx + local_idx,
                'token_count': estimate_tokens(piece),
                'permission': permission,
                'file_hash': file_hash,
                'metadata': dict(metadata) if metadata else None
            })
        if chunks:
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
                'file_hash': file_hash,
                'metadata': dict(metadata) if metadata else None
            })
            local_idx += 1
        
        # Move position with overlap
        if end >= len(text):
            break
        pos = end - overlap if overlap < end else 0
    
    return chunks



def split_tabular_text(text: str, max_size: int) -> List[str]:
    """Split a sheet on row boundaries and repeat the header on every chunk."""
    lines = text.split("\n")
    prefix_lines: List[str] = []
    body_start = 0
    for index, line in enumerate(lines):
        if is_section_marker(line):
            prefix_lines.append(line)
            continue
        if not line.strip():
            continue
        prefix_lines.append(line)
        body_start = index + 1
        break
    if not prefix_lines:
        return []

    prefix = "\n".join(prefix_lines)
    if len(prefix) >= max_size:
        return []

    pieces: List[str] = []
    current_body: List[str] = []

    def flush() -> None:
        if not current_body and pieces:
            return
        content = prefix if not current_body else prefix + "\n" + "\n".join(current_body)
        if content.strip():
            pieces.append(content)

    for line in lines[body_start:]:
        candidate = current_body + [line]
        candidate_text = prefix + "\n" + "\n".join(candidate)
        if current_body and len(candidate_text) > max_size:
            flush()
            current_body = [line]
            continue
        current_body.append(line)
    flush()
    return pieces

def get_overlap(text: str, overlap_size: int) -> str:
    """Get overlap suffix from text"""
    if len(text) <= overlap_size:
        return text
    return text[-overlap_size:]


def estimate_tokens(text: str) -> int:
    """
    Rough token count estimation.

    English: ~4 chars per token; CJK: ~1 char per token (often less, but 1 is a
    safe over-estimate that keeps chunk sizes conservative for Chinese corpora).
    The old flat `len(text) // 4` underestimated Chinese by 2-4x, which made the
    parser over-pack chunks relative to the configured MAX_CHUNK_SIZE budget.
    """
    cjk = sum(1 for ch in text if "一" <= ch <= "鿿")
    latin = len(text) - cjk
    return (latin // 4) + cjk
