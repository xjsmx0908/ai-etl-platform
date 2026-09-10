const TRANSIENT_FETCH_MESSAGES = [
  "Failed to fetch",
  "Fail to fetch",
  "fetch failed",
  "Load failed",
  "NetworkError when attempting to fetch resource",
];

const NETWORK_ERROR_RE = new RegExp(
  `^(${TRANSIENT_FETCH_MESSAGES.map((item) => item.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join("|")})$`,
  "i"
);

export function isTransientFetchError(message: string | undefined): boolean {
  const raw = (message || "").trim();
  if (!raw) return true;
  if (NETWORK_ERROR_RE.test(raw)) return true;
  return /networkerror|econnreset|econnrefused|etimedout|upstream temporarily unavailable/i.test(raw);
}

export function localizeFetchError(message: string | undefined): string {
  const raw = (message || "").trim();
  if (isTransientFetchError(raw)) return "同步暂时失败，请稍后重试";
  return raw;
}
