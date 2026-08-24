"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { useAuth } from "@/lib/auth";
import { UPLOAD_ACCEPT } from "@/lib/fileTypes";
import { CheckCircle2, ChevronRight, Copy, Loader2, XCircle } from "lucide-react";
import type { KnowledgeSpace, TaskStatus, UploadResult } from "@/lib/types";

const PIPELINE_STEPS = [
  { key: "queued", label: "上传 · Kafka 异步" },
  { key: "parsing", label: "解析 · parser-service" },
  { key: "embedding", label: "向量化 · embedding" },
  { key: "completed", label: "入库 · Qdrant + ES" },
];

// Classification is authorized server-side; the options are narrowed here so the
// UI does not offer a level the caller's role would be rejected for.
const PERMISSION_OPTIONS = [
  { value: "public", label: "public（公开）", adminOnly: false },
  { value: "internal", label: "internal（内部）", adminOnly: false },
  { value: "confidential", label: "confidential（机密）", adminOnly: true },
];

export default function DataPage() {
  const { isAdmin } = useAuth();
  const [file, setFile] = useState<File | null>(null);
  const [permission, setPermission] = useState("internal");
  const [knowledgeSpace, setKnowledgeSpace] = useState("");
  const [knowledgeSpaces, setKnowledgeSpaces] = useState<KnowledgeSpace[]>([]);
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
      const res = await apiClient.uploadDocument(file, permission, undefined, knowledgeSpace);
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
    pollRef.current = setInterval(async () => {
      try {
        const d = await apiClient.getTaskStatus(docId);
        failures = 0;
        setTaskStatus(d);
        if (d.status === "completed" || d.status === "failed") {
          stopPolling();
          setStatus("done");
        }
      } catch (e: unknown) {
        // A transient blip keeps polling, but repeated failures mean the status
        // lookup is genuinely broken — stop spinning and surface the error.
        failures += 1;
        if (failures >= 3) {
          stopPolling();
          setStatus("error");
          setError((e as Error).message || "任务状态查询失败");
        }
      }
    }, 2000);
  };

  const isDuplicate = Boolean(uploadResult?.duplicate_of);
  const backendStatus = taskStatus?.status || "";
  const stage = taskStatus?.stage || (status === "uploading" || status === "polling" ? "parsing" : "");
  const isCompleted = backendStatus === "completed";
  const isFailed = backendStatus === "failed";
  const prog =
    taskStatus && (taskStatus.total_chunks ?? 0) > 0
      ? `${taskStatus.chunks_done ?? 0}/${taskStatus.total_chunks}`
      : "";

  const stepState = (key: string): "active" | "done" | "idle" | "failed" => {
    if (isCompleted) return "done";
    if (isFailed) return "failed";
    if (backendStatus === "queued") return key === "queued" ? "active" : "idle";
    if (backendStatus === "processing") {
      if (key === "queued") return "done";
      if (key === "parsing") return stage === "parsing" ? "active" : "done";
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
        <div className="flex flex-wrap items-center gap-3">
          <input
            type="file"
            accept={UPLOAD_ACCEPT}
            onChange={(e) => setFile(e.target.files?.[0] || null)}
            className="text-sm text-slate-600 file:mr-3 file:rounded-lg file:border-0 file:bg-blue-50 file:px-4 file:py-2 file:text-sm file:font-medium file:text-blue-700 hover:file:bg-blue-100"
          />
          <select
			value={knowledgeSpace}
			onChange={(e) => setKnowledgeSpace(e.target.value)}
			aria-label="目标知识空间"
			className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700"
		  >
			{knowledgeSpaces.map((space) => <option key={space.id} value={space.id}>{space.name}</option>)}
		  </select>
		  <select
            value={permission}
            onChange={(e) => setPermission(e.target.value)}
            className="rounded-lg border border-slate-300 bg-white px-3 py-2 text-sm text-slate-700"
          >
            {permissionOptions.map((o) => (
              <option key={o.value} value={o.value}>
                {o.label}
              </option>
            ))}
          </select>
          <button
            onClick={() => void upload()}
            disabled={!file || !knowledgeSpace || status === "uploading" || status === "polling"}
            className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
          >
            {status === "uploading" || status === "polling" ? "上传中…" : "上传"}
          </button>
          {uploadResult && !isDuplicate && (
            <span className="font-mono text-xs text-slate-500">doc: {uploadResult.doc_id}</span>
          )}
        </div>
        <p className="mt-2 text-xs text-slate-400">
          文档编号由系统分配。要更新已有文档，请在
          <Link href="/documents" className="mx-1 text-blue-600 hover:underline">
            文档管理
          </Link>
          中打开该文档，使用「上传新版本」。
          {!isAdmin && <span className="ml-1 text-amber-600">机密级别需管理员上传。</span>}
		  <span className="ml-1">新文档完成处理后仍为草稿，须由管理员发布后才会用于问答。</span>
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
