import { NextRequest, NextResponse } from "next/server";
import { localizeLoginError } from "@/lib/loginErrors";
import { parsePlatformLoginCredential } from "@/lib/platformSession";

// One-click demo login is unauthenticated. On success we set the same HttpOnly
// session cookie as password login; the token never reaches client JS.
export async function POST(req: NextRequest) {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  let body: unknown;
  try {
    body = await req.json();
  } catch {
    return NextResponse.json({ error: "请求格式错误" }, { status: 400 });
  }

  const upstream = await fetch(`${backend}/v1/auth/demo-login`, {
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
    secure: sessionCoreEnabled || process.env.COOKIE_SECURE === "true",
    maxAge: credential.cookieMaxAge,
  });
  return res;
}
