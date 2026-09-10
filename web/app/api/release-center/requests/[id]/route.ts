import { NextRequest } from "next/server";
import { proxyBackend } from "@/lib/backendProxy";

export async function GET(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return proxyBackend(req, `/v1/release-center/requests/${encodeURIComponent(id)}`);
}

export async function POST(req: NextRequest, { params }: { params: Promise<{ id: string }> }) {
  const { id } = await params;
  return proxyBackend(req, `/v1/release-center/requests/${encodeURIComponent(id)}/decision`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: await req.text(),
  });
}
