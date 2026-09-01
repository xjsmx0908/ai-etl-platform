import { NextRequest, NextResponse } from "next/server";
import { isSameOriginRequest } from "@/lib/authSecurity";

function clearSessionCookie(response: NextResponse) {
  response.cookies.set("ai_etl_token", "", {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    maxAge: 0,
    secure: process.env.SESSION_CORE_ENABLED === "true" || process.env.COOKIE_SECURE === "true",
  });
}

function completeLocalLogout(): NextResponse {
  const response = new NextResponse(null, { status: 204 });
  clearSessionCookie(response);
  return response;
}

export async function POST(req: NextRequest) {
  if (!isSameOriginRequest(req)) {
    return NextResponse.json({ error: "forbidden" }, { status: 403 });
  }
  const token = req.cookies.get("ai_etl_token")?.value || "";
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const returnTo = req.nextUrl.searchParams.get("return_to") || "/";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/logout?return_to=${encodeURIComponent(returnTo)}`, {
      method: "POST",
      headers: token ? { Authorization: `Bearer ${token}` } : {},
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    return NextResponse.json({ error: "logout unavailable" }, { status: 503 });
  }
  if (!upstream.ok) {
    return NextResponse.json({ error: "logout unavailable" }, { status: upstream.status });
  }
  if (upstream.status === 204) {
    return completeLocalLogout();
  }
  const data = (await upstream.json().catch(() => null)) as {
    authorization_url?: unknown;
    state?: unknown;
    expires_in?: unknown;
  } | null;
  if (
    typeof data?.authorization_url !== "string" ||
    typeof data.state !== "string" ||
    !data.state ||
    typeof data.expires_in !== "number" ||
    !Number.isInteger(data.expires_in) ||
    data.expires_in <= 0 ||
    data.expires_in > 15 * 60
  ) {
    return completeLocalLogout();
  }
  let authorizationURL: URL;
  try {
    authorizationURL = new URL(data.authorization_url);
  } catch {
    return completeLocalLogout();
  }
  if (authorizationURL.protocol !== "https:" || authorizationURL.username || authorizationURL.password) {
    return completeLocalLogout();
  }
  const response = NextResponse.json({ authorization_url: authorizationURL.toString() });
  clearSessionCookie(response);
  response.cookies.set("ai_etl_logout_state", data.state, {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/api/auth/logout/callback",
    maxAge: data.expires_in,
  });
  return response;
}
