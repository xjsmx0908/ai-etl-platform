"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import Link from "next/link";
import { Card } from "@/components/ui/Card";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { AlertTriangle, Check, ChevronDown, Loader2, MessageSquareText, Send, ShieldAlert, ShieldCheck, X } from "lucide-react";
import { useAuth } from "@/lib/auth";
import type { AnswerMeta, KnowledgeSpace, Source } from "@/lib/types";
import { EMPTY_QUERYABLE_SPACES_MESSAGE, preferredKnowledgeSpace, queryableKnowledgeSpaces } from "@/lib/qaSpaces";

const STRATEGY_LABELS: Record<string, string> = {
  exact_keyword: "精确关键词",
  semantic: "语义检索",
  hybrid: "混合检索（语义 + 关键词）",
};

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

const QUERY_STEPS = [
  { key: "preparing", label: "确认范围" },
  { key: "retrieving", label: "检索文档" },
  { key: "screening", label: "筛选证据" },
  { key: "generating", label: "生成回答" },
  { key: "verifying", label: "校验引用" },
  { key: "finalizing", label: "整理结果" },
];

const PERMISSION_LABELS: Record<string, string> = {
  public: "公开",
  internal: "内部",
  confidential: "机密",
};

// The permission boundary applied to this query's retrieval. When the role
// cannot see confidential docs, the boundary itself is the isolation proof:
// confidential documents are filtered at the source and never reach candidates.
function permissionBoundaryLabel(retrieval?: AnswerMeta["retrieval"]): string {
  if (!retrieval || !retrieval.allowed_permissions?.length) return "—";
  const perms = retrieval.allowed_permissions.map((p) => PERMISSION_LABELS[p] || p).join(" + ");
  return `${ROLE_LABELS[retrieval.permission_role || ""] || retrieval.permission_role || "未知角色"} · ${perms}`;
}

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
  if (retrieval.grounding_unavailable) return { value: "不可用（回答未经校验）", highlight: true };
  if (!retrieval.grounding_checked) return { value: "未触发", highlight: false };
  return retrieval.grounding_passed
    ? { value: "通过（可溯源）", highlight: true }
    : { value: "拦截（无据拒答）", highlight: true };
}

function exactEvidenceLabel(retrieval?: AnswerMeta["retrieval"]): { value: string; highlight: boolean } {
  if (!retrieval) return { value: "—", highlight: false };
  if (!retrieval.exact_evidence_required) return { value: "不需要", highlight: false };
  return retrieval.exact_evidence_matched
    ? { value: "通过", highlight: true }
    : { value: "未通过（已拒答）", highlight: false };
}

// Corpus governance applied to this answer's evidence: superseded/archived
// documents dropped after retrieval (their chunks stay indexed, so the filter
// runs against the registry), plus any conflict disclosed above.
function governanceLabel(retrieval?: AnswerMeta["retrieval"]): string {
  if (!retrieval) return "—";
  const parts: string[] = [];
  if ((retrieval.retired_filtered ?? 0) > 0) parts.push(`剔除作废 ${retrieval.retired_filtered} 条`);
  if ((retrieval.unpublished_filtered ?? 0) > 0) parts.push(`剔除未发布 ${retrieval.unpublished_filtered} 条`);
  if (retrieval.conflict_detected) parts.push(`冲突 ${retrieval.conflicting_docs?.length ?? 0} 份`);
  return parts.length > 0 ? parts.join(" · ") : "无异常";
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
  const { role } = useAuth();
  const [question, setQuestion] = useState("");
  const [knowledgeSpace, setKnowledgeSpace] = useState("");
  const [knowledgeSpaces, setKnowledgeSpaces] = useState<KnowledgeSpace[]>([]);
  const [answer, setAnswer] = useState("");
  const [citations, setCitations] = useState<Source[]>([]);
  const [meta, setMeta] = useState<AnswerMeta>({});
  const [status, setStatus] = useState<"idle" | "streaming" | "done" | "error">("idle");
  const [phase, setPhase] = useState("等待提交");
  const [activeStage, setActiveStage] = useState("");
  const [completedStages, setCompletedStages] = useState<string[]>([]);
  const [error, setError] = useState("");
  const abortRef = useRef<AbortController | null>(null);
  const selectedSpace = knowledgeSpaces.find((space) => space.id === knowledgeSpace);
  const hasQueryableSpaces = knowledgeSpaces.length > 0;

  const stream = useCallback(async (q: string) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setAnswer("");
    setCitations([]);
    setMeta({});
    setStatus("streaming");
    setPhase("正在检索知识库…");
    setActiveStage("preparing");
    setCompletedStages([]);
    setError("");
    let failed = false;
    try {
      await apiClient.querySSE(
        q,
        {
          onStatus: (progress) => {
            setPhase(progress.message);
            setActiveStage(progress.stage);
            if (progress.state === "completed" && QUERY_STEPS.some((step) => step.key === progress.stage)) {
              setCompletedStages((previous) => previous.includes(progress.stage) ? previous : [...previous, progress.stage]);
            }
          },
          onSources: (s) => setCitations(s),
          onDelta: (text) => setAnswer((prev) => prev + text),
          onReplace: (text) => setAnswer(text),
          onDone: (m) => {
            if (typeof m.answer === "string") setAnswer(m.answer);
            if (m.citations) setCitations(m.citations);
            else if (m.sources) setCitations(m.sources);
            setMeta(m);
            setStatus("done");
            setActiveStage("completed");
            setCompletedStages(QUERY_STEPS.map((step) => step.key));
            setPhase("回答已完成");
          },
          onError: (msg) => {
            failed = true;
            setStatus("error");
            setError(msg);
          },
        },
        controller.signal,
        knowledgeSpace ? { knowledge_space_id: knowledgeSpace } : undefined
      );
      if (failed) return;
      setStatus("done");
      setActiveStage("completed");
      setCompletedStages(QUERY_STEPS.map((step) => step.key));
      setPhase("回答已完成");
    } catch (e: unknown) {
      if ((e as Error).name === "AbortError") return;
      setStatus("error");
      setActiveStage("failed");
      setPhase("处理失败");
      setError((e as Error).message || "未知错误");
    }
  }, [knowledgeSpace]);

  useEffect(() => {
    void apiClient.listKnowledgeSpaces().then(({ items }) => {
      const spaces = queryableKnowledgeSpaces(items);
      setKnowledgeSpaces(spaces);
      const preferred = preferredKnowledgeSpace(spaces);
      if (preferred) setKnowledgeSpace(preferred.id);
    }).catch((e: Error) => setError(e.message || "知识空间加载失败"));
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
      <PageHeader icon={MessageSquareText} title="问答工作台" description="基于已发布的企业文档生成可追溯回答" />

      <form onSubmit={onSubmit} className="rounded-xl border border-slate-200 bg-white p-3 shadow-sm">
        <div className="flex flex-col gap-2 sm:flex-row">
          <input
            value={question}
            onChange={(e) => setQuestion(e.target.value)}
            placeholder="输入你的问题，例如：DNQ4/V30 用户手册的生产厂家是谁？"
            className="min-w-0 flex-1 rounded-lg border border-transparent bg-slate-50 px-4 py-2.5 text-sm text-slate-800 outline-none transition placeholder:text-slate-400 focus:border-blue-400 focus:bg-white focus:ring-2 focus:ring-blue-500/10"
            disabled={status === "streaming"}
          />
          {hasQueryableSpaces && (
            <select
              value={knowledgeSpace}
              onChange={(e) => setKnowledgeSpace(e.target.value)}
              disabled={status === "streaming"}
              aria-label="知识空间"
              className="rounded-lg border border-slate-300 bg-white px-3 py-2.5 text-sm text-slate-700 transition focus:border-blue-500 focus:outline-none focus:ring-2 focus:ring-blue-500/15 disabled:bg-slate-50"
            >
              {knowledgeSpaces.map((space) => (
                <option key={space.id} value={space.id}>{space.name}{space.kind === "demo" ? "（演示）" : ""}</option>
              ))}
            </select>
          )}
          <Button
            type="submit"
            disabled={status === "streaming" || !question.trim() || !hasQueryableSpaces}
            icon={status === "streaming" ? undefined : Send}
            loading={status === "streaming"}
          >
            {status === "streaming" ? "提问中…" : "发送"}
          </Button>
        </div>
        {selectedSpace && (
          <p className="mt-2 px-1 text-xs text-slate-400">当前检索范围：{selectedSpace.name} · 仅使用该空间中已发布的文档</p>
        )}
        {!hasQueryableSpaces && (
          <p className="mt-2 px-1 text-xs text-amber-700">{EMPTY_QUERYABLE_SPACES_MESSAGE}</p>
        )}
      </form>

      {(role === "admin" || role === "user" || role === "readonly") && (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1 rounded-xl border border-slate-200 bg-white px-4 py-2.5 text-xs text-slate-500">
          <ShieldCheck className="h-3.5 w-3.5 text-slate-400" />
          <span>
            检索边界：<span className="font-medium text-slate-700">{ROLE_LABELS[role] || role}</span>
          </span>
          <span className="text-slate-300">|</span>
          <span>
            可见范围：
            <span className="font-medium text-slate-700">
              {role === "admin" ? "公开 + 内部 + 机密" : role === "user" ? "公开 + 内部" : "公开"}
            </span>
          </span>
          {role !== "admin" && (
            <span className="text-amber-600">机密文档在检索源头即被过滤，不会进入回答素材</span>
          )}
        </div>
      )}

      {(status === "streaming" || status === "done" || status === "error") && (
        <div className="grid gap-6 lg:grid-cols-[1fr_340px]">
          <div className="space-y-4">
            <Card className="border-slate-200/80 bg-white/95">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <h2 className="text-sm font-semibold text-slate-700">处理进度</h2>
                  <p className="mt-0.5 text-xs text-slate-400">每个阶段完成后自动进入下一步，耗时阶段会持续显示动画</p>
                </div>
                {status === "streaming" && <span className="inline-flex items-center gap-1.5 text-xs text-blue-600"><Loader2 className="h-3.5 w-3.5 animate-spin" />处理中</span>}
                {status === "done" && <span className="inline-flex items-center gap-1.5 text-xs text-emerald-600"><Check className="h-3.5 w-3.5" />回答已完成</span>}
              </div>
              <div className="mt-4 grid grid-cols-2 gap-2 sm:grid-cols-3 lg:grid-cols-6">
                {QUERY_STEPS.map((step) => {
                  const done = completedStages.includes(step.key);
                  const active = activeStage === step.key && status === "streaming";
                  return <div key={step.key} className={`flex items-center gap-2 rounded-lg border px-2.5 py-2 text-xs ${done ? "border-emerald-200 bg-emerald-50 text-emerald-700" : active ? "border-blue-200 bg-blue-50 text-blue-700" : "border-slate-200 bg-slate-50 text-slate-400"}`}>
                    <span className="flex h-5 w-5 shrink-0 items-center justify-center rounded-full bg-white/80">{done ? <Check className="h-3.5 w-3.5" /> : active ? <Loader2 className="h-3.5 w-3.5 animate-spin" /> : <span className="h-1.5 w-1.5 rounded-full bg-current" />}</span>
                    {step.label}
                  </div>;
                })}
              </div>
              {activeStage === "refused" && <div className="mt-3 flex items-center gap-2 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-700"><X className="h-4 w-4" />{phase}</div>}
              {status === "error" && <div className="mt-3 rounded-lg bg-red-50 px-3 py-2 text-xs text-red-700">{phase}</div>}
            </Card>
            {/* Conflicting sources are disclosed, never adjudicated: the system
                does not decide which document is right, it shows both so a human
                can. Placed above the answer because it qualifies everything below. */}
            {meta.retrieval?.conflict_detected && (meta.retrieval.conflicting_docs?.length ?? 0) > 0 && (
              <div className="rounded-xl border border-amber-300 bg-amber-50 p-4">
                <div className="flex items-start gap-2">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600" />
                  <div className="flex-1">
                    <p className="text-sm font-medium text-amber-800">
                      检索到 {meta.retrieval.conflicting_docs?.length} 份文档对此描述可能不一致
                    </p>
                    <p className="mt-1 text-xs text-amber-700">
                      回答可能只采用了其中一份。请核对下列来源，以现行版本为准。
                    </p>
                    <ul className="mt-2 space-y-1">
                      {meta.retrieval.conflicting_docs?.map((c) => (
                        <li key={c.doc_id} className="flex flex-wrap items-center gap-x-2 text-xs text-amber-800">
                          <Link
                            href={`/documents/${encodeURIComponent(c.doc_id)}`}
                            className="font-medium hover:underline"
                          >
                            {c.file_name || c.doc_id}
                          </Link>
                          {c.file_name && <span className="font-mono text-[11px] text-amber-600">{c.doc_id}</span>}
                          {c.effective_date && <span className="text-amber-600">生效 {c.effective_date}</span>}
                          {c.supersedes && (
                            <span className="text-amber-600">声明替代 {c.supersedes}</span>
                          )}
                        </li>
                      ))}
                    </ul>
                  </div>
                </div>
              </div>
            )}

            <Card>
              <div className="mb-2 flex items-center justify-between">
                <h2 className="text-sm font-semibold text-slate-500">回答</h2>
              </div>
              {isRefusal ? (
                <div className="flex items-start gap-2 rounded-lg bg-amber-50 p-3 text-sm text-amber-800">
                  <ShieldAlert className="mt-0.5 h-4 w-4 shrink-0" />
                  <div>
                    <p className="font-medium">未找到相关文档，无法回答该问题。</p>
                    <p className="mt-1 text-xs text-amber-600">
                      系统在检索到可支撑证据前拒绝作答，避免幻觉编造。
                    </p>
                  </div>
                </div>
              ) : answer ? (
                <p className="whitespace-pre-wrap leading-relaxed text-slate-800">
                  {answer}
                  {status === "streaming" && (
                    <span className="ml-0.5 inline-block h-4 w-[2px] animate-blink bg-blue-600 align-middle" />
                  )}
                </p>
              ) : (
                <p className="text-slate-400">{status === "streaming" ? phase : ""}</p>
              )}
            </Card>

            {citations.length > 0 && (
              <Card>
                <h2 className="mb-3 text-sm font-semibold text-slate-500">引用（{citations.length}）</h2>
                <div className="space-y-2">
                  {citations.map((s, i) => (
                    <details key={s.chunk_id} className="group rounded-lg border border-slate-200">
                      <summary className="flex cursor-pointer items-center justify-between px-3 py-2 text-sm hover:bg-slate-50">
                        <span className="flex items-center gap-2">
                          <span className="flex h-5 w-5 items-center justify-center rounded-full bg-blue-50 text-xs font-medium text-blue-600">
                            {i + 1}
                          </span>
                          <span>
                            <span className="font-medium text-slate-700">{s.file_name || s.doc_id}</span>
                            {s.file_name && <span className="mt-0.5 block font-mono text-[11px] text-slate-400">{s.doc_id}</span>}
                          </span>
                        </span>
                        <ChevronDown className="h-3.5 w-3.5 text-slate-400 transition-transform group-open:rotate-180" />
                      </summary>
                      <div className="border-t border-slate-100 px-3 py-2">
                        {(s.effective_date || s.knowledge_base_id || s.applicable_scope) && (
                          <p className="mb-1.5 text-xs text-slate-400">
                            {[s.effective_date && `生效 ${s.effective_date}`, s.knowledge_base_id, s.applicable_scope]
                              .filter(Boolean)
                              .join(" · ")}
                          </p>
                        )}
                        <p className="text-sm text-slate-600">{s.content}</p>
                      </div>
                    </details>
                  ))}
                </div>
              </Card>
            )}

            {status === "error" && (
              <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">{error}</div>
            )}
          </div>

          <div className="space-y-4">
            <details className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
              <summary className="cursor-pointer text-sm font-semibold text-slate-700">回答依据与系统详情</summary>
              <p className="mt-1 text-xs text-slate-400">检索链路和本次查询用量默认收起，不影响回答与引用。</p>
            <div className="mt-4">
              <h2 className="mb-3 text-sm font-semibold text-slate-500">检索链路</h2>
              <dl className="space-y-2 text-sm">
                <Row
                  label="检索边界"
                  value={permissionBoundaryLabel(meta.retrieval)}
                  highlight={
                    meta.retrieval?.allowed_permissions?.length === 3 ||
                    (meta.retrieval?.permission_role === "user" && meta.retrieval?.allowed_permissions?.length === 2)
                  }
                />
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
                <Row
                  label="后端候选"
                  value={
                    meta.retrieval?.backend_candidate_counts
                      ? Object.entries(meta.retrieval.backend_candidate_counts)
                          .map(([name, count]) => `${name} ${count}`)
                          .join(" · ")
                      : meta.retrieval?.cache_hit
                        ? "缓存命中（无后端请求）"
                        : "—"
                  }
                />
                <Row
                  label="融合 / 去重"
                  value={
                    meta.retrieval
                      ? `${meta.retrieval.fused_candidate_count ?? "—"} / ${meta.retrieval.deduplicated_candidate_count ?? "—"}`
                      : "—"
                  }
                />
                <Row
                  label="最终上下文"
                  value={
                    meta.retrieval
                      ? `${meta.retrieval.selected_context_count ?? meta.retrieval.candidate_count} 段 · ${meta.retrieval.unique_document_count ?? 0} 份文档`
                      : "—"
                  }
                />
                <Row
                  label="知识空间"
                  value={
                    meta.retrieval?.resolved_knowledge_space_id
                      ? `${meta.retrieval.resolved_knowledge_space_name || meta.retrieval.resolved_knowledge_space_id}`
                      : "—"
                  }
                />
                <Row
                  label="证据置信度"
                  value={confidenceLabel(meta.retrieval?.max_relevance)}
                  highlight={isHighConfidence(meta.retrieval?.max_relevance)}
                />
                <Row
                  label="强标识校验"
                  value={exactEvidenceLabel(meta.retrieval).value}
                  highlight={exactEvidenceLabel(meta.retrieval).highlight}
                />
                <Row
                  label="忠实度校验"
                  value={groundingLabel(meta.retrieval).value}
                  highlight={groundingLabel(meta.retrieval).highlight}
                />
                <Row
                  label="语料治理"
                  value={governanceLabel(meta.retrieval)}
                  highlight={(meta.retrieval?.retired_filtered ?? 0) > 0}
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
                <Row label="首字耗时" value={meta.ttft || "—"} />
                <Row label="总耗时" value={meta.duration || "—"} />
                <Row label="Prompt 版本" value={meta.prompt_version || "v1"} />
              </dl>
            </div>
            </details>
          </div>
        </div>
      )}
    </div>
  );
}
