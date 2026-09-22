import { NextRequest } from "next/server";

// Public: what the accept page shows before the invitee types anything.
//
// No cookie is read and no Authorization header is sent. The token in the path
// is the credential, and the person holding it is by definition not logged in.
export async function GET(_req: NextRequest, { params }: { params: Promise<{ token: string }> }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const { token } = await params;

  const upstream = await fetch(`${backend}/v1/auth/invites/${encodeURIComponent(token)}`, {
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: upstream.status === 204 ? undefined : { "Content-Type": "application/json" },
  });
}
