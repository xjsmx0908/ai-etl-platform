import { NextResponse } from "next/server";

export async function proxySessionManagement(token: string, path: string, method = "GET") {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}${path}`, {
      method,
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
