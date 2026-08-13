import { NextRequest } from "next/server";

// Create an Agent run. auto_execute defaults to true on the backend, so the
// response is the completed run with its step timeline.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const body = await req.text();
  const authorization = req.headers.get("authorization") || "";

  const upstream = await fetch(`${backend}/v1/agent/runs`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(authorization ? { Authorization: authorization } : {}),
    },
    body,
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
