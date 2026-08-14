"""Image Parser using RapidOCR for standalone image files (PNG, JPG, ...).

Reuses the same lazily-loaded OCR engine as the scanned-PDF path, so text
documents never pay the model load cost.
"""
import os
from typing import Tuple

import cv2
from loguru import logger

_ocr = None


def _get_ocr():
    """Lazily initialize the OCR engine so non-image parses never load it."""
    global _ocr
    if _ocr is None:
        from rapidocr_onnxruntime import RapidOCR
        _ocr = RapidOCR()
        logger.info("OCR engine loaded (for images)")
    return _ocr


def parse_image(file_path: str) -> Tuple[str, int]:
    """
    OCR text from a standalone image file.

    Args:
        file_path: Path to the image file

    Returns:
        Tuple of (extracted_text, file_size_bytes)

    Raises:
        FileNotFoundError: If the file doesn't exist
        ValueError: If the file is not a decodable image
    """
    if not os.path.exists(file_path):
        raise FileNotFoundError(f"File not found: {file_path}")

    file_size = os.path.getsize(file_path)

    try:
        img = cv2.imread(file_path)
        if img is None:
            raise ValueError(f"unable to decode image: {file_path}")

        result, _ = _get_ocr()(img)
        lines = []
        if result:
            for item in result:
                if len(item) > 1 and str(item[1]).strip():
                    lines.append(str(item[1]).strip())
        text = "\n".join(lines)
        logger.info(f"OCR'd image: {file_path}, text_len={len(text)}, size={file_size} bytes")
        return text, file_size
    except ValueError:
        raise
    except Exception as e:
        logger.error(f"Image OCR failed for {file_path}: {e}")
        raise
