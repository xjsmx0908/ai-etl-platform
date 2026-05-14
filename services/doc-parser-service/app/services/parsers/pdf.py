"""PDF Parser using PyMuPDF"""
import fitz
from loguru import logger
from typing import Tuple


def parse_pdf(file_path: str) -> Tuple[str, int]:
    """
    Parse PDF file and extract text
    
    Args:
        file_path: Path to PDF file
        
    Returns:
        Tuple of (extracted_text, file_size_bytes)
    """
    try:
        doc = fitz.open(file_path)
        text_parts = []
        
        for page_num in range(len(doc)):
            page = doc[page_num]
            text = page.get_text("text")
            if text.strip():
                # Add page marker for better chunking
                text_parts.append(f"\n--- Page {page_num + 1} ---\n{text}")
        
        doc.close()
        full_text = "\n".join(text_parts)
        
        # Get file size
        with open(file_path, 'rb') as f:
            file_size = len(f.read())
        
        logger.info(f"Parsed PDF: {file_path}, pages={len(doc)}, size={file_size} bytes")
        return full_text, file_size
        
    except Exception as e:
        logger.error(f"Failed to parse PDF {file_path}: {e}")
        raise
