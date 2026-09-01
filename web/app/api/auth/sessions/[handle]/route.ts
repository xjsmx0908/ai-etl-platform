import { NextRequest, NextResponse } from "next/server";
import { isSameOriginRequest } from "@/lib/authSecurity";
import { proxySessionManagement } from "@/lib/sessionManagementProxy";

export async function DELETE(
  req: NextRequest,
  context: { params: Promise<{ handle: string }> },
) {
  if (!isSameOriginRequest(req)) {
    return NextResponse.json({ error: "forbidden" }, { status: 403 });
  }
  const token = req.cookies.get("ai_etl_token")?.value;
  if (!token) return NextResponse.json({ error: "unauthorized" }, { status: 401 });
  const { handle } = await context.params;
  if (!/^sm1_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/.test(handle)) {
    return NextResponse.json({ error: "invalid session handle" }, { status: 400 });
  }
  return proxySessionManagement(token, `/v1/auth/sessions/${encodeURIComponent(handle)}`, "DELETE");
}
