import { NextRequest, NextResponse } from "next/server";

function clearState(response: NextResponse) {
  response.cookies.set("ai_etl_oidc_state", "", {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/api/auth/oidc/callback",
    maxAge: 0,
  });
}

export async function GET(req: NextRequest) {
  const code = req.nextUrl.searchParams.get("code") || "";
  const state = req.nextUrl.searchParams.get("state") || "";
  const cookieState = req.cookies.get("ai_etl_oidc_state")?.value || "";
  if (!code || !state || !cookieState || req.nextUrl.searchParams.has("error")) {
    const response = NextResponse.redirect(new URL("/login?error=oidc_failed", req.url));
    clearState(response);
    return response;
  }

  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/oidc/callback`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ code, state, cookie_state: cookieState }),
      cache: "no-store",
      signal: AbortSignal.timeout(15_000),
    });
  } catch {
    const response = NextResponse.redirect(new URL("/login?error=oidc_failed", req.url));
    clearState(response);
    return response;
  }
  if (!upstream.ok) {
    const response = NextResponse.redirect(new URL("/login?error=oidc_failed", req.url));
    clearState(response);
    return response;
  }
  const data = (await upstream.json()) as {
    token?: unknown;
    return_to?: unknown;
  };
  if (typeof data.token !== "string" || !data.token) {
    const response = NextResponse.redirect(new URL("/login?error=oidc_failed", req.url));
    clearState(response);
    return response;
  }
  const returnTo =
    typeof data.return_to === "string" && data.return_to.startsWith("/") && !data.return_to.startsWith("//")
      ? data.return_to
      : "/";
  const loginURL = new URL("/login", req.url);
  loginURL.searchParams.set("oidc", "success");
  loginURL.searchParams.set("return_to", returnTo);
  const response = NextResponse.redirect(loginURL);
  response.cookies.set("ai_etl_token", data.token, {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/",
    maxAge: 24 * 60 * 60,
  });
  clearState(response);
  return response;
}
