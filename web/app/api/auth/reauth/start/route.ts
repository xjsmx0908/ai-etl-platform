import { NextRequest, NextResponse } from "next/server";

function sameOriginRequest(req: NextRequest): boolean {
  const origin = req.headers.get("origin");
  const fetchSite = req.headers.get("sec-fetch-site");
  const host = req.headers.get("host");
  if (!origin || !host || fetchSite !== "same-origin") return false;
  try {
    const originURL = new URL(origin);
    const forwardedProtocol = req.headers.get("x-forwarded-proto");
    const expectedProtocol = forwardedProtocol ? `${forwardedProtocol}:` : req.nextUrl.protocol;
    return originURL.host === host && originURL.protocol === expectedProtocol;
  } catch {
    return false;
  }
}

export async function POST(req: NextRequest) {
  if (!sameOriginRequest(req)) {
    return NextResponse.json({ error: "forbidden" }, { status: 403 });
  }
  const token = req.cookies.get("ai_etl_token")?.value || "";
  if (!token.startsWith("ps1_")) {
    return NextResponse.json({ error: "valid platform session required" }, { status: 401 });
  }
  let body: unknown;
  try {
    body = await req.json();
  } catch {
    return NextResponse.json({ error: "invalid request" }, { status: 400 });
  }
  const action = body && typeof body === "object" ? (body as { action?: unknown }).action : undefined;
  const returnTo = body && typeof body === "object" ? (body as { return_to?: unknown }).return_to : undefined;
  if (typeof action !== "string" || !action || typeof returnTo !== "string") {
    return NextResponse.json({ error: "invalid request" }, { status: 400 });
  }
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/reauth/start`, {
      method: "POST",
      headers: { Authorization: `Bearer ${token}`, "Content-Type": "application/json" },
      body: JSON.stringify({ action, return_to: returnTo }),
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    return NextResponse.json({ error: "reauthentication unavailable" }, { status: 503 });
  }
  const data = (await upstream.json().catch(() => null)) as {
    authorization_url?: unknown;
    state?: unknown;
    expires_in?: unknown;
  } | null;
  if (!upstream.ok) {
    return NextResponse.json({ error: "reauthentication unavailable" }, { status: upstream.status });
  }
  if (
    typeof data?.authorization_url !== "string" ||
    typeof data.state !== "string" ||
    typeof data.expires_in !== "number" ||
    !Number.isInteger(data.expires_in) ||
    data.expires_in <= 0 ||
    data.expires_in > 15 * 60
  ) {
    return NextResponse.json({ error: "invalid reauthentication response" }, { status: 502 });
  }
  let authorizationURL: URL;
  try {
    authorizationURL = new URL(data.authorization_url);
  } catch {
    return NextResponse.json({ error: "invalid reauthentication response" }, { status: 502 });
  }
  if (authorizationURL.protocol !== "https:") {
    return NextResponse.json({ error: "invalid reauthentication response" }, { status: 502 });
  }
  const response = NextResponse.json({ authorization_url: authorizationURL.toString() });
  response.cookies.set("ai_etl_reauth_state", data.state, {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/api/auth/oidc/callback",
    maxAge: data.expires_in,
  });
  return response;
}
