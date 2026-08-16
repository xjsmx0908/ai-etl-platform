import { NextRequest } from "next/server";

// List audit trail with query params (limit/offset/action). Admin only, enforced
// by the backend. Auth from HttpOnly cookie, injected server-side as Bearer.
export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const search = req.nextUrl.search;

  const upstream = await fetch(`${backend}/v1/audit${search}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
