"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { useAuth } from "@/lib/auth";
import { Logo } from "@/components/ui/Logo";
import {
  Activity,
  Bot,
  Files,
  GaugeCircle,
  LogOut,
  Menu,
  MessageSquareText,
  ShieldCheck,
  UploadCloud,
  Users,
  X,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";

const NAV_ITEMS: { href: string; label: string; icon: LucideIcon }[] = [
  { href: "/qa", label: "问答工作台", icon: MessageSquareText },
  { href: "/documents", label: "文档管理", icon: Files },
  { href: "/data", label: "数据接入", icon: UploadCloud },
  { href: "/observe", label: "系统可观测", icon: Activity },
  { href: "/quality", label: "检索质量", icon: GaugeCircle },
  { href: "/agent", label: "Agent 编排", icon: Bot },
];

const ADMIN_ITEMS: { href: string; label: string; icon: LucideIcon }[] = [
  { href: "/users", label: "用户管理", icon: Users },
  { href: "/audit", label: "审计日志", icon: ShieldCheck },
];

const ROLE_LABELS: Record<string, string> = {
  admin: "管理员",
  user: "普通用户",
  readonly: "只读用户",
};

export default function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const { user, isAdmin, logout } = useAuth();
  const [mobileOpen, setMobileOpen] = useState(false);
  const items = isAdmin ? [...NAV_ITEMS, ...ADMIN_ITEMS] : NAV_ITEMS;

  useEffect(() => {
    document.body.style.overflow = mobileOpen ? "hidden" : "";
    return () => {
      document.body.style.overflow = "";
    };
  }, [mobileOpen]);

  const nav = (
    <nav className="flex-1 space-y-0.5 overflow-y-auto px-3 py-4">
      {items.map((item) => {
        const active = pathname === item.href || pathname.startsWith(`${item.href}/`);
        const Icon = item.icon;
        return (
          <Link
            key={item.href}
            href={item.href}
            onClick={() => setMobileOpen(false)}
            className={
              active
                ? "relative flex items-center gap-2.5 rounded-lg bg-blue-50 px-3 py-2 text-sm font-medium text-blue-700"
                : "flex items-center gap-2.5 rounded-lg px-3 py-2 text-sm text-slate-600 transition-colors hover:bg-slate-100 hover:text-slate-900"
            }
          >
            {active && (
              <span className="absolute left-0 top-1/2 h-5 w-0.5 -translate-y-1/2 rounded bg-blue-600" />
            )}
            <Icon className="h-4 w-4 shrink-0" />
            {item.label}
          </Link>
        );
      })}
    </nav>
  );

  const userCard = (
    <div className="border-t border-slate-200 px-5 py-3">
      <div className="flex items-center justify-between gap-2">
        <div className="min-w-0">
          <p className="truncate text-sm font-medium text-slate-700">{user?.username || "未登录"}</p>
          <p className="text-xs text-slate-400">{user ? ROLE_LABELS[user.role] || user.role : ""}</p>
        </div>
        <button
          onClick={() => void logout()}
          className="flex shrink-0 items-center gap-1 rounded-md px-2 py-1 text-xs text-slate-500 transition-colors hover:bg-red-50 hover:text-red-600"
        >
          <LogOut className="h-3.5 w-3.5" />
          退出
        </button>
      </div>
    </div>
  );

  return (
    <div className="flex min-h-screen bg-slate-50">
      {/* 桌面侧栏 */}
      <aside className="hidden w-60 shrink-0 flex-col border-r border-slate-200 bg-white lg:flex">
        <div className="border-b border-slate-200 px-5 py-4">
          <Logo />
        </div>
        {nav}
        {userCard}
      </aside>

      {/* 移动端抽屉 */}
      {mobileOpen && (
        <div className="fixed inset-0 z-50 lg:hidden">
          <div className="absolute inset-0 bg-slate-900/40" onClick={() => setMobileOpen(false)} />
          <aside className="absolute left-0 top-0 flex h-full w-60 flex-col border-r border-slate-200 bg-white">
            <div className="flex items-center justify-between border-b border-slate-200 px-5 py-4">
              <Logo />
              <button onClick={() => setMobileOpen(false)} className="text-slate-400 hover:text-slate-600">
                <X className="h-4 w-4" />
              </button>
            </div>
            {nav}
            {userCard}
          </aside>
        </div>
      )}

      <div className="flex min-w-0 flex-1 flex-col">
        <header className="flex h-14 shrink-0 items-center justify-between border-b border-slate-200 bg-white px-4 lg:px-6">
          <div className="flex items-center gap-3">
            <button onClick={() => setMobileOpen(true)} className="text-slate-500 lg:hidden" aria-label="打开菜单">
              <Menu className="h-5 w-5" />
            </button>
            <span className="text-sm text-slate-500">企业智能知识平台</span>
          </div>
          <div className="flex items-center gap-2 text-sm text-slate-700">{user?.username || ""}</div>
        </header>
        <main className="flex-1 overflow-y-auto px-4 py-6 lg:px-6">{children}</main>
      </div>
    </div>
  );
}
