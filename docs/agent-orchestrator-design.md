# Module 3 Agent Orchestrator Design

## Scope

Module 3 adds a stateful Agent Orchestrator to the existing `etl-worker` service.
The first implementation has two layers:

- `internal/agent`: durable state-machine core
- `internal/agentapi`: HTTP adapter and first real tool integration

The MVP focuses on the enterprise control plane:

- strict tool contracts
- argument validation before tool execution
- RBAC checks before tool execution
- durable Agent run and step records
- max-step protection against infinite loops
- lock-based ownership for resumable runs
- fencing tokens for stale owner protection
- optimistic version checks for stale write rejection
- Redis-backed run persistence
- pending-approval state for high-risk tools
- durable approval records with approve/reject audit
- compensation handlers for failed side-effecting tools
- public `/v1/agent/runs` API for create, inspect, resume, approve, reject, approval listing, and cancel
- real read-only tools: `rag_query` and `etl_task_status`
- OpenAI-compatible LLM Planner with structured JSON decision validation
- deterministic Rule Planner for development and tests
- Redis-backed distributed locks outside dev
- run timeout, approval timeout, and explicit cancellation
- Prometheus metrics for Agent run outcomes, tool steps, approval decisions, and lifecycle latency

Out of scope for this cut:

- full Saga workflow across multiple tools
- asynchronous approval notification channels
- external workflow engine integration

Those should be added after the core state machine is stable and tested.

## Package

```text
services/etl-worker/internal/agent
```

Main components:

```text
Planner
  -> returns the next PlanDecision from durable Run state

Orchestrator
  -> acquires lock
  -> loads Run
  -> enforces max steps
  -> persists state transitions
  -> executes Tool through Registry
  -> completes or fails Run

Registry
  -> stores ToolDefinition
  -> validates JSON arguments
  -> invokes ToolHandler

Authorizer
  -> checks tenant/user presence
  -> checks required tool permissions
  -> blocks tools requiring approval unless approved

Store
  -> persists Run and Step history with version/fencing checks

LockManager
  -> grants per-run ownership with TTL and fencing token
```

HTTP adapter:

```text
POST /v1/agent/runs
  -> create Run
  -> optionally execute until completed, failed, or pending_approval

GET /v1/agent/runs/{id}
  -> load tenant-scoped Run state

POST /v1/agent/runs/{id}/resume
  -> continue a non-terminal Run

POST /v1/agent/runs/{id}/approve
  -> approve the current pending tool and resume the Run

POST /v1/agent/runs/{id}/reject
  -> reject the current pending tool and fail the Run

GET /v1/agent/runs/{id}/approvals
  -> list durable approval audit records for a Run

POST /v1/agent/runs/{id}/cancel
  -> cancel a non-terminal Run
```

The HTTP layer converts the authenticated JWT context into an `agent.Actor`.
The state-machine core does not import HTTP or JWT packages.
Because the first tool set includes a RAG query tool, the mounted Agent routes
require both `agent` and `query` scopes.

## State Model

```text
created
-> running
-> waiting_tool
-> running
-> completed

waiting_tool
-> pending_approval
-> waiting_tool
-> running

created/running/waiting_tool/pending_approval
-> failed

created/running/waiting_tool/pending_approval
-> cancelled
```

Timeout transitions:

```text
created/running/waiting_tool/pending_approval
-> failed(error=run_timeout_exceeded)

pending_approval
-> failed(error=approval_timeout_exceeded)
```

Each tool call is persisted before the handler runs. The result or error is then
persisted on the same step. This makes a run resumable by another orchestrator
node after a process crash.

Every run stores:

```text
version
fencing_token
cancelled_by
cancel_reason
cancelled_at
```

`Store.SaveRun` requires the caller's expected version and lock fencing token.
This prevents stale owners and concurrent writers from silently overwriting a
newer state transition.

## Tool Execution Guardrails

Tool execution follows this order:

```text
1. lookup tool by name
2. validate JSON arguments against the tool schema
3. authorize actor against required tool permissions
4. enforce approval marker when required
5. execute handler with timeout
6. compensate partial side effects when required
7. persist structured ToolResult, compensation result, or failure
```

The orchestrator generates a stable idempotency key per tool call:

```text
agent:{run_id}:{step_index}:{tool_name}
```

Handlers for side-effecting tools should pass that key to the downstream
business API.

Side-effecting tools must be either idempotent or registered with a compensation
handler. Unsafe registrations are rejected by the Tool Registry.

Tools marked `RequiresApproval` move the run into `pending_approval` when the
actor has not approved that tool yet. `RunToCompletion` returns at this state
instead of spinning.

## Recovery Model

The module includes in-memory implementations for deterministic tests:

```text
MemoryStore
MemoryLockManager
```

It also includes Redis-backed run persistence and distributed locking:

```text
RedisStore
RedisLockManager
```

The lock manager uses TTL and fencing tokens so a resumed owner can reject stale
updates from an expired owner.

When a run is loaded in `waiting_tool` or `pending_approval`, the orchestrator
does not ask the planner for a new action. It resumes the persisted tool step
with the same arguments and idempotency key. This is the required recovery path
for crashes after the tool step has been persisted but before the result has
been saved.

## Lifecycle Governance

The Orchestrator enforces lifecycle rules before planning a new step or
recovering a persisted tool step:

```text
1. acquire per-run lock
2. load tenant-scoped Run
3. return immediately for terminal states
4. apply run timeout
5. apply pending approval timeout
6. continue planning or recovery only when still non-terminal
```

Run timeout is configured by `AGENT_RUN_TIMEOUT`. Approval timeout is configured
by `AGENT_APPROVAL_TIMEOUT`.

Cancellation is explicit:

```text
POST /v1/agent/runs/{id}/cancel
```

The caller must be either the original run owner or have `agent:approve`.
Terminal runs cannot be cancelled. If the run is waiting on a tool or approval,
the active step is also marked `cancelled`. If a cancellation or lifecycle timeout
closes a run with a pending approval record, the approval record is rejected so
the approval audit does not remain pending after the state machine is terminal.

## Observability

The Agent API emits low-cardinality lifecycle events through an observer
interface. The production adapter is the existing Prometheus metrics registry.

Current Agent metrics:

```text
ai_etl_agent_runs_started_total{auto_execute}
ai_etl_agent_run_completions_total{state,error_type}
ai_etl_agent_run_duration_seconds{state,error_type}
ai_etl_agent_tool_steps_total{tool_name,state}
ai_etl_agent_tool_step_duration_seconds{tool_name,state}
ai_etl_agent_approval_decisions_total{decision,tool_name}
```

The metrics intentionally avoid `run_id`, `tenant_id`, prompt text, or user id
labels. That keeps the cardinality bounded and makes the metrics safe for
Prometheus dashboards and alerts.

Alert rules cover:

```text
AgentRunFailures
AgentLifecycleTimeouts
AgentRunHighLatency
```

## Planner Strategy

The orchestrator depends only on the `agent.Planner` interface.

Current planner implementations:

```text
LLMPlanner
  -> production/default outside dev
  -> calls an OpenAI-compatible chat-completions endpoint
  -> requests a JSON object decision
  -> accepts only tool_call or final
  -> rejects unregistered tools
  -> validates tool arguments against the registered JSON schema

RulePlanner
  -> dev/test fallback
  -> calls rag_query once, then returns final from the tool result
  -> must stay minimal and must not become a second business decision engine
```

Planner selection:

```text
AGENT_PLANNER_TYPE=auto
  dev        -> rule
  non-dev    -> llm

AGENT_PLANNER_TYPE=llm
  always use LLMPlanner

AGENT_PLANNER_TYPE=rule
  allowed for dev/test
  rejected in production
```

LLM planner configuration:

```text
AGENT_PLANNER_ENDPOINT
AGENT_PLANNER_API_KEY
AGENT_PLANNER_MODEL
AGENT_PLANNER_TIMEOUT
AGENT_PLANNER_MAX_TOKENS
```

The LLM can propose a plan, but it cannot bypass the core guardrails. The
orchestrator still persists the step first, then the Registry validates
arguments, RBAC, approval, timeout, idempotency, and compensation before
executing any tool.

## Task Status Read Model

The task status read model records upload and worker lifecycle state:

```text
queued
-> processing
-> completed

queued/processing
-> failed
```

Write points:

```text
POST /v1/upload
  -> saves queued before publishing to Kafka
  -> saves failed if enqueue fails

ETL worker
  -> saves processing when a task is picked up
  -> saves completed after successful processing and ack
  -> saves failed after retries are exhausted and DLQ write succeeds
```

Persistence:

```text
TASK_STATUS_STORE=auto
  dev        -> memory
  non-dev    -> redis

TASK_STATUS_STORE=memory
  -> process-local memory, useful for unit tests and simple dev

TASK_STATUS_STORE=redis
  -> Redis store keyed by tenant_id + task_id
  -> required when API and Worker run as separate processes and status updates must be shared
```

TTL is controlled by `TASK_STATUS_TTL`.

The read model is tenant scoped. A lookup for another tenant's task returns the
same not-found result as a missing task.

## Tool Integration

Current registered production tools:

```text
rag_query
  -> validates {question, top_k}
  -> requires query permission
  -> calls query.Service.Ask in-process
  -> preserves tenant id and role-based document permissions
  -> returns answer, sources, and duration as ToolResult

etl_task_status
  -> validates {task_id}
  -> requires agent permission
  -> reads tenant-scoped TaskStatusStore
  -> returns queued, processing, completed, failed, or not_found
  -> does not reveal whether another tenant owns a task id
```

Additional tools should be registered through the same Registry path so the LLM
planner receives their contracts but cannot execute anything outside the
registered tool set.

## Validation

Current tests cover:

- schema validation blocks bad tool arguments before handler execution
- RBAC blocks unauthorized tools before handler execution
- authorized tool calls receive stable idempotency keys
- orchestrator persists tool and final steps
- another orchestrator can resume a partially completed run
- max-step guard fails a runaway planner
- lock TTL allows takeover
- stale lock release is rejected
- stale run versions are rejected
- stale fencing tokens are rejected
- pending approval blocks tool execution until approved
- waiting tool recovery reuses the same idempotency key
- failed side-effecting tools can be compensated
- unsafe side-effecting tool registration is rejected
- Agent API creates and executes a RAG-backed run
- Agent API loads tenant-scoped run state
- Agent API approves and resumes a pending tool
- Agent API rejects pending approvals
- Agent API lists durable approvals
- Agent API cancels non-terminal runs and rejects pending approval audit records
- Agent API rejects cancel/resume for terminal runs
- Agent API emits run, step, and approval observer events without duplicate read counts
- LLM Planner accepts valid tool_call and final decisions
- LLM Planner rejects invalid JSON, unregistered tools, invalid arguments, and empty final decisions
- planner config resolves `auto` to rule in dev and llm outside dev
- production config rejects `AGENT_PLANNER_TYPE=rule`
- upload API writes queued task status
- worker writes processing, completed, and failed task status
- `etl_task_status` reads tenant-scoped task state
- `etl_task_status` returns not_found for another tenant's task
- run timeout fails stale runs before planning
- approval timeout fails stale pending approvals before tool execution
- pending approval audit records are rejected when lifecycle timeout closes the run
- Prometheus exposes Agent run, step, and approval metrics
