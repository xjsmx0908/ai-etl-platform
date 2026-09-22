import { NextRequest } from "next/server";

// List or create invitations (admin only, enforced by the backend). Auth from
// HttpOnly cookie, injected server-side as a Bearer header.
//
// The path mirrors the backend's /v1/invites rather than nesting under
// /api/users: the backend cannot serve /v1/users/invites/{id} at all, because
// that pattern conflicts with /v1/users/{userID}/external-identities and makes
// the API refuse to start.
export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const search = req.nextUrl.search;

  const upstream = await fetch(`${backend}/v1/invites${search}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: upstream.status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}

export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const body = await req.text();

  const upstream = await fetch(`${backend}/v1/invites`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(token ? { Authorization: `Bearer ${token}` } : {}),
    },
    body,
    cache: "no-store",
  });
  const text = await upstream.text();
  // 201 with the token in it. Nothing is stripped: the caller is the
  // administrator who just created the invite, and this is the only response
  // that will ever carry the link.
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: upstream.status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}
