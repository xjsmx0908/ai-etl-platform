type QuerySSEStreamHandlers<TSource, TMeta> = {
  onStatus?: (progress: { stage: string; message: string; state: string }) => void;
  onSources?: (sources: TSource[]) => void;
  onDelta?: (text: string) => void;
  onReplace?: (text: string) => void;
  onDone?: (meta: TMeta) => void;
  onError?: (message: string) => void;
};

export async function consumeQuerySSEStream<TSource = unknown, TMeta = { duration: string }>(
  body: ReadableStream<Uint8Array>,
  handlers: QuerySSEStreamHandlers<TSource, TMeta>
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = "";

  const handleEvent = (raw: string): "done" | "error" | undefined => {
    const lines = raw.split("\n");
    let event = "message";
    let data = "";
    for (const line of lines) {
      if (line.startsWith("event:")) event = line.slice(6).trim();
      if (line.startsWith("data:")) data += line.slice(5).trim();
    }
    if (!data) {
      return event === "done" || event === "error" ? event : undefined;
    }
    try {
      const payload = JSON.parse(data);
      switch (event) {
        case "status":
          handlers.onStatus?.({
            stage: payload.stage || "processing",
            message: payload.message || "处理中…",
            state: payload.state || "running",
          });
          break;
        case "sources":
          handlers.onSources?.((payload.sources || []) as TSource[]);
          break;
        case "delta":
          handlers.onDelta?.(payload.text || "");
          break;
        case "replace":
          handlers.onReplace?.(payload.text || "");
          if (payload.sources || payload.citations) {
            handlers.onSources?.((payload.citations || payload.sources || []) as TSource[]);
          }
          break;
        case "done":
          handlers.onDone?.({
            duration: payload.duration || "",
            token_usage: payload.token_usage,
            prompt_version: payload.prompt_version,
            retrieval: payload.retrieval,
            answer: payload.answer,
            sources: payload.sources,
            citations: payload.citations,
            ttft: payload.ttft,
          } as TMeta);
          return "done";
        case "error":
          handlers.onError?.(payload.error || "服务错误");
          return "error";
      }
    } catch {
      if (event === "done" || event === "error") return event;
    }
    return undefined;
  };

  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      let idx;
      while ((idx = buffer.indexOf("\n\n")) >= 0) {
        const event = buffer.slice(0, idx);
        buffer = buffer.slice(idx + 2);
        if (handleEvent(event)) {
          await reader.cancel().catch(() => undefined);
          return;
        }
      }
    }
  } finally {
    try {
      reader.releaseLock();
    } catch {
      // cancel() already released the lock
    }
  }
}
