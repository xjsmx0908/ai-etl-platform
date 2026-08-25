"use client";

import Link from "next/link";
import { useEffect, useMemo, useState } from "react";
import {
  Ban,
  Check,
  CheckCircle2,
  CircleDashed,
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
import type { AgentApproval, AgentRun, Document } from "@/lib/types";

const CHECKS = [
  ["managed_space_required", "受管知识空间"],
  ["ingestion_not_completed", "ETL 处理完成"],
  ["document_not_active", "文档状态有效"],
  ["document_not_draft", "当前版本为草稿"],
  ["owner_required", "责任人已填写"],
  ["effective_date_required", "生效日期已填写"],
  ["vector_index_missing", "向量索引可用"],
  ["text_index_missing", "全文索引可用"],
  ["index_count_mismatch", "双索引切块数一致"],
] as const;

const STATE_LABELS: Record<string, string> = {
  created: "已创建",
  running: "执行中",
  waiting_tool: "等待工具",
  pending_approval: "等待审批",
  completed: "已完成",
  failed: "失败",
  cancelled: "已取消",
};

const TOOL_LABELS: Record<string, string> = {
  assess_document_publication: "发布就绪检查",
  publish_document: "执行文档发布",
};

function runTone(state: string): "success" | "danger" | "warning" | "info" {
  if (state === "completed") return "success";
  if (state === "failed" || state === "cancelled") return "danger";
  if (state === "running" || state === "waiting_tool") return "info";
  return "warning";
}

export default function AgentPage() {
  const { user, isAdmin } = useAuth();
  const [documents, setDocuments] = useState<Document[]>([]);
  const [selectedID, setSelectedID] = useState("");
  const [run, setRun] = useState<AgentRun | null>(null);
  const [approvals, setApprovals] = useState<AgentApproval[]>([]);
  const [reason, setReason] = useState("");
  const [openRunID, setOpenRunID] = useState("");
  const [loadingDocs, setLoadingDocs] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let active = true;
    apiClient
      .listDocuments({ limit: 100 })
      .then((response) => {
        if (!active) return;
        const drafts = response.items.filter(
          (doc) => doc.knowledge_space_id && doc.knowledge_space_id !== "user-uploads" && doc.publication_status === "draft",
        );
        setDocuments(drafts);
        setSelectedID((current) => current || drafts[0]?.doc_id || "");
      })
      .catch((err: Error) => active && setError(err.message))
      .finally(() => active && setLoadingDocs(false));
    return () => {
      active = false;
    };
  }, []);

  const selected = useMemo(() => documents.find((doc) => doc.doc_id === selectedID) ?? null, [documents, selectedID]);
  const assessmentData = run?.steps.find((step) => step.tool_name === "assess_document_publication")?.tool_result?.data;
  const blockers = Array.isArray(assessmentData?.blockers) ? assessmentData.blockers.filter((item): item is string => typeof item === "string") : [];
  const counts = assessmentData?.index_counts as { vector?: number; text?: number } | undefined;
  const currentApproval = approvals.find((approval) => approval.status === "pending" || approval.status === "approved");
  const isSelfApproval = currentApproval?.requested_by === user?.id;

  const applyRun = async (next: AgentRun) => {
    setRun(next);
    setApprovals(next.state === "pending_approval" ? await apiClient.listAgentApprovals(next.id) : []);
    const published = next.steps.some(
      (step) => step.tool_name === "publish_document" && step.tool_result?.data?.publication_status === "published",
    );
    if (published) {
      const publishedDocumentID = next.task.startsWith("document_publication:")
        ? next.task.slice("document_publication:".length)
        : selectedID;
      setDocuments((items) =>
        items.map((doc) => (doc.doc_id === publishedDocumentID ? { ...doc, publication_status: "published" } : doc)),
      );
    }
  };

  const openRun = async () => {
    const id = openRunID.trim();
    if (!id) return;
    setBusy(true);
    setError("");
    try {
      const next = await apiClient.getAgentRun(id);
      if (next.task.startsWith("document_publication:")) {
        const documentID = next.task.slice("document_publication:".length);
        if (!documents.some((doc) => doc.doc_id === documentID)) {
          const document = await apiClient.getDocument(documentID);
          setDocuments((items) => (items.some((doc) => doc.doc_id === documentID) ? items : [...items, document]));
        }
        setSelectedID(documentID);
      }
      await applyRun(next);
      setOpenRunID("");
    } catch (err: unknown) {
      setError((err as Error).message || "无法打开运行");
    } finally {
      setBusy(false);
    }
  };

  const start = async () => {
    if (!selected) return;
    setBusy(true);
    setError("");
    setRun(null);
    setApprovals([]);
    try {
      await applyRun(await apiClient.createDocumentPublicationRun(selected.doc_id));
    } catch (err: unknown) {
      setError((err as Error).message || "发布检查失败");
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
    } catch (err: unknown) {
      setError((err as Error).message || "操作失败");
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-3">
        <div>
          <h1 className="text-xl font-semibold text-slate-900">文档发布治理</h1>
          <p className="mt-1 text-sm text-slate-500">受管草稿检查、审批与发布</p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex items-center">
            <input
              value={openRunID}
              onChange={(event) => setOpenRunID(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "Enter") void openRun();
              }}
              placeholder="打开 Run ID"
              className="h-9 w-44 rounded-l-md border border-r-0 border-slate-300 bg-white px-3 text-xs focus:border-blue-500 focus:outline-none sm:w-56"
            />
            <button
              type="button"
              onClick={() => void openRun()}
              disabled={busy || !openRunID.trim()}
              title="打开运行"
              aria-label="打开运行"
              className="flex h-9 w-9 items-center justify-center rounded-r-md border border-slate-300 bg-white text-slate-600 hover:bg-slate-50 disabled:opacity-50"
            >
              <Search className="h-4 w-4" />
            </button>
          </div>
          {run && <Badge tone={runTone(run.state)}>{STATE_LABELS[run.state] || run.state}</Badge>}
        </div>
      </div>

      {error && <div className="border-l-2 border-red-500 bg-red-50 px-4 py-3 text-sm text-red-700">{error}</div>}

      <div className="grid min-h-[560px] overflow-hidden rounded-lg border border-slate-200 bg-white lg:grid-cols-[320px_minmax(0,1fr)]">
        <aside className="border-b border-slate-200 lg:border-b-0 lg:border-r">
          <div className="border-b border-slate-200 px-4 py-3">
            <h2 className="text-sm font-semibold text-slate-800">待发布草稿</h2>
            <p className="mt-0.5 text-xs text-slate-400">{loadingDocs ? "加载中" : `${documents.length} 份受管文档`}</p>
          </div>
          <div className="max-h-[500px] overflow-y-auto p-2">
            {!loadingDocs && documents.length === 0 && (
              <div className="px-3 py-10 text-center text-sm text-slate-400">暂无待治理草稿</div>
            )}
            {documents.map((doc) => (
              <button
                key={doc.doc_id}
                type="button"
                onClick={() => {
                  setSelectedID(doc.doc_id);
                  setRun(null);
                  setApprovals([]);
                  setError("");
                }}
                className={`mb-1 w-full rounded-md px-3 py-3 text-left transition-colors ${
                  selectedID === doc.doc_id ? "bg-blue-50 text-blue-800" : "text-slate-700 hover:bg-slate-50"
                }`}
              >
                <span className="block truncate text-sm font-medium">{doc.file_name}</span>
                <span className="mt-1 flex items-center justify-between gap-2 text-xs text-slate-400">
                  <span className="truncate">{doc.knowledge_space_id}</span>
                  <span>{doc.publication_status === "published" ? "已发布" : doc.status === "completed" ? "ETL 完成" : doc.status}</span>
                </span>
              </button>
            ))}
          </div>
        </aside>

        <section className="min-w-0 p-4 sm:p-5">
          {!selected ? (
            <div className="flex min-h-[460px] items-center justify-center text-sm text-slate-400">选择一份受管草稿开始治理</div>
          ) : (
            <div className="space-y-6">
              <div className="flex flex-wrap items-start justify-between gap-3 border-b border-slate-100 pb-4">
                <div className="min-w-0">
                  <div className="flex items-center gap-2">
                    <FileCheck2 className="h-5 w-5 shrink-0 text-blue-600" />
                    <h2 className="truncate text-base font-semibold text-slate-900">{selected.file_name}</h2>
                  </div>
                  <div className="mt-2 flex flex-wrap gap-x-4 gap-y-1 text-xs text-slate-500">
                    <span>空间：{selected.knowledge_space_id}</span>
                    <span>责任人：{selected.owner || "未填写"}</span>
                    <span>生效日期：{selected.effective_date || "未填写"}</span>
                    {run && <span className="font-mono">Run {run.id}</span>}
                  </div>
                </div>
                {!run && <Button onClick={() => void start()} loading={busy} icon={Play}>开始检查</Button>}
              </div>

              {assessmentData && (
                <section>
                  <div className="mb-3 flex items-center justify-between gap-3">
                    <h3 className="text-sm font-semibold text-slate-800">发布检查</h3>
                    {counts && <span className="text-xs text-slate-400">向量 {counts.vector ?? 0} · 全文 {counts.text ?? 0}</span>}
                  </div>
                  <div className="grid gap-2 sm:grid-cols-2 xl:grid-cols-3">
                    {CHECKS.map(([code, label]) => {
                      const passed = !blockers.includes(code);
                      return (
                        <div key={code} className="flex min-h-10 items-center gap-2 rounded-md border border-slate-200 px-3 py-2 text-sm">
                          {passed ? <CheckCircle2 className="h-4 w-4 shrink-0 text-emerald-600" /> : <XCircle className="h-4 w-4 shrink-0 text-red-500" />}
                          <span className={passed ? "text-slate-700" : "text-red-700"}>{label}</span>
                        </div>
                      );
                    })}
                  </div>
                </section>
              )}

              {run && (
                <section>
                  <h3 className="mb-3 text-sm font-semibold text-slate-800">执行记录</h3>
                  <div className="space-y-0">
                    {run.steps.map((step, index) => {
                      const failed = step.type === "error" || Boolean(step.error);
                      const pending = step.state === "pending_approval";
                      return (
                        <div key={step.index} className="flex gap-3 pb-4">
                          <div className="flex w-6 shrink-0 flex-col items-center">
                            <span className={`flex h-6 w-6 items-center justify-center rounded-full ${failed ? "bg-red-100 text-red-600" : pending ? "bg-amber-100 text-amber-700" : "bg-emerald-100 text-emerald-700"}`}>
                              {failed ? <X className="h-3.5 w-3.5" /> : pending ? <CircleDashed className="h-3.5 w-3.5" /> : <Check className="h-3.5 w-3.5" />}
                            </span>
                            {index < run.steps.length - 1 && <span className="mt-1 w-px flex-1 bg-slate-200" />}
                          </div>
                          <div className="min-w-0 flex-1 pt-0.5">
                            <div className="flex flex-wrap items-center gap-2">
                              <span className="text-sm font-medium text-slate-700">{step.tool_name ? TOOL_LABELS[step.tool_name] || step.tool_name : step.type === "final" ? "工作流完成" : "工作流记录"}</span>
                              {pending && <Badge tone="warning">等待审批</Badge>}
                            </div>
                            {step.observation && <p className="mt-1 text-sm text-slate-500">{step.observation}</p>}
                            {step.error && !pending && <p className="mt-1 text-xs text-red-600">{step.error}</p>}
                          </div>
                        </div>
                      );
                    })}
                  </div>
                </section>
              )}

              {run?.state === "pending_approval" && currentApproval && (
                <section className="border-t border-slate-200 pt-4">
                  <div className="flex items-start gap-3">
                    <ShieldCheck className="mt-0.5 h-5 w-5 shrink-0 text-amber-600" />
                    <div className="min-w-0 flex-1">
                      <h3 className="text-sm font-semibold text-slate-800">管理员审批</h3>
                      <p className="mt-1 text-xs text-slate-500">请求人 {currentApproval.requested_by}，审批后才会执行发布。</p>
                      {currentApproval.status === "pending" && isAdmin && !isSelfApproval && (
                        <div className="mt-3 flex flex-wrap gap-2">
                          <input
                            value={reason}
                            onChange={(event) => setReason(event.target.value)}
                            placeholder="审批意见（可选）"
                            className="min-w-[220px] flex-1 rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-blue-500 focus:outline-none"
                          />
                          <Button size="sm" icon={Check} loading={busy} onClick={() => void act("approve")}>批准发布</Button>
                          <Button size="sm" icon={X} variant="danger" disabled={busy} onClick={() => void act("reject")}>拒绝</Button>
                        </div>
                      )}
                      {currentApproval.status === "pending" && isSelfApproval && <p className="mt-3 text-sm text-amber-700">该请求需要另一位管理员审批。</p>}
                      {currentApproval.status === "pending" && !isAdmin && <p className="mt-3 text-sm text-slate-500">等待管理员处理。</p>}
                      {currentApproval.status === "approved" && isAdmin && (
                        <Button className="mt-3" size="sm" icon={RefreshCw} loading={busy} onClick={() => void act("resume")}>继续发布</Button>
                      )}
                      {currentApproval.status === "approved" && !isAdmin && <p className="mt-3 text-sm text-slate-500">审批已通过，等待管理员继续发布。</p>}
                      {currentApproval.status === "pending" && <Button className="mt-3" size="sm" icon={Ban} variant="ghost" disabled={busy} onClick={() => void act("cancel")}>取消请求</Button>}
                    </div>
                  </div>
                </section>
              )}

              {(run?.state === "created" || run?.state === "waiting_tool") && <Button icon={RefreshCw} variant="secondary" loading={busy} onClick={() => void act("resume")}>继续执行</Button>}

              {run?.final && (
                <div className={`border-l-2 px-4 py-3 text-sm ${blockers.length === 0 ? "border-emerald-500 bg-emerald-50 text-emerald-800" : "border-amber-500 bg-amber-50 text-amber-800"}`}>
                  {run.final}
                </div>
              )}

              <Link href={`/documents/${encodeURIComponent(selected.doc_id)}`} className="inline-flex text-xs text-blue-600 hover:underline">
                查看文档详情
              </Link>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
