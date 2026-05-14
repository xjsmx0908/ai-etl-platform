"""Text Parser for TXT, MD, CSV, LOG files"""
import chardet
from loguru import logger
from typing import Tuple
import os


def parse_text(file_path: str) -> Tuple[str, int]:
    """
    Parse text file with automatic encoding detection
    
    Args:
        file_path: Path to text file
        
    Returns:
        Tuple of (extracted_text, file_size_bytes)
    """
    try:
        # Read binary and detect encoding
        with open(file_path, 'rb') as f:
            raw_data = f.read()
        
        # Detect encoding
        result = chardet.detect(raw_data)
        encoding = result['encoding'] or 'utf-8'
        confidence = result['confidence']
        
        logger.debug(f"Detected encoding: {encoding} (confidence: {confidence:.2f})")
        
        # Decode with detected encoding
        try:
            text = raw_data.decode(encoding)
        except UnicodeDecodeError:
            # Fallback to UTF-8 with error handling
            logger.warning(f"Failed to decode with {encoding}, falling back to UTF-8")
            text = raw_data.decode('utf-8', errors='replace')
        
        file_size = len(raw_data)
        
        logger.info(f"Parsed text file: {file_path}, encoding={encoding}, size={file_size} bytes")
        return text, file_size
        
    except Exception as e:
        logger.error(f"Failed to parse text file {file_path}: {e}")
        raise
