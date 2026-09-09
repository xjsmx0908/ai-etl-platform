"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import { UPLOAD_ACCEPT } from "@/lib/fileTypes";
import { READONLY_UPLOAD_DENIED_MESSAGE, canUploadDocuments } from "@/lib/permissions";
import { CheckCircle2, ChevronRight, Copy, Loader2, XCircle } from "lucide-react";
import type { KnowledgeSpace, TaskStatus, UploadResult } from "@/lib/types";

const PIPELINE_STEPS = [
  { key: "queued", label: "上传 · Kafka 异步" },
  { key: "ocr", label: "扫描解析 · OCR" },
  { key: "parsing", label: "解析 · parser-service" },
  { key: "embedding", label: "向量化 · embedding" },
  { key: "completed", label: "入库 · Qdrant + ES" },
];
const MAX_UPLOAD_SIZE_MB = 100;

// Classification is authorized server-side; the options are narrowed here so the
// UI does not offer a level the caller's role would be rejected for.
const PERMISSION_OPTIONS = [
  { value: "public", label: "public（公开）", adminOnly: false },
  { value: "internal", label: "internal（内部）", adminOnly: false },
  { value: "confidential", label: "confidential（机密）", adminOnly: true },
];

export default function DataPage() {
  const { isAdmin, role } = useAuth();
  const canUpload = canUploadDocuments(role);
  const [file, setFile] = useState<File | null>(null);
  const [permission, setPermission] = useState("internal");
  const [knowledgeSpace, setKnowledgeSpace] = useState("");
  const [knowledgeSpaces, setKnowledgeSpaces] = useState<KnowledgeSpace[]>([]);
  const [showCreateSpace, setShowCreateSpace] = useState(false);
  const [newSpace, setNewSpace] = useState({ id: "", name: "", purpose: "" });
  const [spaceBusy, setSpaceBusy] = useState(false);
  const [spacePurposeDraft, setSpacePurposeDraft] = useState("");
  const [owner, setOwner] = useState("");
  const [effectiveDate, setEffectiveDate] = useState("");
  const [docStatus, setDocStatus] = useState<"active" | "superseded" | "archived">("active");
  const [status, setStatus] = useState<"idle" | "uploading" | "polling" | "done" | "error">("idle");
  const [uploadResult, setUploadResult] = useState<UploadResult | null>(null);
  const [taskStatus, setTaskStatus] = useState<TaskStatus | null>(null);
  const [error, setError] = useState("");
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const permissionOptions = PERMISSION_OPTIONS.filter((o) => isAdmin || !o.adminOnly);

  useEffect(() => {
    void apiClient.listKnowledgeSpaces().then(({ items }) => {
      const writable = items.filter((space) => space.kind === "production");
      setKnowledgeSpaces(writable);
      const preferred = writable.find((space) => space.is_default) || writable[0];
      if (preferred) setKnowledgeSpace(preferred.id);
    }).catch((e: Error) => setError(e.message || "知识空间加载失败"));
    return () => {
      if (pollRef.current) clearInterval(pollRef.current);
    };
  }, []);

  const selectedSpace = knowledgeSpaces.find((space) => space.id === knowledgeSpace);
  const managedUpload = selectedSpace && selectedSpace.id !== "user-uploads";
  const governanceComplete = !managedUpload || !isAdmin || (owner.trim() !== "" && effectiveDate !== "");
  useEffect(() => {
    setSpacePurposeDraft(selectedSpace?.purpose || "");
  }, [selectedSpace?.id, selectedSpace?.purpose]);

  const createSpace = async () => {
    const id = newSpace.id.trim();
    const name = newSpace.name.trim();
    const purpose = newSpace.purpose.trim();
    if (!id || !name || !purpose) return;
    setSpaceBusy(true);
    setError("");
    try {
      const created = await apiClient.createKnowledgeSpace({ id, name, kind: "production", purpose });
      setKnowledgeSpaces((items) => [...items, created]);
      setKnowledgeSpace(created.id);
      setNewSpace({ id: "", name: "", purpose: "" });
      setShowCreateSpace(false);
    } catch (e: unknown) {
      setError((e as Error).message || "创建知识空间失败");
    } finally {
      setSpaceBusy(false);
    }
  };

  const saveSpacePurpose = async () => {
    if (!selectedSpace || selectedSpace.id === "user-uploads") return;
    setSpaceBusy(true);
    setError("");
    try {
      const updated = await apiClient.updateKnowledgeSpace(selectedSpace.id, { purpose: spacePurposeDraft.trim() });
      setKnowledgeSpaces((items) => items.map((space) => space.id === updated.id ? updated : space));
    } catch (e: unknown) {
      setError((e as Error).message || "保存空间用途失败");
    } finally {
      setSpaceBusy(false);
    }
  };

  const stopPolling = () => {
    if (pollRef.current) {
      clearInterval(pollRef.current);
      pollRef.current = null;
    }
  };

  const upload = async () => {
    if (!canUpload || !file) return;
    stopPolling();
    setStatus("uploading");
    setUploadResult(null);
    setTaskStatus(null);
    setError("");
    try {
      const res = await apiClient.uploadDocument(file, permission, undefined, knowledgeSpace, managedUpload && isAdmin ? {
        docStatus,
        owner,
        effectiveDate,
      } : undefined);
      setUploadResult(res);
      // Identical content is already indexed: there is no new task to follow, and
      // polling a doc_id whose ETL finished long ago would just spin.
      if (res.duplicate_of) {
        setStatus("done");
        return;
      }
      setStatus("polling");
      poll(res.doc_id);
    } catch (e: unknown) {
      setStatus("error");
      setError((e as Error).message || "上传失败");
    }
  };

  const poll = (docId: string) => {
    let failures = 0;
    const check = async () => {
      try {
        const d = await apiClient.getTaskStatus(docId);
        failures = 0;
        setTaskStatus(d);
        if (d.status === "completed" || d.status === "failed" || d.status === "cancelled") {
          stopPolling();
          setStatus("done");
        }
      } catch (e: unknown) {
        // Keep polling through transient proxy/upstream blips. Do not replace a
        // known backend terminal status with the browser's generic "Failed to fetch".
        failures += 1;
        if (failures >= 10) {
          stopPolling();
          setStatus("error");
          setError((e as Error).message || "任务状态暂时不可用，请刷新重试");
        }
      }
    };
    void check();
    pollRef.current = setInterval(() => void check(), 2000);
  };

  const isDuplicate = Boolean(uploadResult?.duplicate_of);
  const backendStatus = taskStatus?.status || "";
  const stage = taskStatus?.stage || (status === "uploading" || status === "polling" ? "parsing" : "");
  const isCompleted = backendStatus === "completed";
  const isFailed = backendStatus === "failed";
  const isCancelled = backendStatus === "cancelled";
  const prog =
    taskStatus && (taskStatus.total_chunks ?? 0) > 0
      ? `${taskStatus.chunks_done ?? 0}/${taskStatus.total_chunks}`
      : "";
  const pageProg = taskStatus && (taskStatus.pages_total ?? 0) > 0
    ? `${taskStatus.pages_done ?? 0}/${taskStatus.pages_total}`
    : "";

  const stepState = (key: string): "active" | "done" | "idle" | "failed" => {
    if (isCompleted) return "done";
    if (isFailed) return "failed";
    if (isCancelled) return "failed";
    if (backendStatus === "queued") return key === "queued" ? "active" : "idle";
    if (backendStatus === "processing") {
      if (key === "queued") return "done";
      if (key === "parsing") return stage === "parsing" ? "active" : "done";
      if (key === "ocr") return stage === "ocr" ? "active" : stage === "parsing" || stage === "embedding" ? "done" : "idle";
      if (key === "embedding") return stage === "embedding" ? "active" : "idle";
    }
    // No status yet (initial idle or before the first poll returns): only light
    // up the first step while a task is actually in flight, never at rest.
    if (!backendStatus) {
      const inFlight = status === "uploading" || status === "polling";
      return key === "queued" && inFlight ? "active" : "idle";
    }
    return "idle";
  };

  return (
    <div className="space-y-6">
      <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
        <h2 className="mb-3 text-sm font-semibold text-slate-500">上传文档 · 异步 ETL 流程</h2>
        {role === "readonly" && (
          <p className="mb-3 rounded-lg border border-amber-200 bg-amber-50 px-3 py-2 text-sm text-amber-800">
            {READONLY_UPLOAD_DENIED_MESSAGE}
          </p>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <input
            type="file"
            accept={UPLOAD_ACCEPT}
            disabled={!canUpload}
            onChange={(e) => setFile(canUpload ? e.target.files?.[0] || null : null)}
            className="text-sm text-slate-600 file:mr-3 file:rounded-lg file:border-0 file:bg-blue-50 file:px-4 file:py-2 file:text-sm file:font-medium file:text-blue-700 hover:file:bg-blue-100 disabled:cursor-not-allowed disabled:opacity-50"
          />
          <span className="text-xs text-slate-400">单文件上限 {MAX_UPLOAD_SIZE_MB} MB</span>
          {knowledgeSpaces.length > 0 ? (
            <>
            <select
              value={knowledgeSpace}
              onChange={(e) => setKnowledgeSpace(e.target.value)}
              aria-label="目标知识空间"
              disabled={!canUpload}
              className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {knowledgeSpaces.map((space) => (
                <option key={space.id} value={space.id}>
                  {space.name}{space.id === "user-uploads" ? "（个人上传，处理完成自动发布）" : "（受管，需审批发布）"}
                </option>
              ))}
            </select>
            {isAdmin && <button type="button" onClick={() => setShowCreateSpace((value) => !value)} className="rounded-lg border border-blue-200 px-3 py-2 text-sm text-blue-700 hover:bg-blue-50">{showCreateSpace ? "收起" : "新建受管空间"}</button>}
            </>
          ) : null}
          <select
            value={permission}
            onChange={(e) => setPermission(e.target.value)}
            disabled={!canUpload}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700 disabled:cursor-not-allowed disabled:opacity-50"
          >
            {permissionOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <button
            onClick={() => void upload()}
            disabled={!canUpload || !file || !knowledgeSpace || !governanceComplete || status === "uploading" || status === "polling"}
            title={!canUpload ? READONLY_UPLOAD_DENIED_MESSAGE : undefined}
            className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {status === "uploading" || status === "polling" ? "上传中…" : "上传"}
          </button>
          {uploadResult && !isDuplicate && (
            <span className="font-mono text-xs text-slate-500">doc: {uploadResult.doc_id}</span>
          )}
          {taskStatus && (taskStatus.status === "queued" || taskStatus.status === "processing") && (
            <button onClick={() => void apiClient.cancelTask(taskStatus.doc_id).then(setTaskStatus)} className="rounded-lg border border-red-200 px-3 py-2 text-sm text-red-600 hover:bg-red-50">取消任务</button>
          )}
        </div>
        {showCreateSpace && isAdmin && (
          <div className="mt-3 flex flex-wrap items-end gap-3 rounded-lg border border-blue-100 bg-blue-50/50 p-3">
            <label className="text-xs text-slate-600">空间 ID<input value={newSpace.id} onChange={(e) => setNewSpace((v) => ({ ...v, id: e.target.value }))} placeholder="managed-demo" className="mt-1 block rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /></label>
            <label className="text-xs text-slate-600">空间名称<input value={newSpace.name} onChange={(e) => setNewSpace((v) => ({ ...v, name: e.target.value }))} placeholder="受管测试知识库" className="mt-1 block rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /></label>
            <label className="text-xs text-slate-600 sm:col-span-2">这个空间用来放什么？<textarea value={newSpace.purpose} onChange={(e) => setNewSpace((v) => ({ ...v, purpose: e.target.value }))} placeholder="例如：只放已生效的人事制度" rows={2} className="mt-1 block w-full min-w-[240px] rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /></label>
            <button type="button" onClick={() => void createSpace()} disabled={spaceBusy || !newSpace.id.trim() || !newSpace.name.trim() || !newSpace.purpose.trim()} className="rounded-lg bg-blue-600 px-4 py-2 text-sm font-medium text-white disabled:opacity-50">{spaceBusy ? "创建中…" : "创建受管空间"}</button>
          </div>
        )}
        {isAdmin && selectedSpace && selectedSpace.id !== "user-uploads" && (
          <div className="mt-3 rounded-lg border border-slate-200 bg-slate-50 p-3">
            <label className="text-xs text-slate-600">这个空间用来放什么？
              <textarea value={spacePurposeDraft} onChange={(e) => setSpacePurposeDraft(e.target.value)} placeholder="写清用途后，预审会按这句话判断材料适不适合进这个空间。" rows={2} className="mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" />
            </label>
            <div className="mt-2 flex flex-wrap items-center gap-3">
              <button type="button" onClick={() => void saveSpacePurpose()} disabled={spaceBusy || spacePurposeDraft.trim() === (selectedSpace.purpose || "").trim()} className="rounded-lg bg-slate-800 px-3 py-1.5 text-xs font-medium text-white disabled:opacity-50">{spaceBusy ? "保存中…" : "保存用途"}</button>
              <p className="text-xs text-slate-500">预审只对照这句话，不会把材料类型写进文档。</p>
            </div>
          </div>
        )}
        {managedUpload && isAdmin && file && (
          <div className="mt-3 grid gap-3 rounded-lg border border-amber-100 bg-amber-50/50 p-3 sm:grid-cols-3">
            <label className="text-xs text-slate-600">责任人 <span className="text-red-500">*</span><input value={owner} onChange={(e) => setOwner(e.target.value)} placeholder="知识管理部" className="mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /></label>
            <label className="text-xs text-slate-600">生效日期 <span className="text-red-500">*</span><input type="date" value={effectiveDate} onChange={(e) => setEffectiveDate(e.target.value)} className="mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm" /></label>
            <label className="text-xs text-slate-600">文档状态<select value={docStatus} onChange={(e) => setDocStatus(e.target.value as typeof docStatus)} className="mt-1 block w-full rounded-md border border-slate-300 bg-white px-3 py-2 text-sm"><option value="active">active（有效）</option><option value="superseded">superseded（已替代）</option><option value="archived">archived（归档）</option></select></label>
            <p className="text-xs text-amber-700 sm:col-span-3">受管文档建议上传时填写责任人和生效日期；处理完成后仍为草稿，需在知识发布中心完成 Agent 预审和管理员审批。</p>
          </div>
        )}
        <p className="mt-2 text-xs text-slate-400">
          文档编号由系统分配。要更新已有文档，请在
          <Link href="/documents" className="mx-1 text-blue-600 hover:underline">
            文档管理
          </Link>
          中打开该文档，使用「上传新版本」。
          {role === "user" && <span className="ml-1 text-amber-600">机密级别需管理员上传。</span>}
          {knowledgeSpace === "user-uploads" ? (
            <span className="ml-1 text-emerald-600">个人上传空间：处理完成后自动发布并可用于问答。</span>
          ) : knowledgeSpace ? (
            <span className="ml-1 text-amber-600">受管空间：处理完成后为草稿，须管理员发布后才会用于问答。</span>
          ) : null}
        </p>

        {/* Identical content is a normal outcome, not an error: what the user
            wanted in the knowledge base is already in it. */}
        {isDuplicate && (
          <div className="mt-4 flex items-start gap-2 rounded-lg border border-blue-200 bg-blue-50 p-3 text-sm text-blue-800">
            <Copy className="mt-0.5 h-4 w-4 shrink-0" />
            <div>
              <p className="font-medium">内容相同，已存在于知识库</p>
              <p className="mt-1 text-xs text-blue-700">
                未重复入库，避免同一份内容占用检索候选位。现有文档：
                <Link
                  href={`/documents/${encodeURIComponent(uploadResult?.duplicate_of || "")}`}
                  className="ml-1 font-mono hover:underline"
                >
                  {uploadResult?.duplicate_of}
                </Link>
              </p>
            </div>
          </div>
        )}

        <div className={`mt-5 flex items-center gap-2 ${isDuplicate ? "hidden" : ""}`}>
          {PIPELINE_STEPS.map((step, i) => {
            const state = stepState(step.key);
            const cls =
              state === "active"
                ? "border-blue-500 bg-blue-50 text-blue-700"
                : state === "done"
                  ? "border-emerald-200 bg-emerald-50 text-emerald-700"
                  : state === "failed"
                    ? "border-red-200 bg-red-50 text-red-700"
                    : "border-slate-200 bg-white text-slate-500";
            return (
              <div key={step.key} className="flex flex-1 items-center gap-2">
                <div className={`flex flex-1 items-center justify-center gap-1.5 rounded-lg border px-3 py-2 text-center text-xs ${cls}`}>
                  {state === "active" ? (
                    <Loader2 className="h-3 w-3 animate-spin" />
                  ) : state === "done" ? (
                    <CheckCircle2 className="h-3 w-3" />
                  ) : state === "failed" ? (
                    <XCircle className="h-3 w-3" />
                  ) : null}
                  {step.label}
                  {step.key === "embedding" && prog && state === "active" && (
                    <span className="font-semibold">({prog})</span>
                  )}
                  {step.key === "ocr" && pageProg && state === "active" && <span className="font-semibold">({pageProg} 页)</span>}
                </div>
                {i < PIPELINE_STEPS.length - 1 && <ChevronRight className="h-4 w-4 shrink-0 text-slate-300" />}
              </div>
            );
          })}
        </div>

        {taskStatus && (
          <div className="mt-4 rounded-lg bg-slate-50 p-3 text-xs text-slate-600">
            <span className="font-medium text-slate-700">状态：{taskStatus.status}</span>
            {taskStatus.stage && <span className="ml-2">阶段：{taskStatus.stage}</span>}
            {prog && <span className="ml-2 font-medium text-blue-700">chunk {prog}</span>}
            {pageProg && <span className="ml-2 font-medium text-blue-700">页 {pageProg}</span>}
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
