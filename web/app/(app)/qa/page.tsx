"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import type { AnswerMeta, Source } from "@/lib/types";

const STRATEGY_LABELS: Record<string, string> = {
  exact_keyword: "精确关键词",
  semantic: "语义检索",
  hybrid: "混合检索（语义 + 关键词）",
};

function formatCost(usd: number): string {
  if (usd >= 0.01) return `$${usd.toFixed(3)}`;
  if (usd >= 0.0001) return `$${usd.toFixed(5)}`;
  return `$${usd.toFixed(7)}`;
}

// Evidence confidence from the top Qdrant cosine score. Observational, not a
// gate: the current embedding's distributions overlap too heavily for a hard
// threshold (see ADR 0006).
function confidenceLabel(maxRelevance?: number): string {
  if (maxRelevance === undefined) return "—";
  if (maxRelevance > 0.7) return `高 (${maxRelevance.toFixed(3)})`;
  if (maxRelevance > 0.55) return `中 (${maxRelevance.toFixed(3)})`;
  return `低 (${maxRelevance.toFixed(3)})`;
}

function isHighConfidence(maxRelevance?: number): boolean {
  return (maxRelevance ?? 0) > 0.7;
}

// Post-generation faithfulness verdict. The grounding check only runs in the
// ambiguous relevance band; a blocked answer was replaced with the fixed
// refusal sentence because its claims were not traceable to the sources.
function groundingLabel(retrieval?: AnswerMeta["retrieval"]): { value: string; highlight: boolean } {
  if (!retrieval) return { value: "—", highlight: false };
  if (!retrieval.grounding_checked) return { value: "未触发", highlight: false };
  return retrieval.grounding_passed
    ? { value: "通过（可溯源）", highlight: true }
    : { value: "拦截（无据拒答）", highlight: true };
}

function Row({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <dt className="shrink-0 text-slate-500">{label}</dt>
      <dd
        className={
          highlight ? "truncate text-right font-medium text-emerald-600" : "truncate text-right text-slate-700"
        }
      >
        {value}
      </dd>
    </div>
  );
}

export default function QaPage() {
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [sources, setSources] = useState<Source[]>([]);
  const [meta, setMeta] = useState<AnswerMeta>({});
  const [status, setStatus] = useState<"idle" | "streaming" | "done" | "error">("idle");
  const [error, setError] = useState("");
  const abortRef = useRef<AbortController | null>(null);

  const stream = useCallback(async (q: string) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setAnswer("");
    setSources([]);
    setMeta({});
    setStatus("streaming");
    setError("");
    try {
      await apiClient.querySSE(
        q,
        {
          onSources: (s) => setSources(s),
          onDelta: (text) => setAnswer((prev) => prev + text),
          onDone: (m) => setMeta(m),
          onError: (msg) => {
            setStatus("error");
            setError(msg);
          },
        },
        controller.signal
      );
      setStatus("done");
    } catch (e: unknown) {
      if ((e as Error).name === "AbortError") return;
      setStatus("error");
      setError((e as Error).message || "未知错误");
    }
  }, []);

  useEffect(() => {
    return () => abortRef.current?.abort();
  }, []);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const q = question.trim();
    if (!q || status === "streaming") return;
    void stream(q);
  };

  const isRefusal = answer.includes("未找到相关文档");

  return (
    <div className="space-y-6">
      <form onSubmit={onSubmit} className="flex flex-wrap gap-2">
        <input
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          placeholder="输入问题，基于企业文档知识库回答"
          className="min-w-[280px] flex-1 rounded-lg border border-slate-300 bg-white px-4 py-2.5 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500"
          disabled={status === "streaming"}
        />
        <button
          type="submit"
          disabled={status === "streaming" || !question.trim()}
          className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {status === "streaming" ? "生成中…" : "提问"}
        </button>
      </form>

      {(status === "streaming" || status === "done" || status === "error") && (
        <div className="grid gap-6 lg:grid-cols-[1fr_340px]">
          <div className="space-y-4">
            <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
              <div className="mb-2 flex items-center justify-between">
                <h2 className="text-sm font-semibold text-slate-500">回答</h2>
              </div>
              {isRefusal ? (
                <div className="rounded-lg bg-amber-50 p-3 text-sm text-amber-800">
                  <p className="font-medium">未找到相关文档，无法回答该问题。</p>
                  <p className="mt-1 text-xs text-amber-600">
                    系统在检索到可支撑证据前拒绝作答，避免幻觉编造。
                  </p>
                </div>
              ) : answer ? (
                <p className="whitespace-pre-wrap leading-relaxed text-slate-800">{answer}</p>
              ) : (
                <p className="text-slate-400">{status === "streaming" ? "等待回答…" : ""}</p>
              )}
            </div>

            {sources.length > 0 && (
              <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
                <h2 className="mb-3 text-sm font-semibold text-slate-500">引用来源（{sources.length}）</h2>
                <div className="space-y-2">
                  {sources.map((s) => (
                    <details key={s.chunk_id} className="group rounded-lg border border-slate-200">
                      <summary className="flex cursor-pointer items-center justify-between px-3 py-2 text-sm hover:bg-slate-50">
                        <span className="font-mono text-blue-600">{s.doc_id}</span>
                        <span className="text-xs text-slate-400">score {s.score.toFixed(4)}</span>
                      </summary>
                      <p className="border-t border-slate-100 px-3 py-2 text-sm text-slate-600">{s.content}</p>
                    </details>
                  ))}
                </div>
              </div>
            )}

            {status === "error" && (
              <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">{error}</div>
            )}
          </div>

          <div className="space-y-4">
            <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
              <h2 className="mb-3 text-sm font-semibold text-slate-500">检索链路</h2>
              <dl className="space-y-2 text-sm">
                <Row
                  label="路由策略"
                  value={meta.retrieval ? STRATEGY_LABELS[meta.retrieval.strategy] || meta.retrieval.strategy : "—"}
                />
                <Row
                  label="语义缓存"
                  value={meta.retrieval ? (meta.retrieval.cache_hit ? "命中" : "未命中") : "—"}
                  highlight={meta.retrieval?.cache_hit}
                />
                <Row label="检索后端" value={meta.retrieval?.backends.join(" + ") || "—"} />
                <Row label="候选数" value={meta.retrieval ? String(meta.retrieval.candidate_count) : "—"} />
                <Row
                  label="证据置信度"
                  value={confidenceLabel(meta.retrieval?.max_relevance)}
                  highlight={isHighConfidence(meta.retrieval?.max_relevance)}
                />
                <Row
                  label="忠实度校验"
                  value={groundingLabel(meta.retrieval).value}
                  highlight={groundingLabel(meta.retrieval).highlight}
                />
                <Row label="检索耗时" value={meta.retrieval ? `${meta.retrieval.duration_ms}ms` : "—"} />
              </dl>
            </div>
            <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
              <h2 className="mb-3 text-sm font-semibold text-slate-500">本次查询</h2>
              <dl className="space-y-2 text-sm">
                <Row
                  label="Token"
                  value={
                    meta.token_usage
                      ? `${meta.token_usage.prompt_tokens} in / ${meta.token_usage.completion_tokens} out`
                      : "—"
                  }
                />
                <Row
                  label="估算成本"
                  value={meta.token_usage?.estimated_cost_usd ? formatCost(meta.token_usage.estimated_cost_usd) : "—"}
                  highlight={Boolean(meta.token_usage?.estimated_cost_usd)}
                />
                <Row label="总耗时" value={meta.duration || "—"} />
                <Row label="Prompt 版本" value={meta.prompt_version || "v1"} />
              </dl>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
