"use client";

import { useCallback, useEffect, useRef, useState } from "react";

type Source = {
  doc_id: string;
  chunk_id: string;
  content: string;
  score: number;
};

const API_BASE = process.env.NEXT_PUBLIC_API_BASE || "http://localhost:8080";
const JWT = process.env.NEXT_PUBLIC_JWT || "";

export default function Home() {
  const [question, setQuestion] = useState("");
  const [answer, setAnswer] = useState("");
  const [sources, setSources] = useState<Source[]>([]);
  const [tokens, setTokens] = useState<string>("");
  const [duration, setDuration] = useState<string>("");
  const [status, setStatus] = useState<"idle" | "streaming" | "done" | "error">("idle");
  const [error, setError] = useState("");
  const abortRef = useRef<AbortController | null>(null);

  const stream = useCallback(async (q: string) => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;

    setAnswer("");
    setSources([]);
    setTokens("");
    setDuration("");
    setStatus("streaming");
    setError("");

    try {
      const resp = await fetch(`${API_BASE}/v1/query`, {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          Accept: "text/event-stream",
          ...(JWT ? { Authorization: `Bearer ${JWT}` } : {}),
        },
        body: JSON.stringify({ question: q, top_k: 5 }),
        signal: controller.signal,
      });
      if (!resp.ok || !resp.body) {
        throw new Error(`请求失败: ${resp.status}`);
      }

      const reader = resp.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";

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
      setStatus("done");
    } catch (e: unknown) {
      if ((e as Error).name === "AbortError") return;
      setStatus("error");
      setError((e as Error).message || "未知错误");
    }
  }, []);

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
          setSources(payload.sources || []);
          break;
        case "delta":
          setAnswer((prev) => prev + (payload.text || ""));
          break;
        case "done":
          setDuration(payload.duration || "");
          if (payload.token_usage) {
            const u = payload.token_usage;
            setTokens(`${u.prompt_tokens} in / ${u.completion_tokens} out`);
          }
          break;
        case "error":
          setStatus("error");
          setError(payload.error || "服务错误");
          break;
      }
    } catch {
      // ignore malformed event
    }
  };

  useEffect(() => {
    return () => abortRef.current?.abort();
  }, []);

  const onSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    const q = question.trim();
    if (!q || status === "streaming") return;
    void stream(q);
  };

  return (
    <main className="mx-auto max-w-3xl px-4 py-8">
      <h1 className="text-2xl font-bold">AI ETL 知识问答</h1>
      <p className="mt-1 text-sm text-slate-500">
        企业文档知识库 RAG 演示 · SSE 流式输出
      </p>

      <form onSubmit={onSubmit} className="mt-6 flex gap-2">
        <input
          value={question}
          onChange={(e) => setQuestion(e.target.value)}
          placeholder="输入你的问题，例如：系统支持哪些格式的文件？"
          className="flex-1 rounded-lg border border-slate-300 px-4 py-2 focus:border-blue-500 focus:outline-none"
          disabled={status === "streaming"}
        />
        <button
          type="submit"
          disabled={status === "streaming" || !question.trim()}
          className="rounded-lg bg-blue-600 px-5 py-2 text-white hover:bg-blue-700 disabled:opacity-50"
        >
          {status === "streaming" ? "生成中…" : "提问"}
        </button>
      </form>

      {(status === "streaming" || status === "done" || status === "error") && (
        <section className="mt-6 space-y-4">
          <div className="rounded-lg border border-slate-200 bg-white p-4">
            <h2 className="text-sm font-semibold text-slate-500">回答</h2>
            {answer ? (
              <p className="mt-2 whitespace-pre-wrap leading-relaxed">{answer}</p>
            ) : (
              <p className="mt-2 text-slate-400">
                {status === "streaming" ? "等待回答…" : ""}
              </p>
            )}
            {tokens && (
              <p className="mt-3 text-xs text-slate-400">
                tokens: {tokens} · 耗时: {duration}
              </p>
            )}
          </div>

          {sources.length > 0 && (
            <div className="rounded-lg border border-slate-200 bg-white p-4">
              <h2 className="text-sm font-semibold text-slate-500">
                引用来源（{sources.length}）
              </h2>
              <ul className="mt-2 space-y-2">
                {sources.map((s) => (
                  <li key={s.chunk_id} className="text-sm">
                    <span className="font-mono text-blue-600">{s.doc_id}</span>
                    <span className="ml-2 text-slate-500">
                      score {s.score.toFixed(4)}
                    </span>
                    <p className="mt-1 line-clamp-2 text-slate-600">{s.content}</p>
                  </li>
                ))}
              </ul>
            </div>
          )}

          {status === "error" && (
            <div className="rounded-lg border border-red-200 bg-red-50 p-4 text-sm text-red-700">
              {error}
            </div>
          )}
        </section>
      )}
    </main>
  );
}
