import { NextRequest, NextResponse } from "next/server";
import { isSameOriginRequest } from "@/lib/authSecurity";

export async function DELETE(
  req: NextRequest,
  context: { params: Promise<{ handle: string }> },
) {
  if (!isSameOriginRequest(req)) {
    return NextResponse.json({ error: "forbidden" }, { status: 403 });
  }
  const token = req.cookies.get("ai_etl_token")?.value;
  if (!token) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { handle } = await context.params;
  if (!/^sm1_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(handle)) {
    return NextResponse.json({ error: "invalid session handle" }, { status: 400 });
  }
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/sessions/${encodeURIComponent(handle)}`, {
      method: "DELETE",
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
    headers: upstream.status === 204 ? undefined : {
      "Content-Type": upstream.headers.get("Content-Type") || "application/json",
    },
  });
}
