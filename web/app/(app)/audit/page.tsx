"use client";

import { useCallback, useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import { Button } from "@/components/ui/Button";
import { PageHeader } from "@/components/ui/PageHeader";
import { ChevronLeft, ChevronRight, RotateCcw, Search, ScrollText } from "lucide-react";
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

export default function AuditPage() {
  const { isAdmin, user } = useAuth();
  const [mounted, setMounted] = useState(false);
  const [items, setItems] = useState<AuditEntry[]>([]);
  const [total, setTotal] = useState(0);
  const [action, setAction] = useState("");
  const [queryInput, setQueryInput] = useState("");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const pageSize = 25;
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [expanded, setExpanded] = useState<Set<number>>(new Set());

  useEffect(() => setMounted(true), []);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const data = await apiClient.listAudit({
        limit: pageSize,
        offset: page * pageSize,
        ...(action ? { action } : {}),
        ...(query.trim() ? { q: query.trim() } : {}),
      });
      // Backend already returns newest-first; sort defensively just in case.
      const sorted = [...data.items].sort(
        (a, b) => new Date(b.created_at).getTime() - new Date(a.created_at).getTime()
      );
      setItems(sorted);
      setTotal(data.total);
      setExpanded(new Set());
    } catch (e) {
      setError((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }, [action, page, query]);

  useEffect(() => {
    if (isAdmin) void load();
  }, [isAdmin, load]);

  const changeAction = (nextAction: string) => {
    setAction(nextAction);
    setPage(0);
  };

  const submitSearch = (event: React.FormEvent) => {
    event.preventDefault();
    setPage(0);
    setQuery(queryInput.trim());
  };

  const resetFilters = () => {
    setAction("");
    setQueryInput("");
    setQuery("");
    setPage(0);
  };

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
    <div className="space-y-5">
      <PageHeader icon={ScrollText} title="审计日志" description="追踪登录、数据变更和管理操作，用于安全核查与问题追溯。" />

      {error && <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{error}</div>}

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="border-b border-slate-200 bg-slate-50/60 p-4">
          <form onSubmit={submitSearch} className="flex flex-col gap-3 lg:flex-row lg:items-end">
            <div className="min-w-0 flex-1">
              <label htmlFor="audit-query" className="mb-1.5 block text-xs font-medium text-slate-600">关键词</label>
              <div className="relative">
                <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
                <input
                  id="audit-query"
                  value={queryInput}
                  onChange={(event) => setQueryInput(event.target.value)}
                  placeholder="检索操作者、资源编号、操作或详情"
                  className="h-10 w-full rounded-lg border border-slate-300 bg-white pl-9 pr-3 text-sm outline-none transition focus:border-blue-500 focus:ring-2 focus:ring-blue-500/10"
                />
              </div>
            </div>
            <div className="w-full lg:w-48">
              <label htmlFor="audit-action" className="mb-1.5 block text-xs font-medium text-slate-600">操作类型</label>
              <select
                id="audit-action"
                value={action}
                onChange={(event) => changeAction(event.target.value)}
                className="h-10 w-full rounded-lg border border-slate-300 bg-white px-3 text-sm text-slate-700 outline-none focus:border-blue-500 focus:ring-2 focus:ring-blue-500/10"
              >
                <option value="">全部操作</option>
                {ACTION_OPTIONS.map((key) => <option key={key} value={key}>{ACTION_LABELS[key] || key}</option>)}
              </select>
            </div>
            <div className="flex gap-2">
              <Button type="submit" icon={Search} loading={loading}>检索</Button>
              <Button type="button" variant="secondary" icon={RotateCcw} disabled={!action && !queryInput && !query} onClick={resetFilters}>重置</Button>
            </div>
          </form>
          {(action || query) && (
            <p className="mt-3 text-xs text-slate-500">
              当前条件：{action ? `操作=${ACTION_LABELS[action] || action}` : "全部操作"}{query ? ` · 关键词=${query}` : ""}
            </p>
          )}
        </div>
        <div className="flex items-center justify-between border-b border-slate-100 px-4 py-3">
          <h2 className="text-sm font-semibold text-slate-700">操作记录</h2>
          <span className="rounded-full bg-slate-100 px-2.5 py-1 text-xs text-slate-500">{total} 条记录</span>
        </div>
        {loading ? (
          <div className="p-8 text-center text-sm text-slate-400">加载中…</div>
        ) : items.length === 0 ? (
          <div className="p-8 text-center text-sm text-slate-400">暂无审计记录</div>
        ) : (
          <div className="overflow-x-auto">
            <table className="w-full text-left text-sm">
              <thead className="bg-slate-50/80 text-xs uppercase tracking-wide text-slate-500">
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
                  <tr key={`${entry.created_at}-${idx}`} className="align-top transition-colors hover:bg-blue-50/30">
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
        <div className="flex flex-col gap-3 border-t border-slate-200 px-4 py-3 sm:flex-row sm:items-center sm:justify-between">
          <span className="text-xs text-slate-500">
            {total === 0 ? "暂无记录" : `显示 ${page * pageSize + 1}–${Math.min((page + 1) * pageSize, total)}，共 ${total} 条`}
          </span>
          <nav aria-label="审计日志分页" className="flex items-center gap-1">
            <button
              type="button"
              aria-label="上一页"
              disabled={page === 0 || loading}
              onClick={() => setPage((current) => Math.max(0, current - 1))}
              className="flex h-8 w-8 items-center justify-center rounded-md border border-slate-200 text-slate-600 hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-35"
            >
              <ChevronLeft className="h-4 w-4" />
            </button>
            {paginationItems(page, Math.max(1, Math.ceil(total / pageSize))).map((item, index) =>
              item === "ellipsis" ? (
                <span key={`ellipsis-${index}`} className="flex h-8 w-8 items-center justify-center text-xs text-slate-400">…</span>
              ) : (
                <button
                  key={item}
                  type="button"
                  aria-current={item === page ? "page" : undefined}
                  disabled={loading || total === 0}
                  onClick={() => setPage(item)}
                  className={`flex h-8 min-w-8 items-center justify-center rounded-md px-2 text-xs font-medium ${item === page ? "bg-blue-600 text-white" : "border border-slate-200 text-slate-600 hover:bg-slate-50"}`}
                >
                  {item + 1}
                </button>
              )
            )}
            <button
              type="button"
              aria-label="下一页"
              disabled={(page + 1) * pageSize >= total || loading}
              onClick={() => setPage((current) => current + 1)}
              className="flex h-8 w-8 items-center justify-center rounded-md border border-slate-200 text-slate-600 hover:bg-slate-50 disabled:cursor-not-allowed disabled:opacity-35"
            >
              <ChevronRight className="h-4 w-4" />
            </button>
          </nav>
        </div>
      </div>
    </div>
  );
}
