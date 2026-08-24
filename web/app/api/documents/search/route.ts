import { NextRequest } from "next/server";

// Search document content while keeping the browser session token server-side.
export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const q = req.nextUrl.searchParams.get("q")?.trim() || "";
  if (!q) {
    return Response.json({ error: "q is required" }, { status: 400 });
  }
  const upstreamURL = new URL("/v1/documents/search", backend);
  upstreamURL.searchParams.set("q", q);
  const limit = req.nextUrl.searchParams.get("limit");
  if (limit) upstreamURL.searchParams.set("limit", limit);

  const upstream = await fetch(upstreamURL, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
