import { NextRequest, NextResponse } from "next/server";

export async function GET(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  const returnTo = req.nextUrl.searchParams.get("return_to") || "/";
  let upstream: Response;
  try {
    upstream = await fetch(
      `${backend}/v1/auth/oidc/start?return_to=${encodeURIComponent(returnTo)}`,
      { cache: "no-store", signal: AbortSignal.timeout(10_000) }
    );
  } catch {
    return NextResponse.redirect(new URL("/login?error=oidc_unavailable", req.url));
  }
  if (!upstream.ok) {
    return NextResponse.redirect(new URL("/login?error=oidc_unavailable", req.url));
  }
  const data = (await upstream.json()) as {
    authorization_url?: unknown;
    state?: unknown;
    expires_in?: unknown;
  };
  if (
    typeof data.authorization_url !== "string" ||
    typeof data.state !== "string" ||
    typeof data.expires_in !== "number" ||
    !Number.isInteger(data.expires_in) ||
    data.expires_in <= 0 ||
    data.expires_in > 15 * 60
  ) {
    return NextResponse.redirect(new URL("/login?error=oidc_unavailable", req.url));
  }
  let authorizationURL: URL;
  try {
    authorizationURL = new URL(data.authorization_url);
  } catch {
    return NextResponse.redirect(new URL("/login?error=oidc_unavailable", req.url));
  }
  if (authorizationURL.protocol !== "https:") {
    return NextResponse.redirect(new URL("/login?error=oidc_unavailable", req.url));
  }
  const response = NextResponse.redirect(authorizationURL);
  response.cookies.set("ai_etl_oidc_state", data.state, {
    httpOnly: true,
    sameSite: "lax",
    // OIDC redirect URIs are HTTPS-only, so federated state never permits an
    // insecure transport even when local development cookies do.
    secure: true,
    path: "/api/auth/oidc/callback",
    maxAge: data.expires_in,
  });
  return response;
}
