import { NextRequest } from "next/server";

// Forward the health aggregation to the backend. GET only.
export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const authorization = req.headers.get("authorization") || "";

  const upstream = await fetch(`${backend}/v1/system/health`, {
    headers: authorization ? { Authorization: authorization } : {},
    cache: "no-store",
  });
  const body = await upstream.text();
  return new Response(body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
