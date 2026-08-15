"use client";

import { useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import StatCard from "@/components/StatCard";
import type { Health } from "@/lib/types";

export default function ObservePage() {
  const [health, setHealth] = useState<Health | null>(null);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const d = await apiClient.getHealth();
        if (!cancelled) setHealth(d);
      } catch {
        // 401 redirect is handled by the API client; other failures just skip a tick
      } finally {
        if (!cancelled) setLoading(false);
      }
    };
    void load();
    const id = setInterval(load, 10000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  const entries = health ? Object.entries(health) : [];
  const upCount = entries.filter(([, h]) => h.status === "up").length;
  const degradedCount = entries.filter(([, h]) => h.status === "degraded").length;
  const downCount = entries.length - upCount - degradedCount;
  const overall = health ? (upCount === entries.length ? "up" : "degraded") : "loading";

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <StatCard label="服务总数" value={health ? String(entries.length) : "—"} />
        <StatCard label="在线服务" value={health ? String(upCount) : "—"} />
        <StatCard label="降级服务" value={health ? String(degradedCount) : "—"} />
        <StatCard label="异常服务" value={health ? String(downCount) : "—"} accent={downCount > 0} />
      </div>

      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-slate-500">服务健康</h2>
          <span
            className={
              overall === "up"
                ? "rounded bg-emerald-50 px-2 py-0.5 text-xs text-emerald-700"
                : overall === "loading"
                  ? "rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-500"
                  : "rounded bg-red-50 px-2 py-0.5 text-xs text-red-700"
            }
          >
            {overall === "up" ? "全部在线" : overall === "loading" ? "加载中" : "有服务异常"}
          </span>
        </div>
        {loading && !health ? (
          <div className="p-6 text-center text-sm text-slate-400">加载中…</div>
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {entries.map(([name, h]) => (
              <div
                key={name}
                className="flex items-center justify-between rounded-lg border border-slate-200 px-3 py-2"
              >
                <span className="font-mono text-xs text-slate-700">{name}</span>
                <span className="flex items-center gap-1.5">
                  <span
                    className={`h-2 w-2 rounded-full ${
                      h.status === "up" ? "bg-emerald-500" : h.status === "degraded" ? "bg-amber-500" : "bg-red-500"
                    }`}
                  />
                  <span className="text-xs text-slate-400">{h.latency_ms}ms</span>
                </span>
              </div>
            ))}
          </div>
        )}
        <p className="mt-3 text-xs text-slate-400">
          经 query-api 聚合各服务 healthz · 每 10 秒自动刷新
        </p>
      </div>
    </div>
  );
}
