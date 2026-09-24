"""契约：从存储回填 Elasticsearch 全文行的规则。

`scripts/backfill-elasticsearch-from-store.py` 是 2026-09-24 加上的补偿脚本。加它的
理由是一条实测出来的链路：

    pipeline.go:983     全文写入失败被明确忽略（"ES errors never fail main pipeline"）
    sink.go:104         失败入重试队列
    sink.go:172         重试 12 次耗尽 → 死信
    queue.go:205        死信**只有写**，没有任何读取/告警/计数

于是 2026-09-02 ES 磁盘越过 flood-stage 水位、索引被置 read-only-allow-delete 时，
两份已发布文档的 32 个块全部进了死信，然后静默躺了三个星期。块一直没离开 Qdrant，
所以不需要重解析、不需要重新嵌入 —— 缺的行可以从存储重建。

这里只测规则，不连服务。这些规则写错时的症状都是「看起来修好了」：写错 `_id` 形状
会凭空造出一个存储里不存在的代际身份；漏掉 `permission` 归一化会写出一行检索过
滤不到的记录；忽略「ES 已有行」会把 `count_mismatch` 悄悄填平，把真问题盖住。

四条纯规则：
  es_document_id        点的身份 → 写入侧会用的 `_id`（有代际则 `<gen>__<chunk>`）
  normalize_permission  与 internal/es/indexer.go:597 的 normalizePermission 一致
  build_es_document     存储点 + 登记表行 → ES _source（缺的键**不写空串**）
  plan_document         一个文档该写几行，或为什么跳过
"""

import importlib.util
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SCRIPT = ROOT / "scripts" / "backfill-elasticsearch-from-store.py"


def load_module():
    spec = importlib.util.spec_from_file_location("backfill_es_from_store", SCRIPT)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


backfill = load_module()


def document(**overrides):
    base = {
        "doc_id": "d1",
        "tenant_id": "default",
        "file_name": "f.txt",
        "file_hash": "abc123",
        "permission": "internal",
        "created_at": "2026-09-02T02:34:01.601845Z",
    }
    base.update(overrides)
    return base


def point(chunk_id, *, generation="", index=0, content="body", permission="internal",
          metadata=None, doc_id="d1", tenant_id="default", version=""):
    payload = {
        "chunk_id": chunk_id,
        "doc_id": doc_id,
        "tenant_id": tenant_id,
        "content": content,
        "index": index,
        "permission": permission,
    }
    if generation:
        payload["generation_id"] = generation
        payload["document_version_id"] = version or f"ver-{generation}"
        payload["content_hash"] = "sha256:deadbeef"
    if metadata is not None:
        payload["metadata"] = metadata
    return {"id": 7, "chunk_id": chunk_id, "generation_id": generation, "payload": payload}


class EsDocumentIdTest(unittest.TestCase):
    def test_a_point_without_a_generation_uses_the_bare_chunk_id(self):
        self.assertEqual(backfill.es_document_id(point("d1_0000")), "d1_0000")

    def test_a_point_with_a_generation_is_prefixed_with_it(self):
        self.assertEqual(
            backfill.es_document_id(point("d1_0000", generation="gen-abc")),
            "gen-abc__d1_0000",
        )

    def test_an_empty_generation_string_is_treated_as_absent(self):
        # The scroll flattening turns a missing payload key into "". If that were
        # taken as an identity, every legacy point would be written as "__<chunk>".
        self.assertEqual(backfill.es_document_id(point("d1_0000", generation="")), "d1_0000")


class NormalizePermissionTest(unittest.TestCase):
    def test_public_and_confidential_are_kept(self):
        self.assertEqual(backfill.normalize_permission("public"), "public")
        self.assertEqual(backfill.normalize_permission("confidential"), "confidential")

    def test_case_and_whitespace_do_not_change_the_result(self):
        self.assertEqual(backfill.normalize_permission("  PUBLIC "), "public")

    def test_anything_else_becomes_internal(self):
        # Retrieval filters on this value; a row carrying "SECRET" or "" would be
        # unreachable for every allowed-permission set.
        for raw in ("internal", "SECRET", "", None, "top-secret"):
            self.assertEqual(backfill.normalize_permission(raw), "internal", raw)


class BuildEsDocumentTest(unittest.TestCase):
    def test_a_legacy_point_does_not_gain_generation_fields(self):
        row = backfill.build_es_document(point("d1_0000"), document())
        for key in ("document_version_id", "generation_id", "content_hash"):
            self.assertNotIn(key, row)

    def test_a_generation_point_carries_its_identity_and_hash(self):
        row = backfill.build_es_document(point("d1_0000", generation="gen-abc"), document())
        self.assertEqual(row["generation_id"], "gen-abc")
        self.assertEqual(row["document_version_id"], "ver-gen-abc")
        self.assertEqual(row["content_hash"], "sha256:deadbeef")

    def test_the_registry_supplies_the_fields_the_store_does_not_carry(self):
        row = backfill.build_es_document(point("d1_0000"), document(file_name="手册.md", created_at="2026-09-02T02:34:01.601845Z"))
        self.assertEqual(row["file_name"], "手册.md")
        self.assertEqual(row["file_hash"], "abc123")
        self.assertEqual(row["created_at"], "2026-09-02T02:34:01.601845Z")

    def test_the_stored_index_becomes_chunk_index(self):
        # The store calls it `index`; Elasticsearch calls it `chunk_index`.
        self.assertEqual(backfill.build_es_document(point("d1_0005", index=5), document())["chunk_index"], 5)

    def test_metadata_is_carried_through(self):
        row = backfill.build_es_document(point("d1_0000", metadata={"knowledge_base_id": "user-uploads"}), document())
        self.assertEqual(row["metadata"], {"knowledge_base_id": "user-uploads"})

    def test_a_point_without_metadata_does_not_gain_an_empty_one(self):
        self.assertNotIn("metadata", backfill.build_es_document(point("d1_0000"), document()))


class PlanDocumentTest(unittest.TestCase):
    def test_a_document_the_store_does_not_hold_is_skipped(self):
        rows, skip = backfill.plan_document("d1", [], document(), 0)
        self.assertEqual(rows, [])
        self.assertIn("no points", skip)

    def test_a_document_elasticsearch_already_holds_is_skipped(self):
        # Only a document that is *entirely* absent is in scope. Topping up a
        # partly indexed document would erase count_mismatch, which is a
        # different finding with a different cause.
        rows, skip = backfill.plan_document("d1", [point("d1_0000")], document(), 1)
        self.assertEqual(rows, [])
        self.assertIn("already holds 1", skip)

    def test_a_document_without_a_registry_row_is_skipped(self):
        rows, skip = backfill.plan_document("d1", [point("d1_0000")], None, 0)
        self.assertEqual(rows, [])
        self.assertIn("registry row", skip)

    def test_every_stored_point_becomes_one_row(self):
        points = [point("d1_0000"), point("d1_0001"), point("d1_0002")]
        rows, skip = backfill.plan_document("d1", points, document(), 0)
        self.assertIsNone(skip)
        self.assertEqual([es_id for es_id, _ in rows], ["d1_0000", "d1_0001", "d1_0002"])

    def test_two_points_for_one_id_in_one_generation_produce_one_row(self):
        # Same identity twice means one PUT would overwrite the other. That is
        # duplicate_chunk_points, and writing whichever came last would hide it.
        points = [point("d1_0000", content="first"), point("d1_0000", content="second")]
        rows, skip = backfill.plan_document("d1", points, document(), 0)
        self.assertIsNone(skip)
        self.assertEqual(len(rows), 1)
        self.assertEqual(rows[0][0], "d1_0000")

    def test_two_points_across_generations_produce_two_rows(self):
        # Different generations are different rows, because the writers derive
        # different ids for them. Collapsing them would delete a published copy.
        points = [point("d1_0000", generation="gen-old"), point("d1_0000", generation="gen-new")]
        rows, skip = backfill.plan_document("d1", points, document(), 0)
        self.assertIsNone(skip)
        self.assertEqual(sorted(es_id for es_id, _ in rows), ["gen-new__d1_0000", "gen-old__d1_0000"])


class ParseDocumentsTsvTest(unittest.TestCase):
    def test_columns_are_read_in_order(self):
        parsed = backfill.parse_documents_tsv("d1\tdefault\tf.txt\thash1\tinternal\t2026-09-02T02:34:01.601845Z\n")
        self.assertEqual(
            parsed["d1"],
            {
                "doc_id": "d1",
                "tenant_id": "default",
                "file_name": "f.txt",
                "file_hash": "hash1",
                "permission": "internal",
                "created_at": "2026-09-02T02:34:01.601845Z",
            },
        )

    def test_blank_lines_are_ignored(self):
        parsed = backfill.parse_documents_tsv("\nd1\tdefault\tf.txt\th\tinternal\t2026-09-02T02:34:01.601845Z\n\n")
        self.assertEqual(list(parsed), ["d1"])

    def test_a_short_row_is_an_error(self):
        # Silently accepting it would write a row with an empty created_at, and
        # Elasticsearch rejects an unparseable date rather than skipping it.
        with self.assertRaises(ValueError):
            backfill.parse_documents_tsv("d1\tdefault\tf.txt\n")


class ScrollPagesTest(unittest.TestCase):
    def test_every_page_is_collected_in_order(self):
        pages = {None: (["a", "b"], 2), 2: (["c"], None)}
        self.assertEqual(backfill.scroll_pages(lambda offset: pages[offset]), ["a", "b", "c"])

    def test_a_server_that_never_terminates_raises_instead_of_hanging(self):
        def fetch(offset):
            return ["x"], (offset or 0) + 1

        with self.assertRaises(RuntimeError):
            backfill.scroll_pages(fetch)


if __name__ == "__main__":
    unittest.main()
