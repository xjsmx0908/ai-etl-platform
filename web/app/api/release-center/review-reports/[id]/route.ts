import { NextRequest, NextResponse } from "next/server";

const backend = process.env.ETL_API_URL || "http://query-api:8080/v1";

export async function GET(request: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const token = request.cookies.get("ai_etl_token")?.value;
  const { id } = await params;
  const upstream = await fetch(`${backend}/release-center/review-reports/${encodeURIComponent(id)}`, {
    headers: token ? { Authorization: `Bearer ${token}` } : {},
    cache: "no-store",
  });
  return new NextResponse(await upstream.text(), { status: upstream.status, headers: { "content-type": upstream.headers.get("content-type") || "application/json" } });
}
