import { NextRequest, NextResponse } from "next/server";
import { localizeLoginError } from "@/lib/loginErrors";
import { parsePlatformLoginCredential } from "@/lib/platformSession";

// Login is the ONLY unauthenticated endpoint. On success we set the HttpOnly
// `ai_etl_token` cookie and hand the non-secret user object to the client for
// UI gating. The token itself never reaches client JS.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let body: unknown;
  try {
    body = await req.json();
  } catch {
    return NextResponse.json({ error: "请求格式错误" }, { status: 400 });
  }

  const upstream = await fetch(`${backend}/v1/auth/login`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(body),
    cache: "no-store",
  });

  const text = await upstream.text();
  let data: unknown = null;
  try {
    data = text ? JSON.parse(text) : null;
  } catch {
    data = null;
  }

  if (!upstream.ok) {
    const upstreamError =
      data && typeof data === "object" && typeof (data as { error?: unknown }).error === "string"
        ? (data as { error: string }).error
        : "";
    return NextResponse.json(
      { error: localizeLoginError(upstreamError, upstream.status) },
      { status: upstream.status },
    );
  }

  const sessionCoreEnabled = process.env.SESSION_CORE_ENABLED === "true";
  const credential = parsePlatformLoginCredential(data, sessionCoreEnabled);
  if (!credential) {
    return NextResponse.json({ error: "登录响应缺少 token" }, { status: 502 });
  }

  const res = NextResponse.json(
    {
      user: (data as { user?: unknown }).user,
      expires_at: (data as { expires_at?: unknown }).expires_at,
    },
    { status: 200 }
  );
  res.cookies.set("ai_etl_token", credential.token, {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    // Secure only when explicitly enabled: a production build served over plain
    // HTTP (e.g. http://host:3100) must NOT set Secure, or the browser drops the
    // cookie and every authenticated request 401s back to /login. HTTPS
    // deployments set COOKIE_SECURE=true.
    secure: sessionCoreEnabled || process.env.COOKIE_SECURE === "true",
    // Match the token TTL so the cookie does not silently expire mid-session
    // while the (still valid) token lives on, and vice versa.
    maxAge: credential.cookieMaxAge,
  });
  return res;
}
