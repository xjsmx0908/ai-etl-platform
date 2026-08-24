from types import SimpleNamespace

from app.services import chunker
from app.services.chunker import is_noise_chunk


def test_signature_page_is_noise():
    assert is_noise_chunk("拟 制：         时间：         审 核：")
    assert is_noise_chunk("批准：                     日期：")


def test_table_of_contents_is_noise():
    assert is_noise_chunk("---- 目 次 1 范围 1 1.1 标识 1 2 引用文档 1")
    assert is_noise_chunk("目录\n1 引言 1\n2 概述 2")


def test_long_business_body_that_mentions_catalog_is_not_noise():
    body = (
        "产品目录用于说明可申请的办公用品范围，不是文档的章节索引。"
        + "申请人应填写用途、数量、成本中心和审批依据，并由负责人复核。" * 12
    )
    assert len(body) > 300
    assert not is_noise_chunk(body)


def test_real_body_kept():
    assert not is_noise_chunk(
        "软件环境\n气象台观探测业务数据管理系统运行的硬件设备如下：\n表3‑2 软件运行的硬件设备配置表"
    )


def test_semantic_corpus_content_kept():
    # Golden-set content must never be dropped by the noise filter.
    assert not is_noise_chunk("文档上传支持 PDF、Word 和 Markdown 三种格式。其他格式的文件会被拒绝。")
    assert not is_noise_chunk(
        "知识库中的文档分为三个可见级别：对所有人开放、仅组织内部可见、以及只允许特定人群访问。"
    )


def test_empty_table_fragment_is_noise():
    assert is_noise_chunk("|----+----|\n|  |  |")
    assert is_noise_chunk("   \n   \n")


def test_chunk_text_deduplicates_normalized_content_and_reindexes(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=4000, CHUNK_OVERLAP=0),
    )
    repeated = "办公用品统一通过 OA 系统提交，注明品名、数量与用途。"
    unique = "单件价格超过一百元时，需要单独申请并登记资产。"

    chunks = chunker.chunk_text(
        f"{repeated}\n\n  {repeated}  \n\n{unique}",
        doc_id="policy",
        tenant_id="tenant-a",
    )

    assert [item["content"] for item in chunks] == [repeated, unique]
    assert [item["index"] for item in chunks] == [0, 1]
    assert [item["chunk_id"] for item in chunks] == ["policy_0000", "policy_0001"]
