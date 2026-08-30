import { NextResponse } from "next/server";

export async function GET() {
  const backend = process.env.BACKEND_URL || "http://query-api:8080";
  try {
    const upstream = await fetch(`${backend}/v1/auth/methods`, { cache: "no-store" });
    if (!upstream.ok) {
      return NextResponse.json({ password_enabled: true, oidc_enabled: false }, { status: 200 });
    }
    return NextResponse.json(await upstream.json(), { status: 200 });
  } catch {
    return NextResponse.json({ password_enabled: true, oidc_enabled: false }, { status: 200 });
  }
}
