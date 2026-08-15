import { NextRequest } from "next/server";

// Reset a user's password (admin only, enforced by the backend).
// Auth from HttpOnly cookie, injected server-side as a Bearer header.
export async function POST(req: NextRequest, { params }: { params: { id: string } }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const body = await req.text();

  const upstream = await fetch(`${backend}/v1/users/${encodeURIComponent(params.id)}/password`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
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
