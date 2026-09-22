import { NextRequest } from "next/server";

// Revoke a pending invitation (admin only, enforced by the backend). Auth from
// HttpOnly cookie, injected server-side as a Bearer header.
export async function DELETE(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const { id } = await params;

  const upstream = await fetch(`${backend}/v1/invites/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  // 204 No Content must not carry a body; `new Response(text, 204)` makes
  // Next.js throw and surface as a 500 even though the backend succeeded.
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: upstream.status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}
