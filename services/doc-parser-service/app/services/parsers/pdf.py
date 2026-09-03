"""PDF Parser using PyMuPDF + OCR fallback for scanned (image-only) PDFs."""
import fitz
import os
from loguru import logger
from typing import Tuple

_ocr = None


def _get_ocr():
    """Lazily initialize the OCR engine so text PDFs never pay the model load."""
    global _ocr
    if _ocr is None:
        from rapidocr_onnxruntime import RapidOCR
        _ocr = RapidOCR()
        logger.info("OCR engine loaded (for scanned PDFs)")
    return _ocr


def _ocr_page(page: fitz.Page, page_num: int, dpi: int = 200) -> str:
    """Render a page to an image and OCR the text. Returns '' on failure."""
    try:
        pix = page.get_pixmap(dpi=dpi)
        img_bytes = pix.tobytes("png")
        result, _ = _get_ocr()(img_bytes)
        if result:
            lines = [str(line[1]).strip() for line in result if len(line) > 1 and str(line[1]).strip()]
            return "\n".join(lines)
        return ""
    except Exception as e:
        logger.warning(f"OCR failed on page {page_num + 1}: {e}")
        return ""


def parse_pdf(file_path: str, page_start: int = 0, page_end: int | None = None) -> Tuple[str, int]:
    """
    Parse PDF file and extract text. Pages without a text layer (scanned
    documents) fall back to OCR.
    """
    try:
        doc = fitz.open(file_path)
        text_parts = []
        ocr_pages = 0
        page_count = len(doc)
        page_start = max(0, page_start)
        page_end = page_count if page_end is None else min(page_count, max(page_start, page_end))

        for page_num in range(page_start, page_end):
            page = doc[page_num]
            text = page.get_text("text")
            if text.strip():
                # Add page marker for better chunking
                text_parts.append(f"\n--- Page {page_num + 1} ---\n{text}")
                continue

            # No text layer — try OCR.
            ocr_text = _ocr_page(page, page_num)
            if ocr_text.strip():
                ocr_pages += 1
                text_parts.append(f"\n--- Page {page_num + 1} ---\n{ocr_text}")

        doc.close()
        full_text = "\n".join(text_parts)

        with open(file_path, 'rb') as f:
            file_size = len(f.read())

        logger.info(
            f"Parsed PDF: {file_path}, pages={page_start + 1}-{page_end}/{page_count}, "
            f"ocr_pages={ocr_pages}, size={file_size} bytes"
        )
        return full_text, file_size

    except Exception as e:
        logger.error(f"Failed to parse PDF {file_path}: {e}")
        raise
