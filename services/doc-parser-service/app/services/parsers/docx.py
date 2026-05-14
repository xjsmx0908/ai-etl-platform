"""DOCX Parser using python-docx"""
from docx import Document
from loguru import logger
from typing import Tuple
import os


def parse_docx(file_path: str) -> Tuple[str, int]:
    """
    Parse DOCX file and extract text with structure
    
    Args:
        file_path: Path to DOCX file
        
    Returns:
        Tuple of (extracted_text, file_size_bytes)
    """
    try:
        doc = Document(file_path)
        text_parts = []
        
        for para in doc.paragraphs:
            # Preserve heading structure
            if para.style.name.startswith('Heading'):
                level = para.style.name.replace('Heading ', '')
                prefix = '#' * int(level) if level.isdigit() else '#'
                text_parts.append(f"\n{prefix} {para.text}\n")
            else:
                text_parts.append(para.text)
        
        full_text = "\n".join(text_parts)
        file_size = os.path.getsize(file_path)
        
        logger.info(f"Parsed DOCX: {file_path}, paragraphs={len(doc.paragraphs)}, size={file_size} bytes")
        return full_text, file_size
        
    except Exception as e:
        logger.error(f"Failed to parse DOCX {file_path}: {e}")
        raise
