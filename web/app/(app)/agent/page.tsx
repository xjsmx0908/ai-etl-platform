"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import {
  Bot,
  Check,
  CheckCircle2,
  ChevronDown,
  FileCheck2,
  Play,
  RefreshCw,
  Search,
  ShieldCheck,
  X,
  XCircle,
} from "lucide-react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import { Badge } from "@/components/ui/Badge";
import { Button } from "@/components/ui/Button";
import { Forbidden } from "@/components/ui/Forbidden";
import type {
  AgentApproval,
  AgentRun,
  Document,
  ReleaseRequest,
  ReleaseOverviewItem,
} from "@/lib/types";

const LABELS: Record<string, string> = {
  managed_space_required: "文档不在受管知识空间",
  ingestion_not_completed: "ETL 尚未完成",
  document_not_active: "文档状态不是有效",
  document_not_publishable: "文档当前不可发布",
  owner_required: "缺少责任人",
  effective_date_required: "缺少生效日期",
  exact_candidate_unavailable: "索引版本尚未形成可发布候选",
};
const STATES: Record<string, string> = {
  pending_approval: "待审批",
  completed: "已完成",
  failed: "检查失败",
  cancelled: "已取消",
  running: "检查中",
  waiting_tool: "检查中",
  created: "待执行",
};
const REQUEST_STATES: Record<string, string> = {
  approval_pending: "待审批",
  manual_exception: "人工例外",
  needs_info: "候选已失效",
  rejected: "已拒绝",
  published: "已发布",
};
const TOOLS: Record<string, string> = {
  assess_document_publication: "Agent 发布检查",
  publish_document: "发布动作",
};
const tone = (s: string): "success" | "danger" | "warning" | "info" =>
  s === "completed"
    ? "success"
    : s === "failed" || s === "cancelled"
      ? "danger"
      : s === "running" || s === "waiting_tool"
        ? "info"
        : "warning";

export default function AgentPage() {
  const { user, isAdmin } = useAuth();
  const [documents, setDocuments] = useState<Document[]>([]),
    [releaseRequests, setReleaseRequests] = useState<ReleaseRequest[]>([]),
    [releaseOverview, setReleaseOverview] = useState<ReleaseOverviewItem[]>([]),
    [selectedID, setSelectedID] = useState(""),
    [selectedRequestID, setSelectedRequestID] = useState(""),
    [run, setRun] = useState<AgentRun | null>(null),
    [approvals, setApprovals] = useState<AgentApproval[]>([]),
    [reason, setReason] = useState(""),
    [runID, setRunID] = useState(""),
    [filter, setFilter] = useState<"all" | "ready" | "attention">("all"),
    [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [requestDetail, setRequestDetail] = useState<import("@/lib/types").ReleaseRequestDetail | null>(null);
  useEffect(() => {
    let active = true;
    if (!isAdmin) {
      setLoading(false);
      return () => {
        active = false;
      };
    }
    Promise.all([
      apiClient.listDocuments({ limit: 100 }),
      apiClient.listReleaseRequests(),
      apiClient.listReleaseOverview(),
    ])
      .then(([documentResponse, requestResponse, overviewResponse]) => {
        if (!active) return;
        const ds = documentResponse.items.filter(
          (d) =>
            d.knowledge_space_id &&
            d.knowledge_space_id !== "user-uploads" &&
            d.publication_status === "draft",
        );
        setDocuments(ds);
        setReleaseRequests(requestResponse.items);
        setReleaseOverview(overviewResponse.items);
        const firstRequest =
          requestResponse.items.find(
            (request) =>
              request.state === "approval_pending" ||
              request.state === "manual_exception",
          ) ?? requestResponse.items[0];
        setSelectedRequestID((value) => value || firstRequest?.request_id || "");
        setSelectedID(
          (value) => value || firstRequest?.document_id || ds[0]?.doc_id || "",
        );
      })
      .catch((e: Error) => active && setError(e.message))
      .finally(() => active && setLoading(false));
    return () => {
      active = false;
    };
  }, [isAdmin]);
  const selected = useMemo(
    () => documents.find((d) => d.doc_id === selectedID) ?? null,
    [documents, selectedID],
  );
  const data = run?.steps.find(
    (s) => s.tool_name === "assess_document_publication",
  )?.tool_result?.data;
  const blockers = Array.isArray(data?.blockers)
    ? data.blockers.filter((x): x is string => typeof x === "string")
    : [];
  const approval = approvals.find(
    (a) => a.status === "pending" || a.status === "approved",
  );
  const ready = (d: Document) => Boolean(d.owner && d.effective_date),
    readyCount = documents.filter(ready).length;
  const visible = documents.filter(
    (d) => filter === "all" || (filter === "ready" ? ready(d) : !ready(d)),
  );
  const pendingRequestCount = releaseRequests.filter(
    (request) => request.state === "approval_pending" || request.state === "manual_exception",
  ).length;
  const manualExceptionCount = releaseRequests.filter((request) => request.state === "manual_exception").length;
  const overviewByDocument = useMemo(
    () => new Map(releaseOverview.map((item) => [item.document_id, item])),
    [releaseOverview],
  );
  const requestByDocument = useMemo(
    () => {
      const requests = new Map<string, ReleaseRequest>();
      for (const request of releaseRequests) {
        if (!requests.has(request.document_id)) {
          requests.set(request.document_id, request);
        }
      }
      return requests;
    },
    [releaseRequests],
  );
  const selectedRequest =
    releaseRequests.find((request) => request.request_id === selectedRequestID) ??
    (selected ? requestByDocument.get(selected.doc_id) : undefined);
  useEffect(() => {
    if (!selectedRequest) { setRequestDetail(null); return; }
    apiClient.getReleaseRequest(selectedRequest.request_id).then(setRequestDetail).catch(() => setRequestDetail(null));
  }, [selectedRequest]);
  const approvedDecisionCount = requestDetail?.decisions.filter(
    (decision) => decision.decision === "approved",
  ).length ?? 0;
  const currentUserDecision = requestDetail?.decisions.find(
    (decision) => decision.decided_by === user?.id,
  );
  const selectedDocumentID = selectedRequest?.document_id || selected?.doc_id || "";
  const applyRun = async (next: AgentRun) => {
    setRun(next);
    setApprovals(
      next.state === "pending_approval"
        ? await apiClient.listAgentApprovals(next.id)
        : [],
    );
    if (
      next.steps.some(
        (s) =>
          s.tool_name === "publish_document" &&
          s.tool_result?.data?.publication_status === "published",
      )
    )
      setDocuments((items) => items.filter((d) => d.doc_id !== selectedID));
  };
  const start = async () => {
    if (!selected) return;
    setBusy(true);
    setError("");
    setRun(null);
    try {
      await applyRun(
        await apiClient.createDocumentPublicationRun(selected.doc_id),
      );
    } catch (e: unknown) {
      setError((e as Error).message || "发布检查失败");
    } finally {
      setBusy(false);
    }
  };
  const openRun = async () => {
    if (!runID.trim()) return;
    setBusy(true);
    setError("");
    try {
      const next = await apiClient.getAgentRun(runID.trim());
      const id = next.task.startsWith("document_publication:")
        ? next.task.slice(21)
        : "";
      if (id) {
        if (!documents.some((d) => d.doc_id === id)) {
          const document = await apiClient.getDocument(id);
          setDocuments((items) => [...items, document]);
        }
        setSelectedID(id);
      }
      await applyRun(next);
      setRunID("");
    } catch (e: unknown) {
      setError((e as Error).message || "无法打开运行");
    } finally {
      setBusy(false);
    }
  };
  const act = async (action: "approve" | "reject" | "cancel" | "resume") => {
    if (!run) return;
    setBusy(true);
    setError("");
    try {
      const next =
        action === "approve"
          ? await apiClient.approveAgentRun(run.id, reason)
          : action === "reject"
            ? await apiClient.rejectAgentRun(run.id, reason)
            : action === "cancel"
              ? await apiClient.cancelAgentRun(run.id, reason)
              : await apiClient.resumeAgentRun(run.id);
      await applyRun(next);
      setReason("");
    } catch (e: unknown) {
      setError((e as Error).message || "操作失败");
    } finally {
      setBusy(false);
    }
  };
  if (!isAdmin) return <Forbidden message="知识发布中心仅管理员可见。" />;

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div>
          <div className="flex items-center gap-2">
            <Bot className="h-5 w-5 text-indigo-600" />
            <h1 className="text-xl font-semibold text-slate-900">
              知识发布中心
            </h1>
          </div>
          <p className="mt-1 text-sm text-slate-500">
            确定性发布门禁、Agent 预审与管理员审批。
          </p>
        </div>
        <details className="relative">
          <summary className="flex cursor-pointer list-none items-center gap-1 rounded-lg border border-slate-200 bg-white px-3 py-2 text-xs text-slate-600">
            <Search className="h-3.5 w-3.5" />
            审计/恢复 Run ID
            <ChevronDown className="h-3.5 w-3.5" />
          </summary>
          <div className="absolute right-0 z-10 mt-2 flex w-80 gap-2 rounded-lg border border-slate-200 bg-white p-3 shadow-lg">
            <input
              value={runID}
              onChange={(e) => setRunID(e.target.value)}
              onKeyDown={(e) => e.key === "Enter" && void openRun()}
              placeholder="粘贴 Run ID"
              className="min-w-0 flex-1 rounded-md border border-slate-300 px-3 py-2 text-xs"
            />
            <Button size="sm" loading={busy} onClick={() => void openRun()}>
              打开
            </Button>
          </div>
        </details>
      </div>
      {error && (
        <div className="border-l-2 border-red-500 bg-red-50 px-4 py-3 text-sm text-red-700">
          {error}
        </div>
      )}
      <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <div className="rounded-xl border border-slate-200 bg-white p-4">
          <p className="text-xs text-slate-500">待治理文档</p>
          <p className="mt-1 text-2xl font-semibold">
            {loading ? "—" : documents.length}
          </p>
        </div>
        <div className="rounded-xl border border-emerald-200 bg-emerald-50/50 p-4">
          <p className="text-xs text-emerald-700">资料完整，可检查</p>
          <p className="mt-1 text-2xl font-semibold text-emerald-800">
            {readyCount}
          </p>
        </div>
        <div className="rounded-xl border border-amber-200 bg-amber-50/50 p-4">
          <p className="text-xs text-amber-700">需要补齐信息</p>
          <p className="mt-1 text-2xl font-semibold text-amber-800">
            {documents.length - readyCount}
          </p>
        </div>
        <div className="rounded-xl border border-blue-200 bg-blue-50/50 p-4">
          <p className="text-xs text-blue-700">待处理申请</p>
          <p className="mt-1 text-2xl font-semibold text-blue-800">
            {pendingRequestCount}
          </p>
          {manualExceptionCount > 0 && <p className="mt-1 text-xs text-amber-700">其中人工例外 {manualExceptionCount}</p>}
        </div>
      </div>
      <div className="grid min-h-[560px] overflow-hidden rounded-xl border border-slate-200 bg-white lg:grid-cols-[330px_minmax(0,1fr)]">
        <aside className="border-b border-slate-200 lg:border-b-0 lg:border-r">
          <div className="border-b border-slate-200 p-4">
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-semibold text-slate-800">发布申请</h2>
              <Badge tone="brand">{releaseRequests.length}</Badge>
            </div>
            <p className="mt-1 text-xs text-slate-500">
              持久化审批与历史记录
            </p>
          </div>
          <div className="max-h-64 overflow-y-auto border-b border-slate-200 p-2">
            {releaseRequests.length === 0 && (
              <p className="px-3 py-4 text-center text-xs text-slate-400">
                暂无发布申请
              </p>
            )}
            {releaseRequests.map((request) => (
              <button
                key={request.request_id}
                type="button"
                onClick={() => {
                  setSelectedRequestID(request.request_id);
                  setSelectedID(request.document_id);
                  setRun(null);
                  setApprovals([]);
                  setReason("");
                }}
                className={`mb-1 w-full rounded-lg border px-3 py-3 text-left ${selectedRequest?.request_id === request.request_id ? "border-indigo-200 bg-indigo-50" : "border-transparent hover:bg-slate-50"}`}
              >
                <span className="flex items-center justify-between gap-2">
                  <span className="truncate text-sm font-medium">
                    {documents.find((document) => document.doc_id === request.document_id)?.file_name || request.document_id}
                  </span>
                  <Badge
                    tone={
                      request.state === "published"
                        ? "success"
                        : request.state === "rejected"
                          ? "danger"
                          : "warning"
                    }
                  >
                    {REQUEST_STATES[request.state] || request.state}
                  </Badge>
                </span>
                <span className="mt-1 block truncate text-xs text-slate-400">
                  {request.document_id} · 需 {request.required_approvals} 人审批
                </span>
              </button>
            ))}
          </div>
          <div className="border-b border-slate-200 p-4">
            <div className="flex items-center justify-between">
              <h2 className="text-sm font-semibold text-slate-800">受管草稿</h2>
              <Badge tone="brand">{documents.length}</Badge>
            </div>
            <div className="mt-3 flex gap-1 rounded-lg bg-slate-100 p-1 text-xs">
              {(
                [
                  ["all", "全部"],
                  ["ready", "可检查"],
                  ["attention", "待补齐"],
                ] as const
              ).map(([k, l]) => (
                <button
                  key={k}
                  onClick={() => setFilter(k)}
                  className={`flex-1 rounded-md px-2 py-1.5 ${filter === k ? "bg-white font-medium shadow-sm" : "text-slate-500"}`}
                >
                  {l}
                </button>
              ))}
            </div>
          </div>
          <div className="max-h-[500px] overflow-y-auto p-2">
            {visible.map((d) => (
              <button
                key={d.doc_id}
                type="button"
                onClick={() => {
                  setSelectedID(d.doc_id);
                  setSelectedRequestID(requestByDocument.get(d.doc_id)?.request_id || "");
                  setRun(null);
                  setApprovals([]);
                  setReason("");
                }}
                className={`mb-1 w-full rounded-lg border px-3 py-3 text-left ${selectedID === d.doc_id ? "border-blue-200 bg-blue-50" : "border-transparent hover:bg-slate-50"}`}
              >
                <span className="block truncate text-sm font-medium">
                  {d.file_name}
                </span>
                <span className="mt-1 block truncate text-xs text-slate-400">
                  {d.doc_id} · {overviewByDocument.get(d.doc_id)?.state || (requestByDocument.has(d.doc_id)
                    ? `已生成发布申请 · 需 ${requestByDocument.get(d.doc_id)?.required_approvals} 人审批`
                    : ready(d)
                      ? "资料完整"
                      : "缺少责任人/生效日期")}
                </span>
              </button>
            ))}
          </div>
        </aside>
        <section className="min-w-0 p-5 sm:p-6">
          {!selected && !selectedRequest ? (
            <div className="flex min-h-[460px] items-center justify-center text-sm text-slate-400">
              暂无待治理文档
            </div>
          ) : (
            <div className="space-y-5">
              <div className="flex flex-wrap items-start justify-between gap-3 border-b border-slate-100 pb-5">
                <div>
                  <div className="flex items-center gap-2">
                    <FileCheck2 className="h-5 w-5 text-blue-600" />
                    <h2 className="truncate text-base font-semibold">
                      {selected?.file_name || selectedRequest?.document_id}
                    </h2>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-4 text-xs text-slate-500">
                    {selected ? (
                      <>
                        <span>责任人：{selected.owner || "未填写"}</span>
                        <span>生效日期：{selected.effective_date || "未填写"}</span>
                        <span>空间：{selected.knowledge_space_id}</span>
                      </>
                    ) : (
                      <span>文档：{selectedRequest?.document_id}</span>
                    )}
                  </div>
                </div>
                {selected && !run && !selectedRequest && (
                  <Button
                    onClick={() => void start()}
                    loading={busy}
                    icon={Play}
                  >
                    开始 Agent 检查
                  </Button>
                )}
              </div>
              {requestDetail && (
                <section className="rounded-lg border border-indigo-200 bg-indigo-50/40 p-4">
                  <div className="flex items-center justify-between gap-3"><h3 className="text-sm font-semibold">发布申请与 Agent 预审</h3><Badge tone={requestDetail.request.state === "published" ? "success" : requestDetail.request.state === "rejected" ? "danger" : "warning"}>{REQUEST_STATES[requestDetail.request.state] || requestDetail.request.state}</Badge></div>
                  <p className="mt-2 text-sm text-slate-700">{requestDetail.review.summary || "Agent 未提供补充说明。"}</p>
                  <p className="mt-1 text-xs text-slate-500">风险：{requestDetail.review.risk_level} · 建议：{requestDetail.review.recommendation} · 已批准 {approvedDecisionCount} / {requestDetail.request.required_approvals}</p>
                  {requestDetail.review.findings?.map((finding) => <p key={finding.code} className="mt-2 text-xs text-slate-600">{finding.code}：{finding.summary}</p>)}
                  {requestDetail.request.state === "manual_exception" && <div className="mt-3 rounded-md border border-amber-300 bg-amber-50 px-3 py-2 text-xs text-amber-800">Agent 预审不可用。管理员必须完成人工核对并填写例外理由；批准记录将永久保留。</div>}
                  {(requestDetail.request.state === "approval_pending" || requestDetail.request.state === "manual_exception") && !currentUserDecision && <div className="mt-4 flex flex-wrap gap-2"><input value={reason} onChange={(e) => setReason(e.target.value)} placeholder={requestDetail.request.state === "manual_exception" ? "人工例外理由（必填）" : "审批意见（可选）"} className="min-w-[220px] flex-1 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /><Button size="sm" icon={Check} loading={busy} disabled={requestDetail.request.state === "manual_exception" && !reason.trim()} onClick={async () => { setBusy(true); try { const d = await apiClient.decideReleaseRequest(requestDetail.request.request_id, "approved", reason); setRequestDetail(d); setReleaseRequests((items) => items.map((x) => x.request_id === d.request.request_id ? d.request : x)); if (d.request.state === "published") setDocuments((items) => items.filter((document) => document.doc_id !== d.request.document_id)); setReason(""); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }}>批准发布</Button><Button size="sm" icon={X} variant="danger" disabled={busy} onClick={async () => { setBusy(true); try { const d = await apiClient.decideReleaseRequest(requestDetail.request.request_id, "rejected", reason); setRequestDetail(d); setReleaseRequests((items) => items.map((x) => x.request_id === d.request.request_id ? d.request : x)); setReason(""); } catch (e) { setError((e as Error).message); } finally { setBusy(false); } }}>拒绝</Button></div>}
                  {(requestDetail.request.state === "approval_pending" || requestDetail.request.state === "manual_exception") && currentUserDecision && <p className="mt-3 text-sm text-indigo-700">你已提交{currentUserDecision.decision === "approved" ? "批准" : "拒绝"}决定，正在等待其他管理员处理。</p>}
                  <div className="mt-3 text-xs text-slate-500">审批记录：{requestDetail.decisions.length ? requestDetail.decisions.map((d) => `${d.decided_by} ${d.decision}`).join(" · ") : "暂无"}</div>
                </section>
              )}
              {!run && selected && (
                <div className="rounded-lg border border-slate-200 bg-slate-50 p-4 text-sm text-slate-600">
                  <p className="font-medium text-slate-800">知识发布流程</p>
                  <p className="mt-1">
                    确定性检查 → Agent 预审 → 按风险策略审批 → 发布进入问答检索范围。
                  </p>
                  <Link
                    href={`/documents/${encodeURIComponent(selected.doc_id)}`}
                    className="mt-2 inline-flex text-xs text-blue-600 hover:underline"
                  >
                    去文档详情补齐治理信息 →
                  </Link>
                </div>
              )}
              {data && (
                <section>
                  <div className="mb-3 flex items-center justify-between">
                    <h3 className="text-sm font-semibold">Agent 检查结果</h3>
                    <Badge tone={blockers.length ? "warning" : "success"}>
                      {blockers.length
                        ? `需处理 ${blockers.length} 项`
                        : "检查通过"}
                    </Badge>
                  </div>
                  {blockers.length ? (
                    <div className="space-y-2">
                      {blockers.map((b) => (
                        <div
                          key={b}
                          className="flex items-center gap-2 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800"
                        >
                          <XCircle className="h-4 w-4" />
                          {LABELS[b] || `需要处理：${b}`}
                        </div>
                      ))}
                    </div>
                  ) : (
                    <div className="flex items-center gap-2 rounded-lg border border-emerald-200 bg-emerald-50 px-3 py-3 text-sm text-emerald-800">
                      <CheckCircle2 className="h-4 w-4" />
                      索引、治理信息和发布版本均已核验，等待审批。
                    </div>
                  )}
                </section>
              )}
              {run?.state === "pending_approval" && approval && (
                <section className="rounded-lg border border-indigo-200 bg-indigo-50/50 p-4">
                  <div className="flex items-start gap-3">
                    <ShieldCheck className="h-5 w-5 text-indigo-600" />
                    <div className="flex-1">
                      <h3 className="text-sm font-semibold">管理员审批</h3>
                      <p className="mt-1 text-xs text-slate-600">
                        请求人 {approval.requested_by} · 按文档风险策略等待管理员确认
                      </p>
                      {approval.status === "pending" &&
                        isAdmin &&
                        approval.requested_by !== user?.id && (
                          <div className="mt-3 flex flex-wrap gap-2">
                            <input
                              value={reason}
                              onChange={(e) => setReason(e.target.value)}
                              placeholder="审批意见（可选）"
                              className="min-w-[220px] flex-1 rounded-md border border-slate-300 bg-white px-3 py-2 text-sm"
                            />
                            <Button
                              size="sm"
                              icon={Check}
                              loading={busy}
                              onClick={() => void act("approve")}
                            >
                              批准发布
                            </Button>
                            <Button
                              size="sm"
                              icon={X}
                              variant="danger"
                              disabled={busy}
                              onClick={() => void act("reject")}
                            >
                              拒绝
                            </Button>
                          </div>
                        )}
                      {approval.status === "pending" &&
                        approval.requested_by === user?.id && (
                          <p className="mt-3 text-sm text-amber-700">
                        当前审批策略不允许发起人自审，请由符合条件的管理员处理。
                          </p>
                        )}
                      {approval.status === "approved" && isAdmin && (
                        <Button
                          className="mt-3"
                          size="sm"
                          icon={RefreshCw}
                          loading={busy}
                          onClick={() => void act("resume")}
                        >
                          继续发布
                        </Button>
                      )}
                    </div>
                  </div>
                </section>
              )}
              {run?.final && (
                <div
                  className={`rounded-lg border px-4 py-3 text-sm ${blockers.length ? "border-amber-200 bg-amber-50 text-amber-800" : "border-emerald-200 bg-emerald-50 text-emerald-800"}`}
                >
                  {run.final}
                </div>
              )}
              {run && (
                <details className="group rounded-lg border border-slate-200">
                  <summary className="flex cursor-pointer list-none items-center justify-between px-4 py-3 text-sm font-medium">
                    Agent 执行详情（审计信息）
                    <ChevronDown className="h-4 w-4 transition-transform group-open:rotate-180" />
                  </summary>
                  <div className="border-t border-slate-200 px-4 py-3">
                    <div className="mb-3 flex flex-wrap gap-2 text-xs text-slate-500">
                      <Badge tone={tone(run.state)}>
                        {STATES[run.state] || run.state}
                      </Badge>
                      <span className="font-mono">Run {run.id}</span>
                    </div>
                    {run.steps.map((s) => (
                      <div key={s.index} className="mb-2 text-sm">
                        <span className="mr-2 text-slate-400">{s.index}.</span>
                        <span className="font-medium">
                          {TOOLS[s.tool_name || ""] || "工作流完成"}
                        </span>
                        {s.observation && (
                          <p className="ml-5 text-xs text-slate-500">
                            {s.observation}
                          </p>
                        )}
                      </div>
                    ))}
                  </div>
                </details>
              )}
              <Link
                href={`/documents/${encodeURIComponent(selectedDocumentID)}`}
                className="inline-flex text-xs text-blue-600 hover:underline"
              >
                查看文档详情
              </Link>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
