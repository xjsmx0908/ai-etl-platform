import { NextResponse } from "next/server";

// Clears the HttpOnly session cookie by expiring it immediately.
export async function POST() {
  const res = new NextResponse(null, { status: 204 });
  res.cookies.set("ai_etl_token", "", {
    httpOnly: true,
    sameSite: "lax",
    path: "/",
    maxAge: 0,
    secure: process.env.NODE_ENV === "production",
  });
  return res;
}
