import { NextRequest } from "next/server";

export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const upstream = await fetch(`${backend}/v1/release-center/overview`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const body = await upstream.text();
  return new Response(upstream.status === 204 ? null : body, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
