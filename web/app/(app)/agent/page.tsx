"use client";

import Link from "next/link";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Bot, Check, ChevronDown, FileCheck2, RefreshCw, Search, X } from "lucide-react";
import { apiClient } from "@/lib/apiClient";
import { isTransientFetchError, localizeFetchError } from "@/lib/fetchErrors";
import { useAuth } from "@/lib/auth";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Forbidden } from "@/components/ui/Forbidden";
import { PageHeader } from "@/components/ui/PageHeader";
import type { AgentRun, Document, ReleaseOverviewItem, ReleaseRequest, ReleaseRequestDetail, ReleaseReview } from "@/lib/types";

const STATE_LABELS: Record<string, string> = {
  needs_info: "需补齐", checking: "Agent 预审中", review_blocked: "预审受阻",
  approval_pending: "待审批", published: "已发布", rejected: "已拒绝",
};
const REQUEST_LABELS: Record<string, string> = {
  approval_pending: "待审批", manual_exception: "人工例外", needs_info: "候选已失效",
  rejected: "已拒绝", published: "已发布",
};
const BLOCKER_LABELS: Record<string, string> = {
  managed_space_required: "文档不在受管知识空间", ingestion_not_completed: "ETL 尚未完成",
  document_not_active: "文档状态不是有效", document_not_publishable: "文档当前不可发布",
  owner_required: "缺少责任人", effective_date_required: "缺少生效日期",
  exact_candidate_unavailable: "索引版本尚未形成可发布候选",
  review_expired: "预审已过期，等待重新审查",
};

const FINDING_LABELS: Record<string, string> = {
  sensitive_data_detected: "敏感信息",
  prompt_injection_detected: "提示词注入",
  space_mismatch: "不适合本空间",
  space_fit_uncertain: "是否适合本空间看不准",
  not_knowledge: "不能作为正式知识",
  incomplete_knowledge: "材料不完整",
  fitness_evidence_missing: "缺少适合性证据",
};

function spaceFitLabel(value?: string) {
  return ({ match: "适合", mismatch: "不适合", uncertain: "看不准" } as Record<string, string>)[value || ""] || "";
}

function knowledgeUsableLabel(value?: string) {
  return ({ usable: "可以作为正式知识", not_knowledge: "不能当正式知识", incomplete: "材料不完整" } as Record<string, string>)[value || ""] || "";
}

function findingLabel(code: string) {
  return FINDING_LABELS[code] || code;
}

function riskLabel(risk: string) {
  return ({ low: "低风险", medium: "中风险", high: "高风险", critical: "严重风险" } as Record<string, string>)[risk] || risk;
}

function recommendationLabel(recommendation: string) {
  return ({ publish: "建议发布", needs_info: "需补充信息", reject: "不建议发布", manual_review: "转人工复核" } as Record<string, string>)[recommendation] || recommendation;
}

function reviewStatusLabel(status: string) {
  if (status === "completed" || status === "success") return "已完成";
  if (status === "failed" || status === "error") return "预审失败";
  if (status === "expired") return "已过期";
  return status || "未知";
}

function tone(state: string): "success" | "danger" | "warning" | "info" {
  if (state === "published") return "success";
  if (state === "rejected") return "danger";
  if (state === "checking") return "info";
  return "warning";
}

function relativeTime(value: number | null) {
  if (!value) return "尚未同步";
  const seconds = Math.max(0, Math.floor((Date.now() - value) / 1000));
  return seconds < 5 ? "刚刚" : `${seconds} 秒前`;
}

export default function AgentPage() {
  const { user, isAdmin } = useAuth();
  const [documents, setDocuments] = useState<Document[]>([]);
  const [requests, setRequests] = useState<ReleaseRequest[]>([]);
  const [overview, setOverview] = useState<ReleaseOverviewItem[]>([]);
  const [selectedOverviewID, setSelectedOverviewID] = useState("");
  const [selectedRequestID, setSelectedRequestID] = useState("");
  const [detail, setDetail] = useState<ReleaseRequestDetail | null>(null);
  const [standaloneReview, setStandaloneReview] = useState<ReleaseReview | null>(null);
  const [run, setRun] = useState<AgentRun | null>(null);
  const [reason, setReason] = useState("");
  const [overviewFilter, setOverviewFilter] = useState("all");
  const [overviewSort, setOverviewSort] = useState<"priority" | "name">("priority");
  const [overviewPage, setOverviewPage] = useState(1);
  const [overviewPageSize, setOverviewPageSize] = useState(10);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [lastSyncedAt, setLastSyncedAt] = useState<number | null>(null);

  const refreshSeq = useRef(0);
  const refreshReleaseCenter = useCallback(async (quiet = false) => {
    if (!isAdmin) { setLoading(false); return; }
    if (!quiet) setRefreshing(true);
    const seq = ++refreshSeq.current;
    try {
      const [docs, reqs, states] = await Promise.all([
        apiClient.listDocuments({ limit: 100 }), apiClient.listReleaseRequests(), apiClient.listReleaseOverview(),
      ]);
      if (seq !== refreshSeq.current) return;
      setDocuments(docs.items);
      setRequests(reqs.items);
      setOverview(states.items);
      setLastSyncedAt(Date.now());
      setSelectedOverviewID((id) => id || states.items[0]?.document_id || "");
      setError("");
    } catch (e: unknown) {
      if (seq !== refreshSeq.current) return;
      const raw = (e as Error).message;
      if (!quiet || !isTransientFetchError(raw)) {
        setError(localizeFetchError(raw) || "无法刷新发布中心");
      }
    } finally {
      if (seq === refreshSeq.current) {
        setLoading(false);
        if (!quiet) setRefreshing(false);
      }
    }
  }, [isAdmin]);

  useEffect(() => { void refreshReleaseCenter(); }, [refreshReleaseCenter]);
  useEffect(() => {
    const timer = window.setInterval(() => void refreshReleaseCenter(true), 8000);
    return () => window.clearInterval(timer);
  }, [refreshReleaseCenter]);
  useEffect(() => {
    const params = new URLSearchParams(window.location.search);
    const documentID = params.get("document")?.trim() || "";
    const requestID = params.get("request")?.trim() || "";
    if (documentID) setSelectedOverviewID(documentID);
    if (requestID) setSelectedRequestID(requestID);
  }, []);
  const byDocument = useMemo(() => new Map(overview.map((item) => [item.document_id, item])), [overview]);
  useEffect(() => {
    const selected = requests.find((item) => item.request_id === selectedRequestID)
      || requests.find((item) => item.document_id === selectedOverviewID);
    if (!selected) {
      setDetail(null);
      const reviewID = selectedOverviewID ? byDocument.get(selectedOverviewID)?.review_id : undefined;
      if (!reviewID) { setStandaloneReview(null); return; }
      void apiClient.getReleaseReview(reviewID).then((result) => setStandaloneReview(result.review)).catch(() => setStandaloneReview(null));
      return;
    }
    setStandaloneReview(null);
    void apiClient.getReleaseRequest(selected.request_id).then(setDetail).catch(() => setDetail(null));
  }, [requests, selectedRequestID, selectedOverviewID, byDocument]);

  const visible = useMemo(() => {
    const priority: Record<string, number> = { approval_pending: 0, review_blocked: 1, checking: 2, needs_info: 3, rejected: 4, published: 5 };
    return overview.filter((item) => overviewFilter === "all" || item.state === overviewFilter)
      .filter((item) => !query.trim() || `${item.file_name} ${item.document_id} ${item.knowledge_space_id}`.toLowerCase().includes(query.trim().toLowerCase()))
      .sort((a, b) => overviewSort === "name" ? (a.file_name || a.document_id).localeCompare(b.file_name || b.document_id, "zh-CN") : (priority[a.state] ?? 99) - (priority[b.state] ?? 99));
  }, [overview, overviewFilter, overviewSort, query]);
  const overviewSorted = visible;
  const pageCount = Math.max(1, Math.ceil(overviewSorted.length / overviewPageSize));
  const pageItems = overviewSorted.slice((overviewPage - 1) * overviewPageSize, overviewPage * overviewPageSize);
  useEffect(() => {
    const index = visible.findIndex((item) => item.document_id === selectedOverviewID);
    if (index < 0) return;
    const nextPage = Math.floor(index / overviewPageSize) + 1;
    setOverviewPage((page) => (page === nextPage ? page : nextPage));
  }, [visible, overviewPageSize, selectedOverviewID]);
  const selectedOverview = byDocument.get(selectedOverviewID);
  const selectedRequest = requests.find((item) => item.request_id === selectedRequestID) || (selectedOverview?.request_id ? requests.find((item) => item.request_id === selectedOverview.request_id) : undefined);
  const selectedDocument = documents.find((item) => item.doc_id === (selectedOverviewID || selectedRequest?.document_id));
  const approvedCount = detail?.decisions.filter((item) => item.decision === "approved").length ?? selectedOverview?.approved_decisions ?? 0;
  const currentDecision = detail?.decisions.find((item) => item.decided_by === user?.id);
  const review = detail?.review || standaloneReview;

  const selectItem = async (item: ReleaseOverviewItem) => {
    setSelectedOverviewID(item.document_id); setSelectedRequestID(item.request_id || ""); setRun(null); setStandaloneReview(null); setReason("");
    if (!documents.some((doc) => doc.doc_id === item.document_id)) {
      try { setDocuments((docs) => docs); const doc = await apiClient.getDocument(item.document_id); setDocuments((docs) => [...docs, doc]); }
      catch { setError("无法加载文档详情"); }
    }
  };
  const decide = async (decision: "approved" | "rejected") => {
    if (!detail) return;
    setBusy(true); setError("");
    try { const next = await apiClient.decideReleaseRequest(detail.request.request_id, decision, reason); setDetail(next); setReason(""); await refreshReleaseCenter(); }
    catch (e: unknown) { setError(localizeFetchError((e as Error).message) || "审批操作失败"); }
    finally { setBusy(false); }
  };

  if (!isAdmin) return <Forbidden />;
  return (
    <div className="space-y-4">
      <PageHeader
        icon={Bot}
        title="知识发布中心"
        description="按业务状态查看预审证据，并完成管理员审批"
        actions={
          <div className="flex items-center gap-2">
            <span className="text-xs text-slate-400">最后同步：{relativeTime(lastSyncedAt)}</span>
            <Button variant="secondary" size="sm" icon={RefreshCw} loading={refreshing} onClick={() => void refreshReleaseCenter()}>刷新状态</Button>
          </div>
        }
      />
      {error && <div className="rounded-lg border border-red-200 bg-red-50 px-3 py-2 text-sm text-red-700">{error}</div>}
      {loading && <div className="rounded-lg border border-blue-100 bg-blue-50 px-3 py-2 text-xs text-blue-700">正在同步发布状态…</div>}
      <div className="flex flex-wrap items-center gap-2 rounded-xl border border-slate-200 bg-white p-2 shadow-sm"><span className="px-2 text-xs font-medium text-slate-500">状态筛选</span>
        {[['all','全部状态'],['needs_info','需补齐'],['checking','处理中'],['review_blocked','预审受阻'],['approval_pending','待审批'],['published','已发布'],['rejected','已拒绝']].map(([key,label]) => <button key={key} type="button" onClick={() => { setOverviewFilter(key); setOverviewPage(1); }} className={`rounded-lg px-3 py-2 text-sm ${overviewFilter === key ? 'bg-slate-900 text-white' : 'text-slate-600 hover:bg-slate-100'}`}>{label} <span className="ml-1 text-xs opacity-70">{key === 'all' ? overview.length : overview.filter((item) => item.state === key).length}</span></button>)}
      </div>
      <div className="grid min-h-[620px] gap-4 lg:grid-cols-[360px_1fr]">
        <aside className="rounded-xl border border-slate-200 bg-white shadow-sm">
          <div className="border-b border-slate-200 p-4"><div className="flex items-center justify-between"><h2 className="text-sm font-semibold text-slate-800">统一业务记录</h2><Badge tone="brand">{visible.length}</Badge></div><div className="mt-3 flex gap-2"><div className="relative flex-1"><Search className="absolute left-2 top-2.5 h-4 w-4 text-slate-400" /><input value={query} onChange={(e) => setQuery(e.target.value)} placeholder="搜索文档或空间" className="w-full rounded-md border border-slate-300 py-2 pl-8 pr-2 text-sm" /></div><select aria-label="排序" value={overviewSort} onChange={(e) => setOverviewSort(e.target.value as "priority" | "name")} className="rounded-md border border-slate-300 px-2 text-xs"><option value="priority">优先级</option><option value="name">名称</option></select></div></div>
          <div className="max-h-[500px] overflow-y-auto p-2">{pageItems.map((item) => <button key={item.document_id} type="button" onClick={() => void selectItem(item)} className={`mb-1 w-full rounded-lg border px-3 py-3 text-left ${selectedOverviewID === item.document_id ? 'border-indigo-200 bg-indigo-50' : 'border-transparent hover:bg-slate-50'}`}><span className="flex items-center justify-between gap-2"><span className="truncate text-sm font-medium">{item.file_name || item.document_id}</span><Badge tone={tone(item.state)}>{STATE_LABELS[item.state] || item.state}</Badge></span><span className="mt-1 block truncate text-xs text-slate-400">{item.knowledge_space_id} · {item.blockers?.length ? `${item.blockers.length} 项阻塞` : item.request_id && `${item.approved_decisions || 0} / ${item.required_approvals || 1} 已批准`}</span></button>)}</div>
          <div className="flex items-center justify-between border-t border-slate-200 px-3 py-2 text-xs text-slate-400"><span>业务状态分页 {visible.length ? `${(overviewPage - 1) * overviewPageSize + 1}-${Math.min(overviewPage * overviewPageSize, visible.length)} / ${visible.length}` : '0 / 0'}</span><span className="flex items-center gap-1"><select aria-label="每页条数" value={overviewPageSize} onChange={(e) => { setOverviewPageSize(Number(e.target.value)); setOverviewPage(1); }} className="rounded border border-slate-200 px-1 py-0.5">{[10,25,50].map((n) => <option key={n} value={n}>{n}/页</option>)}</select><button type="button" disabled={overviewPage <= 1} onClick={() => setOverviewPage((n) => n - 1)}>‹</button><span>{overviewPage}/{pageCount}</span><button type="button" disabled={overviewPage >= pageCount} onClick={() => setOverviewPage((n) => n + 1)}>›</button></span></div>
        </aside>
        <main className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm sm:p-6">
          {!selectedOverview && !selectedRequest ? <div className="flex min-h-[560px] items-center justify-center text-sm text-slate-400">暂无业务记录</div> : <div className="space-y-5">
            <div className="flex flex-wrap items-start justify-between gap-3 border-b border-slate-100 pb-4"><div><div className="flex items-center gap-2"><FileCheck2 className="h-5 w-5 text-indigo-600" /><h2 className="text-lg font-semibold text-slate-900">{selectedDocument?.file_name || selectedOverview?.file_name || selectedRequest?.document_id}</h2><Badge tone={tone(selectedOverview?.state || selectedRequest?.state || '')}>{STATE_LABELS[selectedOverview?.state || ''] || REQUEST_LABELS[selectedRequest?.state || ''] || selectedOverview?.state}</Badge></div><div className="mt-2 flex flex-wrap gap-3 text-xs text-slate-500"><span>空间：{selectedDocument?.knowledge_space_id || selectedOverview?.knowledge_space_id}</span><span>权限：{selectedDocument?.permission || selectedOverview?.permission || '—'}</span><span>责任人：{selectedDocument?.owner || '未填写'}</span><span>生效日期：{selectedDocument?.effective_date || '未填写'}</span></div></div><Link href={`/documents/${encodeURIComponent(selectedOverviewID || selectedRequest?.document_id || '')}`} className="text-xs text-indigo-600 hover:underline">查看文档详情 →</Link></div>
            <section><h3 className="mb-4 text-sm font-semibold text-slate-800">发布阶段</h3><div className="grid gap-3 md:grid-cols-4">{(['确定性门禁','Agent 预审','管理员审批','精确版本发布'] as const).map((stage, index) => { const state = selectedOverview?.state || selectedRequest?.state || ''; const statuses = index === 0 ? (state === 'needs_info' || state === 'review_blocked' ? 'blocked' : 'done') : index === 1 ? (state === 'checking' ? 'active' : state === 'review_blocked' ? 'blocked' : 'done') : index === 2 ? (state === 'approval_pending' || state === 'manual_exception' ? 'active' : state === 'published' || state === 'rejected' ? 'done' : 'waiting') : (state === 'published' ? 'done' : 'waiting'); return <div key={stage} className={`rounded-lg border p-3 ${statuses === 'blocked' ? 'border-amber-300 bg-amber-50' : statuses === 'active' ? 'border-indigo-300 bg-indigo-50' : 'border-slate-200 bg-slate-50'}`}><div className="flex items-center justify-between"><span className="text-sm font-medium">{stage}</span><Badge tone={statuses === 'done' ? 'success' : statuses === 'blocked' ? 'warning' : statuses === 'active' ? 'info' : 'neutral'}>{statuses === 'done' ? '完成' : statuses === 'blocked' ? '阻塞' : statuses === 'active' ? '进行中' : '等待'}</Badge></div></div>; })}</div></section>
            {selectedOverview?.blockers?.length ? <section className="rounded-lg border border-amber-200 bg-amber-50 p-4"><h3 className="text-sm font-semibold text-amber-900">确定性门禁阻塞</h3>{selectedOverview.blockers.map((item) => <p key={item} className="mt-1 text-sm text-amber-800">{BLOCKER_LABELS[item] || item}</p>)}</section> : null}
            {review && <section className="rounded-lg border border-indigo-200 bg-indigo-50/40 p-4"><div className="flex items-center justify-between"><h3 className="text-sm font-semibold">Agent 预审</h3><Badge tone={review.status === 'failed' || review.status === 'error' ? 'danger' : detail?.request.state === 'published' ? 'success' : detail?.request.state === 'rejected' ? 'danger' : 'warning'}>{review.status === 'expired' ? '已过期' : review.status === 'failed' || review.status === 'error' ? '预审失败' : detail ? REQUEST_LABELS[detail.request.state] || detail.request.state : '只读审计'}</Badge></div><div className="mt-3 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-4"><div><span className="text-xs text-slate-500">预审状态</span><p className="font-medium">{reviewStatusLabel(review.status)}</p></div><div><span className="text-xs text-slate-500">风险评估</span><p className="font-medium">{riskLabel(review.risk_level)}</p></div><div><span className="text-xs text-slate-500">Agent 建议</span><p className="font-medium">{recommendationLabel(review.recommendation)}</p></div><div><span className="text-xs text-slate-500">管理员审批</span><p className="font-medium">{detail ? `${approvedCount} / ${detail.request.required_approvals}` : '无审批请求'}</p></div></div><div className="mt-3 flex flex-wrap gap-3 text-xs text-slate-500"><span>预审时间：{review.created_at ? new Date(review.created_at).toLocaleString('zh-CN') : '—'}</span>{review.expires_at ? <span>有效期至：{new Date(review.expires_at).toLocaleString('zh-CN')}</span> : null}<span>Prompt 版本：{review.prompt_version || '—'}</span></div><p className="mt-3 text-xs text-slate-600">{review.summary || 'Agent 未提供补充说明。'}</p>{(review.space_fit || review.knowledge_usable || review.kind_label) ? <div className="mt-3 grid gap-3 text-sm sm:grid-cols-3"><div><span className="text-xs text-slate-500">适不适合这个空间</span><p className="font-medium">{spaceFitLabel(review.space_fit) || '未评估'}</p></div><div><span className="text-xs text-slate-500">能不能当正式知识</span><p className="font-medium">{knowledgeUsableLabel(review.knowledge_usable) || '未评估'}</p></div><div><span className="text-xs text-slate-500">材料看起来像</span><p className="font-medium">{review.kind_label || '未标注（不影响发布）'}</p></div></div> : null}{review.findings?.length ? <div className="mt-4 rounded-lg border border-amber-200 bg-amber-50 p-3"><h4 className="text-xs font-semibold text-amber-900">预审发现</h4><div className="mt-2 space-y-2">{review.findings.map((finding, index) => <div key={`${finding.code}-${finding.evidence_ref || index}`} className="rounded-md border border-amber-200/80 bg-white/70 p-2 text-xs"><div className="flex flex-wrap items-center gap-2"><Badge tone="warning">{findingLabel(finding.code)}</Badge><span className="font-medium text-amber-950">{finding.summary}</span><span className="text-amber-700">严重度：{finding.severity}</span></div>{finding.evidence_ref && <p className="mt-1 font-mono text-[11px] text-slate-500">证据块：{finding.evidence_ref}</p>}</div>)}</div></div> : <p className="mt-4 text-xs text-emerald-700">未发现需要人工关注的内容风险。</p>}{selectedDocument?.permission === 'confidential' && review.risk_level === 'high' && review.findings?.some((finding) => finding.code === 'sensitive_data_detected') && <p className="mt-3 text-xs text-amber-700">检测到敏感内容，风险升高，仍需双人审批。</p>}<p className="mt-2 text-xs text-slate-500">Agent 建议仅作为审核证据，不能自行批准或发布。</p>{detail && detail.request.state === 'manual_exception' && <p className="mt-2 text-xs text-amber-700">Agent 预审不可用，管理员必须填写人工例外理由。</p>}{detail && (detail.request.state === 'approval_pending' || detail.request.state === 'manual_exception') && !currentDecision && <div className="mt-4 flex flex-wrap gap-2"><input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={detail.request.state === 'manual_exception' ? '人工例外理由（必填）' : '审批意见（可选）'} className="min-w-[220px] flex-1 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /><Button size="sm" icon={Check} loading={busy} disabled={detail.request.state === 'manual_exception' && !reason.trim()} onClick={() => void decide('approved')}>批准发布</Button><Button size="sm" icon={X} variant="danger" disabled={busy} onClick={() => void decide('rejected')}>拒绝</Button></div>}{detail && currentDecision && detail.request.state === 'approval_pending' && <p className="mt-3 text-sm text-indigo-700">你已提交决定，正在等待其他管理员处理。</p>}{detail && currentDecision && detail.request.state !== 'approval_pending' && <p className="mt-3 text-sm text-emerald-700">审批已生效，发布流程已完成。</p>}{detail && <div className="mt-3 text-xs text-slate-500">审批记录：{detail.decisions.length ? detail.decisions.map((item) => `${item.decided_by} ${item.decision}`).join(' · ') : '暂无'}</div>}</section>}
            <details className="group rounded-lg border border-slate-200"><summary className="flex cursor-pointer list-none items-center justify-between px-4 py-3 text-sm font-medium">Agent 执行详情（审计信息）<ChevronDown className="h-4 w-4 transition-transform group-open:rotate-180" /></summary><div className="border-t border-slate-200 px-4 py-3 text-xs text-slate-500">{run ? <><p>Run ID：<span className="font-mono">{run.id}</span></p><p>执行状态：{run.state} · 步骤数：{run.steps.length}</p>{run.max_token_budget ? <p>Token：{run.tokens_used || 0} / {run.max_token_budget}</p> : null}</> : detail?.review.agent_run_id ? <p>Run ID：<span className="font-mono">{detail.review.agent_run_id}</span></p> : <p>暂无 Agent Run 审计记录</p>}{detail?.review.model && <p>模型：{detail.review.model} · Prompt 版本：{detail.review.prompt_version || '—'}</p>}</div></details>
          </div>}
        </main>
      </div>
    </div>
  );
}
