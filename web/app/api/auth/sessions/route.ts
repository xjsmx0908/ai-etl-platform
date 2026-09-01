import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
  const token = req.cookies.get("ai_etl_token")?.value;
  if (!token) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/sessions`, {
      headers: { Authorization: `Bearer ${token}` },
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    return NextResponse.json({ error: "session management unavailable" }, { status: 503 });
  }
  const text = await upstream.text();
  return new NextResponse(upstream.status === 204 ? null : text, {
    status: upstream.status,
    headers: { "Content-Type": upstream.headers.get("Content-Type") || "application/json" },
  });
}
