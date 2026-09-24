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


def test_paragraph_boundaries_do_not_copy_previous_chunk_into_next(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=4000, CHUNK_OVERLAP=200),
    )
    first = "第一段是完整独立的业务说明，不应被复制到下一段内容中。"
    second = "第二段同样是完整独立的业务说明，应当从自己的正文开始。"

    chunks = chunker.chunk_text(
        f"{first}\n\n{second}",
        doc_id="manual",
        tenant_id="tenant-a",
    )

    assert [item["content"] for item in chunks] == [first, second]


def test_page_markers_start_a_new_semantic_chunk(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=4000, CHUNK_OVERLAP=200),
    )
    first = "第一页包含足够长度的独立正文，用于说明设备安装和安全要求。"
    second = "第二页包含足够长度的独立正文，用于说明厂家和售后联系方式。"

    chunks = chunker.chunk_text(
        f"--- Page 1 ---\n{first}\n--- Page 2 ---\n{second}",
        doc_id="manual",
        tenant_id="tenant-a",
    )

    assert len(chunks) == 2
    assert chunks[1]["content"].startswith("--- Page 2 ---")


def test_employee_roster_table_is_not_noise():
    roster = "--- Sheet: 员工数据 ---\n工号\t姓名\nG00024\t陈伟\nG00001\t江峦"
    assert not is_noise_chunk(roster)
    assert not is_noise_chunk("G00024\t陈伟")


def test_sheet_markers_start_a_new_semantic_chunk(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=4000, CHUNK_OVERLAP=200),
    )
    first = "工号\t姓名\nG00024\t陈伟"
    second = "城市级别\t住宿上限\n一线城市\t600"

    chunks = chunker.chunk_text(
        f"--- Sheet: 员工数据 ---\n{first}\n--- Sheet: 差旅标准 ---\n{second}",
        doc_id="oa",
        tenant_id="tenant-a",
    )

    assert len(chunks) == 2
    assert chunks[0]["content"].startswith("--- Sheet: 员工数据 ---")
    assert "G00024\t陈伟" in chunks[0]["content"]
    assert chunks[1]["content"].startswith("--- Sheet: 差旅标准 ---")
    assert "一线城市\t600" in chunks[1]["content"]


def test_oversized_sheet_repeats_header_on_each_chunk(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=90, CHUNK_OVERLAP=0),
    )
    header = "工号\t姓名\t部门"
    rows = [f"G{i:05d}\t员工{i}\t装备承制" for i in range(1, 12)]
    text = "--- Sheet: 员工数据 ---\n" + header + "\n" + "\n".join(rows)

    chunks = chunker.chunk_text(text, doc_id="oa", tenant_id="tenant-a")

    assert len(chunks) >= 2
    for item in chunks:
        assert item["content"].startswith("--- Sheet: 员工数据 ---")
        assert "工号\t姓名\t部门" in item["content"]
    joined = "\n".join(item["content"] for item in chunks)
    assert "G00001\t员工1\t装备承制" in joined
    assert "G00011\t员工11\t装备承制" in joined


# --- force-split boundary regression (2026-09-24) ---------------------------
# An unstructured body (no blank lines, no headings) longer than MAX_CHUNK_SIZE
# used to be cut at raw character offsets. Three consequences, all reproducible
# against the deployed parser: a chunk could begin and end mid-sentence, a
# terminator-free sentence straddling the cut was whole in no chunk, and the
# overlap tail was re-emitted as its own trailing chunk. See
# docs/optimization-plan.md §1.3 缺陷 21.

PROBE_SENTENCES = (
    "本次巡检覆盖了全部接入通道与导出链路。",
    "每个通道都记录了请求标识与处理耗时。",
    "审计条目按租户隔离并且不可跨租户检索。",
    "文档在入库前会先做一次哈希校验。",
    "解析失败的任务会进入重试队列并保留原始输入。",
    "权限判定发生在检索之前而不是之后。",
    "引用必须指向当前已发布的代际。",
    "被取代的版本仍保留自己的审批证据。",
    "索引水位与磁盘余量各自独立告警。",
    "备份脚本会在写入前校验源对象的完整性。",
    "所有对外端口默认绑定在回环地址上。",
    "登录限流发生在口令校验之前。",
    "任务状态由单一存储负责而不是各处各写一份。",
    "检索结果会先做一次内容去重再排序。",
    "预审结论只描述发布资格不描述合规程度。",
)

# Carries no internal terminator, so any character-level cut lands inside it.
PROBE_LONG_SENTENCE = (
    "当同一份文档在入库过程中被中断并重新投递时系统必须依据检查点恢复已完成的解析与向量化结果"
    "而不能把整份文档从头重新处理一遍否则既浪费算力也会因为两次运行之间的切块边界差异而在检索"
    "索引里留下两份内容相近但身份不同的块进而让同一个问句在两次检索之间返回不同的证据集合"
)


def _unstructured_probe_text() -> str:
    filler = ""
    index = 0
    while len(filler) < 500:
        filler += PROBE_SENTENCES[index % len(PROBE_SENTENCES)]
        index += 1
    return filler[:500] + PROBE_LONG_SENTENCE + "后续小节继续描述其余通道的观测口径与保留期限。" * 2


def _assert_chunks_start_and_end_on_boundaries(text, chunks, min_size):
    contents = [item["content"] for item in chunks]
    for content in contents:
        start = text.index(content)
        assert start == 0 or text[start - 1] in "。！？；\n", content[:40]
        assert content.endswith("。"), content[-40:]
    # No chunk may be a re-emitted tail of another chunk, and no chunk may fall
    # below the configured minimum size.
    for i, outer in enumerate(contents):
        for j, inner in enumerate(contents):
            if i != j:
                assert not (len(inner) < len(outer) and inner in outer), inner[:40]
    assert not [content for content in contents if len(content) < min_size]


def test_force_split_keeps_sentences_whole_and_drops_the_duplicate_tail(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=128, MAX_CHUNK_SIZE=600, CHUNK_OVERLAP=50),
    )
    text = _unstructured_probe_text()
    assert "\n" not in text and len(text) > 600

    chunks = chunker.chunk_text(text, doc_id="probe", tenant_id="tenant-a")

    _assert_chunks_start_and_end_on_boundaries(text, chunks, 128)
    assert any(PROBE_LONG_SENTENCE in item["content"] for item in chunks)


def test_force_split_ends_every_chunk_on_a_sentence_boundary(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=120, CHUNK_OVERLAP=40),
    )
    sentences = [f"第{index:02d}句说明了一个独立的业务规则并给出处理口径。" for index in range(1, 21)]
    text = "".join(sentences)

    chunks = chunker.chunk_text(text, doc_id="policy", tenant_id="tenant-a")

    assert len(chunks) >= 2
    for sentence in sentences:
        assert any(sentence in item["content"] for item in chunks), sentence
    _assert_chunks_start_and_end_on_boundaries(text, chunks, 20)


def test_force_split_overlap_restarts_on_a_sentence_boundary(monkeypatch):
    monkeypatch.setattr(
        chunker,
        "get_settings",
        lambda: SimpleNamespace(MIN_CHUNK_SIZE=20, MAX_CHUNK_SIZE=120, CHUNK_OVERLAP=40),
    )
    lines = [f"第{index:02d}行说明了一个独立的业务规则并给出处理口径。" for index in range(1, 11)]
    text = "\n".join(lines)

    chunks = chunker.chunk_text(text, doc_id="policy", tenant_id="tenant-a")

    assert len(chunks) >= 2
    _assert_chunks_start_and_end_on_boundaries(text, chunks, 20)
    for item in chunks:
        assert item["content"].split("\n")[0].endswith("。"), item["content"][:40]
