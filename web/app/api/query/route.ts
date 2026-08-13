import { NextRequest } from "next/server";

// Forward /api/query to the backend so the browser only talks to this origin.
// The client's Accept header is passed through: text/event-stream gets SSE,
// otherwise the backend returns JSON.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const body = await req.text();
  const authorization = req.headers.get("authorization") || "";
  const accept = req.headers.get("accept") || "application/json";

  const upstream = await fetch(`${backend}/v1/query`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      Accept: accept,
      ...(authorization ? { Authorization: authorization } : {}),
    },
    body,
    // Do not buffer: keep the SSE stream flowing token by token.
    cache: "no-store",
  });

  const passthroughHeaders: Record<string, string> = {
    "Cache-Control": "no-cache, no-transform",
  };
  if (accept.includes("text/event-stream")) {
    passthroughHeaders["Content-Type"] = "text/event-stream";
    passthroughHeaders["Connection"] = "keep-alive";
  } else {
    passthroughHeaders["Content-Type"] = "application/json";
  }
  return new Response(upstream.body, {
    status: upstream.status,
    headers: passthroughHeaders,
  });
}
