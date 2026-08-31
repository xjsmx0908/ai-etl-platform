import { NextRequest, NextResponse } from "next/server";

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
    const fallback =
      upstream.status === 401
        ? "用户名或密码错误"
        : upstream.status === 403
          ? "账号已停用"
          : `登录失败: ${upstream.status}`;
    const message =
      data && typeof data === "object" && typeof (data as { error?: unknown }).error === "string"
        ? ((data as { error: string }).error as string)
        : fallback;
    return NextResponse.json({ error: message }, { status: upstream.status });
  }

  const token = (data as { token?: unknown }).token;
  const expiresAtValue = (data as { expires_at?: unknown }).expires_at;
  const sessionCoreEnabled = process.env.SESSION_CORE_ENABLED === "true";
  if (
    typeof token !== "string" ||
    !token ||
    (sessionCoreEnabled && !token.startsWith("ps1_")) ||
    typeof expiresAtValue !== "string"
  ) {
    return NextResponse.json({ error: "登录响应缺少 token" }, { status: 502 });
  }
  const expiresAt = Date.parse(expiresAtValue);
  const remainingSeconds = Math.floor((expiresAt - Date.now()) / 1000);
  if (!Number.isFinite(expiresAt) || remainingSeconds <= 0) {
    return NextResponse.json({ error: "登录响应已过期" }, { status: 502 });
  }

  const res = NextResponse.json(
    {
      user: (data as { user?: unknown }).user,
      expires_at: (data as { expires_at?: unknown }).expires_at,
    },
    { status: 200 }
  );
  res.cookies.set("ai_etl_token", token, {
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
    maxAge: sessionCoreEnabled ? Math.min(30 * 60, remainingSeconds) : 24 * 60 * 60,
  });
  return res;
}
