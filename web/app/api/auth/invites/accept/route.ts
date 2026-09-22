import { NextRequest } from "next/server";

// Public: turn an invite token plus a chosen password into an account.
//
// No cookie is read and no Authorization header is sent. The backend does not
// issue a session here either -- the new account logs in through the ordinary
// login path, so one code path stays responsible for issuing sessions.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const body = await req.text();

  const upstream = await fetch(`${backend}/v1/auth/invites/accept`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: upstream.status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}
