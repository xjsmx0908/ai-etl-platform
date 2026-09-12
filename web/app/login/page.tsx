"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { apiClient } from "@/lib/apiClient";
import { localizeLoginError } from "@/lib/loginErrors";
import { getUser } from "@/lib/auth";
import { Logo } from "@/components/ui/Logo";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { Eye, EyeOff, Lock, ShieldCheck, Sparkles, User } from "lucide-react";

const FEATURES = [
  { icon: Sparkles, label: "多路召回 · 只回答已发布知识" },
  { icon: ShieldCheck, label: "权限隔离 · 引用可追溯" },
];

export default function LoginPage() {
  const router = useRouter();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [showPw, setShowPw] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [passwordEnabled, setPasswordEnabled] = useState(true);
  const [oidcEnabled, setOIDCEnabled] = useState(false);
  const [demoLoginEnabled, setDemoLoginEnabled] = useState(false);
  const [demoAccount, setDemoAccount] = useState<"" | "user" | "admin">("");

  useEffect(() => {
    if (getUser()) router.replace("/");
  }, [router]);

  useEffect(() => {
    fetch("/api/auth/methods", { cache: "no-store" })
      .then((response) => response.json())
      .then((methods: { password_enabled?: boolean; oidc_enabled?: boolean; demo_login_enabled?: boolean }) => {
        setPasswordEnabled(methods.password_enabled !== false);
        setOIDCEnabled(methods.oidc_enabled === true);
        setDemoLoginEnabled(methods.demo_login_enabled === true);
      })
      .catch(() => undefined);
  }, []);

  useEffect(() => {
    const searchParams = new URLSearchParams(window.location.search);
    if (searchParams.get("oidc") !== "success") return;
    apiClient.currentSession()
      .then(() => {
        const returnTo = searchParams.get("return_to") || "/";
        router.replace(returnTo.startsWith("/") && !returnTo.startsWith("//") ? returnTo : "/");
      })
      .catch(() => setError("企业身份登录会话无效，请重试"));
  }, [router]);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username.trim() || !password || loading || demoAccount) return;
    setLoading(true);
    setError("");
    try {
      await apiClient.login(username.trim(), password);
      router.replace("/");
    } catch (err) {
      setError(localizeLoginError((err as Error).message));
    } finally {
      setLoading(false);
    }
  };

  const onDemo = async (account: "user" | "admin") => {
    if (loading || demoAccount) return;
    setDemoAccount(account);
    setError("");
    try {
      await apiClient.demoLogin(account);
      router.replace(account === "admin" ? "/release-center" : "/qa");
    } catch (err) {
      setError(localizeLoginError((err as Error).message));
    } finally {
      setDemoAccount("");
    }
  };

  return (
    <div className="relative flex min-h-screen bg-slate-50">
      {/* 左：品牌渐变面板 */}
      <div className="bg-brand-gradient relative hidden w-1/2 flex-col justify-between overflow-hidden p-12 text-white lg:flex">
        {/* 装饰光斑 */}
        <div className="pointer-events-none absolute -right-24 -top-24 h-96 w-96 rounded-full bg-white/10 blur-3xl" />
        <div className="pointer-events-none absolute -bottom-32 -left-16 h-80 w-80 rounded-full bg-white/10 blur-3xl" />
        <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(circle_at_top_right,rgba(255,255,255,0.14),transparent_55%)]" />

        <div className="relative">
          <Logo size="lg" inverse />
        </div>

        <div className="relative">
          <p className="text-xs font-semibold uppercase tracking-widest text-blue-100/80">企业智能知识平台</p>
          <h1 className="mt-3 text-3xl font-semibold leading-tight">让文档沉淀为<br />可检索、可问答、可发布的知识资产</h1>
          <div className="mt-8 space-y-3">
            {FEATURES.map(({ icon: Icon, label }) => (
              <div key={label} className="flex items-center gap-3 text-sm text-blue-100">
                <span className="flex h-8 w-8 items-center justify-center rounded-lg bg-white/15">
                  <Icon className="h-4 w-4" />
                </span>
                {label}
              </div>
            ))}
          </div>
        </div>

        <p className="relative text-xs text-blue-200/70">© 2026 知境 · 企业知识库</p>
      </div>

      {/* 右：登录表单 */}
      <div className="relative flex flex-1 items-center justify-center px-4 py-10">
        {/* 背景装饰圆点 */}
        <div className="pointer-events-none absolute right-10 top-10 hidden h-40 w-40 rounded-full bg-blue-50 lg:block" />
        <div className="pointer-events-none absolute bottom-10 left-10 hidden h-56 w-56 rounded-full bg-indigo-50 lg:block" />

        <div className="relative w-full max-w-sm">
          <div className="mb-8 flex justify-center lg:hidden">
            <Logo size="lg" />
          </div>
          <div className="rounded-2xl border border-slate-200 bg-white p-8 shadow-xl shadow-slate-200/60">
            <div className="flex flex-col items-center text-center lg:items-start lg:text-left">
              <h1 className="text-xl font-semibold text-slate-900">欢迎登录</h1>
              <p className="mt-1 text-sm text-slate-500">登录知境 · 企业知识库</p>
            </div>
            {oidcEnabled && (
              <a
                href="/api/auth/oidc/start"
                className="mt-6 flex h-11 w-full items-center justify-center rounded-lg bg-blue-600 px-4 text-sm font-medium text-white transition hover:bg-blue-700"
              >
                使用企业身份登录
              </a>
            )}
            {oidcEnabled && passwordEnabled && (
              <div className="my-5 flex items-center gap-3 text-xs text-slate-400">
                <span className="h-px flex-1 bg-slate-200" />或使用本地账户<span className="h-px flex-1 bg-slate-200" />
              </div>
            )}
            {passwordEnabled && <form onSubmit={onSubmit} className={oidcEnabled ? "space-y-4" : "mt-6 space-y-4"}>
              <div>
                <label className="mb-1.5 block text-xs font-medium text-slate-600">用户名</label>
                <div className="relative">
                  <User className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
                  <Input
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    autoComplete="username"
                    placeholder="请输入用户名"
                    className="pl-9"
                  />
                </div>
              </div>
              <div>
                <label className="mb-1.5 block text-xs font-medium text-slate-600">密码</label>
                <div className="relative">
                  <Lock className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
                  <Input
                    type={showPw ? "text" : "password"}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="current-password"
                    placeholder="请输入密码"
                    className="pl-9 pr-10"
                  />
                  <button
                    type="button"
                    onClick={() => setShowPw((v) => !v)}
                    className="absolute right-2.5 top-1/2 -translate-y-1/2 text-slate-400 hover:text-slate-600"
                    aria-label="切换密码可见"
                  >
                    {showPw ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                  </button>
                </div>
              </div>
              {error && (
                <div className="rounded-lg bg-red-50 px-3 py-2.5 text-sm text-red-700">{error}</div>
              )}
              <Button type="submit" className="w-full" size="lg" loading={loading} disabled={!username.trim() || !password || !!demoAccount}>
                {loading ? "登录中…" : "登录"}
              </Button>
            </form>}
            {demoLoginEnabled && (
              <div className={passwordEnabled || oidcEnabled ? "mt-5 space-y-2" : "mt-6 space-y-2"}>
                {(passwordEnabled || oidcEnabled) && (
                  <div className="my-1 flex items-center gap-3 text-xs text-slate-400">
                    <span className="h-px flex-1 bg-slate-200" />体验演示<span className="h-px flex-1 bg-slate-200" />
                  </div>
                )}
                <Button type="button" variant="secondary" className="w-full" loading={demoAccount === "user"} disabled={loading || !!demoAccount} onClick={() => void onDemo("user")}>
                  体验问答（普通账号）
                </Button>
                <Button type="button" variant="secondary" className="w-full" loading={demoAccount === "admin"} disabled={loading || !!demoAccount} onClick={() => void onDemo("admin")}>
                  体验发布预审（管理员）
                </Button>
              </div>
            )}
          </div>
          <p className="mt-6 text-center text-xs text-slate-400">
            问题或账户需协助，请联系系统管理员
          </p>
        </div>
      </div>
    </div>
  );
}
