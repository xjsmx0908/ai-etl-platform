import { NextRequest } from "next/server";
import { proxyBackend } from "@/lib/backendProxy";

export async function GET(req: NextRequest) {
  return proxyBackend(req, "/v1/release-center/overview");
}
