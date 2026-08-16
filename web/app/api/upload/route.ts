import { NextRequest } from "next/server";

// Forward the multipart upload to the backend, passing the raw body and
// content-type (which carries the boundary) straight through. Auth comes from
// the HttpOnly cookie, injected server-side as a Bearer header.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const contentType = req.headers.get("content-type") || "";
  const body = await req.arrayBuffer();

  const upstream = await fetch(`${backend}/v1/upload`, {
    method: "POST",
    headers: {
      "Content-Type": contentType,
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
