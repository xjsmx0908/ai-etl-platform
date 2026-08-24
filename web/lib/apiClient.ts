import type {
  AgentRun,
  AnswerMeta,
  AuditListResponse,
  Document,
  DocumentChunksResponse,
  DocumentSearchResult,
  DocumentsResponse,
  Health,
  LoginResponse,
	KnowledgeSpace,
  Source,
  TaskStatus,
  UploadResult,
  User,
} from "./types";
import { clearUser, setUser } from "./auth";

// Every call goes through /api/* (same-origin) so the HttpOnly `ai_etl_token`
// cookie is sent automatically by the browser. The Next.js route handlers
// turn that cookie into `Authorization: Bearer <token>` on the upstream call.

const BASE = "/api";

type RequestOptions = RequestInit & { redirectOn401?: boolean };

async function request<T>(path: string, init: RequestOptions = {}): Promise<T> {
  const { redirectOn401 = true, ...fetchInit } = init;
  const isFormData = typeof FormData !== "undefined" && fetchInit.body instanceof FormData;
  const resp = await fetch(`${BASE}${path}`, {
    ...fetchInit,
    headers: {
      ...(fetchInit.body && !isFormData ? { "Content-Type": "application/json" } : {}),
      ...(fetchInit.headers || {}),
    },
  });

  if (resp.status === 204) return undefined as T;

  const text = await resp.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = text;
  }

  if (!resp.ok) {
    const message =
      data && typeof data === "object" && typeof (data as { error?: unknown }).error === "string"
        ? ((data as { error: string }).error as string)
        : `请求失败: ${resp.status}`;
    if (resp.status === 401 && redirectOn401) {
      clearUser();
      if (typeof window !== "undefined") window.location.replace("/login");
    }
    throw new Error(message);
  }
  return data as T;
}

// ── Auth ─────────────────────────────────────────────────────────────────

export async function login(username: string, password: string): Promise<User> {
  const resp = await request<LoginResponse>("/auth/login", {
    method: "POST",
    body: JSON.stringify({ username, password }),
    redirectOn401: false,
  });
  setUser(resp.user);
  return resp.user;
}

export async function logout(): Promise<void> {
  await request<void>("/auth/logout", { method: "POST" });
  clearUser();
}

// ── Documents ────────────────────────────────────────────────────────────

export type ListDocumentsParams = {
  limit?: number;
  offset?: number;
  status?: string;
  permission?: string;
  q?: string;
};

export async function listDocuments(params: ListDocumentsParams = {}): Promise<DocumentsResponse> {
  const sp = new URLSearchParams();
  if (params.limit != null) sp.set("limit", String(params.limit));
  if (params.offset != null) sp.set("offset", String(params.offset));
  if (params.status) sp.set("status", params.status);
  if (params.permission) sp.set("permission", params.permission);
  if (params.q) sp.set("q", params.q);
  const qs = sp.toString();
  return request<DocumentsResponse>(`/documents${qs ? `?${qs}` : ""}`);
}

export async function getDocument(id: string): Promise<Document> {
  return request<Document>(`/documents/${encodeURIComponent(id)}`);
}

export async function getDocumentChunks(id: string): Promise<DocumentChunksResponse> {
  return request<DocumentChunksResponse>(`/documents/${encodeURIComponent(id)}/chunks`);
}

export async function searchDocuments(
  q: string,
  limit = 100
): Promise<{ total: number; items: DocumentSearchResult[] }> {
  const sp = new URLSearchParams({ q, limit: String(limit) });
  return request(`/documents/search?${sp.toString()}`);
}

export async function deleteDocument(id: string): Promise<void> {
  return request<void>(`/documents/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function updateDocumentPublication(id: string, publicationStatus: "draft" | "published" | "retired"): Promise<Document> {
  return request<Document>(`/documents/${encodeURIComponent(id)}`, {
    method: "PATCH",
    body: JSON.stringify({ publication_status: publicationStatus }),
  });
}

export async function listKnowledgeSpaces(): Promise<{ items: KnowledgeSpace[] }> {
  return request<{ items: KnowledgeSpace[] }>("/knowledge-spaces");
}

// ── Upload / Task ────────────────────────────────────────────────────────

// Passing docId replaces that document with a new version (the caller must own
// it, or be an admin). Omitting it creates a new document, and identical content
// is recognised as a duplicate instead of being ingested twice.
export async function uploadDocument(
  file: File,
  permission: string,
  docId?: string,
  knowledgeSpaceId?: string
): Promise<UploadResult> {
  const fd = new FormData();
  fd.append("file", file);
  fd.append("permission", permission);
  if (docId?.trim()) fd.append("doc_id", docId.trim());
  if (knowledgeSpaceId?.trim()) fd.append("knowledge_space_id", knowledgeSpaceId.trim());
  return request<UploadResult>("/upload", { method: "POST", body: fd });
}

export async function getTaskStatus(docId: string): Promise<TaskStatus> {
  return request<TaskStatus>(`/tasks/${encodeURIComponent(docId)}`);
}

// ── Users (admin only) ───────────────────────────────────────────────────

export type CreateUserParams = {
  username: string;
  password: string;
  role: string;
  tenant_id: string;
  active?: boolean;
};

export type UpdateUserParams = {
  role?: string;
  tenant_id?: string;
  active?: boolean;
};

export async function listUsers(params: { limit?: number; offset?: number } = {}): Promise<{ items: User[]; total: number }> {
  const sp = new URLSearchParams();
  if (params.limit != null) sp.set("limit", String(params.limit));
  if (params.offset != null) sp.set("offset", String(params.offset));
  const qs = sp.toString();
  return request<{ items: User[]; total: number }>(`/users${qs ? `?${qs}` : ""}`);
}

export async function createUser(params: CreateUserParams): Promise<User> {
  return request<User>("/users", { method: "POST", body: JSON.stringify(params) });
}

export async function updateUser(id: string, params: UpdateUserParams): Promise<User> {
  return request<User>(`/users/${encodeURIComponent(id)}`, {
    method: "PUT",
    body: JSON.stringify(params),
  });
}

export async function deleteUser(id: string): Promise<void> {
  return request<void>(`/users/${encodeURIComponent(id)}`, { method: "DELETE" });
}

export async function setUserPassword(id: string, password: string): Promise<void> {
  return request<void>(`/users/${encodeURIComponent(id)}/password`, {
    method: "POST",
    body: JSON.stringify({ password }),
  });
}

// ── Audit (admin only) ───────────────────────────────────────────────────

export type ListAuditParams = {
  limit?: number;
  offset?: number;
  action?: string;
};

export async function listAudit(params: ListAuditParams = {}): Promise<AuditListResponse> {
  const sp = new URLSearchParams();
  if (params.limit != null) sp.set("limit", String(params.limit));
  if (params.offset != null) sp.set("offset", String(params.offset));
  if (params.action) sp.set("action", params.action);
  const qs = sp.toString();
  return request<AuditListResponse>(`/audit${qs ? `?${qs}` : ""}`);
}

// ── System / Agent / Query ───────────────────────────────────────────────

export async function getHealth(): Promise<Health> {
  return request<Health>("/system/health");
}

export async function createAgentRun(task: string): Promise<AgentRun> {
  return request<AgentRun>("/agent/runs", {
    method: "POST",
    body: JSON.stringify({ task, auto_execute: true }),
  });
}

export type QuerySSEHandlers = {
  onSources?: (sources: Source[]) => void;
  onDelta?: (text: string) => void;
  onDone?: (meta: AnswerMeta) => void;
  onError?: (message: string) => void;
};

export async function querySSE(
  question: string,
  handlers: QuerySSEHandlers,
  signal?: AbortSignal,
  filters?: { knowledge_space_id?: string }
): Promise<void> {
  const resp = await fetch(`${BASE}/query`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Accept: "text/event-stream" },
    body: JSON.stringify({ question, top_k: 5, ...filters }),
    signal,
  });

  if (resp.status === 401) {
    clearUser();
    if (typeof window !== "undefined") window.location.replace("/login");
    throw new Error("未登录或登录已过期");
  }
  if (!resp.ok) throw new Error(`请求失败: ${resp.status}`);
  if (!resp.body) throw new Error("服务无响应");

  const reader = resp.body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

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
          handlers.onSources?.(payload.sources || []);
          break;
        case "delta":
          handlers.onDelta?.(payload.text || "");
          break;
        case "done":
          handlers.onDone?.({
            duration: payload.duration || "",
            token_usage: payload.token_usage,
            prompt_version: payload.prompt_version,
            retrieval: payload.retrieval,
          });
          break;
        case "error":
          handlers.onError?.(payload.error || "服务错误");
          break;
      }
    } catch {
      // ignore malformed events
    }
  };

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
}

export const apiClient = {
  login,
  logout,
  listDocuments,
  getDocument,
  getDocumentChunks,
  searchDocuments,
  deleteDocument,
	updateDocumentPublication,
	listKnowledgeSpaces,
  uploadDocument,
  getTaskStatus,
  listUsers,
  createUser,
  updateUser,
  deleteUser,
  setUserPassword,
  listAudit,
  getHealth,
  createAgentRun,
  querySSE,
};
