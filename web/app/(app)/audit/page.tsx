"use client";

import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import type { AuditEntry } from "@/lib/types";

const ACTION_LABELS: Record<string, string> = {
  login: "登录",
  upload: "上传文档",
  delete: "删除文档",
};

const ACTION_OPTIONS = ["login", "upload", "delete"];

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", { hour12: false });
}

export default function AuditPage() {
  const { isAdmin, user } = useAuth();
  const [mounted, setMounted] = useState(false);
  const [items, setItems] = useState<AuditEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [action, setAction] = useState("");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  useEffect(() => setMounted(true), []);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await apiClient.listAudit({
        limit: 100,
        ...(action ? { action } : {}),
      });
      // Backend already returns newest-first; sort defensively just in case.
      const sorted = [...data.items].sort(
        (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      );
      setItems(sorted);
      setTotal(data.total);
    } catch (e) {
      setError((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }, [action]);

  useEffect(() => {
    if (isAdmin) void load();
  }, [isAdmin, load]);

  const toggleDetail = (idx: number) => {
    setExpanded((prev) => {
      const next = new Set(prev);
      if (next.has(idx)) next.delete(idx);
      else next.add(idx);
      return next;
    });
  };

  if (!mounted) {
    return (
      <div className="flex min-h-[60vh] items-center justify-center text-sm text-slate-400">
        加载中…
      </div>
    );
  }

  if (!isAdmin) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-10 text-center shadow-sm">
        <p className="text-sm font-medium text-slate-700">无权访问</p>
        <p className="mt-1 text-xs text-slate-500">该页面仅管理员可见。</p>
      </div>
    );
  }

  if (user && !isAdmin) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-10 text-center shadow-sm">
        <p className="text-sm font-medium text-slate-700">无权访问</p>
        <p className="mt-1 text-xs text-slate-500">该页面仅管理员可见。</p>
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <h2 className="text-sm font-semibold text-slate-500">审计日志</h2>
        <div>
          <label className="mb-1 block text-xs font-medium text-slate-500">操作</label>
          <select
            value={action}
            onChange={(e) => setAction(e.target.value)}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm"
          >
            <option value="">全部</option>
            {ACTION_OPTIONS.map((k) => (
              <option key={k} value={k}>
                {ACTION_LABELS[k] || k}
              </option>
            ))}
          </select>
        </div>
      </div>

      {error && <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{error}</div>}

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h3 className="text-sm font-semibold text-slate-500">操作记录</h3>
          <span className="text-xs text-slate-400">共 {total} 条</span>
        </div>
        {loading ? (
          <div className="p-8 text-center text-sm text-slate-400">加载中…</div>
        ) : items.length === 0 ? (
          <div className="p-8 text-center text-sm text-slate-400">暂无审计记录</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50 text-xs text-slate-500">
                <tr>
                  <th className="px-4 py-2 font-medium">时间</th>
                  <th className="px-4 py-2 font-medium">操作</th>
                  <th className="px-4 py-2 font-medium">资源</th>
                  <th className="px-4 py-2 font-medium">操作者</th>
                  <th className="px-4 py-2 font-medium">结果</th>
                  <th className="px-4 py-2 font-medium">详情</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {items.map((entry, idx) => (
                  <tr key={`${entry.created_at}-${idx}`} className="hover:bg-slate-50/60">
                    <td className="whitespace-nowrap px-4 py-2.5 text-xs text-slate-500">
                      {formatDate(entry.created_at)}
                    </td>
                    <td className="px-4 py-2.5">
                      <span className="rounded bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-700">
                        {ACTION_LABELS[entry.action] || entry.action}
                      </span>
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-600">
                      {entry.resource_type ? (
                        <span className="font-mono">
                          {entry.resource_type}
                          {entry.resource_id ? (
                            <span className="text-slate-400"> · {entry.resource_id}</span>
                          ) : null}
                        </span>
                      ) : (
                        <span className="text-slate-400">—</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-600">
                      <span className="font-mono">{entry.actor_user_id || "—"}</span>
                      {entry.actor_role ? (
                        <span className="ml-1.5 text-slate-400">{ROLE_LABELS[entry.actor_role] || entry.actor_role}</span>
                      ) : null}
                    </td>
                    <td className="px-4 py-2.5">
                      <span
                        className={
                          entry.result === "success"
                            ? "rounded bg-emerald-50 px-2 py-0.5 text-xs font-medium text-emerald-700"
                            : "rounded bg-red-50 px-2 py-0.5 text-xs font-medium text-red-700"
                        }
                      >
                        {entry.result === "success" ? "成功" : "失败"}
                      </span>
                    </td>
                    <td className="px-4 py-2.5">
                      {entry.detail ? (
                        <>
                          <button
                            onClick={() => toggleDetail(idx)}
                            className="rounded-md px-2 py-1 text-xs text-blue-600 hover:bg-blue-50"
                          >
                            {expanded.has(idx) ? "收起" : "展开"}
                          </button>
                          {expanded.has(idx) && (
                            <pre className="mt-2 max-w-xs overflow-x-auto rounded-lg bg-slate-50 p-2 font-mono text-xs text-slate-600">
                              {JSON.stringify(entry.detail, null, 2)}
                            </pre>
                          )}
                        </>
                      ) : (
                        <span className="text-xs text-slate-400">—</span>
                      )}
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
