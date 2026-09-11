"use client";

import Link from "next/link";
import { useParams } from "next/navigation";
import { useCallback, useEffect, useRef, useState } from "react";
import { apiClient } from "@/lib/apiClient";
import { Button } from "@/components/ui/Button";
import { Modal } from "@/components/ui/Modal";
import { useAuth } from "@/lib/auth";
import { formatUploader, formatDurationMs, getFileTypeMeta, spaceLabel } from "@/lib/docDisplay";
import { UPLOAD_ACCEPT } from "@/lib/fileTypes";
import { PageHeader } from "@/components/ui/PageHeader";
import { Bot, FileText, Upload, Pencil } from "lucide-react";
import type { Document, DocumentChunk, KnowledgeSpace } from "@/lib/types";

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
  active: "有效",
  superseded: "已替代",
  archived: "归档",
};

const PUBLICATION_LABELS: Record<string, string> = {
  draft: "草稿",
  published: "已发布",
  retired: "已退役",
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
  const { user, isAdmin } = useAuth();
  const [doc, setDoc] = useState<Document | null>(null);
  const [chunks, setChunks] = useState<DocumentChunk[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [replaceOpen, setReplaceOpen] = useState(false);
  const [replaceFile, setReplaceFile] = useState<File | null>(null);
  const [replacing, setReplacing] = useState(false);
  const [replaceError, setReplaceError] = useState("");
  const [replaced, setReplaced] = useState(false);
  const [updatingPublication, setUpdatingPublication] = useState(false);
  const [editingGovernance, setEditingGovernance] = useState(false);
  const [governanceForm, setGovernanceForm] = useState({ owner: "", effective_date: "", doc_status: "active" as "active" | "superseded" | "archived", supersedes: "" });
  const [savingGovernance, setSavingGovernance] = useState(false);
  const [spaces, setSpaces] = useState<KnowledgeSpace[]>([]);
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

  useEffect(() => {
    void apiClient.listKnowledgeSpaces().then((data) => setSpaces(data.items)).catch(() => undefined);
  }, []);

  const closeReplace = () => {
    setReplaceOpen(false);
    setReplaceFile(null);
    setReplaceError("");
  };

  // The server enforces ownership on replace (only the uploader or an admin may
  // overwrite a document). Mirror it here so the action is not offered at all
  // when it would be rejected — but keep the check best-effort: a mismatch in
  // identity (e.g. stale localStorage) still surfaces a clear server error.
  const canReplace = !!(doc && (isAdmin || (user?.id && doc.uploaded_by === user.id)));

  // Uploading with this document's own doc_id replaces it in place: the new
  // version is written and enqueued first, and only then is the previous
  // version's derived data retired. The server also enforces ownership.
  const submitReplace = async () => {
    if (!replaceFile || !doc) return;
    setReplacing(true);
    setReplaceError("");
    try {
      await apiClient.uploadDocument(replaceFile, doc.permission, doc.doc_id, doc.knowledge_space_id);
      closeReplace();
      setReplaced(true);
      await load();
    } catch (e) {
      setReplaceError((e as Error).message || "上传失败");
    } finally {
      setReplacing(false);
    }
  };

  const updatePublication = async (publicationStatus: "draft" | "published" | "retired") => {
    if (!doc) return;
    setUpdatingPublication(true);
    setError("");
    try {
      setDoc(await apiClient.updateDocumentPublication(doc.doc_id, publicationStatus));
    } catch (e) {
      setError((e as Error).message || "发布状态更新失败");
    } finally {
      setUpdatingPublication(false);
    }
  };

  const openGovernance = () => {
    if (!doc) return;
    setGovernanceForm({ owner: doc.owner || "", effective_date: doc.effective_date || "", doc_status: doc.doc_status || "active", supersedes: doc.supersedes || "" });
    setEditingGovernance(true);
  };

  const saveGovernance = async () => {
    if (!doc || !governanceForm.owner.trim() || !governanceForm.effective_date) return;
    setSavingGovernance(true);
    setError("");
    try {
      setDoc(await apiClient.updateDocumentGovernance(doc.doc_id, { ...governanceForm, owner: governanceForm.owner.trim(), supersedes: governanceForm.supersedes.trim() || undefined }));
      setEditingGovernance(false);
    } catch (e) {
      setError((e as Error).message || "治理信息保存失败");
    } finally {
      setSavingGovernance(false);
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

      <PageHeader
        icon={FileText}
        title={doc.file_name}
        description="查看发布状态、治理信息与切块内容"
        actions={
          <Button
            variant="secondary"
            size="sm"
            icon={Upload}
            onClick={() => setReplaceOpen(true)}
            disabled={!canReplace}
            title={
              isAdmin
                ? ""
                : user?.id && doc.uploaded_by === user.id
                  ? "上传新版本替换当前文档"
                  : doc?.uploaded_by
                    ? "仅该文档的上传者或管理员可上传新版本"
                    : "仅管理员可上传新版本（未知上传者）"
            }
          >
            上传新版本
          </Button>
        }
      />

      <div className="rounded-xl border border-slate-200 bg-white p-4 shadow-sm">
        <div className="flex flex-wrap items-center gap-2">
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
          <span className={`rounded px-2 py-0.5 text-xs font-medium ${doc.publication_status === "published" ? "bg-emerald-50 text-emerald-700" : "bg-amber-50 text-amber-700"}`}>
            {PUBLICATION_LABELS[doc.publication_status] || doc.publication_status}
          </span>
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
			<dt className="text-xs text-slate-400">知识空间</dt>
			<dd>{spaceLabel(doc.knowledge_space_id, spaces)}</dd>
		  </div>
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
            <dt className="text-xs text-slate-400">解析</dt>
            <dd>{formatDurationMs(doc.stage_timings?.parse_ms)}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">扫描识别</dt>
            <dd>{formatDurationMs(doc.stage_timings?.ocr_ms)}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">向量化</dt>
            <dd>{formatDurationMs(doc.stage_timings?.embed_ms)}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">入库</dt>
            <dd>{formatDurationMs(doc.stage_timings?.store_ms)}</dd>
          </div>
          <div>
            <dt className="text-xs text-slate-400">入库合计</dt>
            <dd>{formatDurationMs(doc.stage_timings?.total_ms)}</dd>
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
        {isAdmin && doc.knowledge_space_id !== "user-uploads" && doc.publication_status === "draft" && (
          <div className="mt-4 rounded-lg border border-blue-100 bg-blue-50/50 p-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <div><p className="text-sm font-medium text-slate-700">发布信息</p><p className="mt-1 text-xs text-slate-500">补齐责任人和生效日期不会自动发布，保存后仍需经过知识发布中心的预审与审批。</p></div>
              <Button size="sm" variant="secondary" icon={Pencil} onClick={openGovernance}>编辑治理信息</Button>
            </div>
          </div>
        )}
        {isAdmin && (
          <div className="mt-4 flex flex-wrap gap-2 border-t border-slate-100 pt-3">
            {doc.knowledge_space_id !== "user-uploads" && doc.publication_status === "draft" ? (
              <Link href="/release-center" className="inline-flex items-center gap-1.5 rounded-lg bg-blue-600 px-2.5 py-1.5 text-xs font-medium text-white hover:bg-blue-700">
                <Bot className="h-4 w-4" />
                进入知识发布中心
              </Link>
            ) : (
              <Button size="sm" onClick={() => void updatePublication("published")} loading={updatingPublication} disabled={doc.publication_status === "published" || doc.status !== "completed"}>
                发布
              </Button>
            )}
            <Button size="sm" variant="secondary" onClick={() => void updatePublication("draft")} disabled={updatingPublication || doc.publication_status === "draft"}>
              退回草稿
            </Button>
            <Button size="sm" variant="danger" onClick={() => void updatePublication("retired")} disabled={updatingPublication || doc.publication_status === "retired"}>
              退役
            </Button>
          </div>
        )}
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

      {editingGovernance && (
        <Modal title="编辑治理信息" onClose={() => setEditingGovernance(false)}>
          <div className="space-y-3">
            <label className="block text-xs font-medium text-slate-600">责任人<input value={governanceForm.owner} onChange={(e) => setGovernanceForm((v) => ({ ...v, owner: e.target.value }))} className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2 text-sm" /></label>
            <label className="block text-xs font-medium text-slate-600">生效日期<input type="date" value={governanceForm.effective_date} onChange={(e) => setGovernanceForm((v) => ({ ...v, effective_date: e.target.value }))} className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2 text-sm" /></label>
            <label className="block text-xs font-medium text-slate-600">文档状态<select value={governanceForm.doc_status} onChange={(e) => setGovernanceForm((v) => ({ ...v, doc_status: e.target.value as typeof governanceForm.doc_status }))} className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2 text-sm"><option value="active">有效</option><option value="superseded">已替代</option><option value="archived">归档</option></select></label>
            <label className="block text-xs font-medium text-slate-600">替代文档（可选）<input value={governanceForm.supersedes} onChange={(e) => setGovernanceForm((v) => ({ ...v, supersedes: e.target.value }))} className="mt-1 w-full rounded-md border border-slate-300 px-3 py-2 text-sm" /></label>
            <div className="flex justify-end gap-2 pt-2"><Button variant="secondary" onClick={() => setEditingGovernance(false)}>取消</Button><Button loading={savingGovernance} disabled={!governanceForm.owner.trim() || !governanceForm.effective_date} onClick={() => void saveGovernance()}>保存治理信息</Button></div>
          </div>
        </Modal>
      )}

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
              新文件将替换「{doc.file_name}」的内容。密级（{PERMISSION_LABELS[doc.permission] || doc.permission}）保持不变，原文档链接仍然有效。
            </p>
            <p className="font-mono text-[11px] text-slate-400">{doc.doc_id}</p>
            <p className="rounded-lg border border-amber-200 bg-amber-50 p-3 text-xs text-amber-700">
              旧版本的切块与向量将在新版本入库后被清理，此操作不可撤销。
            </p>
            <input
              ref={fileInputRef}
              type="file"
              accept={UPLOAD_ACCEPT}
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
