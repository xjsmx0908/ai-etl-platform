"use client";

import { useCallback, useEffect, useRef, useState } from "react";

type Source = {
  doc_id: string;
  chunk_id: string;
  content: string;
  score: number;
};

type RetrievalInfo = {
  strategy: string;
  cache_hit: boolean;
  backends: string[];
  candidate_count: number;
  duration_ms: number;
  partial_errors?: string[];
};

type TokenUsage = {
  prompt_tokens: number;
  completion_tokens: number;
  estimated_cost_usd?: number;
};

type AnswerMeta = {
  retrieval?: RetrievalInfo;
  token_usage?: TokenUsage;
  prompt_version?: string;
  duration?: string;
};

// Requests go through the Next.js route handler at /api/query, which proxies to
// the backend over SSE. The browser only talks to this origin (no CORS).
const API_BASE = process.env.NEXT_PUBLIC_API_BASE || "/api";
const JWT = process.env.NEXT_PUBLIC_JWT || "";

const SUGGESTIONS = [
  "系统支持哪些格式的文件？",
  "一次最多能传多大的文件？",
  "文档权限分几级？",
  "知识库缓存是怎么工作的？",
  "搜索结果是怎么排序的？",
  "任务一直失败会怎样？",
];

// Questions the knowledge base cannot answer — the demo shows correct refusal.
const REFUSAL_DEMO = "系统支持语音转文字功能吗？";

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

export default function Home() {
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [sources, setSources] = useState<Source[]>([]);
  const [meta, setMeta] = useState<AnswerMeta>({});
  const [status, setStatus] = useState<"idle" | "streaming" | "done" | "error">("idle");
  const [error, setError] = useState("");
  const [refusalDemo, setRefusalDemo] = useState(false);
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
    setRefusalDemo(false);

    try {
      const resp = await fetch(`${API_BASE}/query`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          ...(JWT ? { Authorization: `Bearer ${JWT}` } : {}),
        },
        body: JSON.stringify({ question: q, top_k: 5 }),
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) throw new Error(`请求失败: ${resp.status}`);

      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, { stream: true });
        let idx;
        while ((idx = buffer.indexOf("\n\n")) >= 0) {
          const event = buffer.slice(0, idx);
          buffer = buffer.slice(idx + 2);
          handleEvent(event);
        }
      }
      setStatus("done");
    } catch (e: unknown) {
      if ((e as Error).name === "AbortError") return;
      setStatus("error");
      setError((e as Error).message || "未知错误");
    }
  }, []);

  const handleEvent = (raw: string) => {
    const lines = raw.split("\n");
    let event = "message";
    let data = "";
    for (const line of lines) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      if (line.startsWith("data:")) data += line.slice(5).trim();
    }
    if (!data) return;
    try {
      const payload = JSON.parse(data);
      switch (event) {
        case "sources":
          setSources(payload.sources || []);
          break;
        case "delta":
          setAnswer((prev) => prev + (payload.text || ""));
          break;
        case "done":
          setMeta({
            duration: payload.duration || "",
            token_usage: payload.token_usage,
            prompt_version: payload.prompt_version,
            retrieval: payload.retrieval,
          });
          break;
        case "error":
          setStatus("error");
          setError(payload.error || "服务错误");
          break;
      }
    } catch {
      // ignore malformed event
    }
  };

  // Non-streaming fetch for refusal demo / metadata (simpler when no SSE needed).
  const askJSON = useCallback(async (q: string) => {
    abortRef.current?.abort();
    setAnswer("");
    setSources([]);
    setMeta({});
    setStatus("streaming");
    setError("");
    setRefusalDemo(false);

    try {
      const resp = await fetch(`${API_BASE}/query`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          ...(JWT ? { Authorization: `Bearer ${JWT}` } : {}),
        },
        body: JSON.stringify({ question: q, top_k: 5 }),
      });
      if (!resp.ok) throw new Error(`请求失败: ${resp.status}`);
      const d = await resp.json();
      setAnswer(d.answer || "");
      setSources(d.sources || []);
      setMeta({
        duration: d.duration || "",
        token_usage: d.token_usage,
        prompt_version: d.prompt_version,
        retrieval: d.retrieval,
      });
      setStatus("done");
    } catch (e: unknown) {
      setStatus("error");
      setError((e as Error).message || "未知错误");
    }
  }, []);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const q = question.trim();
    if (!q || status === "streaming") return;
    void stream(q);
  };

  const onRefusalDemo = () => {
    if (status === "streaming") return;
    setRefusalDemo(true);
    void askJSON(REFUSAL_DEMO);
  };

  useEffect(() => {
    return () => abortRef.current?.abort();
  }, []);

  const isRefusal = answer.includes("未找到相关文档");

  return (
    <main className="min-h-screen bg-slate-50">
      {/* Header */}
      <header className="border-b border-slate-200 bg-white">
        <div className="mx-auto flex max-w-6xl items-center justify-between px-6 py-4">
          <div>
            <h1 className="text-lg font-semibold text-slate-900">AI-ETL Platform</h1>
            <p className="text-xs text-slate-500">
              企业文档知识库 · 检索增强生成 · SSE 流式 · 可观测
            </p>
          </div>
          <span className="rounded-full bg-emerald-50 px-3 py-1 text-xs font-medium text-emerald-700">
            ● 服务在线
          </span>
        </div>
      </header>

      <div className="mx-auto max-w-6xl px-6 py-6">
        {/* Query bar */}
        <form onSubmit={onSubmit} className="flex flex-wrap gap-2">
          <div className="relative flex-1 min-w-[280px]">
            <input
              value={question}
              onChange={(e) => setQuestion(e.target.value)}
              placeholder="输入问题，或从下方快捷问题中选择"
              className="w-full rounded-lg border border-slate-300 bg-white px-4 py-2.5 pr-28 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500"
              disabled={status === "streaming"}
            />
            <div className="absolute right-1.5 top-1/2 flex -translate-y-1/2 gap-1">
              <select
                value=""
                onChange={(e) => {
                  if (e.target.value) {
                    setQuestion(e.target.value);
                    void stream(e.target.value);
                  }
                }}
                className="rounded border border-slate-200 bg-white px-2 py-1 text-xs text-slate-600"
              >
                <option value="">快捷问题 ▾</option>
                {SUGGESTIONS.map((s) => (
                  <option key={s} value={s}>
                    {s}
                  </option>
                ))}
              </select>
            </div>
          </div>
          <button
            type="submit"
            disabled={status === "streaming" || !question.trim()}
            className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {status === "streaming" ? "生成中…" : "提问"}
          </button>
          <button
            type="button"
            onClick={onRefusalDemo}
            disabled={status === "streaming"}
            className="rounded-lg border border-slate-300 bg-white px-4 py-2.5 text-sm text-slate-700 hover:bg-slate-100 disabled:opacity-50"
            title="演示幻觉防护：问一个知识库外的问题"
          >
            拒答演示
          </button>
        </form>

        {/* Result area */}
        {(status === "streaming" || status === "done" || status === "error") && (
          <div className="mt-6 grid gap-6 lg:grid-cols-[1fr_340px]">
            {/* Left: answer + sources */}
            <div className="space-y-4">
              <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
                <div className="mb-2 flex items-center justify-between">
                  <h2 className="text-sm font-semibold text-slate-500">回答</h2>
                  {refusalDemo && (
                    <span className="rounded bg-amber-50 px-2 py-0.5 text-xs text-amber-700">
                      幻觉防护演示
                    </span>
                  )}
                </div>
                {isRefusal ? (
                  <div className="rounded-lg bg-amber-50 p-3 text-sm text-amber-800">
                    <p className="font-medium">未找到相关文档，无法回答该问题。</p>
                    <p className="mt-1 text-xs text-amber-600">
                      系统在检索到任何可支撑的证据前拒绝作答，避免幻觉编造。
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
                  <h2 className="mb-3 text-sm font-semibold text-slate-500">
                    引用来源（{sources.length}）
                  </h2>
                  <div className="space-y-2">
                    {sources.map((s) => (
                      <details key={s.chunk_id} className="group rounded-lg border border-slate-200">
                        <summary className="flex cursor-pointer items-center justify-between px-3 py-2 text-sm hover:bg-slate-50">
                          <span className="font-mono text-blue-600">{s.doc_id}</span>
                          <span className="text-xs text-slate-400">score {s.score.toFixed(4)}</span>
                        </summary>
                        <p className="border-t border-slate-100 px-3 py-2 text-sm text-slate-600">
                          {s.content}
                        </p>
                      </details>
                    ))}
                  </div>
                </div>
              )}

              {status === "error" && (
                <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">
                  {error}
                </div>
              )}
            </div>

            {/* Right: retrieval + meta */}
            <div className="space-y-4">
              <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
                <h2 className="mb-3 text-sm font-semibold text-slate-500">检索链路</h2>
                <dl className="space-y-2 text-sm">
                  <Row label="路由策略" value={meta.retrieval ? (STRATEGY_LABELS[meta.retrieval.strategy] || meta.retrieval.strategy) : "—"} />
                  <Row
                    label="语义缓存"
                    value={
                      meta.retrieval
                        ? meta.retrieval.cache_hit
                          ? "命中（重复提问直接返回）"
                          : "未命中"
                        : "—"
                    }
                    highlight={meta.retrieval?.cache_hit}
                  />
                  <Row label="检索后端" value={meta.retrieval?.backends.join(" + ") || "—"} />
                  <Row label="候选数" value={meta.retrieval ? String(meta.retrieval.candidate_count) : "—"} />
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
                    value={
                      meta.token_usage?.estimated_cost_usd
                        ? formatCost(meta.token_usage.estimated_cost_usd)
                        : "—"
                    }
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
    </main>
  );
}

function Row({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <dt className="shrink-0 text-slate-500">{label}</dt>
      <dd
        className={
          highlight
            ? "truncate text-right font-medium text-emerald-600"
            : "truncate text-right text-slate-700"
        }
      >
        {value}
      </dd>
    </div>
  );
}
