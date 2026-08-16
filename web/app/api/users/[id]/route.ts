import { NextRequest } from "next/server";

// Read, update, or delete a single user (admin only, enforced by the backend).
// Auth from HttpOnly cookie, injected server-side as a Bearer header.
export async function GET(req: NextRequest, { params }: { params: { id: string } }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";

  const upstream = await fetch(`${backend}/v1/users/${encodeURIComponent(params.id)}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}

export async function PUT(req: NextRequest, { params }: { params: { id: string } }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const body = await req.text();

  const upstream = await fetch(`${backend}/v1/users/${encodeURIComponent(params.id)}`, {
    method: "PUT",
    headers: {
      "Content-Type": "application/json",
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

export async function DELETE(req: NextRequest, { params }: { params: { id: string } }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";

  const upstream = await fetch(`${backend}/v1/users/${encodeURIComponent(params.id)}`, {
    method: "DELETE",
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
