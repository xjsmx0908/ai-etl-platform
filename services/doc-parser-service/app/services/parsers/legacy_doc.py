"""Legacy binary Word parser backed by headless LibreOffice."""

import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from typing import Tuple

from loguru import logger

from app.services.parsers.docx import parse_docx


CONVERSION_TIMEOUT_SECONDS = 60


class LegacyDocConversionError(RuntimeError):
    """Raised when LibreOffice cannot convert a legacy Word document."""


def parse_doc(file_path: str) -> Tuple[str, int]:
    """Convert a binary Word document to DOCX and extract its text."""
    file_size = os.path.getsize(file_path)
    libreoffice = shutil.which("libreoffice")
    if libreoffice is None:
        raise LegacyDocConversionError("LibreOffice is not installed")

    with tempfile.TemporaryDirectory(prefix="legacy-doc-") as temp_dir:
        work_dir = Path(temp_dir)
        source_path = work_dir / "source.doc"
        output_path = work_dir / "source.docx"
        profile_dir = work_dir / "profile"
        profile_dir.mkdir()
        shutil.copyfile(file_path, source_path)

        command = [
            libreoffice,
            "--headless",
            "--nologo",
            "--nodefault",
            "--nolockcheck",
            "--norestore",
            f"-env:UserInstallation={profile_dir.as_uri()}",
            "--convert-to",
            "docx:Office Open XML Text",
            "--outdir",
            str(work_dir),
            str(source_path),
        ]

        try:
            result = subprocess.run(
                command,
                capture_output=True,
                text=True,
                timeout=CONVERSION_TIMEOUT_SECONDS,
                check=False,
            )
        except subprocess.TimeoutExpired as exc:
            raise LegacyDocConversionError(
                f"Legacy Word conversion timed out after {CONVERSION_TIMEOUT_SECONDS}s"
            ) from exc

        if result.returncode != 0 or not output_path.is_file():
            detail = result.stderr.strip() or result.stdout.strip() or "no DOCX output produced"
            logger.error(f"Legacy Word conversion failed: {detail[:500]}")
            raise LegacyDocConversionError(f"Legacy Word conversion failed: {detail[:500]}")

        extracted_text, _ = parse_docx(str(output_path))
        logger.info(f"Parsed legacy Word document: {file_path}, size={file_size} bytes")
        return extracted_text, file_size
