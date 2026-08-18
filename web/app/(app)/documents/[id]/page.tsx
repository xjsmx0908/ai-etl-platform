"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { Button } from "@/components/ui/Button";
import { Modal } from "@/components/ui/Modal";
import { formatUploader, getFileTypeMeta } from "@/lib/docDisplay";
import { Upload } from "lucide-react";
import type { Document, DocumentChunk } from "@/lib/types";

const STATUS_LABELS: Record<string, string> = {
  queued: "排队中",
  pending: "等待中",
  processing: "处理中",
  completed: "已完成",
  failed: "失败",
};

const PERMISSION_LABELS: Record<string, string> = {
  public: "公开",
  internal: "内部",
  confidential: "机密",
};

// Knowledge-source lifecycle, distinct from the ETL `status` above.
const DOC_STATUS_LABELS: Record<string, string> = {
  active: "现行",
  superseded: "已被替代",
  archived: "已归档",
};

function docStatusChip(docStatus: string): string {
  return docStatus === "active" ? "bg-emerald-50 text-emerald-700" : "bg-slate-100 text-slate-500";
}

function statusChip(status: string): string {
  const s = status.toLowerCase();
  if (s === "completed") return "bg-emerald-50 text-emerald-700";
  if (s === "processing") return "bg-blue-50 text-blue-700";
  if (s === "failed") return "bg-red-50 text-red-700";
  return "bg-amber-50 text-amber-700";
}

function formatSize(bytes?: number): string {
  if (!bytes && bytes !== 0) return "—";
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(2)} MB`;
}

function formatDate(iso?: string): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "—";
  return d.toLocaleString("zh-CN", { hour12: false });
}

export default function DocumentDetailPage() {
  const { id } = useParams<{ id: string }>();
  const [doc, setDoc] = useState<Document | null>(null);
  const [chunks, setChunks] = useState<DocumentChunk[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [replaceOpen, setReplaceOpen] = useState(false);
  const [replaceFile, setReplaceFile] = useState<File | null>(null);
  const [replacing, setReplacing] = useState(false);
  const [replaceError, setReplaceError] = useState("");
  const [replaced, setReplaced] = useState(false);
  const fileInputRef = useRef<HTMLInputElement>(null);

  const load = useCallback(async () => {
    if (!id) return;
    setLoading(true);
    setError("");
    try {
      const [d, c] = await Promise.all([apiClient.getDocument(id), apiClient.getDocumentChunks(id)]);
      setDoc(d);
      setChunks(c.items);
    } catch (e) {
      const msg = (e as Error).message || "加载失败";
      setError(/404|not found/i.test(msg) ? "文档不存在或无权访问" : msg);
    } finally {
      setLoading(false);
    }
  }, [id]);

  useEffect(() => {
    void load();
  }, [load]);

  const closeReplace = () => {
    setReplaceOpen(false);
    setReplaceFile(null);
    setReplaceError("");
  };

  // Uploading with this document's own doc_id replaces it in place: the new
  // version is written and enqueued first, and only then is the previous
  // version's derived data retired. The server also enforces ownership.
  const submitReplace = async () => {
    if (!replaceFile || !doc) return;
    setReplacing(true);
    setReplaceError("");
    try {
      await apiClient.uploadDocument(replaceFile, doc.permission, doc.doc_id);
      closeReplace();
      setReplaced(true);
      await load();
    } catch (e) {
      setReplaceError((e as Error).message || "上传失败");
    } finally {
      setReplacing(false);
    }
  };

  if (loading) return <div className="p-8 text-center text-sm text-slate-400">加载中…</div>;
  if (error)
    return (
      <div className="space-y-4">
        <Link href="/documents" className="text-sm text-blue-600 hover:underline">
          ← 返回文档列表
        </Link>
        <div className="rounded-lg border border-red-200 bg-red-50 p-3 text-sm text-red-700">{error}</div>
      </div>
    );
  if (!doc) return null;

  return (
    <div className="space-y-4">
      <Link href="/documents" className="text-sm text-blue-600 hover:underline">
        ← 返回文档列表
      </Link>

      <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="text-base font-semibold text-slate-800">{doc.file_name}</h1>
          {(() => {
            const meta = getFileTypeMeta(doc.file_name);
            const Icon = meta.icon;
            return (
              <span className={`inline-flex items-center gap-1 rounded px-2 py-0.5 text-xs font-medium ${meta.badgeClass}`}>
                <Icon className={`h-3.5 w-3.5 ${meta.iconClass}`} />
                {meta.label}
              </span>
            );
          })()}
          <span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
            {PERMISSION_LABELS[doc.permission] || doc.permission}
          </span>
          <span className={`rounded px-2 py-0.5 text-xs font-medium ${statusChip(doc.status)}`}>
            {STATUS_LABELS[doc.status] || doc.status}
          </span>
          {doc.doc_status && doc.doc_status !== "active" && (
            <span className={`rounded px-2 py-0.5 text-xs font-medium ${docStatusChip(doc.doc_status)}`}>
              {DOC_STATUS_LABELS[doc.doc_status] || doc.doc_status}
            </span>
          )}
          <Button
            variant="secondary"
            size="sm"
            icon={Upload}
            className="ml-auto"
            onClick={() => setReplaceOpen(true)}
          >
            上传新版本
          </Button>
        </div>

        {replaced && (
          <div className="mt-3 rounded-lg border border-emerald-200 bg-emerald-50 p-3 text-xs text-emerald-700">
            新版本已接收，正在后台重新解析入库。旧版本的切块会在处理完成后被替换。
          </div>
        )}

        {doc.doc_status && doc.doc_status !== "active" && (
          <div className="mt-3 rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-700">
            该文档已{DOC_STATUS_LABELS[doc.doc_status] || doc.doc_status}，不再作为回答依据；切块仍保留以便追溯。
          </div>
        )}
        <dl className="mt-3 grid grid-cols-2 gap-x-6 gap-y-2 text-sm sm:grid-cols-3">
          <div>
            <dt className="text-xs text-slate-400">文档 ID</dt>
            <dd className="font-mono text-xs text-blue-600">{doc.doc_id}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">文件类型</dt>
            <dd>{getFileTypeMeta(doc.file_name).label}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">分块</dt>
            <dd>{doc.chunks_total ? `${doc.chunks_done ?? 0}/${doc.chunks_total}` : "—"}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">大小</dt>
            <dd>{formatSize(doc.file_size)}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">上传者</dt>
            <dd>
              {(() => {
                const up = formatUploader(doc.uploaded_by);
                return up.title ? <span title={up.title}>{up.label}</span> : up.label;
              })()}
            </dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">创建时间</dt>
            <dd>{formatDate(doc.created_at)}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">更新时间</dt>
            <dd>{formatDate(doc.updated_at)}</dd>
          </div>
          {doc.effective_date && (
            <div>
              <dt className="text-xs text-slate-400">生效日期</dt>
              <dd>{doc.effective_date}</dd>
            </div>
          )}
          {doc.owner && (
            <div>
              <dt className="text-xs text-slate-400">责任人</dt>
              <dd>{doc.owner}</dd>
            </div>
          )}
          {doc.supersedes && (
            <div>
              <dt className="text-xs text-slate-400">替代文档</dt>
              <dd>
                <Link
                  href={`/documents/${encodeURIComponent(doc.supersedes)}`}
                  className="font-mono text-xs text-blue-600 hover:underline"
                >
                  {doc.supersedes}
                </Link>
              </dd>
            </div>
          )}
        </dl>
        {doc.metadata && Object.keys(doc.metadata).length > 0 && (
          <div className="mt-3 border-t border-slate-100 pt-3">
            <dt className="text-xs text-slate-400">元数据</dt>
            <dd className="mt-1 flex flex-wrap gap-2">
              {Object.entries(doc.metadata).map(([k, v]) => (
                <span key={k} className="rounded bg-slate-50 px-2 py-0.5 text-xs text-slate-600">
                  {k}: {String(v)}
                </span>
              ))}
            </dd>
          </div>
        )}
      </div>

      <div className="overflow-hidden rounded-xl border border-slate-200 bg-white shadow-sm">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3">
          <h2 className="text-sm font-semibold text-slate-500">文档切块（{chunks.length}）</h2>
        </div>
        {chunks.length === 0 ? (
          <div className="p-8 text-center text-sm text-slate-400">暂无切块</div>
        ) : (
          <div className="divide-y divide-slate-100">
            {chunks.map((chunk) => (
              <details key={chunk.chunk_id} className="px-4 py-3">
                <summary className="cursor-pointer text-sm">
                  <span className="font-mono text-xs text-blue-600">#{chunk.index}</span>
                  <span className="ml-2 line-clamp-1 text-slate-600">{chunk.content}</span>
                </summary>
                <pre className="mt-2 whitespace-pre-wrap rounded-lg bg-slate-50 p-3 text-xs text-slate-700">
                  {chunk.content}
                </pre>
                {chunk.metadata && Object.keys(chunk.metadata).length > 0 && (
                  <div className="mt-2 flex flex-wrap gap-2">
                    {Object.entries(chunk.metadata).map(([k, v]) => (
                      <span key={k} className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                        {k}: {v}
                      </span>
                    ))}
                  </div>
                )}
              </details>
            ))}
          </div>
        )}
      </div>

      {replaceOpen && (
        <Modal
          title="上传新版本"
          onClose={closeReplace}
          footer={
            <>
              <Button variant="secondary" onClick={closeReplace} disabled={replacing}>
                取消
              </Button>
              <Button variant="danger" onClick={() => void submitReplace()} loading={replacing} disabled={!replaceFile}>
                确认替换
              </Button>
            </>
          }
        >
          <div className="space-y-3 text-sm text-slate-600">
            <p>
              新文件将替换文档
              <span className="mx-1 font-mono text-xs text-blue-600">{doc.doc_id}</span>
              的内容。文档编号、密级（{PERMISSION_LABELS[doc.permission] || doc.permission}）保持不变，引用该编号的链接依然有效。
            </p>
            <p className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-700">
              旧版本的切块与向量将在新版本入库后被清理，此操作不可撤销。
            </p>
            <input
              ref={fileInputRef}
              type="file"
              accept=".txt,.md,.pdf,.docx,.png,.jpg,.jpeg,.webp,.bmp"
              onChange={(e) => setReplaceFile(e.target.files?.[0] || null)}
              className="w-full text-sm text-slate-600 file:mr-3 file:rounded-lg file:border-0 file:bg-blue-50 file:px-4 file:py-2 file:text-sm file:font-medium file:text-blue-700 hover:file:bg-blue-100"
            />
            {replaceError && <p className="text-xs text-red-600">{replaceError}</p>}
          </div>
        </Modal>
      )}
    </div>
  );
}
