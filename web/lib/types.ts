// Shared API types for the AI-ETL web client.

export type Role = "admin" | "user" | "readonly";

export type User = {
  id: string;
  username: string;
  role: Role;
  tenant_id: string;
  active: boolean;
  created_at?: string;
  updated_at?: string;
};

export type LoginResponse = {
  token: string;
  expires_at: string;
  user: User;
};

export type DocumentPermission = "public" | "internal" | "confidential";

// Lifecycle of a document as a knowledge source, distinct from `status` (the ETL
// processing state). A superseded/archived document keeps its chunks indexed but
// is excluded from answer evidence.
export type DocStatus = "active" | "superseded" | "archived";

export type Document = {
  doc_id: string;
  tenant_id: string;
  file_name: string;
  permission: DocumentPermission;
  status: string;
  stage?: string;
  chunks_done?: number;
  chunks_total?: number;
  file_size?: number;
  metadata?: Record<string, unknown>;
  uploaded_by?: string;
  created_at: string;
  updated_at?: string;
  completed_at?: string;
  doc_status?: DocStatus;
  effective_date?: string; // YYYY-MM-DD
  supersedes?: string; // doc_id this version replaces
  owner?: string; // accountable owner, distinct from uploaded_by
  knowledge_space_id: string;
  publication_status: "draft" | "published" | "retired";
  deletion_status: "active" | "pending";
};

export type KnowledgeSpace = {
  id: string;
  name: string;
  kind: "production" | "demo";
  is_default: boolean;
  active: boolean;
};

// Upload outcome. `status: "duplicate"` means the exact same bytes were already
// indexed, `duplicate_of` names the document holding them, and nothing was
// ingested a second time — a normal result, not an error.
export type UploadResult = {
  doc_id: string;
  task_id: string;
  status?: string;
  duplicate_of?: string;
  message?: string;
};

export type DocumentsResponse = {
  items: Document[];
  total: number;
  limit: number;
  offset: number;
};

export type DocumentChunk = {
  chunk_id: string;
  index: number;
  content: string;
  metadata?: Record<string, string>;
};

export type DocumentChunksResponse = {
  doc_id: string;
  total: number;
  items: DocumentChunk[];
};

export type DocumentSearchResult = {
  doc_id: string;
  file_name: string;
  permission: DocumentPermission;
  status: string;
  hit_count: number;
  snippet: string;
  best_score: number;
};

export type AuditEntry = {
  actor_user_id: string;
  actor_role: string;
  action: string;
  resource_type?: string;
  resource_id?: string;
  result: string; // success | failure
  detail?: Record<string, unknown>;
  created_at: string;
};

export type AuditListResponse = {
  items: AuditEntry[];
  total: number;
  limit: number;
  offset: number;
};

export type Tenant = {
  id: string;
  name: string;
  created_at?: string;
};

export type Source = {
  doc_id: string;
  chunk_id: string;
  content: string;
  score: number;
  file_name?: string;
  effective_date?: string;
  knowledge_base_id?: string;
  applicable_scope?: string;
};

// One document involved in a disclosed conflict. The backend never decides which
// side is correct — it discloses both and leaves adjudication to a human.
export type ConflictingDoc = {
  doc_id: string;
  file_name?: string;
  effective_date?: string; // YYYY-MM-DD, empty = not tracked
  supersedes?: string;
};

export type RetrievalInfo = {
  strategy: string;
  cache_hit: boolean;
  backends: string[];
  candidate_count: number;
  backend_candidate_counts?: Record<string, number>;
  fused_candidate_count?: number;
  deduplicated_candidate_count?: number;
  selected_context_count: number;
  unique_document_count: number;
  selected_knowledge_base_id?: string;
  selected_applicable_scope?: string;
  resolved_knowledge_space_id?: string;
  resolved_knowledge_space_name?: string;
  unpublished_filtered?: number;
  exact_evidence_required: boolean;
  exact_evidence_matched: boolean;
  cross_scope_filtered?: number;
  scope_ambiguous?: boolean;
  duration_ms: number;
  max_relevance?: number;
  grounding_checked?: boolean;
  grounding_passed?: boolean;
  grounding_unavailable?: boolean;
  allowed_permissions?: string[];
  permission_role?: string;
  // Corpus governance: candidates dropped because their document is superseded
  // or archived, and conflicts among the documents that did survive.
  retired_filtered?: number;
  conflict_detected?: boolean;
  conflicting_docs?: ConflictingDoc[];
};

export type TokenUsage = {
  prompt_tokens: number;
  completion_tokens: number;
  estimated_cost_usd?: number;
};

export type AnswerMeta = {
  retrieval?: RetrievalInfo;
  token_usage?: TokenUsage;
  prompt_version?: string;
  duration?: string;
};

export type TaskStatus = {
  task_id: string;
  doc_id: string;
  status: string; // queued | processing | completed | failed
  stage?: string;
  chunks_done?: number;
  total_chunks?: number;
  error?: string;
  file_path?: string;
};

export type AgentStep = {
  index: number;
  type: string; // tool_call | final | error
  state: string;
  thought?: string;
  tool_name?: string;
  tool_arguments?: unknown;
  tool_result?: {
    content: string;
    data?: Record<string, unknown>;
  };
  observation?: string;
  error?: string;
  duration?: number;
};

export type AgentRun = {
  id: string;
  user_id?: string;
  task: string;
  state: string;
  final?: string;
  error?: string;
  steps: AgentStep[];
};

export type AgentApproval = {
  id: string;
  run_id: string;
  step_index: number;
  tool_name: string;
  status: "pending" | "approved" | "rejected";
  requested_by: string;
  requested_at: string;
  decided_by?: string;
  decided_at?: string;
  reason?: string;
};

export type HealthService = { status: string; latency_ms: number };
export type Health = {
  overall: string;
  services: Record<string, HealthService>;
};
