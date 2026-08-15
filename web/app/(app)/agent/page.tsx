"use client";

import { useState } from "react";
import { apiClient } from "@/lib/apiClient";
import type { AgentRun } from "@/lib/types";

const AGENT_SUGGESTIONS = [
  "查询知识库：文档权限分几级？",
  "查询知识库：一次最多能传多大的文件？",
];

const STEP_TYPE_LABELS: Record<string, string> = {
  tool_call: "工具调用",
  final: "生成结论",
  error: "错误",
};

export default function AgentPage() {
  const [task, setTask] = useState("");
  const [run, setRun] = useState<AgentRun | null>(null);
  const [status, setStatus] = useState<"idle" | "running" | "done" | "error">("idle");
  const [error, setError] = useState("");

  const execute = async (t: string) => {
    setStatus("running");
    setRun(null);
    setError("");
    try {
      const d = await apiClient.createAgentRun(t);
      setRun(d);
      setStatus("done");
    } catch (e: unknown) {
      setStatus("error");
      setError((e as Error).message || "未知错误");
    }
  };

  return (
    <div className="space-y-6">
      <form
        onSubmit={(e) => {
          e.preventDefault();
          if (task.trim() && status !== "running") void execute(task.trim());
        }}
        className="flex flex-wrap gap-2"
      >
        <input
          value={task}
          onChange={(e) => setTask(e.target.value)}
          placeholder="给 Agent 一个任务，例如：查询知识库中的文档权限说明"
          className="min-w-[280px] flex-1 rounded-lg border border-slate-300 bg-white px-4 py-2.5 text-sm focus:border-blue-500 focus:outline-none"
          disabled={status === "running"}
        />
        <button
          type="submit"
          disabled={status === "running" || !task.trim()}
          className="rounded-lg bg-blue-600 px-5 py-2.5 text-sm font-medium text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {status === "running" ? "执行中…" : "执行"}
        </button>
        <div className="flex gap-1">
          {AGENT_SUGGESTIONS.map((s) => (
            <button
              key={s}
              type="button"
              onClick={() => void execute(s)}
              disabled={status === "running"}
              className="rounded-lg border border-slate-300 bg-white px-3 py-2.5 text-xs text-slate-600 hover:bg-slate-100 disabled:opacity-50"
            >
              {s.replace("查询知识库：", "")}
            </button>
          ))}
        </div>
      </form>

      {status === "done" && run && (
        <div className="rounded-xl border border-slate-200 bg-white p-5 shadow-sm">
          <div className="mb-4 flex items-center justify-between">
            <h2 className="text-sm font-semibold text-slate-500">Agent Run · {run.id}</h2>
            <span
              className={
                run.state === "completed"
                  ? "rounded bg-emerald-50 px-2 py-0.5 text-xs text-emerald-700"
                  : run.state === "failed"
                    ? "rounded bg-red-50 px-2 py-0.5 text-xs text-red-700"
                    : "rounded bg-amber-50 px-2 py-0.5 text-xs text-amber-700"
              }
            >
              {run.state}
            </span>
          </div>

          <div className="space-y-0">
            {run.steps.map((step) => (
              <div key={step.index} className="relative flex gap-3 pb-5">
                <div className="flex flex-col items-center">
                  <span
                    className={
                      step.type === "final"
                        ? "flex h-6 w-6 items-center justify-center rounded-full bg-emerald-100 text-xs"
                        : step.type === "error"
                          ? "flex h-6 w-6 items-center justify-center rounded-full bg-red-100 text-xs"
                          : "flex h-6 w-6 items-center justify-center rounded-full bg-blue-100 text-xs"
                    }
                  >
                    {step.type === "final" ? "✓" : step.type === "error" ? "✗" : step.index}
                  </span>
                  {step.index < run.steps.length && <span className="mt-1 w-px flex-1 bg-slate-200" />}
                </div>
                <div className="flex-1">
                  <div className="flex items-center gap-2">
                    <span className="text-sm font-medium text-slate-700">
                      {STEP_TYPE_LABELS[step.type] || step.type}
                    </span>
                    {step.tool_name && (
                      <span className="rounded bg-slate-100 px-2 py-0.5 font-mono text-xs text-blue-700">
                        {step.tool_name}
                      </span>
                    )}
                    {step.duration ? <span className="text-xs text-slate-400">{step.duration}ms</span> : null}
                  </div>
                  {step.thought && <p className="mt-1 text-xs text-slate-500">思考：{step.thought}</p>}
                  {step.observation && (
                    <p className="mt-1 line-clamp-3 text-sm text-slate-600">结果：{step.observation}</p>
                  )}
                  {step.error && <p className="mt-1 text-xs text-red-600">{step.error}</p>}
                </div>
              </div>
            ))}
          </div>

          {run.final && (
            <div className="mt-2 rounded-lg bg-slate-50 p-3">
              <p className="text-xs text-slate-500">最终回答</p>
              <p className="mt-1 text-sm text-slate-800">{run.final}</p>
            </div>
          )}
          {run.error && !run.final && <p className="mt-2 text-sm text-red-600">Run 失败：{run.error}</p>}
        </div>
      )}

      {status === "error" && (
        <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">{error}</div>
      )}
    </div>
  );
}
