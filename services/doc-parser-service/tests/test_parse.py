import hashlib
from types import SimpleNamespace

import pytest

from app.routers import parse as parse_module


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

    def fake_chunk_text(text, doc_id, tenant_id, permission, file_hash):
        captured["text"] = text
        captured["doc_id"] = doc_id
        captured["tenant_id"] = tenant_id
        captured["permission"] = permission
        captured["file_hash"] = file_hash
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
    )

    assert fake_file.read_sizes == [parse_module.STREAM_CHUNK_SIZE] * 4
    assert captured["file_hash"] == hashlib.sha256(b"abcdefghij").hexdigest()
    assert response.file_size_bytes == 13
    assert response.total_chunks == 1
    assert response.chunks[0].file_hash == hashlib.sha256(b"abcdefghij").hexdigest()


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
        )

    assert exc.value.status_code == 413
    assert fake_file.read_sizes == [parse_module.STREAM_CHUNK_SIZE]
