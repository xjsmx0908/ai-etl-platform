"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { apiClient } from "@/lib/apiClient";
import { getUser } from "@/lib/auth";
import { Logo } from "@/components/ui/Logo";
import { Input } from "@/components/ui/Input";
import { Button } from "@/components/ui/Button";
import { Eye, EyeOff, Sparkles } from "lucide-react";

export default function LoginPage() {
  const router = useRouter();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [showPw, setShowPw] = useState(false);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);

  useEffect(() => {
    if (getUser()) router.replace("/");
  }, [router]);

  const onSubmit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!username.trim() || !password || loading) return;
    setLoading(true);
    setError("");
    try {
      await apiClient.login(username.trim(), password);
      router.replace("/");
    } catch (err) {
      setError((err as Error).message || "登录失败");
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="flex min-h-screen bg-slate-50">
      {/* 左：品牌渐变面板 */}
      <div className="bg-brand-gradient relative hidden w-1/2 flex-col justify-between p-10 text-white lg:flex">
        <div>
          <Logo size="lg" />
        </div>
        <div>
          <h1 className="text-2xl font-semibold">知境 · 企业知识库</h1>
          <p className="mt-2 max-w-sm text-sm text-blue-100">
            企业智能知识平台 —— 让文档沉淀为可检索、可问答、可量化的知识资产。
          </p>
          <div className="mt-6 flex items-center gap-2 text-xs text-blue-100">
            <Sparkles className="h-4 w-4" />
            多路召回 · 语义检索 · 忠实度校验 · 权限隔离
          </div>
        </div>
      </div>

      {/* 右：登录表单 */}
      <div className="flex flex-1 items-center justify-center px-4 py-10">
        <div className="w-full max-w-sm">
          <div className="mb-6 flex justify-center lg:hidden">
            <Logo size="lg" />
          </div>
          <div className="rounded-xl border border-slate-200 bg-white p-8 shadow-sm">
            <h1 className="text-lg font-semibold text-slate-900">欢迎登录</h1>
            <p className="mt-1 text-sm text-slate-500">登录知境 · 企业知识库</p>
            <form onSubmit={onSubmit} className="mt-6 space-y-4">
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-600">用户名</label>
                <Input value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" />
              </div>
              <div>
                <label className="mb-1 block text-xs font-medium text-slate-600">密码</label>
                <div className="relative">
                  <Input
                    type={showPw ? "text" : "password"}
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                    autoComplete="current-password"
                    className="pr-10"
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
              {error && <div className="rounded-lg bg-red-50 p-3 text-sm text-red-700">{error}</div>}
              <Button type="submit" className="w-full" size="lg" loading={loading} disabled={!username.trim() || !password}>
                {loading ? "登录中…" : "登录"}
              </Button>
            </form>
          </div>
        </div>
      </div>
    </div>
  );
}
