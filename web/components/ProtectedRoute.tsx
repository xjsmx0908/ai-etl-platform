"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { getUser } from "@/lib/auth";
import type { User } from "@/lib/types";

// UI-level guard. The real gate is the HttpOnly cookie server-side: any 401
// bounces to /login via the API client. This component just keeps unauthenticated
// users out of the app shell and optionally restricts a page to admins.
export default function ProtectedRoute({
  children,
  adminOnly,
}: {
  children: React.ReactNode;
  adminOnly?: boolean;
}) {
  const router = useRouter();
  const [mounted, setMounted] = useState(false);
  const [user, setUser] = useState<User | null>(null);

  useEffect(() => {
    setMounted(true);
    const u = getUser();
    setUser(u);
    if (!u) router.replace("/login");
  }, [router]);

  if (!mounted || !user) {
    return (
      <div className="flex min-h-[60vh] items-center justify-center text-sm text-slate-400">
        加载中…
      </div>
    );
  }

  if (adminOnly && user.role !== "admin") {
    return (
      <div className="rounded-xl border border-slate-200 bg-white p-10 text-center shadow-sm">
        <p className="text-sm font-medium text-slate-700">无权访问</p>
        <p className="mt-1 text-xs text-slate-500">该页面仅管理员可见。</p>
      </div>
    );
  }

  return <>{children}</>;
}
