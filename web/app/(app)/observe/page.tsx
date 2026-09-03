"use client";

import { useEffect, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import StatCard from "@/components/StatCard";
import { Card } from "@/components/ui/Card";
import { Badge } from "@/components/ui/Badge";
import { Spinner } from "@/components/ui/Spinner";
import { Activity, AlertTriangle, CheckCircle2, GaugeCircle, Server, XCircle } from "lucide-react";
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

  const entries = health?.services ? Object.entries(health.services) : [];
  const upCount = entries.filter(([, h]) => h.status === "up").length;
  const degradedCount = entries.filter(([, h]) => h.status === "degraded").length;
  const downCount = entries.length - upCount - degradedCount;
  const overall = health ? (upCount === entries.length ? "up" : "degraded") : "loading";

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <StatCard label="服务总数" value={health ? String(entries.length) : "—"} icon={Server} />
        <StatCard
          label="在线服务"
          value={health ? String(upCount) : "—"}
          tone={upCount > 0 ? "success" : "default"}
          icon={Activity}
        />
        <StatCard
          label="降级服务"
          value={health ? String(degradedCount) : "—"}
          tone={degradedCount > 0 ? "danger" : "default"}
          icon={AlertTriangle}
        />
        <StatCard
          label="异常服务"
          value={health ? String(downCount) : "—"}
          tone={downCount > 0 ? "danger" : "default"}
          icon={XCircle}
        />
      </div>

      <Card
        header={
          <span className="flex items-center gap-2">
            <GaugeCircle className="h-4 w-4 text-slate-400" />
            服务健康
          </span>
        }
        meta={
          <Badge tone={overall === "up" ? "success" : overall === "loading" ? "neutral" : "danger"}>
            {overall === "up" ? "全部在线" : overall === "loading" ? "加载中" : "有服务异常"}
          </Badge>
        }
      >
        {loading && !health ? (
          <Spinner label="加载服务健康…" />
        ) : (
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
            {entries.map(([name, h]) => (
              <div
                key={name}
                className="flex items-center justify-between rounded-lg border border-slate-200 px-3 py-2"
              >
                <span className="flex items-center gap-1.5 font-mono text-xs text-slate-700">
                  {h.status === "up" ? (
                    <CheckCircle2 className="h-3.5 w-3.5 text-emerald-500" />
                  ) : h.status === "degraded" ? (
                    <AlertTriangle className="h-3.5 w-3.5 text-amber-500" />
                  ) : (
                    <XCircle className="h-3.5 w-3.5 text-red-500" />
                  )}
                  {name}
                </span>
                <span className="flex items-center gap-1.5">
                  <span
                    className={`h-2 w-2 animate-pulse rounded-full ${
                      h.status === "up" ? "bg-emerald-500" : h.status === "degraded" ? "bg-amber-500" : "bg-red-500"
                    }`}
                  />
                  <span className="text-xs text-slate-400">{h.latency_ms}ms</span>
                </span>
              </div>
            ))}
          </div>
        )}
        <p className="mt-3 text-xs text-slate-400">经 query-api 聚合主链路依赖（共 7 项）· 每 10 秒自动刷新。Kafka、PostgreSQL、MinIO、监控面板及一次性初始化容器属于基础设施/运维组件，不在此健康探测白名单中。</p>
      </Card>
    </div>
  );
}
