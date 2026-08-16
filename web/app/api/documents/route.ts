import { NextRequest } from "next/server";

// List documents with query params (limit/offset/status/permission/q).
// Auth from HttpOnly cookie, injected server-side as a Bearer header.
export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const search = req.nextUrl.search;

  const upstream = await fetch(`${backend}/v1/documents${search}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
