import { NextRequest } from "next/server";

async function forward(req: NextRequest, id: string, method: "PATCH") {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const body = await req.text();
  const upstream = await fetch(`${backend}/v1/knowledge-spaces/${encodeURIComponent(id)}`, {
    method,
    headers: {
      ...(body ? { "Content-Type": "application/json" } : {}),
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body,
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(text, { status: upstream.status, headers: { "Content-Type": "application/json" } });
}

export async function PATCH(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return forward(req, id, "PATCH");
}
