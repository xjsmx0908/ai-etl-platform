import { NextRequest } from "next/server";

// Fetch the chunks of a single document. Auth from HttpOnly cookie.
export async function GET(req: NextRequest, { params }: { params: { id: string } }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const token = req.cookies.get("ai_etl_token")?.value || "";

  const upstream = await fetch(`${backend}/v1/documents/${encodeURIComponent(params.id)}/chunks`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
