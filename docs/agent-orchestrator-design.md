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
- compensation handlers for failed side-effecting tools
- public `/v1/agent/runs` API for create, inspect, resume, and approve
- first real read-only tool: `rag_query`, backed by the existing Query Service
- OpenAI-compatible LLM Planner with structured JSON decision validation
- deterministic Rule Planner for development and tests

Out of scope for this first cut:

- external human approval queue and approval audit service
- full Saga workflow across multiple tools
- Redis-backed distributed lock manager

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
```

The HTTP layer converts the authenticated JWT context into an `agent.Actor`.
The state-machine core does not import HTTP or JWT packages.
Because the first available production tool is `rag_query`, the mounted Agent
routes require both `agent` and `query` scopes.

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

Each tool call is persisted before the handler runs. The result or error is then
persisted on the same step. This makes a run resumable by another orchestrator
node after a process crash.

Every run stores:

```text
version
fencing_token
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

The first cut includes in-memory implementations for deterministic tests:

```text
MemoryStore
MemoryLockManager
```

It also includes Redis-backed run persistence:

```text
RedisStore
```

The lock manager uses TTL and fencing tokens so a resumed owner can reject stale
updates from an expired owner.

When a run is loaded in `waiting_tool` or `pending_approval`, the orchestrator
does not ask the planner for a new action. It resumes the persisted tool step
with the same arguments and idempotency key. This is the required recovery path
for crashes after the tool step has been persisted but before the result has
been saved.

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

## First Tool Integration

The first registered production tool is:

```text
rag_query
  -> validates {question, top_k}
  -> requires query permission
  -> calls query.Service.Ask in-process
  -> preserves tenant id and role-based document permissions
  -> returns answer, sources, and duration as ToolResult
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
- LLM Planner accepts valid tool_call and final decisions
- LLM Planner rejects invalid JSON, unregistered tools, invalid arguments, and empty final decisions
- planner config resolves `auto` to rule in dev and llm outside dev
- production config rejects `AGENT_PLANNER_TYPE=rule`
