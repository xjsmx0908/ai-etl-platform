import hashlib
import os
from types import SimpleNamespace

import pytest
from docx import Document

from app.routers import parse as parse_module
from app.services.chunker import estimate_tokens
from app.services.parser import parse_document
from app.services.parsers.docx import parse_docx


def test_parse_docx_preserves_table_content_and_document_order(tmp_path):
    path = tmp_path / "services.docx"
    document = Document()
    document.add_heading("服务清单", level=1)
    document.add_paragraph("表格前的说明")
    table = document.add_table(rows=2, cols=3)
    table.style = "Table Grid"
    table.cell(0, 0).text = "服务名称"
    table.cell(0, 1).text = "配置位置"
    table.cell(0, 2).text = "日志位置"
    table.cell(1, 0).text = "前端服务"
    table.cell(1, 1).text = "/srv/frontend"
    table.cell(1, 2).text = "/var/log/frontend"
    document.add_paragraph("表格后的说明")
    document.save(path)

    text, _ = parse_docx(str(path))

    assert "服务名称\t配置位置\t日志位置" in text
    assert "前端服务\t/srv/frontend\t/var/log/frontend" in text
    assert text.index("表格前的说明") < text.index("服务名称\t配置位置")
    assert text.index("日志位置") < text.index("表格后的说明")


def test_parse_document_converts_legacy_word_doc(tmp_path, monkeypatch):
    converted_docx = tmp_path / "converted.docx"
    document = Document()
    document.add_paragraph("Legacy Word content")
    document.save(converted_docx)

    converter = tmp_path / "libreoffice"
    converter.write_text(
        """#!/bin/sh
set -eu
outdir=""
while [ "$#" -gt 0 ]; do
    if [ "$1" = "--outdir" ]; then
        outdir="$2"
        shift 2
        continue
    fi
    shift
done
cp "$FAKE_CONVERTED_DOCX" "$outdir/source.docx"
""",
        encoding="utf-8",
    )
    converter.chmod(0o755)
    monkeypatch.setenv("PATH", f"{tmp_path}:{os.environ['PATH']}")
    monkeypatch.setenv("FAKE_CONVERTED_DOCX", str(converted_docx))

    legacy_doc = tmp_path / "source.doc"
    legacy_doc.write_bytes(bytes.fromhex("d0cf11e0a1b11ae1") + b"legacy-word-probe")

    text, file_size, parser_name = parse_document(str(legacy_doc))

    assert text == "Legacy Word content"
    assert file_size == os.path.getsize(legacy_doc)
    assert parser_name == "doc"


def test_estimate_tokens_cjk_not_underestimated():
    # The old `len // 4` counted Chinese at 4 chars/token (underestimating by
    # 2-4x). A Chinese sentence should estimate ~1 token per char.
    text = "知识库中的文档分为多个可见级别" * 8  # 15 chars x 8 = 120 chars
    assert len(text) == 120
    tokens = estimate_tokens(text)
    assert tokens >= 80, f"expected CJK counted ~1:1, got {tokens} for 80 chars"
    assert tokens < 160


def test_estimate_tokens_mixed_cjk_latin():
    # Latin still counts ~4 chars/token; CJK ~1:1. Mixing both must reflect it.
    cjk = "文档解析支持三种格式"  # 10 CJK
    latin = "pdf upload support format"  # 25 latin chars
    tokens = estimate_tokens(cjk + latin)
    # 10 CJK + ~6 latin tokens = ~16; allow slack but must be > pure-latin scaling
    assert tokens >= 14
    assert tokens <= 22, f"expected ~16, got {tokens}"


def test_estimate_tokens_english_rule_of_thumb():
    text = "this is a sample english document text"  # 37 chars
    assert estimate_tokens(text) == 37 // 4


class FakeUploadFile:
    def __init__(self, filename, chunks):
        self.filename = filename
        self._chunks = list(chunks)
        self.read_sizes = []

    async def read(self, size=-1):
        self.read_sizes.append(size)
        if not self._chunks:
            return b""
        return self._chunks.pop(0)


@pytest.mark.asyncio
async def test_parse_endpoint_streams_upload_in_chunks(monkeypatch):
    monkeypatch.setattr(parse_module, "get_settings", lambda: SimpleNamespace(MAX_FILE_SIZE_MB=1))
    monkeypatch.setattr(parse_module, "parse_document", lambda path: ("parsed content", 13, "plain"))

    captured = {}

    def fake_chunk_text(text, doc_id, tenant_id, permission, file_hash, metadata):
        captured["text"] = text
        captured["doc_id"] = doc_id
        captured["tenant_id"] = tenant_id
        captured["permission"] = permission
        captured["file_hash"] = file_hash
        captured["metadata"] = metadata
        return [
            {
                "chunk_id": "chunk-1",
                "doc_id": doc_id,
                "tenant_id": tenant_id,
                "content": text,
                "index": 0,
                "token_count": None,
                "permission": permission,
                "file_hash": file_hash,
                "metadata": metadata,
            }
        ]

    monkeypatch.setattr(parse_module, "chunk_text", fake_chunk_text)

    fake_file = FakeUploadFile("report.txt", [b"abcd", b"efgh", b"ij"])
    response = await parse_module.parse_document_endpoint(
        doc_id="doc-1",
        tenant_id="tenant-a",
        file=fake_file,
        permission="internal",
        file_hash=None,
        metadata='{"contract_no":"CN-2026-0001"}',
    )

    assert fake_file.read_sizes == [parse_module.STREAM_CHUNK_SIZE] * 4
    assert captured["file_hash"] == hashlib.sha256(b"abcdefghij").hexdigest()
    assert captured["metadata"] == {"contract_no": "CN-2026-0001"}
    assert response.file_size_bytes == 13
    assert response.total_chunks == 1
    assert response.chunks[0].file_hash == hashlib.sha256(b"abcdefghij").hexdigest()
    assert response.chunks[0].metadata == {"contract_no": "CN-2026-0001"}


@pytest.mark.asyncio
async def test_parse_endpoint_rejects_oversized_upload_without_full_buffer(monkeypatch):
    monkeypatch.setattr(parse_module, "get_settings", lambda: SimpleNamespace(MAX_FILE_SIZE_MB=0))
    monkeypatch.setattr(parse_module, "parse_document", lambda path: ("parsed content", 13, "plain"))
    monkeypatch.setattr(parse_module, "chunk_text", lambda **kwargs: [])

    fake_file = FakeUploadFile("report.txt", [b"abcd", b"efgh"])

    with pytest.raises(parse_module.HTTPException) as exc:
        await parse_module.parse_document_endpoint(
            doc_id="doc-1",
            tenant_id="tenant-a",
            file=fake_file,
            permission="internal",
            file_hash=None,
            metadata=None,
        )

    assert exc.value.status_code == 413
    assert fake_file.read_sizes == [parse_module.STREAM_CHUNK_SIZE]
