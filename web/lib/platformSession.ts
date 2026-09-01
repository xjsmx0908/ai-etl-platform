export type PlatformLoginCredential = {
  token: string;
  cookieMaxAge: number;
};

export function parsePlatformLoginCredential(
  data: unknown,
  sessionCoreEnabled: boolean,
): PlatformLoginCredential | null {
  if (!data || typeof data !== "object") return null;
  const token = (data as { token?: unknown }).token;
  const expiresAt = (data as { expires_at?: unknown }).expires_at;
  if (
    typeof token !== "string" ||
    !token ||
    (sessionCoreEnabled && !token.startsWith("ps1_")) ||
    typeof expiresAt !== "string"
  ) {
    return null;
  }
  const absoluteExpiry = Date.parse(expiresAt);
  const remainingSeconds = Math.floor((absoluteExpiry - Date.now()) / 1000);
  if (!Number.isFinite(absoluteExpiry) || remainingSeconds <= 0) return null;
  return {
    token,
    cookieMaxAge: sessionCoreEnabled ? Math.min(30 * 60, remainingSeconds) : 24 * 60 * 60,
  };
}
