"use client";

import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import type { Document } from "@/lib/types";

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

function statusChip(status: string): string {
  const s = status.toLowerCase();
  if (s === "completed") return "bg-emerald-50 text-emerald-700";
  if (s === "processing") return "bg-blue-50 text-blue-700";
  if (s === "failed") return "bg-red-50 text-red-700";
  return "bg-amber-50 text-amber-700";
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

export default function DocumentsPage() {
  const [items, setItems] = useState<Document[]>([]);
  const [total, setTotal] = useState(0);
  const [qInput, setQInput] = useState("");
  const [q, setQ] = useState("");
  const [status, setStatus] = useState("");
  const [permission, setPermission] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [deleting, setDeleting] = useState<string | null>(null);

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

  return (
    <div className="space-y-4">
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

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h2 className="text-sm font-semibold text-slate-500">文档列表</h2>
          <span className="text-xs text-slate-400">共 {total} 篇</span>
        </div>
        {loading ? (
          <div className="p-8 text-center text-sm text-slate-400">加载中…</div>
        ) : items.length === 0 ? (
          <div className="p-8 text-center text-sm text-slate-400">暂无文档</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-4 py-2 font-medium">文档 ID</th>
                  <th className="px-4 py-2 font-medium">文件名</th>
                  <th className="px-4 py-2 font-medium">权限</th>
                  <th className="px-4 py-2 font-medium">状态</th>
                  <th className="px-4 py-2 font-medium">分块</th>
                  <th className="px-4 py-2 font-medium">大小</th>
                  <th className="px-4 py-2 font-medium">创建时间</th>
                  <th className="px-4 py-2 font-medium">操作</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {items.map((doc) => (
                  <tr key={doc.doc_id} className="hover:bg-slate-50/60">
                    <td className="px-4 py-2.5 font-mono text-xs text-blue-600">{doc.doc_id}</td>
                    <td className="px-4 py-2.5 text-slate-700">{doc.file_name}</td>
                    <td className="px-4 py-2.5">
                      <span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                        {PERMISSION_LABELS[doc.permission] || doc.permission}
                      </span>
                    </td>
                    <td className="px-4 py-2.5">
                      <span className={`rounded px-2 py-0.5 text-xs font-medium ${statusChip(doc.status)}`}>
                        {STATUS_LABELS[doc.status] || doc.status}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-600">
                      {doc.chunks_total ? `${doc.chunks_done ?? 0}/${doc.chunks_total}` : "—"}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-600">{formatSize(doc.file_size)}</td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">{formatDate(doc.created_at)}</td>
                    <td className="px-4 py-2.5">
                      <button
                        onClick={() => void onDelete(doc)}
                        disabled={deleting === doc.doc_id}
                        className="rounded-md px-2 py-1 text-xs text-red-600 hover:bg-red-50 disabled:opacity-50"
                      >
                        {deleting === doc.doc_id ? "删除中…" : "删除"}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
