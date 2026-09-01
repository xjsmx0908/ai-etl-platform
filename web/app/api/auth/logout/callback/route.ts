import { NextRequest, NextResponse } from "next/server";

function clearLogoutState(response: NextResponse) {
  response.cookies.set("ai_etl_logout_state", "", {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/api/auth/logout/callback",
    maxAge: 0,
  });
}

function failure(req: NextRequest): NextResponse {
  const response = NextResponse.redirect(new URL("/login?error=logout_callback_failed", req.url));
  clearLogoutState(response);
  return response;
}

function safeReturnPath(value: unknown): string {
  if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//") || value.includes("\\")) {
    return "/";
  }
  return value;
}

export async function GET(req: NextRequest) {
  const state = req.nextUrl.searchParams.get("state") || "";
  const cookieState = req.cookies.get("ai_etl_logout_state")?.value || "";
  if (!state || !cookieState || state !== cookieState || req.nextUrl.searchParams.has("error")) {
    return failure(req);
  }
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/logout/callback`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ state, cookie_state: cookieState }),
      cache: "no-store",
      signal: AbortSignal.timeout(10_000),
    });
  } catch {
    return failure(req);
  }
  if (!upstream.ok) return failure(req);
  const data = (await upstream.json().catch(() => null)) as { return_to?: unknown } | null;
  const response = NextResponse.redirect(new URL(safeReturnPath(data?.return_to), req.url));
  clearLogoutState(response);
  return response;
}
