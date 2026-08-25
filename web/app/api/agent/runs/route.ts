import { NextRequest } from "next/server";

// Create an Agent run. Governance runs may stop at pending approval, so the
// browser follows up through the run action routes. Auth stays in HttpOnly cookie.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const body = await req.text();
  const token = req.cookies.get("ai_etl_token")?.value || "";

  const upstream = await fetch(`${backend}/v1/agent/runs`, {
    method: "POST",
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
