"use client";

import { useEffect, useState } from "react";
import { Badge } from "@/components/ui/Badge";

// Retrieval-quality data is served by /evals/latest.json (the latest real-model
// eval summary) so the page always reflects the last actual evaluation run.
type QualityRecall = { k: number; value: number; note?: string };
type QualityExperiment = { title: string; verdict: string; detail: string; good?: boolean };
type QualityData = {
  timestamp?: string;
  model_mode?: string;
  embed_model?: string;
  embed_dimension?: number;
  dataset?: string;
  noise_floor?: number;
  recall: QualityRecall[];
  experiments: QualityExperiment[];
};

export default function QualityPage() {
  const [quality, setQuality] = useState<QualityData | null>(null);
  const [loadError, setLoadError] = useState(false);

  useEffect(() => {
    fetch("/evals/latest.json")
      .then((r) => (r.ok ? r.json() : Promise.reject(new Error("quality report not found"))))
      .then(setQuality)
      .catch(() => setLoadError(true));
  }, []);

  if (loadError) {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-5 text-sm text-slate-600 shadow-sm">
        <p className="font-medium text-slate-800">这是离线评测回归页，不是某一次问答的即时评分，也不是企业业务 Gold 验收。</p>
        <p className="mt-2 text-slate-500">
          本页只展示最近一次离线检索评测报告。企业技术门槛看 Recall@5 / hit_rate ≥ 90%，Recall@1 只说明第一名稳不稳，不能当作企业级 SLO。
        </p>
        <p className="mt-2 text-slate-500">
          当前未找到评测报告（<code className="rounded bg-slate-100 px-1">docs/evals/reports/latest.json</code> 未挂载或无法读取）。不影响问答工作台使用。
        </p>
      </div>
    );
  }
  if (!quality) {
    return <div className="p-5 text-sm text-slate-400">加载检索质量数据…</div>;
  }

  const evalDate = quality.timestamp ? new Date(quality.timestamp).toISOString().slice(0, 10) : "";
  const modelLabel = quality.embed_model
    ? `${quality.embed_model}${quality.embed_dimension ? ` ${quality.embed_dimension}d` : ""}`
    : "";

  return (
    <div className="space-y-6">
      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <div className="mb-4 rounded-lg border border-blue-100 bg-blue-50 p-3 text-xs text-blue-800">
          本页面向管理员和运维人员，展示最近一次真实模型离线评测。它不是某一次问答的即时评分，也不是已签字的企业业务 Gold 验收，不能当作企业级 SLO，也不会改变在线问答结果。企业技术门槛看 Recall@5 / hit_rate ≥ 90%；Recall@1 只说明第一名稳不稳。
        </div>
        <h2 className="mb-1 text-sm font-semibold text-slate-500">
          真实模型检索质量{modelLabel ? `（${modelLabel}）` : ""}
        </h2>
        <p className="mb-4 text-xs text-slate-400">
          {quality.dataset || "离线评测集"}
          {evalDate ? ` · 最近一次真实评测 ${evalDate}` : ""} · 来源 docs/evals/reports
        </p>
        <div className="flex items-end gap-6">
          {quality.recall.map((r) => (
            <div key={r.k} className="flex flex-col items-center">
              <div className="relative flex h-40 w-14 items-end overflow-hidden rounded-lg bg-slate-100">
                <div
                  className="bg-brand-gradient w-full rounded-t-lg transition-[height] duration-500"
                  style={{ height: `${r.value}%` }}
                />
                <span className="absolute inset-x-0 top-1 text-center text-xs font-bold text-slate-700">
                  {r.value}%
                </span>
              </div>
              <span className="mt-2 text-xs font-medium text-slate-600">Recall@{r.k}</span>
              {r.note ? <span className="mt-1 max-w-[88px] text-center text-[11px] leading-4 text-slate-400">{r.note}</span> : null}
            </div>
          ))}
          <div className="ml-4 max-w-[280px] text-xs text-slate-500">
            <p className="font-medium text-slate-600">Recall@k 是目标文档出现在前 k 条来源的比例</p>
            <p className="mt-1">
              Recall@5 表示找不找得到，是仓库的企业技术门槛（≥ 90%）。Recall@1 表示第一名稳不稳，会被相似制度或长文档抢位，不是签字后的企业业务 Gold。
            </p>
          </div>
        </div>
      </div>

      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <h2 className="mb-3 text-sm font-semibold text-slate-500">相对企业技术门槛的结论</h2>
        <div className="grid gap-3 sm:grid-cols-3">
          {quality.experiments.map((e) => (
            <div key={e.title} className="rounded-lg border border-slate-200 p-3">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium text-slate-700">{e.title}</span>
                <Badge tone={e.good === false ? "danger" : e.good === true ? "success" : "neutral"}>
                  {e.verdict}
                </Badge>
              </div>
              <p className="mt-2 text-xs text-slate-500">{e.detail}</p>
            </div>
          ))}
        </div>
        <p className="mt-3 text-xs text-slate-400">
          判定标准：同配置连跑 3 轮，pass_rate 自然波动 {quality.noise_floor ?? 4.26}%——差异低于该值不算真实改进
        </p>
      </div>
    </div>
  );
}
