import { NextRequest, NextResponse } from "next/server";
import { proxySessionManagement } from "@/lib/sessionManagementProxy";

export async function GET(req: NextRequest) {
  const token = req.cookies.get("ai_etl_token")?.value;
  if (!token) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  return proxySessionManagement(token, "/v1/auth/sessions");
}
