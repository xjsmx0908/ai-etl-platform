"""契约：跨层索引一致性对账的判定规则。

`scripts/check-index-consistency.py` 是 2026-09-24 加上的诊断脚本。加它的理由是一个
实测出来的盲区：同一份文档在四个层里各说各话，而仓库里**没有任何东西**比较它们。

    Postgres        documents / index_manifests     产品认为存在什么
    Qdrant          documents-v2                    向量检索能返回什么
    Elasticsearch   documents_text_v2               关键词检索能返回什么
    Query API       GET /v1/documents/{id}/chunks   页面能显示什么

已发布文档可以在一层可见、另一层不可见，而没有测试、告警或对账任务会注意到。现有
的 manifest 对账只覆盖**有 manifest 行**的文档，所以从来没有拿到过 manifest 的文档
在构造上就在它的射程之外。

这里只测判定规则（纯函数 classify_document）与分页（scroll_pages），不连服务 —— 规则写错时症状是
「对账永远报绿」，光靠跑一次正例发现不了；而分页写错时症状是对账**变安静**（存储侧被读短），
同样发现不了。

四条判定（classify_document，比较块的**集合**）：
  keyword_unsearchable   ES 里没有块而 Qdrant 有 → 全文召回静默丢掉这份文档
  count_mismatch         ES 与 Qdrant 的块数不一致
  citation_unverifiable  端点隐藏了 Qdrant 仍在提供的块 → 引用里的 chunk id
                         在文档页面上找不到
  registry_count_stale   登记表自己的块数（chunks_done / chunks_total）与存储不一致
                         → 文档清单与详情页渲染的就是这两个数，于是「有块的文档」
                           在页面上显示成「—」，而其它层看起来都对

三条判定（classify_payload_fields，比较存储点上的**字段**）：
  space_key_unset        某个 chunk id 的**所有**存储拷贝都不带文档的知识空间
                         → 该块在该空间内进不了向量分支，只剩关键词可召回
                         （只要有一份拷贝带对就不报 —— 存储保留每个代际）
  permission_drift       点的 permission 与登记表不一致 → 检索按旧值过滤（越权或黑洞）
  duplicate_chunk_points 同一个 chunk_id 在**同一个代际**里有多个存储点 → 某次写入没有替换
                         （点 id 由 (代际, chunk id) 派生，Qdrant 对已存在的 id 是覆盖；
                          跨代际的多份是设计，不报）
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "check-index-consistency.py"


def load_module():
    spec = importlib.util.spec_from_file_location("check_index_consistency", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


MODULE = load_module()


def kinds(findings):
    return [finding["kind"] for finding in findings]


class ClassifyDocumentTest(unittest.TestCase):
    def test_consistent_document_reports_nothing(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000"], 1, ["d1_0000"]
        )
        self.assertEqual(findings, [])

    def test_document_absent_from_the_text_index(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 0, ["d1_0000", "d1_0001"]
        )
        self.assertEqual(kinds(findings), ["keyword_unsearchable"])
        self.assertEqual(findings[0]["qdrant_chunks"], 2)
        self.assertEqual(findings[0]["es_chunks"], 0)

    def test_chunk_count_disagreement(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 1, ["d1_0000", "d1_0001"]
        )
        self.assertEqual(kinds(findings), ["count_mismatch"])

    def test_endpoint_hides_a_chunk_search_still_serves(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 2, ["d1_0000"]
        )
        self.assertEqual(kinds(findings), ["citation_unverifiable"])
        self.assertEqual(findings[0]["hidden_chunk_ids"], ["d1_0001"])
        self.assertEqual(findings[0]["endpoint_chunks"], 1)

    def test_missing_text_index_and_hidden_chunk_are_both_reported(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000", "d1_0001"], 0, ["d1_0000"]
        )
        self.assertEqual(sorted(kinds(findings)), ["citation_unverifiable", "keyword_unsearchable"])

    def test_document_without_stored_chunks_reports_nothing(self):
        # 没有块的文档不是不一致 —— 它只是没有内容，对账不该在这里造出噪声。
        findings = MODULE.classify_document("d1", {"file_name": "a.txt"}, [], 0, [])
        self.assertEqual(findings, [])

    def test_hidden_ids_are_sorted_and_deduplicated(self):
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0001", "d1_0000", "d1_0001"], 3, []
        )
        self.assertEqual(findings[0]["hidden_chunk_ids"], ["d1_0000", "d1_0001"])

    def test_stale_registry_counters_are_reported(self):
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 0, "chunks_total": 0},
            ["d1_0000"],
            1,
            ["d1_0000"],
        )
        self.assertEqual(kinds(findings), ["registry_count_stale"])
        self.assertEqual(findings[0]["stored_chunks"], 1)
        self.assertEqual(findings[0]["registry_chunks_total"], 0)

    def test_registry_counters_that_match_report_nothing(self):
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 1, "chunks_total": 1},
            ["d1_0000"],
            1,
            ["d1_0000"],
        )
        self.assertEqual(findings, [])

    def test_registry_done_counter_alone_is_enough_to_report(self):
        # The total can be right while the progress counter is wrong: the page renders
        # both, so comparing only one of them leaves half the column unchecked.
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 2, "chunks_total": 1},
            ["d1_0000"],
            1,
            ["d1_0000"],
        )
        self.assertEqual(kinds(findings), ["registry_count_stale"])
        self.assertEqual(findings[0]["registry_chunks_done"], 2)

    def test_registry_counters_are_compared_even_with_nothing_stored(self):
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 5, "chunks_total": 5},
            [],
            0,
            [],
        )
        self.assertEqual(kinds(findings), ["registry_count_stale"])
        self.assertEqual(findings[0]["stored_chunks"], 0)

    def test_zero_total_detail_says_the_page_reads_a_dash(self):
        # Both document pages render `chunks_total ? done/total : "-"`, so a zero
        # total is the one case where the reader sees no number at all.
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 0, "chunks_total": 0},
            ["d1_0000"],
            1,
            ["d1_0000"],
        )
        self.assertEqual(kinds(findings), ["registry_count_stale"])
        self.assertIn("dash", findings[0]["detail"])

    def test_nonzero_mismatch_detail_does_not_claim_a_dash(self):
        # A non-zero total renders a number, not a dash. Saying otherwise would
        # mislabel the finding for every re-ingested document.
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 2, "chunks_total": 2},
            ["d1_0000", "d1_0001", "d1_0002"],
            3,
            ["d1_0000", "d1_0001", "d1_0002"],
        )
        self.assertEqual(kinds(findings), ["registry_count_stale"])
        self.assertNotIn("dash", findings[0]["detail"])

    def test_stored_points_are_reported_next_to_distinct_chunks(self):
        # A re-ingested document keeps every generation in the store. Reporting the
        # point count next to the distinct chunk count is what lets a reader tell
        # "the counters are stale" from "the store holds superseded copies".
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "a.txt", "chunks_done": 0, "chunks_total": 0},
            ["d1_0000", "d1_0000", "d1_0001"],
            3,
            ["d1_0000", "d1_0001"],
        )
        self.assertEqual(kinds(findings), ["registry_count_stale"])
        self.assertEqual(findings[0]["stored_chunks"], 2)
        self.assertEqual(findings[0]["stored_points"], 3)

    def test_registry_row_without_counters_is_not_a_disagreement(self):
        # A caller that does not pass the registry row must not be told its counters
        # are stale -- the rule has no opinion without both fields.
        findings = MODULE.classify_document(
            "d1", {"file_name": "a.txt"}, ["d1_0000"], 1, ["d1_0000"]
        )
        self.assertEqual(findings, [])

    def test_file_name_and_publication_status_are_carried_through(self):
        findings = MODULE.classify_document(
            "d1",
            {"file_name": "kafka-recovery.txt", "publication_status": "published"},
            ["d1_0000"],
            0,
            ["d1_0000"],
        )
        self.assertEqual(findings[0]["file_name"], "kafka-recovery.txt")
        self.assertEqual(findings[0]["publication_status"], "published")


def points(*entries):
    """Build stored-point rows the way qdrant_points() shapes them.

    Each entry is `(chunk_id, permission, knowledge_base_id)`, or
    `(chunk_id, permission, knowledge_base_id, document_version_id, generation_id)`
    when the rule needs to tell generations apart.
    """
    rows = []
    for entry in entries:
        chunk_id, permission, space = entry[:3]
        rows.append(
            {
                "chunk_id": chunk_id,
                "permission": permission,
                "knowledge_base_id": space,
                "document_version_id": entry[3] if len(entry) > 3 else "",
                "generation_id": entry[4] if len(entry) > 4 else "",
            }
        )
    return rows


class ClassifyPayloadFieldsTest(unittest.TestCase):
    def test_consistent_points_report_nothing(self):
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(("d1_0000", "internal", "sp"), ("d1_0001", "internal", "sp")),
        )
        self.assertEqual(findings, [])

    def test_points_without_the_space_key_are_reported(self):
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(("d1_0000", "internal", "sp"), ("d1_0001", "internal", "")),
        )
        self.assertEqual(kinds(findings), ["space_key_unset"])
        self.assertEqual(findings[0]["unset_chunk_ids"], ["d1_0001"])
        self.assertEqual(findings[0]["points_total"], 2)
        self.assertEqual(findings[0]["points_unset"], 1)

    def test_all_points_without_the_space_key_are_reported(self):
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(("d1_0000", "internal", ""), ("d1_0001", "internal", "")),
        )
        self.assertEqual(findings[0]["points_unset"], 2)
        self.assertEqual(findings[0]["unset_chunk_ids"], ["d1_0000", "d1_0001"])

    def test_a_chunk_id_whose_other_copy_carries_the_key_is_not_reported(self):
        # 存储保留每个代际，被取代的旧拷贝可能完全没有 metadata。只要**有一份**拷贝带对了
        # 空间键，这个 chunk 在该空间内就是召得回的 —— 按点判定会把它误报成「只剩关键词」。
        # 线上 `HR-2024-001_0000` 正是这个形状。
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(
                ("d1_0000", "internal", "sp", "ver-2", "gen-2"),
                ("d1_0000", "internal", "", "", ""),
                ("d1_0001", "internal", "", "ver-2", "gen-2"),
            ),
        )
        self.assertEqual(kinds(findings), ["space_key_unset"])
        self.assertEqual(findings[0]["unset_chunk_ids"], ["d1_0001"])

    def test_document_without_a_space_is_not_compared_against_one(self):
        # 文档自己没有空间时没有可比对象 —— 否则每个带空间值的点都会被误报。
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "", "permission": "internal"},
            points(("d1_0000", "internal", "some-space"), ("d1_0001", "internal", "")),
        )
        self.assertEqual(findings, [])

    def test_permission_drift_is_reported_against_the_registry(self):
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(("d1_0000", "internal", "sp"), ("d1_0001", "public", "sp")),
        )
        self.assertEqual(kinds(findings), ["permission_drift"])
        self.assertEqual(findings[0]["registry_permission"], "internal")
        self.assertEqual(findings[0]["drifted_chunk_ids"], ["d1_0001"])

    def test_two_points_for_one_chunk_id_in_one_generation_are_reported(self):
        # 同一个代际里出现两份同 chunk id —— 点 id 由 (代际, chunk id) 派生，Qdrant 对已存在的
        # id 是覆盖，所以这种形状意味着某次写入**没有**替换，是真正的不幂等。
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(
                ("d1_0000", "internal", "sp", "ver-1", "gen-1"),
                ("d1_0000", "internal", "sp", "ver-1", "gen-1"),
                ("d1_0001", "internal", "sp", "ver-1", "gen-1"),
            ),
        )
        self.assertEqual(kinds(findings), ["duplicate_chunk_points"])
        duplicate = findings[0]
        self.assertEqual(duplicate["duplicated_chunk_ids"], ["d1_0000"])
        self.assertEqual(duplicate["points_total"], 3)
        self.assertEqual(duplicate["unique_chunk_ids"], 2)

    def test_two_points_for_one_chunk_id_across_generations_are_not_reported(self):
        # 跨代际的同一 chunk id 是**设计**：存储保留每个代际，这样重复入库不会在新代际
        # 发布之前丢掉已发布的那一份。线上 116 个这样的 chunk id，0 个同身份 ——
        # 把它们报成「重复入库是追加」就是给设计贴错标签。
        #
        # 身份是 (版本, 代际) 两个维度，所以三对点分别只差代际、只差版本、两者都差 ——
        # 少写一个维度就会把其中一对误报成「不幂等」。
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(
                ("d1_0000", "internal", "sp", "ver-1", "gen-1"),
                ("d1_0000", "internal", "sp", "ver-2", "gen-2"),
                ("d1_0001", "internal", "sp", "ver-1", "gen-1"),
                ("d1_0001", "internal", "sp", "ver-1", "gen-2"),
                ("d1_0002", "internal", "sp", "ver-1", "gen-1"),
                ("d1_0002", "internal", "sp", "ver-2", "gen-1"),
            ),
        )
        self.assertEqual(findings, [])

    def test_document_without_stored_points_reports_nothing(self):
        findings = MODULE.classify_payload_fields(
            "d1", {"file_name": "a.txt", "knowledge_space_id": "sp"}, []
        )
        self.assertEqual(findings, [])

    def test_blank_permission_on_a_point_is_drift(self):
        # 空白权限值不是「一致」，否则漂移会被当成正常。
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(("d1_0000", "  ", "sp")),
        )
        self.assertEqual(kinds(findings), ["permission_drift"])

    def test_whitespace_padded_permission_is_not_drift(self):
        # 值两端带空白不算漂移 —— 否则对账会报出一堆假的「权限不一致」。
        findings = MODULE.classify_payload_fields(
            "d1",
            {"file_name": "a.txt", "knowledge_space_id": "sp", "permission": "internal"},
            points(("d1_0000", " internal ", "sp"), ("d1_0001", "internal", "sp")),
        )
        self.assertEqual(findings, [])


class ScrollPagesTest(unittest.TestCase):
    """分页：Qdrant 的 scroll 每次最多返回 `limit` 个点，另给一个 next_page_offset。

    忽略那个 offset 会把文档**读短**，而对账的每一条判定都是拿存储侧的 id 去和另一层比 ——
    读短了存储侧就变小，于是检查**变安静**而不是变吵。这是检查最不该错的方向：它会漏掉问题。
    线上有 1 份文档有 2170 个存储点，所以这不是假想。
    """

    def test_every_page_is_collected_in_order(self):
        pages = [
            (["a", "b"], "offset-1"),
            (["c"], "offset-2"),
            (["d"], None),
        ]
        calls = []

        def fetch(offset):
            calls.append(offset)
            return pages[len(calls) - 1]

        self.assertEqual(MODULE.scroll_pages(fetch), ["a", "b", "c", "d"])
        self.assertEqual(calls, [None, "offset-1", "offset-2"])

    def test_a_single_page_makes_one_call(self):
        calls = []

        def fetch(offset):
            calls.append(offset)
            return (["only"], None)

        self.assertEqual(MODULE.scroll_pages(fetch), ["only"])
        self.assertEqual(calls, [None])

    def test_the_offset_from_one_page_is_passed_to_the_next(self):
        seen = []

        def fetch(offset):
            seen.append(offset)
            if offset is None:
                return (["first"], "cursor")
            return (["second"], None)

        MODULE.scroll_pages(fetch)
        self.assertEqual(seen, [None, "cursor"])

    def test_a_server_that_never_terminates_raises_instead_of_hanging(self):
        # 只给一页就停下来的实现在这里也会「通过」—— 所以必须钉住「不终止要报错」。
        def fetch(offset):
            return (["more"], "again")

        with self.assertRaises(RuntimeError):
            MODULE.scroll_pages(fetch)


if __name__ == "__main__":
    unittest.main()
