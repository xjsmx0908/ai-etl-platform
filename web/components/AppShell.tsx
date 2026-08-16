"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useAuth } from "@/lib/auth";

const NAV_ITEMS = [
  { href: "/qa", label: "问答工作台" },
  { href: "/documents", label: "文档管理" },
  { href: "/data", label: "数据接入" },
  { href: "/observe", label: "系统可观测" },
  { href: "/quality", label: "检索质量" },
  { href: "/agent", label: "Agent 编排" },
];

const ADMIN_ITEMS = [
  { href: "/users", label: "用户管理" },
  { href: "/audit", label: "审计日志" },
];

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

export default function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { user, isAdmin, logout } = useAuth();

  const items = isAdmin ? [...NAV_ITEMS, ...ADMIN_ITEMS] : NAV_ITEMS;

  return (
    <div className="flex min-h-screen bg-slate-50">
      <aside className="flex w-56 shrink-0 flex-col border-r border-slate-200 bg-white">
        <div className="border-b border-slate-200 px-5 py-4">
          <h1 className="text-base font-semibold text-slate-900">知境 · 企业知识库</h1>
          <p className="mt-0.5 text-xs text-slate-500">企业智能知识平台</p>
        </div>
        <nav className="flex-1 space-y-0.5 overflow-y-auto px-3 py-4">
          {items.map((item) => {
            const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
            return (
              <Link
                key={item.href}
                href={item.href}
                className={
                  active
                    ? "block rounded-lg bg-blue-50 px-3 py-2 text-sm font-medium text-blue-700"
                    : "block rounded-lg px-3 py-2 text-sm text-slate-600 hover:bg-slate-100 hover:text-slate-900"
                }
              >
                {item.label}
              </Link>
            );
          })}
        </nav>
        <div className="border-t border-slate-200 px-5 py-3">
          <div className="flex items-center justify-between gap-2">
            <div className="min-w-0">
              <p className="truncate text-sm font-medium text-slate-700">{user?.username || "未登录"}</p>
              <p className="text-xs text-slate-400">{user ? ROLE_LABELS[user.role] || user.role : ""}</p>
            </div>
            <button
              onClick={() => void logout()}
              className="shrink-0 rounded-md px-2 py-1 text-xs text-slate-500 hover:bg-red-50 hover:text-red-600"
            >
              退出
            </button>
          </div>
        </div>
      </aside>
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center justify-between border-b border-slate-200 bg-white px-6">
          <span className="text-sm text-slate-500">企业智能知识平台</span>
          <div className="flex items-center gap-3">
            <span className="text-sm text-slate-700">{user?.username || ""}</span>
            <button
              onClick={() => void logout()}
              className="rounded-md px-2 py-1 text-xs text-slate-500 hover:bg-red-50 hover:text-red-600"
            >
              退出
            </button>
          </div>
        </header>
        <main className="flex-1 overflow-y-auto px-6 py-6">{children}</main>
      </div>
    </div>
  );
}
