"use client";

import Link from "next/link";
import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { Button } from "@/components/ui/Button";
import { Input } from "@/components/ui/Input";
import { Card } from "@/components/ui/Card";
import { Badge, statusTone } from "@/components/ui/Badge";
import { Spinner } from "@/components/ui/Spinner";
import { EmptyState } from "@/components/ui/EmptyState";
import { Files, Search, ShieldCheck, Sparkles } from "lucide-react";
import { useAuth } from "@/lib/auth";
import { formatUploader, getFileTypeMeta } from "@/lib/docDisplay";
import type { Document, DocumentSearchResult } from "@/lib/types";

const STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  pending: "等待中",
  processing: "处理中",
  completed: "已完成",
  failed: "失败",
};

const PERMISSION_LABELS: Record<string, string> = {
  public: "公开",
  internal: "内部",
  confidential: "机密",
};

function formatSize(bytes?: number): string {
  if (!bytes && bytes !== 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", { hour12: false });
}

export default function DocumentsPage() {
  const { isAdmin, role } = useAuth();
  const [items, setItems] = useState<Document[]>([]);
  const [total, setTotal] = useState(0);
  const [qInput, setQInput] = useState("");
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [permission, setPermission] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<string | null>(null);
  const [searchInput, setSearchInput] = useState("");
  const [searchResults, setSearchResults] = useState<DocumentSearchResult[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState("");

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await apiClient.listDocuments({
        limit: 100,
        ...(q ? { q } : {}),
        ...(status ? { status } : {}),
        ...(permission ? { permission } : {}),
      });
      setItems(data.items);
      setTotal(data.total);
    } catch (e) {
      setError((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }, [q, status, permission]);

  useEffect(() => {
    void load();
  }, [load]);

  const onSearch = (e: React.FormEvent) => {
    e.preventDefault();
    setQ(qInput.trim());
  };

  const onDelete = async (doc: Document) => {
    if (!window.confirm(`确认删除文档「${doc.file_name}」（${doc.doc_id}）？该操作不可恢复。`)) return;
    setDeleting(doc.doc_id);
    setError("");
    try {
      await apiClient.deleteDocument(doc.doc_id);
      void load();
    } catch (e) {
      setError((e as Error).message || "删除失败");
    } finally {
      setDeleting(null);
    }
  };

  const onContentSearch = async () => {
    const q = searchInput.trim();
    if (!q) return;
    setSearching(true);
    setSearchError("");
    try {
      const data = await apiClient.searchDocuments(q);
      setSearchResults(data.items);
    } catch (e) {
      setSearchError((e as Error).message || "内容搜索失败");
      setSearchResults([]);
    } finally {
      setSearching(false);
    }
  };

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-xl border border-blue-100 bg-blue-50/60 px-4 py-3 text-xs text-slate-600">
        <span className="flex items-center gap-1.5 font-medium text-slate-700">
          <Sparkles className="h-3.5 w-3.5 text-blue-600" />
          多格式解析：TXT / Markdown / DOCX / PDF / 图片 OCR / 扫描件 OCR
        </span>
        <span className="flex items-center gap-1.5">
          <ShieldCheck className="h-3.5 w-3.5 text-blue-600" />
          权限隔离：公开 / 内部 / 机密，按角色过滤
        </span>
        <span className="flex items-center gap-1.5">
          <Sparkles className="h-3.5 w-3.5 text-blue-600" />
          语义切块 + 向量 / 全文混合检索
        </span>
      </div>

      <div className="flex flex-wrap items-center gap-x-6 gap-y-1 rounded-xl border border-slate-200 bg-white px-4 py-2.5 text-xs text-slate-500">
        <ShieldCheck className="h-3.5 w-3.5 text-slate-400" />
        <span>
          当前角色：<span className="font-medium text-slate-700">{role === "admin" ? "管理员" : role === "user" ? "普通用户" : role === "readonly" ? "只读用户" : "—"}</span>
        </span>
        <span className="text-slate-300">|</span>
        <span>
          检索边界：
          <span className="font-medium text-slate-700">
            {role === "admin" ? "公开 + 内部 + 机密（全部文档）" : role === "user" ? "公开 + 内部" : role === "readonly" ? "公开" : "—"}
          </span>
        </span>
        {role !== "admin" && (
          <span className="text-slate-400">机密文档对当前角色不可见（检索源头过滤）</span>
        )}
      </div>

      <div className="flex flex-wrap items-end gap-3">
        <div className="min-w-[220px] flex-1">
          <label className="mb-1 block text-xs font-medium text-slate-500">搜索</label>
          <form onSubmit={onSearch} className="flex gap-2">
            <input
              value={qInput}
              onChange={(e) => setQInput(e.target.value)}
              placeholder="按文件名 / doc_id 检索"
              className="w-full rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500"
            />
            <button
              type="submit"
              className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white hover:bg-blue-700"
            >
              搜索
            </button>
          </form>
        </div>
        <div>
          <label className="mb-1 block text-xs font-medium text-slate-500">状态</label>
          <select
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm"
          >
            <option value="">全部</option>
            {Object.entries(STATUS_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
        <div>
          <label className="mb-1 block text-xs font-medium text-slate-500">权限</label>
          <select
            value={permission}
            onChange={(e) => setPermission(e.target.value)}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm"
          >
            <option value="">全部</option>
            {Object.entries(PERMISSION_LABELS).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
        </div>
      </div>

      {error && <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{error}</div>}

      <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
        <label className="mb-1 block text-xs font-medium text-slate-500">内容全文检索（对文档正文）</label>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void onContentSearch();
          }}
          className="flex gap-2"
        >
          <Input value={searchInput} onChange={(e) => setSearchInput(e.target.value)} placeholder="输入正文关键词，如「pipeline」" />
          <Button type="submit" icon={Search}>
            内容搜索
          </Button>
        </form>
        {searchError && <p className="mt-2 text-xs text-red-600">{searchError}</p>}
        {searching && <p className="mt-2 text-xs text-slate-400">搜索中…</p>}
        {searchResults !== null && !searching && (
          <div className="mt-3 space-y-2">
            {searchResults.length === 0 ? (
              <p className="text-xs text-slate-400">未找到匹配文档</p>
            ) : (
              searchResults.map((r) => (
                <div key={r.doc_id} className="rounded-lg border border-slate-100 p-3">
                  <div className="flex flex-wrap items-center gap-2">
                    <Link href={`/documents/${r.doc_id}`} className="text-sm font-medium text-blue-600 hover:underline">
                      {r.file_name}
                    </Link>
                    <Badge>{PERMISSION_LABELS[r.permission] || r.permission}</Badge>
                    <Badge tone={statusTone(r.status)}>{STATUS_LABELS[r.status] || r.status}</Badge>
                    <span className="text-xs text-slate-400">
                      命中 {r.hit_count} 段 · 相关度 {r.best_score.toFixed(4)}
                    </span>
                  </div>
                  <p className="mt-1 line-clamp-2 text-xs text-slate-600">{r.snippet}</p>
                </div>
              ))
            )}
          </div>
        )}
      </div>

      <Card header="文档列表" meta={`共 ${total} 篇`} padding="none">
        {loading ? (
          <Spinner />
        ) : items.length === 0 ? (
          <EmptyState icon={Files} title="暂无文档" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-4 py-2 font-medium">文档 ID</th>
                  <th className="px-4 py-2 font-medium">文件名</th>
                  <th className="px-4 py-2 font-medium">类型</th>
                  <th className="px-4 py-2 font-medium">权限</th>
				  <th className="px-4 py-2 font-medium">知识空间</th>
				  <th className="px-4 py-2 font-medium">发布</th>
                  <th className="px-4 py-2 font-medium">状态</th>
                  <th className="px-4 py-2 font-medium">分块</th>
                  <th className="px-4 py-2 font-medium">大小</th>
                  <th className="px-4 py-2 font-medium">创建时间</th>
                  <th className="px-4 py-2 font-medium">上传者</th>
                  <th className="px-4 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {items.map((doc) => (
                  <tr key={doc.doc_id} className="hover:bg-slate-50/60">
                    <td className="px-4 py-2.5 font-mono text-xs text-blue-600">
                      <Link href={`/documents/${doc.doc_id}`} className="hover:underline">
                        {doc.doc_id}
                      </Link>
                    </td>
                    <td className="px-4 py-2.5 text-slate-700">
                      <Link href={`/documents/${doc.doc_id}`} className="hover:text-blue-600 hover:underline">
                        {doc.file_name}
                      </Link>
                    </td>
                    <td className="px-4 py-2.5">
                      {(() => {
                        const meta = getFileTypeMeta(doc.file_name);
                        const Icon = meta.icon;
                        return (
                          <span className={`inline-flex items-center gap-1 rounded px-1.5 py-0.5 text-xs font-medium ${meta.badgeClass}`}>
                            <Icon className={`h-3.5 w-3.5 ${meta.iconClass}`} />
                            {meta.label}
                          </span>
                        );
                      })()}
                    </td>
                    <td className="px-4 py-2.5">
                      <Badge>{PERMISSION_LABELS[doc.permission] || doc.permission}</Badge>
                    </td>
					<td className="px-4 py-2.5 text-xs text-slate-600">{doc.knowledge_space_id || "—"}</td>
					<td className="px-4 py-2.5">
					  <Badge tone={doc.publication_status === "published" ? "success" : "warning"}>
						{doc.publication_status === "published" ? "已发布" : doc.publication_status === "retired" ? "已退役" : "草稿"}
					  </Badge>
					</td>
                    <td className="px-4 py-2.5">
                      <Badge tone={statusTone(doc.status)}>{STATUS_LABELS[doc.status] || doc.status}</Badge>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-600">
                      {doc.chunks_total ? `${doc.chunks_done ?? 0}/${doc.chunks_total}` : "—"}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-600">{formatSize(doc.file_size)}</td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">{formatDate(doc.created_at)}</td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">
                      {(() => {
                        const up = formatUploader(doc.uploaded_by);
                        return up.title ? (
                          <span title={up.title}>{up.label}</span>
                        ) : (
                          up.label
                        );
                      })()}
                    </td>
                    <td className="px-4 py-2.5">
                      {isAdmin && (
                        <button
                          onClick={() => void onDelete(doc)}
                          disabled={deleting === doc.doc_id}
                          className="rounded-md px-2 py-1 text-xs text-red-600 hover:bg-red-50 disabled:opacity-50"
                        >
                          {deleting === doc.doc_id ? "删除中…" : "删除"}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Card>
    </div>
  );
}
