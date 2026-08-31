import { NextRequest, NextResponse } from "next/server";

function clearReauthenticationState(response: NextResponse) {
  response.cookies.set("ai_etl_reauth_state", "", {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/api/auth/oidc/callback",
    maxAge: 0,
  });
}

function reauthenticationFailure(req: NextRequest): NextResponse {
  const destination = new URL("/", req.url);
  destination.searchParams.set("reauth", "failed");
  const response = NextResponse.redirect(destination);
  clearReauthenticationState(response);
  return response;
}

function safeReturnPath(value: unknown): string {
  if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//") || value.includes("\\")) {
    return "/";
  }
  return value;
}

async function completeReauthentication(req: NextRequest, cookieState: string): Promise<NextResponse> {
  const code = req.nextUrl.searchParams.get("code") || "";
  const state = req.nextUrl.searchParams.get("state") || "";
  const currentToken = req.cookies.get("ai_etl_token")?.value || "";
  if (!code || !state || !currentToken.startsWith("ps1_") || req.nextUrl.searchParams.has("error")) {
    return reauthenticationFailure(req);
  }
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let upstream: Response;
  try {
    upstream = await fetch(`${backend}/v1/auth/reauth/callback`, {
      method: "POST",
      headers: { Authorization: `Bearer ${currentToken}`, "Content-Type": "application/json" },
      body: JSON.stringify({ code, state, cookie_state: cookieState }),
      cache: "no-store",
      signal: AbortSignal.timeout(15_000),
    });
  } catch {
    return reauthenticationFailure(req);
  }
  if (!upstream.ok) {
    return reauthenticationFailure(req);
  }
  const data = (await upstream.json().catch(() => null)) as {
    token?: unknown;
    expires_at?: unknown;
    return_to?: unknown;
  } | null;
  if (typeof data?.token !== "string" || !data.token.startsWith("ps1_") || typeof data.expires_at !== "string") {
    return reauthenticationFailure(req);
  }
  const expiresAt = Date.parse(data.expires_at);
  const remainingSeconds = Math.floor((expiresAt - Date.now()) / 1000);
  if (!Number.isFinite(expiresAt) || remainingSeconds <= 0) {
    return reauthenticationFailure(req);
  }
  const response = NextResponse.redirect(new URL(safeReturnPath(data.return_to), req.url));
  response.cookies.set("ai_etl_token", data.token, {
    httpOnly: true,
    sameSite: "lax",
    secure: true,
    path: "/",
    maxAge: Math.min(30 * 60, remainingSeconds),
  });
  clearReauthenticationState(response);
  return response;
}

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
  const reauthenticationState = req.cookies.get("ai_etl_reauth_state")?.value || "";
  const callbackState = req.nextUrl.searchParams.get("state") || "";
  if (reauthenticationState && callbackState === reauthenticationState) {
    return completeReauthentication(req, reauthenticationState);
  }
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
    expires_at?: unknown;
    return_to?: unknown;
  };
  const sessionCoreEnabled = process.env.SESSION_CORE_ENABLED === "true";
  if (
    typeof data.token !== "string" ||
    !data.token ||
    (sessionCoreEnabled && !data.token.startsWith("ps1_")) ||
    typeof data.expires_at !== "string"
  ) {
    const response = NextResponse.redirect(new URL("/login?error=oidc_failed", req.url));
    clearState(response);
    return response;
  }
  const expiresAt = Date.parse(data.expires_at);
  const remainingSeconds = Math.floor((expiresAt - Date.now()) / 1000);
  if (!Number.isFinite(expiresAt) || remainingSeconds <= 0) {
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
    maxAge: sessionCoreEnabled ? Math.min(30 * 60, remainingSeconds) : 24 * 60 * 60,
  });
  clearState(response);
  return response;
}
