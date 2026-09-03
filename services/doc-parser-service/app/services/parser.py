"""Parser orchestrator - routes to appropriate parser based on file type"""
import os
from pathlib import Path
from loguru import logger
from typing import Tuple

from app.services.parsers import pdf, docx, legacy_doc, text, image, spreadsheet, presentation


# File extension to parser mapping
PARSER_MAP = {
    '.pdf': pdf.parse_pdf,
    '.docx': docx.parse_docx,
    '.doc': legacy_doc.parse_doc,
    '.xls': spreadsheet.parse_spreadsheet,
    '.xlsx': spreadsheet.parse_spreadsheet,
    '.pptx': presentation.parse_presentation,
    '.txt': text.parse_text,
    '.md': text.parse_text,
    '.markdown': text.parse_text,
    '.csv': text.parse_text,
    '.log': text.parse_text,
    '.rtf': text.parse_text,
    '.odt': text.parse_text,
    '.png': image.parse_image,
    '.jpg': image.parse_image,
    '.jpeg': image.parse_image,
    '.webp': image.parse_image,
    '.bmp': image.parse_image,
}


def parse_document(file_path: str, page_start: int = 0, page_end: int | None = None) -> Tuple[str, int, str]:
    """
    Parse document file using appropriate parser
    
    Args:
        file_path: Path to document file
        
    Returns:
        Tuple of (extracted_text, file_size_bytes, parser_name)
        
    Raises:
        ValueError: If file type is not supported
        FileNotFoundError: If file doesn't exist
    """
    if not os.path.exists(file_path):
        raise FileNotFoundError(f"File not found: {file_path}")
    
    ext = Path(file_path).suffix.lower()
    
    if ext not in PARSER_MAP:
        # Default to text parser for unknown types
        logger.warning(f"Unknown file type {ext}, using text parser")
        parser_func = text.parse_text
        parser_name = "text"
    else:
        parser_func = PARSER_MAP[ext]
        parser_name = ext[1:]  # Remove leading dot
    
    logger.info(f"Parsing document: {file_path} (type: {parser_name})")
    
    try:
        if parser_name == "pdf":
            extracted_text, file_size = parser_func(file_path, page_start, page_end)
        else:
            extracted_text, file_size = parser_func(file_path)
        return extracted_text, file_size, parser_name
    except Exception as e:
        logger.error(f"Parser {parser_name} failed for {file_path}: {e}")
        raise
