import { NextRequest } from "next/server";

// Fetch async processing status for a document.
export async function GET(req: NextRequest, { params }: { params: { id: string } }) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const authorization = req.headers.get("authorization") || "";
  const upstream = await fetch(`${backend}/v1/tasks/${encodeURIComponent(params.id)}`, {
    headers: authorization ? { Authorization: authorization } : {},
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
