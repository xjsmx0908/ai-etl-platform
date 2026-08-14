"""Tests for standalone image OCR (PNG/JPG/...)."""
from unittest import mock

import cv2
import numpy as np
import pytest

from app.services.parser import parse_document
from app.services.parsers.image import parse_image


def _write_image(path: str) -> None:
    """Write a small blank-but-valid image with cv2."""
    img = np.zeros((40, 240, 3), dtype=np.uint8)
    cv2.imwrite(path, img)


# Mirrors RapidOCR's result shape: [[box, text, confidence], ...]
FAKE_OCR = [
    [[0, 0, 60, 0, 60, 20, 0, 20], "frame header 0x5d", 0.95],
    [[0, 30, 60, 30, 60, 50, 0, 50], "payload length 100", 0.90],
]


def test_parse_image_returns_ocr_text(tmp_path):
    path = str(tmp_path / "demo.png")
    _write_image(path)
    with mock.patch("app.services.parsers.image._get_ocr") as mock_ocr:
        mock_ocr.return_value = mock.Mock(return_value=(FAKE_OCR, 0.01))
        text, size = parse_image(path)
    assert "frame header 0x5d" in text
    assert "payload length 100" in text
    assert size > 0


def test_parse_image_missing_file():
    with pytest.raises(FileNotFoundError):
        parse_image("/nonexistent/nope.png")


@pytest.mark.parametrize("ext", [".png", ".jpg", ".jpeg", ".webp", ".bmp"])
def test_parse_document_routes_image_extensions(tmp_path, ext):
    path = str(tmp_path / f"photo{ext}")
    _write_image(path)
    with mock.patch("app.services.parsers.image._get_ocr") as mock_ocr:
        mock_ocr.return_value = mock.Mock(return_value=(FAKE_OCR, 0.01))
        text, size, parser_name = parse_document(path)
    assert "frame header 0x5d" in text
    assert parser_name == ext[1:]  # "png", "jpg", ...
