import type { NextRequest } from "next/server";

export function isSameOriginRequest(req: NextRequest): boolean {
  const origin = req.headers.get("origin");
  const fetchSite = req.headers.get("sec-fetch-site");
  const host = req.headers.get("host");
  if (!origin || !host || fetchSite !== "same-origin") return false;
  try {
    const originURL = new URL(origin);
    const forwardedProtocol = req.headers.get("x-forwarded-proto");
    const expectedProtocol = forwardedProtocol ? `${forwardedProtocol}:` : req.nextUrl.protocol;
    return originURL.host === host && originURL.protocol === expectedProtocol;
  } catch {
    return false;
  }
}

export function safeLocalReturnPath(value: unknown): string {
  if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//") || value.includes("\\")) {
    return "/";
  }
  return value;
}
