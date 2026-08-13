import { NextRequest } from "next/server";

// Forward the multipart upload to the backend, passing the raw body and
// content-type (which carries the boundary) straight through.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const authorization = req.headers.get("authorization") || "";
  const contentType = req.headers.get("content-type") || "";
  const body = await req.arrayBuffer();

  const upstream = await fetch(`${backend}/v1/upload`, {
    method: "POST",
    headers: {
      "Content-Type": contentType,
      ...(authorization ? { Authorization: authorization } : {}),
    },
    body,
    cache: "no-store",
  });
  const text = await upstream.text();
  return new Response(text, {
    status: upstream.status,
    headers: { "Content-Type": "application/json" },
  });
}
