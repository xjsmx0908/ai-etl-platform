import { NextRequest } from "next/server";

const ACTIONS = new Set(["approve", "reject", "resume", "cancel", "approvals"]);

async function proxy(req: NextRequest, params: Promise<{ id: string; action: string }>, method: "GET" | "POST") {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const { id, action } = await params;
  if (!ACTIONS.has(action) || (method === "GET" && action !== "approvals") || (method === "POST" && action === "approvals")) {
    return new Response(JSON.stringify({ error: "not found" }), {
      status: 404,
      headers: { "Content-Type": "application/json" },
    });
  }
  const body = method === "POST" ? await req.text() : undefined;
  const upstream = await fetch(`${backend}/v1/agent/runs/${encodeURIComponent(id)}/${action}`, {
    method,
    headers: {
      ...(body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body,
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}

export async function GET(req: NextRequest, { params }: { params: Promise<{ id: string; action: string }> }) {
  return proxy(req, params, "GET");
}

export async function POST(req: NextRequest, { params }: { params: Promise<{ id: string; action: string }> }) {
  return proxy(req, params, "POST");
}
