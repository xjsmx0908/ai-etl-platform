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
};

export type DocumentsResponse = {
  items: Document[];
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
};

export type RetrievalInfo = {
  strategy: string;
  cache_hit: boolean;
  backends: string[];
  candidate_count: number;
  duration_ms: number;
  max_relevance?: number;
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
  observation?: string;
  error?: string;
  duration?: number;
};

export type AgentRun = {
  id: string;
  task: string;
  state: string;
  final?: string;
  error?: string;
  steps: AgentStep[];
};

export type HealthService = { status: string; latency_ms: number };
export type Health = Record<string, HealthService>;
