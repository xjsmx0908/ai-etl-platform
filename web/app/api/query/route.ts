import { NextRequest } from "next/server";

// Forward /api/query to the backend, streaming the SSE response through so the
// browser only talks to this origin. The backend lives at BACKEND_URL (set in
// compose); the query-api streaming endpoint needs Accept: text/event-stream.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const body = await req.text();
  const authorization = req.headers.get("authorization") || "";

  const upstream = await fetch(`${backend}/v1/query`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: "text/event-stream",
      ...(authorization ? { Authorization: authorization } : {}),
    },
    body,
    // Do not buffer: keep the SSE stream flowing token by token.
    cache: "no-store",
  });

  const passthroughHeaders: Record<string, string> = {
    "Content-Type": "text/event-stream",
    "Cache-Control": "no-cache, no-transform",
    Connection: "keep-alive",
  };
  if (!upstream.ok) {
    passthroughHeaders["Content-Type"] = "application/json";
  }
  return new Response(upstream.body, {
    status: upstream.status,
    headers: passthroughHeaders,
  });
}
