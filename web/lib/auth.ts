import { useCallback, useEffect, useState } from "react";
import type { User } from "./types";

// The real session gate is the HttpOnly `ai_etl_token` cookie (server-set,
// invisible to JS). For UI gating we mirror the *non-secret* user info in
// localStorage; any 401 from the API still bounces to /login regardless.

const USER_KEY = "ai_etl_user";

export function getUser(): User | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = localStorage.getItem(USER_KEY);
    return raw ? (JSON.parse(raw) as User) : null;
  } catch {
    return null;
  }
}

export function setUser(u: User): void {
  try {
    localStorage.setItem(USER_KEY, JSON.stringify(u));
  } catch {
    // ignore storage failures (e.g. private mode)
  }
}

export function clearUser(): void {
  try {
    localStorage.removeItem(USER_KEY);
  } catch {
    // ignore
  }
}

export function useAuth() {
  const [user, setUserState] = useState<User | null>(null);

  useEffect(() => {
    setUserState(getUser());
    const onStorage = () => setUserState(getUser());
    window.addEventListener("storage", onStorage);
    return () => window.removeEventListener("storage", onStorage);
  }, []);

  const logout = useCallback(async () => {
    try {
      const response = await fetch("/api/auth/logout", { method: "POST" });
      if (!response.ok) return;
      const data = response.status === 204
        ? null
        : await response.json().catch(() => null) as { authorization_url?: unknown } | null;
      clearUser();
      if (typeof data?.authorization_url === "string") {
        window.location.assign(data.authorization_url);
        return;
      }
      if (typeof window !== "undefined") window.location.replace("/login");
    } catch {
      // Keep local UI state when server-side revocation cannot be confirmed.
    }
  }, []);

  return {
    user,
    role: user?.role ?? null,
    isAdmin: user?.role === "admin",
    logout,
  };
}
