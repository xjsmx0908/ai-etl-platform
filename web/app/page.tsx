"use client";

import { useCallback, useEffect, useRef, useState } from "react";

// ── Types ────────────────────────────────────────────────────────────────

type Source = { doc_id: string; chunk_id: string; content: string; score: number };
type RetrievalInfo = {
  strategy: string;
  cache_hit: boolean;
  backends: string[];
  candidate_count: number;
  duration_ms: number;
};
type TokenUsage = { prompt_tokens: number; completion_tokens: number; estimated_cost_usd?: number };
type AnswerMeta = {
  retrieval?: RetrievalInfo;
  token_usage?: TokenUsage;
  prompt_version?: string;
  duration?: string;
};
type HealthService = { status: string; latency_ms: number };
type SessionStats = { queries: number; tokens: number; cost: number; cacheHits: number };

const API_BASE = process.env.NEXT_PUBLIC_API_BASE || "/api";
const JWT = process.env.NEXT_PUBLIC_JWT || "";

// Verified to retrieve the seeded corpus (real-model eval hits).
const SUGGESTIONS = [
  "系统支持哪些格式的文件？",
  "一次最多能传多大的文件？",
  "文档的可见范围有哪几档？",
  "同样的问题反复问，会不会每次都重新算一遍？",
  "上传文档时可以附加业务标签吗？",
  "批量导入既有资料有办法吗？",
];
const REFUSAL_DEMO = "系统支持语音转文字功能吗？";
const STRATEGY_LABELS: Record<string, string> = {
  exact_keyword: "精确关键词",
  semantic: "语义检索",
  hybrid: "混合检索（语义 + 关键词）",
};

const TAB_QA = "qa";
const TAB_OBSERVE = "observe";
const TAB_QUALITY = "quality";
const TAB_AGENT = "agent";
const TAB_DATA = "data";

// Static quality data (source: docs/evals/reports, real-model evaluation).
const QUALITY_DATA = {
  recall: [
    { k: 1, label: "Recall@1", value: 19, note: "目标文档排第 1 的比例" },
    { k: 3, label: "Recall@3", value: 32, note: "目标在前 3 名" },
    { k: 5, label: "Recall@5", value: 64, note: "目标在前 5 名（可找到）" },
  ],
  noiseFloor: 4.26,
  experiments: [
    {
      title: "ES 混合检索",
      verdict: "保留",
      detail: "关闭 ES 后 pass_rate 52% → 开 ES 69%（+17pp）",
      good: true,
    },
    {
      title: "英文 reranker",
      verdict: "关闭",
      detail: "ms-marco（英文）对中文 0 例受益、2 例受害，69% → 74%",
      good: true,
    },
    {
      title: "评测方法论",
      verdict: "语义集",
      detail: "锚点集假 100% 掩盖真实能力，语义集 Recall@1 仅 19%",
      good: true,
    },
  ],
};

function formatCost(usd: number): string {
  if (usd >= 0.01) return `$${usd.toFixed(3)}`;
  if (usd >= 0.0001) return `$${usd.toFixed(5)}`;
  return `$${usd.toFixed(7)}`;
}

export default function Home() {
  const [tab, setTab] = useState(TAB_QA);
  const [stats, setStats] = useState<SessionStats>({ queries: 0, tokens: 0, cost: 0, cacheHits: 0 });

  const onQueryDone = useCallback((token_usage: TokenUsage | undefined, cacheHit: boolean) => {
    setStats((prev) => ({
      queries: prev.queries + 1,
      tokens: prev.tokens + (token_usage?.prompt_tokens || 0) + (token_usage?.completion_tokens || 0),
      cost: prev.cost + (token_usage?.estimated_cost_usd || 0),
      cacheHits: prev.cacheHits + (cacheHit ? 1 : 0),
    }));
  }, []);

  return (
    <main className="min-h-screen bg-slate-50">
      <Header />
      <TabNav active={tab} onChange={setTab} />
      <div className="mx-auto max-w-6xl px-6 py-6">
        {tab === TAB_QA && <QaWorkspace onQueryDone={onQueryDone} />}
        {tab === TAB_OBSERVE && <ObservePanel stats={stats} />}
        {tab === TAB_QUALITY && <QualityPanel />}
        {tab === TAB_AGENT && <AgentPanel />}
        {tab === TAB_DATA && <DataPanel />}
      </div>
    </main>
  );
}

// ── Header & Nav ─────────────────────────────────────────────────────────

function Header() {
  return (
    <header className="border-b border-slate-200 bg-white">
      <div className="mx-auto flex max-w-6xl items-center justify-between px-6 py-3.5">
        <div>
          <h1 className="text-base font-semibold text-slate-900">AI-ETL Platform</h1>
          <p className="text-xs text-slate-500">企业文档知识库 · RAG · 可观测 · Agent</p>
        </div>
        <span className="rounded-full bg-emerald-50 px-3 py-1 text-xs font-medium text-emerald-700">
          ● 服务在线
        </span>
      </div>
    </header>
  );
}

function TabNav({ active, onChange }: { active: string; onChange: (t: string) => void }) {
  const items = [
    { id: TAB_QA, label: "问答工作台" },
    { id: TAB_AGENT, label: "Agent 编排" },
    { id: TAB_DATA, label: "数据接入" },
    { id: TAB_OBSERVE, label: "系统可观测" },
    { id: TAB_QUALITY, label: "检索质量" },
  ];
  return (
    <nav className="border-b border-slate-200 bg-white">
      <div className="mx-auto flex max-w-6xl gap-1 px-6">
        {items.map((it) => (
          <button
            key={it.id}
            onClick={() => onChange(it.id)}
            className={
              active === it.id
                ? "border-b-2 border-blue-600 px-4 py-2.5 text-sm font-medium text-blue-600"
                : "border-b-2 border-transparent px-4 py-2.5 text-sm text-slate-500 hover:text-slate-800"
            }
          >
            {it.label}
          </button>
        ))}
      </div>
    </nav>
  );
}

// ── Tab: Q&A Workspace ───────────────────────────────────────────────────

function QaWorkspace({ onQueryDone }: { onQueryDone: (tu: TokenUsage | undefined, cacheHit: boolean) => void }) {
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
          onQueryDone(payload.token_usage, payload.retrieval?.cache_hit);
          break;
        case "error":
          setStatus("error");
          setError(payload.error || "服务错误");
          break;
      }
    } catch {
      // ignore
    }
  };

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
        headers: { "Content-Type": "application/json", ...(JWT ? { Authorization: `Bearer ${JWT}` } : {}) },
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
      onQueryDone(d.token_usage, d.retrieval?.cache_hit);
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
    <div className="space-y-6">
      <form onSubmit={onSubmit} className="flex flex-wrap gap-2">
        <div className="relative min-w-[280px] flex-1">
          <input
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder="输入问题，或从右侧快捷问题中选择"
            className="w-full rounded-lg border border-slate-300 bg-white px-4 py-2.5 pr-32 text-sm focus:border-blue-500 focus:outline-none focus:ring-1 focus:ring-blue-500"
            disabled={status === "streaming"}
          />
          <div className="absolute right-1.5 top-1/2 flex -translate-y-1/2">
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

      {(status === "streaming" || status === "done" || status === "error") && (
        <div className="grid gap-6 lg:grid-cols-[1fr_340px]">
          <div className="space-y-4">
            <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
              <div className="mb-2 flex items-center justify-between">
                <h2 className="text-sm font-semibold text-slate-500">回答</h2>
                {refusalDemo && (
                  <span className="rounded bg-amber-50 px-2 py-0.5 text-xs text-amber-700">幻觉防护演示</span>
                )}
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
                <Row label="路由策略" value={meta.retrieval ? STRATEGY_LABELS[meta.retrieval.strategy] || meta.retrieval.strategy : "—"} />
                <Row label="语义缓存" value={meta.retrieval ? (meta.retrieval.cache_hit ? "命中" : "未命中") : "—"} highlight={meta.retrieval?.cache_hit} />
                <Row label="检索后端" value={meta.retrieval?.backends.join(" + ") || "—"} />
                <Row label="候选数" value={meta.retrieval ? String(meta.retrieval.candidate_count) : "—"} />
                <Row label="检索耗时" value={meta.retrieval ? `${meta.retrieval.duration_ms}ms` : "—"} />
              </dl>
            </div>
            <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
              <h2 className="mb-3 text-sm font-semibold text-slate-500">本次查询</h2>
              <dl className="space-y-2 text-sm">
                <Row label="Token" value={meta.token_usage ? `${meta.token_usage.prompt_tokens} in / ${meta.token_usage.completion_tokens} out` : "—"} />
                <Row label="估算成本" value={meta.token_usage?.estimated_cost_usd ? formatCost(meta.token_usage.estimated_cost_usd) : "—"} highlight={Boolean(meta.token_usage?.estimated_cost_usd)} />
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

// ── Tab: Observability ───────────────────────────────────────────────────

function ObservePanel({ stats }: { stats: SessionStats }) {
  const [health, setHealth] = useState<Record<string, HealthService> | null>(null);

  useEffect(() => {
    let cancelled = false;
    const load = async () => {
      try {
        const resp = await fetch(`${API_BASE}/system/health`, {
          headers: JWT ? { Authorization: `Bearer ${JWT}` } : {},
        });
        if (!resp.ok) return;
        const d = await resp.json();
        if (!cancelled) setHealth(d.services);
      } catch {
        // ignore
      }
    };
    void load();
    const id = setInterval(load, 10000);
    return () => {
      cancelled = true;
      clearInterval(id);
    };
  }, []);

  const overall = health
    ? Object.values(health).every((h) => h.status === "up")
      ? "up"
      : "degraded"
    : "loading";

  return (
    <div className="space-y-6">
      <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
        <StatCard label="会话查询次数" value={String(stats.queries)} />
        <StatCard label="累计 Token" value={stats.tokens.toLocaleString()} />
        <StatCard label="累计成本" value={formatCost(stats.cost)} accent />
        <StatCard label="缓存命中" value={String(stats.cacheHits)} />
      </div>

      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <div className="mb-3 flex items-center justify-between">
          <h2 className="text-sm font-semibold text-slate-500">服务健康</h2>
          <span
            className={
              overall === "up"
                ? "rounded bg-emerald-50 px-2 py-0.5 text-xs text-emerald-700"
                : "rounded bg-red-50 px-2 py-0.5 text-xs text-red-700"
            }
          >
            {overall === "up" ? "全部在线" : "有服务异常"}
          </span>
        </div>
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {health
            ? Object.entries(health).map(([name, h]) => (
                <div key={name} className="flex items-center justify-between rounded-lg border border-slate-200 px-3 py-2">
                  <span className="font-mono text-xs text-slate-700">{name}</span>
                  <span className="flex items-center gap-1.5">
                    <span className={`h-2 w-2 rounded-full ${h.status === "up" ? "bg-emerald-500" : h.status === "degraded" ? "bg-amber-500" : "bg-red-500"}`} />
                    <span className="text-xs text-slate-400">{h.latency_ms}ms</span>
                  </span>
                </div>
              ))
            : "加载中…"}
        </div>
        <p className="mt-3 text-xs text-slate-400">
          经 query-api 聚合各服务 healthz · 会话指标由前端累计每次查询的真实 token 与成本
        </p>
      </div>
    </div>
  );
}

function StatCard({ label, value, accent }: { label: string; value: string; accent?: boolean }) {
  return (
    <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
      <p className="text-xs text-slate-500">{label}</p>
      <p className={accent ? "mt-1 text-xl font-semibold text-emerald-600" : "mt-1 text-xl font-semibold text-slate-800"}>
        {value}
      </p>
    </div>
  );
}

// ── Tab: Retrieval Quality ───────────────────────────────────────────────

function QualityPanel() {
  return (
    <div className="space-y-6">
      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <h2 className="mb-1 text-sm font-semibold text-slate-500">真实模型语义检索（nomic-embed-text 768d）</h2>
        <p className="mb-4 text-xs text-slate-400">44 例语义数据集 · query 口语化改写 · 来源 docs/evals</p>
        <div className="flex items-end gap-6">
          {QUALITY_DATA.recall.map((r) => (
            <div key={r.k} className="flex flex-col items-center">
              <div className="relative flex h-40 w-14 items-end overflow-hidden rounded-lg bg-slate-100">
                <div
                  className="w-full rounded-b-lg bg-blue-500"
                  style={{ height: `${r.value}%` }}
                />
                <span className="absolute inset-x-0 top-1 text-center text-xs font-bold text-slate-700">
                  {r.value}%
                </span>
              </div>
              <span className="mt-2 text-xs font-medium text-slate-600">{r.label}</span>
            </div>
          ))}
          <div className="ml-4 max-w-[260px] text-xs text-slate-500">
            <p className="font-medium text-slate-600">为什么 Recall@1 只有 19%？</p>
            <p className="mt-1">
              锚点数据集把 Recall 虚高到 86%（测的是关键词匹配）；换真实语义集后，目标文档进入前 5 名
              的比例是 64%——这是系统真实水平，也指向改进方向（中文 embedding、query 改写）。
            </p>
          </div>
        </div>
      </div>

      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <h2 className="mb-3 text-sm font-semibold text-slate-500">用实验数据做的配置决策</h2>
        <div className="grid gap-3 sm:grid-cols-3">
          {QUALITY_DATA.experiments.map((e) => (
            <div key={e.title} className="rounded-lg border border-slate-200 p-3">
              <div className="flex items-center justify-between">
                <span className="text-sm font-medium text-slate-700">{e.title}</span>
                <span className="rounded bg-emerald-50 px-2 py-0.5 text-xs font-medium text-emerald-700">{e.verdict}</span>
              </div>
              <p className="mt-2 text-xs text-slate-500">{e.detail}</p>
            </div>
          ))}
        </div>
        <p className="mt-3 text-xs text-slate-400">
          判定标准：同配置连跑 3 轮，pass_rate 自然波动 {QUALITY_DATA.noiseFloor}%——差异低于该值不算真实改进
        </p>
      </div>
    </div>
  );
}

// ── Tab: Agent Orchestration ──────────────────────────────────────────────

type AgentStep = {
  index: number;
  type: string; // tool_call | final | error
  state: string;
  thought?: string;
  tool_name?: string;
  tool_arguments?: unknown;
  observation?: string;
  error?: string;
  duration?: number;
};
type AgentRun = {
  id: string;
  task: string;
  state: string;
  final?: string;
  error?: string;
  steps: AgentStep[];
};

const AGENT_SUGGESTIONS = [
  "查询知识库：文档权限分几级？",
  "查询知识库：一次最多能传多大的文件？",
];

const STEP_TYPE_LABELS: Record<string, string> = {
  tool_call: "工具调用",
  final: "生成结论",
  error: "错误",
};

function AgentPanel() {
  const [task, setTask] = useState("");
  const [run, setRun] = useState<AgentRun | null>(null);
  const [status, setStatus] = useState<"idle" | "running" | "done" | "error">("idle");
  const [error, setError] = useState("");

  const execute = async (t: string) => {
    setStatus("running");
    setRun(null);
    setError("");
    try {
      const resp = await fetch(`${API_BASE}/agent/runs`, {
        method: "POST",
        headers: { "Content-Type": "application/json", ...(JWT ? { Authorization: `Bearer ${JWT}` } : {}) },
        body: JSON.stringify({ task: t, auto_execute: true }),
      });
      if (!resp.ok) throw new Error(`请求失败: ${resp.status}`);
      const d = (await resp.json()) as AgentRun;
      setRun(d);
      setStatus("done");
    } catch (e: unknown) {
      setStatus("error");
      setError((e as Error).message || "未知错误");
    }
  };

  return (
    <div className="space-y-6">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (task.trim() && status !== "running") void execute(task.trim());
        }}
        className="flex flex-wrap gap-2"
      >
        <input
          value={task}
          onChange={(e) => setTask(e.target.value)}
          placeholder="给 Agent 一个任务，例如：查询知识库中的文档权限说明"
          className="min-w-[280px] flex-1 rounded-lg border border-slate-300 bg-white px-4 py-2.5 text-sm focus:border-blue-500 focus:outline-none"
          disabled={status === "running"}
        />
        <button
          type="submit"
          disabled={status === "running" || !task.trim()}
          className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {status === "running" ? "执行中…" : "执行"}
        </button>
        <div className="flex gap-1">
          {AGENT_SUGGESTIONS.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => void execute(s)}
              disabled={status === "running"}
              className="rounded-lg border border-slate-300 bg-white px-3 py-2.5 text-xs text-slate-600 hover:bg-slate-100 disabled:opacity-50"
            >
              {s.replace("查询知识库：", "")}
            </button>
          ))}
        </div>
      </form>

      {status === "done" && run && (
        <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="text-sm font-semibold text-slate-500">Agent Run · {run.id}</h2>
            <span
              className={
                run.state === "completed"
                  ? "rounded bg-emerald-50 px-2 py-0.5 text-xs text-emerald-700"
                  : run.state === "failed"
                    ? "rounded bg-red-50 px-2 py-0.5 text-xs text-red-700"
                    : "rounded bg-amber-50 px-2 py-0.5 text-xs text-amber-700"
              }
            >
              {run.state}
            </span>
          </div>

          <div className="space-y-0">
            {run.steps.map((step) => (
              <div key={step.index} className="relative flex gap-3 pb-5">
                <div className="flex flex-col items-center">
                  <span
                    className={
                      step.type === "final"
                        ? "flex h-6 w-6 items-center justify-center rounded-full bg-emerald-100 text-xs"
                        : step.type === "error"
                          ? "flex h-6 w-6 items-center justify-center rounded-full bg-red-100 text-xs"
                          : "flex h-6 w-6 items-center justify-center rounded-full bg-blue-100 text-xs"
                    }
                  >
                    {step.type === "final" ? "✓" : step.type === "error" ? "✗" : step.index}
                  </span>
                  {step.index < run.steps.length && <span className="mt-1 w-px flex-1 bg-slate-200" />}
                </div>
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium text-slate-700">
                      {STEP_TYPE_LABELS[step.type] || step.type}
                    </span>
                    {step.tool_name && (
                      <span className="rounded bg-slate-100 px-2 py-0.5 font-mono text-xs text-blue-700">
                        {step.tool_name}
                      </span>
                    )}
                    {step.duration ? <span className="text-xs text-slate-400">{step.duration}ms</span> : null}
                  </div>
                  {step.thought && <p className="mt-1 text-xs text-slate-500">思考：{step.thought}</p>}
                  {step.observation && (
                    <p className="mt-1 line-clamp-3 text-sm text-slate-600">结果：{step.observation}</p>
                  )}
                  {step.error && <p className="mt-1 text-xs text-red-600">{step.error}</p>}
                </div>
              </div>
            ))}
          </div>

          {run.final && (
            <div className="mt-2 rounded-lg bg-slate-50 p-3">
              <p className="text-xs text-slate-500">最终回答</p>
              <p className="mt-1 text-sm text-slate-800">{run.final}</p>
            </div>
          )}
          {run.error && !run.final && <p className="mt-2 text-sm text-red-600">Run 失败：{run.error}</p>}
        </div>
      )}

      {status === "error" && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">{error}</div>
      )}
    </div>
  );
}

// ── Tab: Data Ingestion ──────────────────────────────────────────────────

type TaskStatus = {
  task_id: string;
  doc_id: string;
  status: string; // queued | processing | completed | failed
  stage?: string;
  error?: string;
  file_path?: string;
};

const PIPELINE_STEPS = [
  { key: "queued", label: "上传 · Kafka 异步" },
  { key: "parsing", label: "解析 · parser-service" },
  { key: "embedding", label: "向量化 · embedding" },
  { key: "completed", label: "入库 · Qdrant + ES" },
];

function DataPanel() {
  const [file, setFile] = useState<File | null>(null);
  const [permission, setPermission] = useState("internal");
  const [status, setStatus] = useState<"idle" | "uploading" | "polling" | "done" | "error">("idle");
  const [uploadResult, setUploadResult] = useState<{ doc_id?: string; task_id?: string } | null>(null);
  const [taskStatus, setTaskStatus] = useState<TaskStatus | null>(null);
  const [error, setError] = useState("");
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  useEffect(() => {
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, []);

  const stopPolling = () => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  };

  const upload = async () => {
    if (!file) return;
    stopPolling();
    setStatus("uploading");
    setUploadResult(null);
    setTaskStatus(null);
    setError("");
    try {
      const fd = new FormData();
      fd.append("file", file);
      fd.append("doc_id", `demo-${Date.now()}`);
      fd.append("tenant_id", "demo");
      fd.append("permission", permission);
      const resp = await fetch(`${API_BASE}/upload`, {
        method: "POST",
        headers: JWT ? { Authorization: `Bearer ${JWT}` } : {},
        body: fd,
      });
      const d = await resp.json();
      if (!resp.ok) throw new Error(d.error || `上传失败: ${resp.status}`);
      setUploadResult({ doc_id: d.doc_id, task_id: d.task_id });
      setStatus("polling");
      poll(d.doc_id);
    } catch (e: unknown) {
      setStatus("error");
      setError((e as Error).message || "上传失败");
    }
  };

  const poll = (docId: string) => {
    pollRef.current = setInterval(async () => {
      try {
        const resp = await fetch(`${API_BASE}/tasks/${docId}`, {
          headers: JWT ? { Authorization: `Bearer ${JWT}` } : {},
        });
        if (!resp.ok) return;
        const d = (await resp.json()) as TaskStatus;
        setTaskStatus(d);
        if (d.status === "completed" || d.status === "failed") {
          stopPolling();
          setStatus("done");
        }
      } catch {
        // keep polling
      }
    }, 2000);
  };

  const currentStep = taskStatus ? taskStatus.status : status === "uploading" ? "queued" : "";
  const failed = taskStatus?.status === "failed";

  return (
    <div className="space-y-6">
      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <h2 className="mb-3 text-sm font-semibold text-slate-500">上传文档 · 展示异步 ETL 流程</h2>
        <div className="flex flex-wrap items-center gap-3">
          <input
            type="file"
            accept=".txt,.md,.pdf,.docx"
            onChange={(e) => setFile(e.target.files?.[0] || null)}
            className="text-sm text-slate-600 file:mr-3 file:rounded-lg file:border-0 file:bg-blue-50 file:px-4 file:py-2 file:text-sm file:font-medium file:text-blue-700 hover:file:bg-blue-100"
          />
          <select
            value={permission}
            onChange={(e) => setPermission(e.target.value)}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700"
          >
            <option value="internal">internal</option>
            <option value="public">public</option>
          </select>
          <button
            onClick={upload}
            disabled={!file || status === "uploading" || status === "polling"}
            className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {status === "uploading" || status === "polling" ? "上传中…" : "上传"}
          </button>
          {uploadResult && (
            <span className="font-mono text-xs text-slate-500">
              doc: {uploadResult.doc_id}
            </span>
          )}
        </div>

        <div className="mt-5 flex items-center gap-2">
          {PIPELINE_STEPS.map((step, i) => {
            const active =
              (step.key === "queued" && currentStep === "queued") ||
              (step.key === "parsing" && currentStep === "processing") ||
              (step.key === "embedding" && currentStep === "processing") ||
              (step.key === "completed" && currentStep === "completed");
            const done = currentStep === "completed" || (currentStep === "processing" && i < 2) || (currentStep === "failed" && i < 2);
            return (
              <div key={step.key} className="flex flex-1 items-center gap-2">
                <div className={`flex-1 rounded-lg border px-3 py-2 text-center text-xs ${active ? "border-blue-500 bg-blue-50 text-blue-700" : done ? "border-emerald-200 bg-emerald-50 text-emerald-700" : failed ? "border-red-200 bg-red-50 text-red-700" : "border-slate-200 bg-white text-slate-500"}`}>
                  {step.label}
                </div>
                {i < PIPELINE_STEPS.length - 1 && <span className="text-slate-300">→</span>}
              </div>
            );
          })}
        </div>

        {taskStatus && (
          <div className="mt-4 rounded-lg bg-slate-50 p-3 text-xs text-slate-600">
            <span className="font-medium text-slate-700">状态：{taskStatus.status}</span>
            {taskStatus.stage && <span className="ml-2">阶段：{taskStatus.stage}</span>}
            {taskStatus.error && <span className="ml-2 text-red-600">{taskStatus.error}</span>}
          </div>
        )}
        {error && <div className="mt-3 text-sm text-red-600">{error}</div>}
      </div>

      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <h2 className="mb-2 text-sm font-semibold text-slate-500">为什么这是可靠的 ETL？</h2>
        <ul className="space-y-1 text-xs text-slate-600">
          <li>· 上传后立即返回任务编号，后台异步处理（Kafka 削峰解耦）</li>
          <li>· 处理失败自动重试，重试耗尽进 DLQ，不丢消息</li>
          <li>· 相同幂等键重复上传复用第一次结果</li>
          <li>· 每个任务记录状态，可追溯处理阶段</li>
        </ul>
      </div>
    </div>
  );
}

function Row({ label, value, highlight }: { label: string; value: string; highlight?: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <dt className="shrink-0 text-slate-500">{label}</dt>
      <dd className={highlight ? "truncate text-right font-medium text-emerald-600" : "truncate text-right text-slate-700"}>{value}</dd>
    </div>
  );
}
