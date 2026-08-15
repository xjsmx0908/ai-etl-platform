"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";

// Landing page: forward straight into the app shell's Q&A workspace. The
// (app) route group's ProtectedRoute bounces unauthenticated visitors to
// /login.
export default function Home() {
  const router = useRouter();
  useEffect(() => {
    router.replace("/qa");
  }, [router]);
  return null;
}
