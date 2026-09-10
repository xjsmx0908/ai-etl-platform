import { NextRequest } from "next/server";

export async function proxyBackend(req: NextRequest, path: string, init: RequestInit = {}): Promise<Response> {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const headers = new Headers(init.headers);
  if (token && !headers.has("Authorization")) {
    headers.set("Authorization", `Bearer ${token}`);
  }
  try {
    const upstream = await fetch(`${backend}${path}`, {
      ...init,
      headers,
      cache: "no-store",
    });
    const text = await upstream.text();
    return new Response(upstream.status === 204 ? null : text, {
      status: upstream.status,
      headers: { "Content-Type": upstream.headers.get("Content-Type") || "application/json" },
    });
  } catch {
    return Response.json({ error: "upstream temporarily unavailable", retryable: true }, { status: 503 });
  }
}
