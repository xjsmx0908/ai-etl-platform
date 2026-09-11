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
import { PageHeader } from "@/components/ui/PageHeader";
import { ChevronLeft, ChevronRight, ChevronsLeft, ChevronsRight, Files, FileText, Filter, RotateCcw, Search, ShieldCheck, Sparkles } from "lucide-react";
import { useAuth } from "@/lib/auth";
import { formatUploader, getFileTypeMeta, spaceLabel } from "@/lib/docDisplay";
import type { Document, DocumentSearchResult, KnowledgeSpace } from "@/lib/types";

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

function permissionTone(permission: string) {
  if (permission === "public") return "success" as const;
  if (permission === "internal") return "info" as const;
  if (permission === "confidential") return "warning" as const;
  return "neutral" as const;
}

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

function paginationItems(current: number, total: number): Array<number | "ellipsis"> {
  if (total <= 7) return Array.from({ length: total }, (_, index) => index);
  const pages = new Set([0, total - 1, current - 1, current, current + 1]);
  const visible = [...pages].filter((page) => page >= 0 && page < total).sort((a, b) => a - b);
  const result: Array<number | "ellipsis"> = [];
  visible.forEach((page, index) => {
    if (index > 0 && page - visible[index - 1] > 1) result.push("ellipsis");
    result.push(page);
  });
  return result;
}

export default function DocumentsPage() {
  const { isAdmin, role } = useAuth();
  const [items, setItems] = useState<Document[]>([]);
  const [total, setTotal] = useState(0);
  const [qInput, setQInput] = useState("");
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [permission, setPermission] = useState("");
  const [page, setPage] = useState(0);
  const [pageSize, setPageSize] = useState(20);
  const [pageInput, setPageInput] = useState("1");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<string | null>(null);
  const [searchInput, setSearchInput] = useState("");
  const [searchResults, setSearchResults] = useState<DocumentSearchResult[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [searchError, setSearchError] = useState("");
  const [spaces, setSpaces] = useState<KnowledgeSpace[]>([]);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await apiClient.listDocuments({
        limit: pageSize,
        offset: page * pageSize,
        ...(q ? { q } : {}),
        ...(status ? { status } : {}),
        ...(permission ? { permission } : {}),
      });
      setItems(data.items);
      setTotal(data.total);
      setPage((current) => Math.min(current, Math.max(0, Math.ceil(data.total / pageSize) - 1)));
    } catch (e) {
      setError((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }, [q, status, permission, page, pageSize]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    void apiClient.listKnowledgeSpaces().then((data) => setSpaces(data.items)).catch(() => undefined);
  }, []);

  useEffect(() => {
    setPageInput(String(page + 1));
  }, [page]);

  const onSearch = (e: React.FormEvent) => {
    e.preventDefault();
    setPage(0);
    setQ(qInput.trim());
  };

  const onDelete = async (doc: Document) => {
    if (!window.confirm(`确认删除文档「${doc.file_name}」（${doc.doc_id}）？该操作不可恢复。`)) return;
    setDeleting(doc.doc_id);
    setError("");
    try {
      await apiClient.deleteDocument(doc.doc_id);
      if (items.length === 1 && page > 0) setPage((current) => current - 1);
      else void load();
    } catch (e) {
      setError((e as Error).message || "删除失败");
    } finally {
      setDeleting(null);
    }
  };

  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const rangeStart = total === 0 ? 0 : page * pageSize + 1;
  const rangeEnd = Math.min((page + 1) * pageSize, total);

  const goToPage = () => {
    const requested = Number.parseInt(pageInput, 10);
    if (!Number.isFinite(requested)) return;
    setPage(Math.min(totalPages - 1, Math.max(0, requested - 1)));
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
    <div className="space-y-6">
      <PageHeader
        icon={FileText}
        title="文档管理"
        description="按文件名查找、筛选并管理知识库文档"
        actions={
          <div className="rounded-lg bg-slate-100 px-3 py-2 text-right">
            <div className="text-lg font-semibold leading-none text-slate-800">{total}</div>
            <div className="mt-1 text-[11px] text-slate-500">当前可见文档</div>
          </div>
        }
      />

      <div className="flex flex-wrap items-center gap-x-6 gap-y-2 rounded-xl border border-blue-100 bg-blue-50/70 px-4 py-3 text-xs text-slate-600">
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

      <div className="rounded-xl border border-slate-200 bg-white px-3 py-3 shadow-sm">
        <div className="flex flex-col gap-2 lg:flex-row lg:items-center">
          <div className="flex shrink-0 items-center gap-2 text-xs font-semibold text-slate-700">
            <Filter className="h-4 w-4 text-blue-600" />
            筛选
          </div>
          <form onSubmit={onSearch} className="flex min-w-0 flex-1 gap-2">
            <div className="relative min-w-0 flex-1">
              <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
              <input
                value={qInput}
                onChange={(e) => setQInput(e.target.value)}
                placeholder="文件名 / 文档 ID"
                className="w-full rounded-lg border border-slate-200 bg-slate-50 py-2 pl-9 pr-3 text-sm transition focus:border-blue-400 focus:bg-white focus:outline-none focus:ring-2 focus:ring-blue-500/10"
              />
            </div>
            <button type="submit" className="rounded-lg bg-blue-600 px-3.5 text-sm font-medium text-white transition hover:bg-blue-700">搜索</button>
          </form>
          <select value={status} onChange={(e) => { setStatus(e.target.value); setPage(0); }} aria-label="处理状态" className="rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 focus:border-blue-400 focus:outline-none">
            <option value="">处理状态：全部</option>
            {Object.entries(STATUS_LABELS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
          <select value={permission} onChange={(e) => { setPermission(e.target.value); setPage(0); }} aria-label="访问权限" className="rounded-lg border border-slate-200 bg-white px-3 py-2 text-sm text-slate-700 focus:border-blue-400 focus:outline-none">
            <option value="">访问权限：全部</option>
            {Object.entries(PERMISSION_LABELS).map(([k, v]) => <option key={k} value={k}>{v}</option>)}
          </select>
          {(q || status || permission) && (
            <button
              type="button"
              onClick={() => { setQInput(""); setQ(""); setStatus(""); setPermission(""); setPage(0); }}
              className="inline-flex shrink-0 items-center gap-1 px-1 text-xs text-slate-500 transition hover:text-blue-600"
            >
              <RotateCcw className="h-3.5 w-3.5" />
              清除
            </button>
          )}
        </div>
      </div>

      {error && <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{error}</div>}

      <div className="rounded-xl border border-slate-200 bg-white px-3 py-3 shadow-sm">
        <form
          onSubmit={(e) => {
            e.preventDefault();
            void onContentSearch();
          }}
          className="flex flex-col gap-2 sm:flex-row sm:items-center"
        >
          <div className="flex shrink-0 items-center gap-2 text-xs font-semibold text-slate-700"><Search className="h-4 w-4 text-indigo-600" />正文检索</div>
          <Input value={searchInput} onChange={(e) => setSearchInput(e.target.value)} placeholder="搜索文档正文，例如：pipeline" className="border-slate-200 bg-slate-50 py-2" />
          <Button type="submit" icon={Search} className="shrink-0 sm:px-4">搜索正文</Button>
        </form>
        {searchError && <p className="mt-2 text-xs text-red-600">{searchError}</p>}
        {searching && <p className="mt-2 text-xs text-slate-400">搜索中…</p>}
        {searchResults !== null && !searching && (
          <div className="mt-3 space-y-2">
            {searchResults.length === 0 ? (
              <div className="rounded-lg border border-dashed border-slate-200 bg-white/70 px-3 py-4 text-center text-xs text-slate-400">未找到匹配文档</div>
            ) : (
              searchResults.map((r) => (
                <div key={r.doc_id} className="rounded-lg border border-slate-200 bg-white/80 p-3 shadow-sm">
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

      <Card
        header={
          <div className="flex items-center gap-2 text-slate-800">
            <Files className="h-4 w-4 text-blue-600" />
            <span>文档列表</span>
          </div>
        }
        meta={q || status || permission ? `筛选后 ${total} 篇` : `共 ${total} 篇`}
        padding="none"
      >
        {loading ? (
          <Spinner />
        ) : items.length === 0 ? (
          <EmptyState icon={Files} title="暂无文档" />
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50/90 text-xs text-slate-500">
                <tr>
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
                  <tr key={doc.doc_id} className="transition-colors hover:bg-blue-50/30">
                    <td className="px-4 py-2.5 text-slate-700">
                      <Link href={`/documents/${doc.doc_id}`} className="font-medium hover:text-blue-600 hover:underline">
                        {doc.file_name}
                      </Link>
                      <div className="mt-0.5 font-mono text-[11px] text-slate-400">{doc.doc_id}</div>
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
                      <Badge tone={permissionTone(doc.permission)}>{PERMISSION_LABELS[doc.permission] || doc.permission}</Badge>
                    </td>
					<td className="px-4 py-2.5 text-xs text-slate-600">{spaceLabel(doc.knowledge_space_id, spaces)}</td>
					<td className="px-4 py-2.5">
					  <Badge tone={doc.deletion_status === "pending" ? "warning" : doc.publication_status === "published" ? "success" : "warning"}>
						{doc.deletion_status === "pending" ? "删除处理中" : doc.publication_status === "published" ? "已发布" : doc.publication_status === "retired" ? "已退役" : "草稿"}
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
                          disabled={deleting === doc.doc_id || doc.deletion_status === "pending"}
                          className="rounded-md px-2 py-1 text-xs text-red-600 hover:bg-red-50 disabled:opacity-50"
                        >
                          {doc.deletion_status === "pending" ? "处理中" : deleting === doc.doc_id ? "删除中…" : "删除"}
                        </button>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
        {(
          <div className="flex flex-col gap-3 border-t border-slate-200 bg-slate-50/40 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-center gap-3 text-xs text-slate-500">
              <span>显示 {rangeStart}–{rangeEnd}，共 {total} 篇</span>
              <label className="flex items-center gap-1.5">
                每页
                <select
                  value={pageSize}
                  onChange={(e) => { setPageSize(Number(e.target.value)); setPage(0); }}
                  className="rounded-md border border-slate-200 bg-white px-2 py-1 text-xs text-slate-600 focus:border-blue-400 focus:outline-none"
                  aria-label="每页条数"
                >
                  <option value={20}>20</option>
                  <option value={50}>50</option>
                  <option value={100}>100</option>
                </select>
                篇
              </label>
            </div>
            <nav aria-label="文档列表分页" className="flex flex-wrap items-center justify-end gap-1">
                <button
                  type="button"
                  aria-label="上一页"
                  disabled={page === 0 || loading}
                  onClick={() => setPage((current) => Math.max(0, current - 1))}
                  className="flex h-8 w-8 items-center justify-center rounded-md border border-slate-200 bg-white text-slate-600 transition hover:bg-blue-50 hover:text-blue-700 disabled:cursor-not-allowed disabled:opacity-35"
                >
                  <ChevronLeft className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  aria-label="第一页"
                  disabled={page === 0 || loading}
                  onClick={() => setPage(0)}
                  className="hidden h-8 w-8 items-center justify-center rounded-md border border-slate-200 bg-white text-slate-600 transition hover:bg-blue-50 hover:text-blue-700 disabled:cursor-not-allowed disabled:opacity-35 sm:flex"
                >
                  <ChevronsLeft className="h-4 w-4" />
                </button>
                {paginationItems(page, totalPages).map((item, index) =>
                  item === "ellipsis" ? (
                    <span key={`ellipsis-${index}`} className="flex h-8 w-8 items-center justify-center text-xs text-slate-400">…</span>
                  ) : (
                    <button
                      key={item}
                      type="button"
                      aria-current={item === page ? "page" : undefined}
                      disabled={loading}
                      onClick={() => setPage(item)}
                      className={`flex h-8 min-w-8 items-center justify-center rounded-md px-2 text-xs font-medium transition ${item === page ? "bg-blue-600 text-white shadow-sm" : "border border-slate-200 bg-white text-slate-600 hover:bg-blue-50 hover:text-blue-700"}`}
                    >
                      {item + 1}
                    </button>
                  )
                )}
                <button
                  type="button"
                  aria-label="下一页"
                  disabled={(page + 1) * pageSize >= total || loading}
                  onClick={() => setPage((current) => Math.min(totalPages - 1, current + 1))}
                  className="flex h-8 w-8 items-center justify-center rounded-md border border-slate-200 bg-white text-slate-600 transition hover:bg-blue-50 hover:text-blue-700 disabled:cursor-not-allowed disabled:opacity-35"
                >
                  <ChevronRight className="h-4 w-4" />
                </button>
                <button
                  type="button"
                  aria-label="最后一页"
                  disabled={page === totalPages - 1 || loading}
                  onClick={() => setPage(totalPages - 1)}
                  className="hidden h-8 w-8 items-center justify-center rounded-md border border-slate-200 bg-white text-slate-600 transition hover:bg-blue-50 hover:text-blue-700 disabled:cursor-not-allowed disabled:opacity-35 sm:flex"
                >
                  <ChevronsRight className="h-4 w-4" />
                </button>
                <form onSubmit={(event) => { event.preventDefault(); goToPage(); }} className="ml-2 hidden items-center gap-1.5 sm:flex">
                  <span className="text-xs text-slate-400">跳至</span>
                  <input
                    value={pageInput}
                    onChange={(event) => setPageInput(event.target.value.replace(/\D/g, ""))}
                    aria-label="跳转页码"
                    className="h-8 w-12 rounded-md border border-slate-200 bg-white px-2 text-center text-xs text-slate-700 outline-none focus:border-blue-400 focus:ring-2 focus:ring-blue-500/10"
                    inputMode="numeric"
                  />
                  <span className="text-xs text-slate-400">页</span>
                </form>
            </nav>
          </div>
        )}
      </Card>
    </div>
  );
}
