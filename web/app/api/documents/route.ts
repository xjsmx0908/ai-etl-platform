import { NextRequest } from "next/server";
import { proxyBackend } from "@/lib/backendProxy";

// List documents with query params (limit/offset/status/permission/q).
// Auth from HttpOnly cookie, injected server-side as a Bearer header.
export async function GET(req: NextRequest) {
  return proxyBackend(req, `/v1/documents${req.nextUrl.search}`);
}
